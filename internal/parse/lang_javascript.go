package parse

import (
	_ "embed"
	"path"
	"strings"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/javascript.scm
var javascriptQuery string

// javascriptSpec is the JavaScript (and JSX) language spec. Module identity
// is the file itself; see jsFamily for the hooks shared with TypeScript.
type javascriptSpec struct{ jsFamily }

func init() { Register(javascriptSpec{}) }

func (javascriptSpec) Lang() model.Lang        { return model.LangJavaScript }
func (javascriptSpec) Extensions() []string    { return model.LangJavaScript.Extensions() }
func (javascriptSpec) Grammar() unsafe.Pointer { return tsjs.Language() }
func (javascriptSpec) QuerySource() string     { return javascriptQuery }
func (javascriptSpec) ExtractorVersion() int   { return 2 }

// ResolveImport implements model.LanguageSpec (see jsResolveImport).
func (javascriptSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	return jsResolveImport(spec, fromFile, ctx, false)
}

// jsFamily holds the extraction hooks shared by JavaScript, TypeScript and
// TSX. The specs embed it; the hook methods are promoted.
type jsFamily struct{}

// ModuleOf implements model.LanguageSpec: a JS/TS module is its file.
func (jsFamily) ModuleOf(p string, _ *model.Facts, _ model.RepoContext) string {
	return model.CleanRel(p)
}

// IsTestPath reports foo.test.ts, foo.spec.js and files under __tests__/.
func (jsFamily) IsTestPath(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	base := path.Base(p)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	return strings.Contains("/"+p, "/__tests__/")
}

func (jsFamily) testTarget(name string) string { return jsTestTarget(name) }

// jsTestTarget derives the tested name from a describe/it title: the whole
// title, else its first word, when that looks like code: an identifier path
// with a separator ("Calculator#product", "a.b"), a call suffix ("run()")
// or an upper-case letter ("UserService", "formatName"). Plain words
// ("works", "sums") are prose. Qualified targets keep only the member name,
// since tests edges are resolved by bare name.
func jsTestTarget(name string) string {
	norm := func(s string) string {
		s = strings.TrimSuffix(strings.TrimSpace(s), "()")
		return strings.NewReplacer("#", ".", "::", ".").Replace(s)
	}
	last := func(s string) string {
		if i := strings.LastIndexByte(s, '.'); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	codeLike := func(raw, s string) bool {
		return jsIsIdentPath(s) && (strings.HasSuffix(raw, "()") || strings.ContainsAny(raw, ".#:") ||
			strings.ToLower(s) != s)
	}
	name = strings.TrimSpace(name)
	if whole := norm(name); codeLike(name, whole) {
		return last(whole)
	}
	first, _, _ := strings.Cut(name, " ")
	if f := norm(first); codeLike(first, f) {
		return last(f)
	}
	return ""
}

// jsIsIdentPath reports whether s is ident(.ident)*.
func jsIsIdentPath(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if !jsIsIdent(part) {
			return false
		}
	}
	return true
}

func jsIsIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80:
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// isExported marks module exports (export statements, CommonJS exports,
// ambient declarations) and non-private members of types.
func (jsFamily) isExported(d *model.Def, n *sitter.Node, src []byte) bool {
	if d.Kind == model.KindTest {
		return false
	}
	switch n.Kind() {
	case "call_expression", "pair":
		return false
	case "assignment_expression":
		return true
	case "variable_declarator":
		if p := n.Parent(); p != nil {
			n = p
		}
	case "required_parameter", "optional_parameter":
		return !jsHasPrivateModifier(n, src)
	}
	p := n.Parent()
	if p == nil {
		return false
	}
	switch p.Kind() {
	case "export_statement", "ambient_declaration":
		return true
	case "class_body", "interface_body", "object_type", "enum_body":
		return !strings.HasPrefix(d.Name, "#") && !jsHasPrivateModifier(n, src)
	}
	return false
}

// jsHasPrivateModifier reports a private/protected accessibility modifier.
func jsHasPrivateModifier(n *sitter.Node, src []byte) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "accessibility_modifier" {
			m := string(src[c.StartByte():c.EndByte()])
			return m == "private" || m == "protected"
		}
	}
	return false
}

