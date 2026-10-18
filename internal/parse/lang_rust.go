package parse

import (
	_ "embed"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"

	toml "github.com/pelletier/go-toml/v2"
	sitter "github.com/tree-sitter/go-tree-sitter"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/rust.scm
var rustQuery string

// rustSpec is the Rust language spec. Module identity is the crate name
// (from the nearest Cargo.toml) followed by the module path derived from
// the file's position in the mod tree, e.g. "fixture_shapes::shapes::circle".
type rustSpec struct{}

func init() { Register(rustSpec{}) }

func (rustSpec) Lang() model.Lang           { return model.LangRust }
func (rustSpec) Extensions() []string       { return model.LangRust.Extensions() }
func (rustSpec) Grammar() unsafe.Pointer    { return tsrust.Language() }
func (rustSpec) QuerySource() string        { return rustQuery }
func (rustSpec) ExtractorVersion() int      { return 2 }
func (rustSpec) testTarget(n string) string { return rustTestTarget(n) }

// IsTestPath reports integration tests (a "tests" directory) and test
// module files (tests.rs, *_test.rs, *_tests.rs).
func (rustSpec) IsTestPath(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	if !strings.HasSuffix(p, ".rs") {
		return false
	}
	base := path.Base(p)
	if base == "tests.rs" || strings.HasSuffix(base, "_test.rs") || strings.HasSuffix(base, "_tests.rs") {
		return true
	}
	return slices.Contains(strings.Split(path.Dir(p), "/"), "tests")
}

// isExported treats any visibility modifier (pub, pub(crate), ...) as
// exported. Enum variants and trait items inherit the visibility of their
// enum or trait.
func (rustSpec) isExported(_ *model.Def, n *sitter.Node, _ []byte) bool {
	if rustHasVisibility(n) {
		return true
	}
	switch n.Kind() {
	case "enum_variant", "function_item", "function_signature_item", "associated_type", "const_item":
		if list := n.Parent(); list != nil {
			if item := list.Parent(); item != nil && (item.Kind() == "enum_item" || item.Kind() == "trait_item") {
				return rustHasVisibility(item)
			}
		}
	}
	return false
}

func rustHasVisibility(n *sitter.Node) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "visibility_modifier":
			return true
		case "attribute_item", "line_comment", "block_comment", "function_modifiers":
			continue
		}
		return false
	}
	return false
}

// signature shortens macro definitions; everything else uses the default
// (text up to the body, or the first line).
func (rustSpec) signature(_ model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	if n.Kind() == "macro_definition" {
		if name := n.ChildByFieldName("name"); name != nil {
			return "macro_rules! " + string(src[name.StartByte():name.EndByte()]), true
		}
	}
	return "", false
}

// rustTestTarget maps test_parse_foo / parse_foo_test -> parse_foo.
func rustTestTarget(name string) string {
	if s, ok := strings.CutPrefix(name, "test_"); ok && s != "" {
		name = s
	}
	if s, ok := strings.CutSuffix(name, "_test"); ok && s != "" {
		name = s
	}
	return name
}

// ---------------------------------------------------------------- imports

var (
	rustModDeclRe   = regexp.MustCompile(`^(?:pub\s*(?:\([^)]*\))?\s*)?(?:unsafe\s+)?mod\s+(?:r#)?([A-Za-z_][A-Za-z0-9_]*)\s*;?$`)
	rustLineComment = regexp.MustCompile(`//[^\n]*`)
	rustBlockCommnt = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// importSpec turns a captured use tree into compact text (expanded into
// leaves by postProcess) and a `mod x;` declaration into "self::x".
func (rustSpec) importSpec(raw string) string {
	raw = rustBlockCommnt.ReplaceAllString(raw, " ")
	raw = rustLineComment.ReplaceAllString(raw, " ")
	raw = strings.Join(strings.Fields(raw), " ")
	if m := rustModDeclRe.FindStringSubmatch(raw); m != nil {
		return "self::" + m[1]
	}
	return raw
}

// useLeaf is one imported path of a use tree.
type useLeaf struct {
	path     []string
	alias    string
	wildcard bool
}

// Limits for parsing use trees, which may come from malformed source.
const (
	rustMaxUseTokens = 4096
	rustMaxUseDepth  = 16
)

// parseUseTree expands a use tree such as "a::{b, c::d as e, f::*}" into
// its leaves. Malformed input yields the leaves parsed so far.
func parseUseTree(s string) []useLeaf {
	toks := rustUseTokens(s)
	p := &useParser{toks: toks}
	p.tree(nil, 0)
	return p.out
}

func rustUseTokens(s string) []string {
	var toks []string
	for i := 0; i < len(s) && len(toks) < rustMaxUseTokens; {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case strings.HasPrefix(s[i:], "::"):
			toks = append(toks, "::")
			i += 2
		case c == '{' || c == '}' || c == ',' || c == '*':
			toks = append(toks, string(c))
			i++
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80:
			j := i
			for j < len(s) && (s[j] == '_' || s[j] == '#' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' ||
				s[j] >= '0' && s[j] <= '9' || s[j] >= 0x80) {
				j++
			}
			toks = append(toks, strings.TrimPrefix(s[i:j], "r#"))
			i = j
		default:
			i++ // stray punctuation in broken input
		}
	}
	return toks
}