// signature renders declarator-style definitions; others use the default
// (text up to the body).
func (jsFamily) signature(_ model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	text := func(a, b uint) string { return string(src[a:b]) }
	// upToBody renders n up to the body of its function value fn.
	upToBody := func(fn *sitter.Node) (string, bool) {
		if fn == nil {
			return "", false
		}
		switch fn.Kind() {
		case "arrow_function", "function_expression", "generator_function", "function":
		default:
			return "", false
		}
		body := fn.ChildByFieldName("body")
		if body == nil {
			return "", false
		}
		return strings.TrimSpace(text(n.StartByte(), body.StartByte())), true
	}
	switch n.Kind() {
	case "variable_declarator":
		kw := ""
		if p := n.Parent(); p != nil {
			kw, _, _ = strings.Cut(text(p.StartByte(), p.EndByte()), " ")
			kw += " "
		}
		if s, ok := upToBody(n.ChildByFieldName("value")); ok {
			return kw + s, true
		}
		return kw + firstLine(text(n.StartByte(), n.EndByte())), true
	case "public_field_definition", "field_definition", "pair":
		if s, ok := upToBody(n.ChildByFieldName("value")); ok {
			return s, true
		}
		return strings.TrimSuffix(firstLine(text(n.StartByte(), n.EndByte())), ";"), true
	case "assignment_expression":
		if s, ok := upToBody(n.ChildByFieldName("right")); ok {
			return s, true
		}
	case "call_expression": // test blocks: up to the callback body
		if args := n.ChildByFieldName("arguments"); args != nil {
			for i := int(args.NamedChildCount()) - 1; i >= 0; i-- {
				if s, ok := upToBody(args.NamedChild(uint(i))); ok {
					return s, true
				}
			}
		}
	case "function_signature", "method_signature", "abstract_method_signature", "property_signature":
		return strings.TrimSuffix(firstLine(text(n.StartByte(), n.EndByte())), ";"), true
	}
	return "", false
}

// jsTestFuncs are test-framework entry points; calls to them are structure,
// not references into the repository.
var jsTestFuncs = map[string]bool{
	"describe": true, "context": true, "suite": true, "it": true, "test": true,
	"specify": true, "expect": true, "beforeEach": true, "afterEach": true,
	"beforeAll": true, "afterAll": true, "before": true, "after": true,
}