type useParser struct {
	toks []string
	pos  int
	out  []useLeaf
}

func (p *useParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

// tree parses one use tree with the given path prefix.
func (p *useParser) tree(prefix []string, depth int) {
	if depth > rustMaxUseDepth {
		p.pos = len(p.toks)
		return
	}
	cur := slices.Clone(prefix)
	if p.peek() == "::" { // leading :: (2015 global path)
		p.pos++
	}
	for p.pos < len(p.toks) {
		switch t := p.peek(); t {
		case "{":
			p.pos++
			for p.pos < len(p.toks) && p.peek() != "}" {
				start := p.pos
				p.tree(cur, depth+1)
				if p.peek() == "," {
					p.pos++
				}
				if p.pos == start { // no progress on garbage
					p.pos++
				}
			}
			if p.peek() == "}" {
				p.pos++
			}
			return
		case "*":
			p.pos++
			if len(cur) > 0 {
				p.out = append(p.out, useLeaf{path: cur, wildcard: true})
			}
			return
		case "::", ",", "}", "as":
			return
		default:
			cur = append(cur, t)
			p.pos++
			switch p.peek() {
			case "::":
				p.pos++
				continue
			case "as":
				p.pos++
				leaf := useLeaf{path: cur}
				if a := p.peek(); a != "" && a != "," && a != "}" && a != "::" {
					leaf.alias = a
					p.pos++
				}
				p.add(leaf)
				return
			}
			p.add(useLeaf{path: cur})
			return
		}
	}
}

// add records a leaf; a trailing "self" (use a::{self}) imports the prefix.
func (p *useParser) add(l useLeaf) {
	if n := len(l.path); n > 1 && l.path[n-1] == "self" {
		l.path = l.path[:n-1]
	}
	if len(l.path) > 0 {
		p.out = append(p.out, l)
	}
}

// ---------------------------------------------------------------- post-processing

// rustBuiltinCalls are unqualified, receiver-less call names that never
// resolve into the repository: prelude enum constructors and std macros.
var rustBuiltinCalls = map[string]bool{
	"Some": true, "Ok": true, "Err": true,
	"println": true, "print": true, "eprintln": true, "eprint": true, "vec": true,
	"assert": true, "assert_eq": true, "assert_ne": true, "debug_assert": true,
	"debug_assert_eq": true, "debug_assert_ne": true, "panic": true, "todo": true,
	"unimplemented": true, "unreachable": true, "matches": true, "dbg": true,
	"writeln": true, "format_args": true,
}

// postProcess merges impl blocks into their types, resolves Self, rewrites
// relative module paths into synthetic imports, expands use trees and
// recomputes the test-file flag.
func (rustSpec) postProcess(f *model.Facts) {
	rustMergeImpls(f)

	refs := f.Refs[:0]
	for _, r := range f.Refs {
		if r.Name == "Self" {
			continue
		}
		if r.Qualifier == "" && r.Receiver == "" && r.Kind == model.RefCall && rustBuiltinCalls[r.Name] {
			continue
		}
		if r.Qualifier == "Self" || strings.HasPrefix(r.Qualifier, "Self::") {
			if owner := rustOwnerType(f, r.Enclosing); owner != "" {
				r.Qualifier = owner + strings.TrimPrefix(r.Qualifier, "Self")
			}
		}
		refs = append(refs, r)
	}
	f.Refs = refs

	rustExpandImports(f)
	rustQualifyPaths(f)
	slices.SortStableFunc(f.Imports, func(a, b model.Import) int { return int(a.Line) - int(b.Line) })

	// Test marks only for callables; #[cfg(test)] modules keep the flag.
	tests := f.Tests[:0]
	for _, t := range f.Tests {
		if f.Defs[t.Def].Kind.IsCallable() {
			tests = append(tests, t)
		}
	}
	f.Tests = tests
	if len(f.Tests) == 0 {
		f.Tests = nil
	}
	// A file is a test file when every top-level item is test code; inline
	// #[cfg(test)] modules do not make a source file a test file.
	f.TestFile = false
	if len(f.Tests) > 0 {
		f.TestFile = true
		for i := range f.Defs {
			if f.Defs[i].Parent == model.NoParent && !f.Defs[i].Flags.Has(model.FlagTest) {
				f.TestFile = false
				break
			}
		}
	}
}

// rustIsImplDef recognizes the temporary defs of impl blocks.
func rustIsImplDef(d *model.Def) bool {
	return d.Kind == model.KindType && (strings.HasPrefix(d.Sig, "impl") || strings.HasPrefix(d.Sig, "unsafe impl"))
}

// rustImplTypeParams returns the generic parameter names of an impl sig:
// "impl<T: Clone, 'a, const N: usize> ..." -> [T N].
func rustImplTypeParams(sig string) []string {
	rest := strings.TrimPrefix(strings.TrimPrefix(sig, "unsafe "), "impl")
	if !strings.HasPrefix(rest, "<") {
		return nil
	}
	depth, start := 0, 1
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "const "))
		if i := strings.IndexAny(s, ":= "); i >= 0 {
			s = s[:i]
		}
		if s != "" && !strings.HasPrefix(s, "'") {
			out = append(out, s)
		}
	}
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				add(rest[start:i])
				return out
			}
		case ',':
			if depth == 1 {
				add(rest[start:i])
				start = i + 1
			}
		}
	}
	return out
}