// postProcess normalizes imports (raw name lists become imported names, "*"
// marks wildcards), turns receivers naming a whole-module binding into
// qualifiers, maps aliased imports back to their exported names, and fixes
// definitions the query cannot express precisely.
func (jsFamily) postProcess(f *model.Facts) {
	renames := map[string]string{} // local alias -> imported name
	modules := map[string]bool{}   // identifiers bound to a whole module
	aliasLine := map[string]uint32{}
	for i := range f.Imports {
		im := &f.Imports[i]
		var names []string
		for _, raw := range im.Names {
			for _, b := range jsBindings(raw) {
				if !containsString(names, b.orig) {
					names = append(names, b.orig)
				}
				if b.local != b.orig {
					renames[b.local] = b.orig
				}
			}
		}
		if im.Wildcard {
			names = []string{"*"}
		}
		im.Names = names
		if im.Alias != "" {
			modules[im.Alias] = true
			aliasLine[im.Alias] = im.Line
		}
	}

	refs := f.Refs[:0]
	for _, r := range f.Refs {
		if r.Qualifier == "" && r.Receiver != "" && modules[jsRootIdent(r.Receiver)] {
			r.Qualifier, r.Receiver = r.Receiver, ""
		}
		bare := r.Qualifier == "" && r.Receiver == ""
		switch {
		case bare && r.Name == "require":
			continue
		case bare && f.TestFile && jsTestFuncs[r.Name]:
			continue
		case f.TestFile && jsTestFuncs[jsRootIdent(r.Receiver)] && !strings.Contains(r.Receiver, "("):
			continue // it.each, describe.skip
		case r.Receiver == "module" || r.Receiver == "exports" || r.Receiver == "module.exports":
			continue
		case r.Name == "prototype":
			continue
		}
		if to, ok := renames[r.Name]; ok && bare {
			r.Name = to
		}
		refs = append(refs, r)
	}
	f.Refs = refs

	jsReparentParamProps(f)
	drop := make([]bool, len(f.Defs))
	changed := false
	for i := range f.Defs {
		d := &f.Defs[i]
		// const x = require("x") binds a module, not a value definition.
		if (d.Kind == model.KindConstant || d.Kind == model.KindVariable) && d.Parent == model.NoParent {
			if line, ok := aliasLine[d.Name]; ok && line == d.StartLine {
				drop[i], changed = true, true
			}
		}
	}
	if jsMarkOverloads(f, drop) {
		changed = true
	}
	if changed {
		jsRemoveDefs(f, drop)
	}
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// jsRootIdent returns the leading identifier of an identifier path such as
// "ns.sub", or "" when s is not one (calls, indexing, this.x is "this").
func jsRootIdent(s string) string {
	if !jsIsIdentPath(s) {
		return ""
	}
	root, _, _ := strings.Cut(s, ".")
	return root
}

// jsReparentParamProps moves constructor parameter properties
// (constructor(public id: string)) from the constructor to its class.
func jsReparentParamProps(f *model.Facts) {
	for i := range f.Defs {
		d := &f.Defs[i]
		if d.Kind != model.KindField || d.Parent == model.NoParent {
			continue
		}
		if p := f.Defs[d.Parent]; p.Kind == model.KindConstructor {
			d.Parent = p.Parent
		}
	}
}

// jsMarkOverloads marks all but the last of same-named functions/methods in
// the same scope (overload signatures followed by the implementation,
// getter/setter pairs, redeclarations) for removal.
func jsMarkOverloads(f *model.Facts, drop []bool) bool {
	type key struct {
		parent int32
		kind   model.NodeKind
		name   string
		scope  string
	}
	last := map[key]int{}
	for i := range f.Defs {
		d := &f.Defs[i]
		if drop[i] || (d.Kind != model.KindFunction && d.Kind != model.KindMethod) {
			continue
		}
		last[key{d.Parent, d.Kind, d.Name, strings.Join(d.Scope, ".")}] = i
	}
	marked := false
	for i := range f.Defs {
		d := &f.Defs[i]
		if drop[i] || (d.Kind != model.KindFunction && d.Kind != model.KindMethod) {
			continue
		}
		if last[key{d.Parent, d.Kind, d.Name, strings.Join(d.Scope, ".")}] != i {
			drop[i], marked = true, true
		}
	}
	return marked
}

// jsRemoveDefs deletes the marked definitions, re-parenting their children
// and re-enclosing their references to the nearest surviving ancestor, and
// drops inheritance and test marks of removed definitions.
func jsRemoveDefs(f *model.Facts, drop []bool) {
	newIdx := make([]int32, len(f.Defs))
	out := make([]model.Def, 0, len(f.Defs))
	for i := range f.Defs {
		d := f.Defs[i]
		p := d.Parent
		if p != model.NoParent {
			p = newIdx[p] // parents precede children, already mapped
		}
		if drop[i] {
			newIdx[i] = p
			continue
		}
		d.Parent = p
		newIdx[i] = int32(len(out))
		out = append(out, d)
	}
	f.Defs = out
	mapIdx := func(i int32) int32 {
		if i == model.NoParent {
			return i
		}
		return newIdx[i]
	}
	for i := range f.Refs {
		f.Refs[i].Enclosing = mapIdx(f.Refs[i].Enclosing)
	}
	inh := f.Inherits[:0]
	for _, in := range f.Inherits {
		if drop[in.Child] {
			continue
		}
		in.Child = newIdx[in.Child]
		inh = append(inh, in)
	}
	f.Inherits = inh
	if len(f.Inherits) == 0 {
		f.Inherits = nil
	}
	tests := f.Tests[:0]
	for _, t := range f.Tests {
		if drop[t.Def] {
			continue
		}
		t.Def = newIdx[t.Def]
		tests = append(tests, t)
	}
	f.Tests = tests
	if len(f.Tests) == 0 {
		f.Tests = nil
	}
}

// jsBinding is one imported name: orig as exported by the target module,
// local as bound in the importing file.
type jsBinding struct{ orig, local string }

// jsBindings parses a captured import name list: a default import
// identifier ("React"), named imports or re-exports ("{ a, type B, c as d }")
// or a CommonJS object pattern ("{ add, multiply: mul, ...rest }").
func jsBindings(raw string) []jsBinding {
	raw = strings.TrimSpace(jsStripComments(raw))
	if !strings.HasPrefix(raw, "{") {
		if jsIsIdent(raw) {
			return []jsBinding{{raw, raw}}
		}
		return nil
	}
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "{"), "}")
	var out []jsBinding
	for _, item := range jsSplitTopLevel(raw) {
		item = strings.TrimSpace(item)
		item = strings.TrimSpace(strings.TrimPrefix(item, "type "))
		item = strings.TrimSpace(strings.TrimPrefix(item, "typeof "))
		if item == "" || strings.HasPrefix(item, "...") {
			continue
		}
		if i := strings.IndexByte(item, '='); i >= 0 { // default value
			item = strings.TrimSpace(item[:i])
		}
		orig, local := item, item
		if a, b, ok := strings.Cut(item, " as "); ok {
			orig, local = strings.TrimSpace(a), strings.TrimSpace(b)
		} else if a, b, ok := strings.Cut(item, ":"); ok {
			orig, local = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		if orig == "default" {
			orig = local
		}
		if !jsIsIdent(orig) || !jsIsIdent(local) {
			continue
		}
		out = append(out, jsBinding{orig, local})
	}
	return out
}

// jsSplitTopLevel splits s on commas outside brackets.
func jsSplitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// jsStripComments removes /* */ and // comments.
func jsStripComments(s string) string {
	if !strings.Contains(s, "/") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '/' && i+1 < len(s) {
			switch s[i+1] {
			case '*':
				end := strings.Index(s[i+2:], "*/")
				if end < 0 {
					return b.String()
				}
				i += end + 3
				b.WriteByte(' ')
				continue
			case '/':
				end := strings.IndexByte(s[i:], '\n')
				if end < 0 {
					return b.String()
				}
				i += end
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