// rustMergeImpls removes the temporary impl defs. Items of an impl become
// children of the type with the same name in the same scope, and the impl's
// inheritance edges and references move to that type. Without such a type
// (it is defined in another file) the items keep the impl's parent and get
// the type name as scope, so their qualified name is still Type.item.
func rustMergeImpls(f *model.Facts) {
	n := len(f.Defs)
	isImpl := make([]bool, n)
	found := false
	for i := range f.Defs {
		if rustIsImplDef(&f.Defs[i]) {
			isImpl[i], found = true, true
		}
	}
	if !found {
		return
	}

	type scopeKey struct {
		parent int32
		name   string
	}
	types := map[scopeKey]int32{}
	for i := range f.Defs {
		d := &f.Defs[i]
		if !isImpl[i] && d.Kind.IsType() && len(d.Scope) == 0 {
			k := scopeKey{d.Parent, d.Name}
			if _, dup := types[k]; !dup {
				types[k] = int32(i)
			}
		}
	}
	// repl maps an impl def to the def that takes its place as a container:
	// the merged type, or the impl's own (non-impl) parent.
	target := make([]int32, n)
	repl := func(i int32) int32 {
		for guard := 0; i >= 0 && isImpl[i] && guard <= n; guard++ {
			if target[i] >= 0 {
				return target[i]
			}
			i = f.Defs[i].Parent
		}
		return i
	}
	for i := range f.Defs {
		target[i] = -1
		if isImpl[i] {
			if t, ok := types[scopeKey{f.Defs[i].Parent, f.Defs[i].Name}]; ok {
				target[i] = t
			}
		}
	}

	// New parents (old indices) and scopes of the remaining defs.
	parent := make([]int32, n)
	for i := range f.Defs {
		d := &f.Defs[i]
		parent[i] = d.Parent
		if isImpl[i] || d.Parent < 0 || !isImpl[d.Parent] {
			continue
		}
		impl := &f.Defs[d.Parent]
		if target[d.Parent] < 0 && !slices.Contains(rustImplTypeParams(impl.Sig), impl.Name) {
			d.Scope = append([]string{impl.Name}, d.Scope...)
		}
		parent[i] = repl(d.Parent)
	}

	// Depth-first order over the remaining defs keeps parents first.
	children := make([][]int32, n)
	var roots []int32
	for i := range f.Defs {
		if isImpl[i] {
			continue
		}
		if p := parent[i]; p >= 0 {
			children[p] = append(children[p], int32(i))
		} else {
			roots = append(roots, int32(i))
		}
	}
	newIdx := make([]int32, n)
	for i := range newIdx {
		newIdx[i] = model.NoParent
	}
	order := make([]int32, 0, n)
	var visit func(int32)
	visit = func(i int32) {
		newIdx[i] = int32(len(order))
		order = append(order, i)
		for _, c := range children[i] {
			visit(c)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	mapIdx := func(i int32) int32 {
		if i < 0 {
			return model.NoParent
		}
		if isImpl[i] {
			i = repl(i)
			if i < 0 {
				return model.NoParent
			}
		}
		return newIdx[i]
	}

	defs := make([]model.Def, 0, len(order))
	for _, old := range order {
		d := f.Defs[old]
		d.Parent = model.NoParent
		if parent[old] >= 0 {
			d.Parent = newIdx[parent[old]]
		}
		defs = append(defs, d)
	}
	for i := range f.Refs {
		f.Refs[i].Enclosing = mapIdx(f.Refs[i].Enclosing)
	}
	inh := f.Inherits[:0]
	for _, in := range f.Inherits {
		c := in.Child
		if isImpl[c] {
			if target[c] < 0 {
				continue // the implementing type is defined elsewhere
			}
			c = target[c]
		}
		in.Child = newIdx[c]
		inh = append(inh, in)
	}
	f.Inherits = inh
	if len(f.Inherits) == 0 {
		f.Inherits = nil
	}
	tests := f.Tests[:0]
	for _, t := range f.Tests {
		if !isImpl[t.Def] {
			t.Def = newIdx[t.Def]
			tests = append(tests, t)
		}
	}
	slices.SortFunc(tests, func(a, b model.TestMark) int { return int(a.Def) - int(b.Def) })
	f.Tests = tests
	f.Defs = defs
}

// rustOwnerType names the type Self refers to at def i: the nearest
// enclosing type, or the scope of an impl item whose type is elsewhere.
func rustOwnerType(f *model.Facts, i int32) string {
	for guard := 0; i >= 0 && int(i) < len(f.Defs) && guard <= len(f.Defs); guard++ {
		d := &f.Defs[i]
		if d.Kind.IsType() {
			return d.Name
		}
		if len(d.Scope) > 0 {
			return d.Scope[len(d.Scope)-1]
		}
		i = d.Parent
	}
	return ""
}

// rustModuleChain returns the inline modules (outermost first) enclosing
// source line, through any intermediate items.
func rustModuleChain(f *model.Facts, line uint32) []string {
	inner := model.NoParent
	for i := range f.Defs {
		d := &f.Defs[i]
		if d.StartLine <= line && line <= d.EndLine {
			inner = int32(i) // defs are parent-first, so the last hit is innermost
		}
	}
	var chain []string
	for guard := 0; inner >= 0 && guard <= len(f.Defs); guard++ {
		d := &f.Defs[inner]
		if d.Kind == model.KindModule {
			chain = append(chain, d.Name)
		}
		inner = d.Parent
	}
	slices.Reverse(chain)
	return chain
}

// rustRelativePath rewrites self:: and super:: paths written inside inline
// modules so that they are relative to the file's module, which is what
// ResolveImport understands.
func rustRelativePath(segs, chain []string) []string {
	if len(chain) == 0 || len(segs) == 0 {
		return segs
	}
	switch segs[0] {
	case "self":
		return slices.Concat([]string{"self"}, chain, segs[1:])
	case "super":
		k := 0
		for k < len(segs) && segs[k] == "super" {
			k++
		}
		if k <= len(chain) {
			return slices.Concat([]string{"self"}, chain[:len(chain)-k], segs[k:])
		}
		return slices.Concat(segs[:k-len(chain)], segs[k:])
	}
	return segs
}

// rustExpandImports turns each captured use tree into one import per leaf:
// Spec is the full path, Names the imported name ("*" for globs).
func rustExpandImports(f *model.Facts) {
	if len(f.Imports) == 0 {
		return
	}
	var out []model.Import
	for _, im := range f.Imports {
		chain := rustModuleChain(f, im.Line)
		for _, l := range parseUseTree(im.Spec) {
			segs := rustRelativePath(l.path, chain)
			if len(segs) == 0 {
				continue
			}
			ni := model.Import{Spec: strings.Join(segs, "::"), Alias: l.alias, Line: im.Line}
			switch last := segs[len(segs)-1]; {
			case l.wildcard:
				ni.Names, ni.Wildcard = []string{"*"}, true
			case l.alias == "_":
			case last != "self" && last != "super" && last != "crate":
				ni.Names = []string{last}
			}
			out = append(out, ni)
		}
	}
	f.Imports = out
}

// rustIsModuleSeg reports whether a path segment names a module (or crate)
// rather than a type: lowercase or underscore first letter, or a keyword.
func rustIsModuleSeg(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r == '_' || unicode.IsLower(r)
}

// rustQualifyPaths makes multi-segment module paths in qualifiers
// resolvable. A qualifier such as "crate::geometry", "super" or
// "std::mem" (any module prefix other than a single plain identifier,
// which import bindings already handle) gets a synthetic import of that
// module whose alias is the path with "/" separators and a leading "/"
// (never a valid identifier or receiver expression); the qualifier is
// rewritten to that alias, followed by any remaining type segments
// ("/crate/shapes::Shape"). Roots bound by a use declaration are expanded
// through it first.
func rustQualifyPaths(f *model.Facts) {
	bound := map[string]string{} // local module name -> full path
	for _, im := range f.Imports {
		if im.Wildcard || im.Alias == "_" {
			continue
		}
		local := im.Alias
		if local == "" && len(im.Names) == 1 {
			local = im.Names[0]
		}
		if local != "" && rustIsModuleSeg(local) {
			if _, dup := bound[local]; !dup {
				bound[local] = im.Spec
			}
		}
	}
	seen := map[string]bool{}
	for _, im := range f.Imports {
		if im.Alias != "" {
			seen[im.Alias] = true
		}
	}
	chains := map[uint32][]string{}
	chainAt := func(line uint32) []string {
		c, ok := chains[line]
		if !ok {
			c = rustModuleChain(f, line)
			chains[line] = c
		}
		return c
	}
	rewrite := func(qual string, line uint32) string {
		segs := strings.Split(qual, "::")
		k := 0
		for k < len(segs) && segs[k] != "" && rustIsModuleSeg(segs[k]) {
			k++
		}
		if k == 0 || slices.Contains(segs, "") {
			return qual
		}
		keyword := segs[0] == "crate" || segs[0] == "self" || segs[0] == "super"
		if k == 1 && !keyword {
			return qual
		}
		mod := segs[:k]
		if full, ok := bound[mod[0]]; ok && !keyword {
			// Already relative to the use declaration's position.
			mod = slices.Concat(strings.Split(full, "::"), mod[1:])
		} else {
			mod = rustRelativePath(mod, chainAt(line))
		}
		spec := strings.Join(mod, "::")
		alias := "/" + strings.Join(mod, "/")
		if !seen[alias] {
			seen[alias] = true
			f.Imports = append(f.Imports, model.Import{Spec: spec, Alias: alias, Line: line})
		}
		if k == len(segs) {
			return alias
		}
		return alias + "::" + strings.Join(segs[k:], "::")
	}
	for i := range f.Refs {
		if q := f.Refs[i].Qualifier; strings.Contains(q, "::") || q == "crate" || q == "self" || q == "super" {
			f.Refs[i].Qualifier = rewrite(q, f.Refs[i].Line)
		}
	}
	for i := range f.Inherits {
		if q := f.Inherits[i].Qualifier; strings.Contains(q, "::") || q == "crate" || q == "self" || q == "super" {
			f.Inherits[i].Qualifier = rewrite(q, f.Inherits[i].Line)
		}
	}
}

// ---------------------------------------------------------------- crates and modules

// cargoManifest is the part of Cargo.toml graphmaster reads.
type cargoManifest struct {
	Package struct {
		Name string `toml:"name"`
	} `toml:"package"`
	Lib struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"lib"`
	Bin []struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"bin"`
	Dependencies      map[string]any `toml:"dependencies"`
	DevDependencies   map[string]any `toml:"dev-dependencies"`
	BuildDependencies map[string]any `toml:"build-dependencies"`
	Target            map[string]struct {
		Dependencies      map[string]any `toml:"dependencies"`
		DevDependencies   map[string]any `toml:"dev-dependencies"`
		BuildDependencies map[string]any `toml:"build-dependencies"`
	} `toml:"target"`
	Workspace struct {
		Dependencies map[string]any `toml:"dependencies"`
	} `toml:"workspace"`
}

// depTables returns every dependency table of the manifest.
func (m *cargoManifest) depTables() []map[string]any {
	out := []map[string]any{m.Dependencies, m.DevDependencies, m.BuildDependencies}
	for _, t := range m.Target {
		out = append(out, t.Dependencies, t.DevDependencies, t.BuildDependencies)
	}
	return out
}

// cargoDepPath returns the path of a path dependency and whether the entry
// defers to [workspace.dependencies] ("x = { workspace = true }").
func cargoDepPath(v any) (p string, workspace bool) {
	t, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	if w, _ := t["workspace"].(bool); w {
		return "", true
	}
	p, _ = t["path"].(string)
	return p, false
}

// manifestCache memoizes parsed manifests by content; reads still go
// through the RepoContext so the indexer records them as dependencies.
var manifestCache = struct {
	sync.Mutex
	m map[string]*cargoManifest
}{m: map[string]*cargoManifest{}}

const maxManifestCache = 256

func parseCargoManifest(b []byte) *cargoManifest {
	key := string(b)
	manifestCache.Lock()
	defer manifestCache.Unlock()
	if m, ok := manifestCache.m[key]; ok {
		return m
	}
	m := &cargoManifest{}
	if err := toml.Unmarshal(b, m); err != nil {
		m = &cargoManifest{}
	}
	if len(manifestCache.m) >= maxManifestCache {
		clear(manifestCache.m)
	}
	manifestCache.m[key] = m
	return m
}

// rustCrate is one Cargo package.
type rustCrate struct {
	dir     string            // package directory ("" = repo root)
	pkg     string            // package name with '-' -> '_'
	lib     string            // library crate name
	libRoot string            // library crate root file
	bins    map[string]string // binary crate root file -> crate name
	deps    map[string]string // crate name used in code -> path dependency's package dir
}

func rustIdent(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "-", "_") }

// rustLoadCrate reads the package enclosing dir. Without a Cargo.toml the
// repository root is treated as a package named "crate".
func rustLoadCrate(ctx model.RepoContext, dir string) rustCrate {
	c := rustCrate{pkg: "crate", bins: map[string]string{}, deps: map[string]string{}}
	var man *cargoManifest
	if ctx != nil {
		if mf, ok := model.FindUp(ctx, dir, "Cargo.toml"); ok {
			c.dir = model.CleanRel(path.Dir(mf))
			if b, err := ctx.ReadFile(mf); err == nil {
				man = parseCargoManifest(b)
			}
			c.pkg = rustIdent(path.Base("/" + c.dir))
			if c.dir == "" {
				c.pkg = "crate"
			}
		}
	}
	join := func(rel string) string { return model.CleanRel(path.Join(c.dir, rel)) }
	c.lib, c.libRoot = c.pkg, join("src/lib.rs")
	if man != nil {
		if n := rustIdent(man.Package.Name); n != "" {
			c.pkg, c.lib = n, n
		}
		if n := rustIdent(man.Lib.Name); n != "" {
			c.lib = n
		}
		if man.Lib.Path != "" {
			c.libRoot = join(man.Lib.Path)
		}
		for _, b := range man.Bin {
			if b.Path != "" && b.Name != "" {
				c.bins[join(b.Path)] = rustIdent(b.Name)
			}
		}
		rustLoadDeps(ctx, &c, man)
	}
	if mainRS := join("src/main.rs"); c.bins[mainRS] == "" {
		c.bins[mainRS] = c.pkg
	}
	return c
}

// rustLoadDeps records the path dependencies of the package, following
// "workspace = true" entries to the enclosing workspace manifest. Registry
// and git dependencies stay external.
func rustLoadDeps(ctx model.RepoContext, c *rustCrate, man *cargoManifest) {
	var ws *cargoManifest
	wsDir := ""
	loadWorkspace := func() {
		if ws != nil {
			return
		}
		ws = &cargoManifest{}
		if man.Workspace.Dependencies != nil {
			ws, wsDir = man, c.dir
			return
		}
		if c.dir == "" {
			return
		}
		if mf, ok := model.FindUp(ctx, parentRel(c.dir), "Cargo.toml"); ok {
			if b, err := ctx.ReadFile(mf); err == nil {
				ws, wsDir = parseCargoManifest(b), model.CleanRel(path.Dir(mf))
			}
		}
	}
	for _, table := range man.depTables() {
		for name, v := range table {
			p, inherit := cargoDepPath(v)
			dir := c.dir
			if inherit {
				loadWorkspace()
				p, _ = cargoDepPath(ws.Workspace.Dependencies[name])
				dir = wsDir
			}
			if p != "" {
				c.deps[rustIdent(name)] = model.CleanRel(path.Join(dir, p))
			}
		}
	}
}

// parentRel returns the parent of a repo-relative directory ("" for a
// top-level one).
func parentRel(dir string) string {
	if d := path.Dir(dir); d != "." {
		return d
	}
	return ""
}

// rustModule locates a file in the mod tree of its crate.
type rustModule struct {
	crate   rustCrate
	name    string   // crate (target) name
	segs    []string // module path inside the crate
	root    string   // crate root file
	rootDir string   // directory of the crate root (top of the mod tree)
}

// identity is the module identity: crate name and module path.
func (m rustModule) identity() string {
	return strings.Join(append([]string{m.name}, m.segs...), "::")
}

// rustModuleSegs converts the path of a file below a mod tree root into
// module segments: "a/b.rs" -> [a b], "a/mod.rs" -> [a].
func rustModuleSegs(rel string) []string {
	var segs []string
	for _, p := range strings.Split(rel, "/") {
		if p != "" {
			segs = append(segs, rustIdent(p))
		}
	}
	if n := len(segs); n > 0 {
		segs[n-1] = strings.TrimSuffix(segs[n-1], ".rs")
		if segs[n-1] == "mod" {
			segs = segs[:n-1]
		}
	}
	return segs
}

// rustLocate places file p (repo-relative) in its crate's mod tree.
func rustLocate(p string, ctx model.RepoContext) rustModule {
	p = model.CleanRel(p)
	c := rustLoadCrate(ctx, path.Dir(p))
	m := rustModule{crate: c}
	rel := p
	if c.dir != "" {
		rel = strings.TrimPrefix(p, c.dir+"/")
	}
	root := func(name, file string) rustModule {
		m.name, m.root, m.rootDir = name, file, model.CleanRel(path.Dir(file))
		return m
	}
	if p == c.libRoot {
		return root(c.lib, p)
	}
	if name, ok := c.bins[p]; ok {
		return root(name, p)
	}
	join := func(rel string) string { return model.CleanRel(path.Join(c.dir, rel)) }
	for _, td := range []string{"src/bin/", "tests/", "examples/", "benches/"} {
		rest, ok := strings.CutPrefix(rel, td)
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) == 1 {
			return root(rustIdent(strings.TrimSuffix(parts[0], ".rs")), p)
		}
		m.name = rustIdent(parts[0])
		m.rootDir = join(td + parts[0])
		m.root = path.Join(m.rootDir, "main.rs")
		if parts[1] != "main.rs" {
			m.segs = rustModuleSegs(parts[1])
		}
		return m
	}
	if rel == "build.rs" {
		return root("build_script_build", p)
	}
	if rest, ok := strings.CutPrefix(rel, "src/"); ok {
		m.name, m.root = c.lib, c.libRoot
		if mainRS := join("src/main.rs"); ctx != nil && !ctx.FileExists(c.libRoot) && ctx.FileExists(mainRS) {
			m.name, m.root = c.bins[mainRS], mainRS // binary-only package
		}
		m.rootDir = join("src")
		m.segs = rustModuleSegs(rest)
		return m
	}
	// Targets with a custom path ([[bin]] path = "crates/core/main.rs") own
	// the mod tree below their root's directory.
	best := ""
	for file, name := range c.customRoots() {
		dir := model.CleanRel(path.Dir(file))
		if dir != "" && !strings.HasPrefix(p, dir+"/") {
			continue
		}
		// Deepest root wins; ties break on the file name so map order
		// cannot change a module's identity.
		if m.name == "" || len(dir) > len(best) || (len(dir) == len(best) && file < m.root) {
			m.name, m.root, m.rootDir, best = name, file, dir, dir
		}
	}
	if m.name != "" {
		m.segs = rustModuleSegs(strings.TrimPrefix(p, best+"/"))
		return m
	}
	m.name, m.root, m.rootDir = c.pkg, c.libRoot, c.dir
	m.segs = rustModuleSegs(rel)
	return m
}

// customRoots returns the crate roots declared with a path outside the
// standard layout, keyed by root file.
func (c rustCrate) customRoots() map[string]string {
	join := func(rel string) string { return model.CleanRel(path.Join(c.dir, rel)) }
	out := map[string]string{}
	if c.libRoot != join("src/lib.rs") {
		out[c.libRoot] = c.lib
	}
	for file, name := range c.bins {
		if file != join("src/main.rs") && !strings.HasPrefix(file, join("src")+"/") {
			out[file] = name
		}
	}
	return out
}

// ModuleOf returns "crate::mod::path" for the file.
func (rustSpec) ModuleOf(p string, _ *model.Facts, ctx model.RepoContext) string {
	return rustLocate(p, ctx).identity()
}

// rustStdCrates are the sysroot crates; their paths are always external.
var rustStdCrates = map[string]bool{"std": true, "core": true, "alloc": true, "proc_macro": true, "test": true}

// rustModFile returns the file of module segs below rootDir, if any.
func rustModFile(ctx model.RepoContext, m rustModule, segs []string) (string, bool) {
	if len(segs) == 0 {
		return m.root, ctx.FileExists(m.root)
	}
	base := path.Join(append([]string{m.rootDir}, segs...)...)
	if f := model.CleanRel(base + ".rs"); ctx.FileExists(f) {
		return f, true
	}
	if f := model.CleanRel(base + "/mod.rs"); ctx.FileExists(f) {
		return f, true
	}
	return "", false
}

// ResolveImport maps a use path to the file of the deepest module it
// names: "crate::geometry::square_of" -> src/geometry.rs. Paths start at
// crate, self, super, the package's own library crate, or a child module
// of the importing module; sysroot and other crates are external. Items
// re-exported with `pub use` are followed to their defining module.
func (rustSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	return rustResolveImport(spec, fromFile, ctx, 0)
}

const rustMaxReexportDepth = 4

func rustResolveImport(spec, fromFile string, ctx model.RepoContext, depth int) model.ImportTarget {
	segs := strings.Split(strings.TrimPrefix(strings.TrimSpace(spec), "::"), "::")
	if len(segs) == 0 || segs[0] == "" || slices.Contains(segs, "") {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	if rustStdCrates[segs[0]] {
		return model.ImportTarget{Kind: model.TargetExternal, Module: segs[0]}
	}
	if ctx == nil {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	m := rustLocate(fromFile, ctx)
	base := m
	rest := segs
	switch segs[0] {
	case "crate":
		base.segs, rest = nil, segs[1:]
	case "self":
		rest = segs[1:]
	case "super":
		k := 0
		for k < len(segs) && segs[k] == "super" {
			k++
		}
		if k > len(m.segs) {
			return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
		}
		base.segs, rest = m.segs[:len(m.segs)-k], segs[k:]
	default:
		switch {
		case segs[0] == m.crate.lib && ctx.FileExists(m.crate.libRoot):
			base, rest = rustLocate(m.crate.libRoot, ctx), segs[1:]
		case rustHasModFile(ctx, m, append(slices.Clone(m.segs), segs[0])):
			// Child module of the importing module (2018 uniform paths).
		case rustHasModFile(ctx, m, segs[:1]):
			base.segs = nil // crate-relative (2015 paths)
		case m.crate.deps[segs[0]] != "":
			// Path dependency, e.g. another workspace member.
			dep := rustLoadCrate(ctx, m.crate.deps[segs[0]])
			if dep.dir != m.crate.deps[segs[0]] || !ctx.FileExists(dep.libRoot) {
				return model.ImportTarget{Kind: model.TargetExternal, Module: segs[0]}
			}
			base, rest = rustLocate(dep.libRoot, ctx), segs[1:]
		default:
			return model.ImportTarget{Kind: model.TargetExternal, Module: segs[0]}
		}
	}

	cur, ok := rustModFile(ctx, base, base.segs)
	if !ok {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	modSegs := slices.Clone(base.segs)
	i := 0
	for ; i < len(rest); i++ {
		f, ok := rustModFile(ctx, base, append(slices.Clone(modSegs), rest[i]))
		if !ok {
			break
		}
		cur, modSegs = f, append(modSegs, rest[i])
	}
	if i < len(rest) && depth < rustMaxReexportDepth {
		if t, ok := rustFollowReexport(ctx, cur, rest[i], rest[i+1:], depth); ok {
			return t
		}
	}
	base.segs = modSegs
	return model.ImportTarget{Kind: model.TargetFile, Path: cur, Module: base.identity()}
}

func rustHasModFile(ctx model.RepoContext, m rustModule, segs []string) bool {
	_, ok := rustModFile(ctx, m, segs)
	return ok
}

var rustPubUseRe = regexp.MustCompile(`(?s)\bpub(?:\s*\([^)]*\))?\s+use\s+([^;]+);`)

// rustFollowReexport resolves item (followed by more path segments) through
// a `pub use` of module file modFile when the file does not define it.
func rustFollowReexport(ctx model.RepoContext, modFile, item string, more []string, depth int) (model.ImportTarget, bool) {
	b, err := ctx.ReadFile(modFile)
	if err != nil {
		return model.ImportTarget{}, false
	}
	src := string(b)
	if rustDefinesItem(src, item) {
		return model.ImportTarget{}, false
	}
	var globs [][]string
	for _, m := range rustPubUseRe.FindAllStringSubmatch(src, 64) {
		for _, l := range parseUseTree(m[1]) {
			switch {
			case l.wildcard:
				globs = append(globs, l.path)
			case l.alias == item || (l.alias == "" && l.path[len(l.path)-1] == item):
				spec := strings.Join(slices.Concat(l.path, more), "::")
				t := rustResolveImport(spec, modFile, ctx, depth+1)
				return t, t.Kind != model.TargetUnresolved
			}
		}
	}
	for _, g := range globs {
		spec := strings.Join(slices.Concat(g, []string{item}, more), "::")
		t := rustResolveImport(spec, modFile, ctx, depth+1)
		if t.Kind != model.TargetFile {
			continue
		}
		if b, err := ctx.ReadFile(t.Path); err == nil && rustDefinesItem(string(b), item) {
			return t, true
		}
	}
	return model.ImportTarget{}, false
}

// rustDefinesItem reports whether src declares an item called name.
func rustDefinesItem(src, name string) bool {
	re, err := regexp.Compile(`\b(?:struct|enum|trait|fn|type|union|const|static|mod)\s+(?:r#)?` +
		regexp.QuoteMeta(name) + `\b|macro_rules!\s*` + regexp.QuoteMeta(name) + `\b`)
	return err == nil && re.MatchString(src)
}
