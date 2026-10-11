package parse

import (
	_ "embed"
	"encoding/json"
	"path"
	"reflect"
	"strings"
	"sync"
	"time"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/c.scm
var cQuery string

// cSpec is the C language spec. It shares most of its behavior with C++
// (lang_cpp.go) through the cfam* helpers in this file.
//
// Module identity is the C/C++ project a file belongs to (see
// cfamModuleOf), so a header and its implementation share a module even
// when they live in include/ and src/. Qualified names do not repeat it:
// C has a single global namespace, so the qname of vec_add is "vec_add".
//
// Files ending in .h are C (model.LangC). C++ code in .h headers is parsed
// with the C grammar and therefore extracted best-effort only; projects
// should prefer .hpp/.hh for C++ headers.
type cSpec struct{}

func init() { Register(cSpec{}) }

func (cSpec) Lang() model.Lang             { return model.LangC }
func (cSpec) Extensions() []string         { return model.LangC.Extensions() }
func (cSpec) Grammar() unsafe.Pointer      { return tsc.Language() }
func (cSpec) QuerySource() string          { return cQuery }
func (cSpec) ExtractorVersion() int        { return 1 }
func (cSpec) IsTestPath(p string) bool     { return cfamIsTestPath(p) }
func (cSpec) importSpec(raw string) string { return cfamImportSpec(raw) }
func (cSpec) postProcess(f *model.Facts)   { cfamPostProcess(f, false) }

// QName joins the container chain with "." and omits the module.
func (cSpec) QName(_ string, parts []string) string { return strings.Join(parts, ".") }

func (cSpec) isExported(d *model.Def, n *sitter.Node, src []byte) bool {
	return cfamRefine(d, n, src, false)
}

func (cSpec) signature(k model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	return cfamSignature(k, n, src)
}

// ModuleOf implements model.LanguageSpec.
func (cSpec) ModuleOf(p string, _ *model.Facts, ctx model.RepoContext) string {
	return cfamModuleOf(p, ctx)
}

// ResolveImport implements model.LanguageSpec.
func (cSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	return cfamResolveImport(spec, fromFile, ctx)
}

// ---------------------------------------------------------------- tests

// cfamTestDirs are directory names whose C/C++ files are tests.
var cfamTestDirs = map[string]bool{"test": true, "tests": true, "unittest": true, "unittests": true, "testing": true}

// cfamIsTestPath recognizes test_x.c, x_test.cc, x_tests.cpp,
// x_unittest.cc, xTest.cpp and files under test/ or tests/.
func cfamIsTestPath(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	dir, base := path.Split(p)
	for _, seg := range strings.Split(strings.Trim(dir, "/"), "/") {
		if cfamTestDirs[strings.ToLower(seg)] {
			return true
		}
	}
	stem := strings.TrimSuffix(base, path.Ext(base))
	lower := strings.ToLower(stem)
	return strings.HasPrefix(lower, "test_") ||
		strings.HasSuffix(lower, "_test") || strings.HasSuffix(lower, "_tests") ||
		strings.HasSuffix(lower, "_unittest") || strings.HasSuffix(lower, "-test") ||
		(strings.HasSuffix(stem, "Test") && len(stem) > 4) || (strings.HasSuffix(stem, "Tests") && len(stem) > 5)
}

// cfamTestHeaders mark a file as a test file when included.
var cfamTestHeaders = []string{
	"gtest/gtest.h", "gmock/gmock.h", "catch2/", "catch.hpp", "doctest", "unity.h",
	"cmocka.h", "check.h", "criterion/", "CUnit/", "boost/test/", "cppunit/",
}

// ---------------------------------------------------------------- imports

// cfamImportSpec keeps include delimiters ("x.h" / <x.h>) and compacts
// using-directives captured whole ("using namespace a::b;").
func cfamImportSpec(raw string) string {
	s := compactText(raw, maxExprText*2)
	return strings.TrimSpace(strings.TrimSuffix(s, ";"))
}

// cfamExternalNS are namespaces that never live in the repository.
var cfamExternalNS = map[string]bool{"std": true, "__gnu_cxx": true, "__cxxabiv1": true}

// cfamDecodeUsing turns the compacted text of a using-directive,
// using-declaration or namespace alias into an import:
//
//	using namespace a::b   -> Spec "a::b", wildcard (Names ["*"])
//	using std::cout        -> Spec "std", Names ["cout"] (external roots only)
//	using a::B             -> Spec "a::B"
//	namespace fs = std::fs -> Spec "std::fs", Alias "fs"
//
// Includes ("x.h", <x.h>) and anything unrecognized are returned unchanged.
func cfamDecodeUsing(im model.Import) (model.Import, bool) {
	s := strings.TrimSpace(im.Spec)
	switch {
	case strings.HasPrefix(s, "using namespace "):
		ns := cfamCleanQual(strings.TrimPrefix(s, "using namespace "))
		if ns == "" {
			return im, false
		}
		im.Spec, im.Wildcard, im.Names = ns, true, []string{"*"}
	case strings.HasPrefix(s, "using "):
		q := strings.TrimSpace(strings.TrimPrefix(s, "using "))
		q = strings.TrimSpace(strings.TrimPrefix(q, "typename "))
		q = cfamCleanQual(q)
		i := strings.LastIndex(q, "::")
		if q == "" || i <= 0 {
			return im, false
		}
		im.Spec = q
		if root, _, _ := strings.Cut(q, "::"); cfamExternalNS[root] {
			im.Spec, im.Names = q[:i], []string{q[i+2:]}
		}
	case strings.HasPrefix(s, "namespace "):
		alias, target, ok := strings.Cut(strings.TrimPrefix(s, "namespace "), "=")
		alias, target = strings.TrimSpace(alias), cfamCleanQual(target)
		if !ok || alias == "" || target == "" {
			return im, false
		}
		im.Spec, im.Alias = target, alias
	}
	return im, true
}

// cfamCleanQual strips template arguments, the whitespace around "::" and
// a leading "::" from a qualified name: ":: a::B<int>::c" -> "a::B::c".
func cfamCleanQual(q string) string {
	return strings.Join(cfamSplitQualified(q), "::")
}

// cfamStripTemplateArgs removes balanced <...> groups (but not operator<, <<).
func cfamStripTemplateArgs(s string) string {
	if !strings.ContainsRune(s, '<') {
		return s
	}
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '<' && depth == 0 && (strings.HasSuffix(s[:i], "operator") || strings.HasSuffix(s[:i], "operator<")):
			b.WriteByte(c)
		case c == '<':
			depth++
		case c == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// cfamSplitQualified splits a C++ qualified name on "::" outside template
// arguments and strips the arguments: "a::B<x::y>::c" -> [a B c].
func cfamSplitQualified(q string) []string {
	var out []string
	for _, p := range strings.Split(cfamStripTemplateArgs(q), "::") {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------- refine

// cfamFlagDrop marks a definition that postProcess removes (friend
// declarations). It never leaves this package.
const cfamFlagDrop model.DefFlags = 1 << 15

// cfamRefine is the exportChecker hook. Being the only hook that sees the
// syntax node of each definition, it also sets the declaration, static and
// abstract flags and turns const variables into constants.
func cfamRefine(d *model.Def, n *sitter.Node, src []byte, cpp bool) bool {
	kind := n.Kind()
	static := cfamHasSpecifier(n, src, "storage_class_specifier", "static")
	if static {
		d.Flags |= model.FlagStatic
	}
	switch kind {
	case "declaration", "field_declaration":
		switch d.Kind {
		case model.KindFunction:
			d.Flags |= model.FlagDeclaration
			if v := n.ChildByFieldName("default_value"); v != nil && strings.TrimSpace(cfamText(v, src)) == "0" {
				d.Flags |= model.FlagAbstract
			}
		case model.KindVariable:
			if cfamHasSpecifier(n, src, "storage_class_specifier", "extern") && !cfamHasChild(n, "init_declarator") {
				d.Flags |= model.FlagDeclaration
			}
			if (cfamHasSpecifier(n, src, "type_qualifier", "const") || cfamHasSpecifier(n, src, "type_qualifier", "constexpr")) &&
				!cfamDeclaresPointer(n) {
				d.Kind = model.KindConstant
			}
		}
		if p := n.Parent(); p != nil && p.Kind() == "friend_declaration" {
			d.Flags |= cfamFlagDrop
		}
	}
	if !cpp {
		switch d.Kind {
		case model.KindFunction, model.KindVariable, model.KindConstant:
			return !static
		}
		return true
	}
	if cppInAnonymousNamespace(n) {
		return false
	}
	if access, ok := cppMemberAccess(n, src); ok {
		return access == "public"
	}
	switch d.Kind {
	case model.KindFunction, model.KindVariable, model.KindConstant:
		return !static
	}
	return true
}

func cfamText(n *sitter.Node, src []byte) string { return string(src[n.StartByte():n.EndByte()]) }

// cfamHasSpecifier reports whether n has a direct child of kind whose text is
// word (e.g. storage_class_specifier "static").
func cfamHasSpecifier(n *sitter.Node, src []byte, kind, word string) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == kind && strings.TrimSpace(cfamText(c, src)) == word {
			return true
		}
	}
	return false
}

// cfamDeclaresPointer reports whether a declaration declares a pointer
// (const char *p is a pointer to const, not a constant).
func cfamDeclaresPointer(n *sitter.Node) bool {
	d := n.ChildByFieldName("declarator")
	if d != nil && d.Kind() == "init_declarator" {
		d = d.ChildByFieldName("declarator")
	}
	return d != nil && d.Kind() == "pointer_declarator"
}

func cfamHasChild(n *sitter.Node, kind string) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c != nil && c.Kind() == kind {
			return true
		}
	}
	return false
}

// cppInAnonymousNamespace reports whether n is nested in namespace { ... }.
func cppInAnonymousNamespace(n *sitter.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "namespace_definition" && p.ChildByFieldName("name") == nil {
			return true
		}
	}
	return false
}

// cppMemberAccess returns the access level of a class member declaration:
// the nearest preceding access specifier, else the default of the
// enclosing class (private) or struct/union (public). ok is false for
// non-members.
func cppMemberAccess(n *sitter.Node, src []byte) (string, bool) {
	item := n
	if p := item.Parent(); p != nil && p.Kind() == "template_declaration" {
		item = p
	}
	list := item.Parent()
	if list == nil || list.Kind() != "field_declaration_list" {
		return "", false
	}
	for s := item.PrevNamedSibling(); s != nil; s = s.PrevNamedSibling() {
		if s.Kind() == "access_specifier" {
			return strings.TrimSpace(strings.TrimSuffix(cfamText(s, src), ":")), true
		}
	}
	if owner := list.Parent(); owner != nil && owner.Kind() == "class_specifier" {
		return "private", true
	}
	return "public", true
}

// ---------------------------------------------------------------- signatures

// cfamSignature renders signatures without bodies, initializers or
// trailing semicolons.
func cfamSignature(k model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	text := func(a, b uint) string { return string(src[a:b]) }
	switch n.Kind() {
	case "preproc_def":
		return strings.TrimSpace(firstLine(cfamText(n, src))), true
	case "preproc_function_def":
		end := n.EndByte()
		if p := n.ChildByFieldName("parameters"); p != nil {
			end = p.EndByte()
		}
		return text(n.StartByte(), end), true
	case "function_definition":
		end := n.EndByte()
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Kind() {
			case "compound_statement", "field_initializer_list", "default_method_clause",
				"delete_method_clause", "try_statement":
				end = min(end, c.StartByte())
			}
		}
		return strings.TrimSpace(text(n.StartByte(), end)), true
	case "declaration", "field_declaration":
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cfamText(n, src)), ";"))
		if typ := n.ChildByFieldName("type"); typ != nil && typ.ChildByFieldName("body") != nil {
			// struct { ... } x: keep the keyword, drop the body.
			if decl := n.ChildByFieldName("declarator"); decl != nil {
				return cfamKeyword(typ) + " " + cfamText(decl, src), true
			}
		}
		if k != model.KindFunction && k != model.KindMethod && k != model.KindConstructor {
			s = firstLine(s)
			if i := strings.Index(s, "="); i > 0 {
				s = strings.TrimSpace(s[:i])
			}
			s = strings.TrimSuffix(strings.TrimSpace(s), "{")
		}
		return s, true
	case "type_definition":
		typ, decl := n.ChildByFieldName("type"), n.ChildByFieldName("declarator")
		if typ != nil && decl != nil && typ.ChildByFieldName("body") != nil {
			return "typedef " + cfamKeyword(typ) + " " + cfamText(decl, src), true
		}
		return strings.TrimSuffix(strings.TrimSpace(cfamText(n, src)), ";"), true
	case "type_identifier":
		// Alias of a named struct with a body: the def node is the name.
		if p := n.Parent(); p != nil && p.Kind() == "type_definition" {
			if typ := p.ChildByFieldName("type"); typ != nil {
				name := ""
				if tn := typ.ChildByFieldName("name"); tn != nil {
					name = " " + cfamText(tn, src)
				}
				return "typedef " + cfamKeyword(typ) + name + " " + cfamText(n, src), true
			}
		}
	case "alias_declaration":
		return strings.TrimSuffix(strings.TrimSpace(cfamText(n, src)), ";"), true
	}
	return "", false
}

// cfamKeyword maps struct_specifier -> "struct" etc.
func cfamKeyword(n *sitter.Node) string {
	return strings.TrimSuffix(n.Kind(), "_specifier")
}

// ---------------------------------------------------------------- post-process

// cfamPostProcess applies the C/C++ fixups described in queries/c.scm and
// queries/cpp.scm.
func cfamPostProcess(f *model.Facts, cpp bool) {
	if cpp {
		cppPostProcessDefs(f)
	}
	cfamDropDefs(f)
	if cpp {
		cppPlaceOutOfLine(f)
		cppPostProcessRefs(f)
		cppPostProcessTests(f)
	}
	// File-scope uses of names (include guards, #if conditions) are not
	// stored by the indexer and only bloat the facts.
	refs := f.Refs[:0]
	for _, r := range f.Refs {
		if r.Kind == model.RefMember && r.Enclosing == model.NoParent && r.Receiver == "" {
			continue
		}
		refs = append(refs, r)
	}
	f.Refs = refs
	for i := range f.Inherits {
		f.Inherits[i].Qualifier = strings.TrimPrefix(f.Inherits[i].Qualifier, "::")
	}
	imps := f.Imports[:0]
	for _, im := range f.Imports {
		if cpp {
			var ok bool
			if im, ok = cfamDecodeUsing(im); !ok {
				continue
			}
		}
		for _, h := range cfamTestHeaders {
			if strings.Contains(im.Spec, h) {
				f.TestFile = true
			}
		}
		imps = append(imps, im)
	}
	f.Imports = imps
	if len(f.Imports) == 0 {
		f.Imports = nil
	}
}

// cfamDropDefs removes definitions that are not symbols of the file:
// locals (anything nested in a function body), fields outside a type,
// friend declarations, and everything nested in a removed definition.
// References inside a removed definition move to its nearest kept
// ancestor; inheritance and test marks of removed definitions vanish.
func cfamDropDefs(f *model.Facts) {
	n := len(f.Defs)
	drop := make([]bool, n)
	dropped := false
	for i := range f.Defs {
		d := &f.Defs[i]
		p := d.Parent
		switch {
		case d.Flags.Has(cfamFlagDrop):
			drop[i] = true
		case p >= 0 && int(p) < i && (drop[p] || f.Defs[p].Kind.IsCallable()):
			drop[i] = true
		case d.Kind == model.KindField && (p < 0 || !f.Defs[p].Kind.IsType() && f.Defs[p].Kind != model.KindField):
			drop[i] = true
		}
		dropped = dropped || drop[i]
	}
	if !dropped {
		return
	}
	newIdx := make([]int32, n)
	defs := make([]model.Def, 0, n)
	for i := range f.Defs {
		if drop[i] {
			newIdx[i] = model.NoParent
			continue
		}
		d := f.Defs[i]
		if d.Parent >= 0 {
			d.Parent = newIdx[d.Parent]
		}
		newIdx[i] = int32(len(defs))
		defs = append(defs, d)
	}
	// kept maps an old index to its nearest kept ancestor (or itself).
	kept := func(i int32) int32 {
		for guard := 0; i >= 0 && int(i) < n && guard <= n; guard++ {
			if !drop[i] {
				return newIdx[i]
			}
			i = f.Defs[i].Parent
		}
		return model.NoParent
	}
	for i := range f.Refs {
		f.Refs[i].Enclosing = kept(f.Refs[i].Enclosing)
	}
	inh := f.Inherits[:0]
	for _, in := range f.Inherits {
		if in.Child >= 0 && int(in.Child) < n && !drop[in.Child] {
			in.Child = newIdx[in.Child]
			inh = append(inh, in)
		}
	}
	f.Inherits = inh
	tests := f.Tests[:0]
	for _, t := range f.Tests {
		if t.Def >= 0 && int(t.Def) < n && !drop[t.Def] {
			t.Def = newIdx[t.Def]
			tests = append(tests, t)
		}
	}
	f.Tests = tests
	f.Defs = defs
	if len(f.Inherits) == 0 {
		f.Inherits = nil
	}
	if len(f.Tests) == 0 {
		f.Tests = nil
	}
}

// ---------------------------------------------------------------- modules

// cfamBuildMarkers are files that mark the root of a C/C++ project.
var cfamBuildMarkers = []string{
	"CMakeLists.txt", "Makefile", "makefile", "GNUmakefile", "meson.build",
	"configure.ac", "configure.in", "compile_commands.json", "BUILD.bazel", "BUILD",
	"SConstruct", "conanfile.txt", "conanfile.py", "vcpkg.json", "xmake.lua", "premake5.lua",
}

// cfamLayoutDirs are conventional top-level source directories of a C/C++
// project; the directory containing the first of them is the project root.
var cfamLayoutDirs = map[string]bool{
	"src": true, "source": true, "sources": true, "include": true, "inc": true,
	"lib": true, "libs": true, "test": true, "tests": true, "testing": true,
}

// cfamModuleOf returns the project directory of a C/C++ file ("." for the
// repository root), so headers and implementations share a module:
//
//  1. the outermost ancestor directory holding a build file (CMakeLists.txt,
//     Makefile, meson.build, compile_commands.json, ...);
//  2. otherwise the directory above the first conventional source directory
//     (src/, include/, lib/, tests/, ...);
//  3. otherwise the file's directory.
func cfamModuleOf(p string, ctx model.RepoContext) string {
	dir := model.CleanRel(path.Dir(model.CleanRel(p)))
	if ctx != nil {
		root, found := "", false
		for d := dir; ; d = cfamParentDir(d) {
			for _, m := range cfamBuildMarkers {
				if ctx.FileExists(path.Join(d, m)) {
					root, found = d, true
					break
				}
			}
			if d == "" {
				break
			}
		}
		if found {
			return cfamDotRoot(root)
		}
	}
	if dir != "" {
		segs := strings.Split(dir, "/")
		for i, s := range segs {
			if cfamLayoutDirs[s] {
				return cfamDotRoot(strings.Join(segs[:i], "/"))
			}
		}
	}
	return cfamDotRoot(dir)
}

func cfamParentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func cfamDotRoot(d string) string {
	if d == "" {
		return "."
	}
	return d
}

// cfamResolveImport resolves includes:
//
//   - "x.h" searches the including file's directory, then the include
//     directories, then the including directory's ancestors;
//   - <x.h> searches only the include directories and is external when not
//     found in the repository;
//   - include directories are the -I/-iquote/-isystem directories of a
//     compile_commands.json (nearest to the file, or build/) and every
//     ancestor's include/ directory, nearest first.
//
// C++ using-directives name namespaces: std and friends are external,
// anything else is unresolved (namespaces are not files or directories).
func cfamResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	s := strings.TrimSpace(spec)
	var inner string
	local := false
	switch {
	case len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"':
		inner, local = s[1:len(s)-1], true
	case len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>':
		inner = s[1 : len(s)-1]
	default:
		root, _, _ := strings.Cut(cfamCleanQual(s), "::")
		if cfamExternalNS[root] {
			return model.ImportTarget{Kind: model.TargetExternal, Module: s}
		}
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: s}
	}
	inner = strings.TrimSpace(strings.ReplaceAll(inner, `\`, "/"))
	if inner == "" || strings.HasPrefix(inner, "/") {
		return model.ImportTarget{Kind: model.TargetExternal, Module: inner}
	}
	if ctx != nil {
		fromDir := model.CleanRel(path.Dir(model.CleanRel(fromFile)))
		try := func(dir string) (model.ImportTarget, bool) {
			joined := path.Join(dir, inner)
			if strings.HasPrefix(joined, "../") || joined == ".." {
				return model.ImportTarget{}, false
			}
			cand := model.CleanRel(joined)
			if cand == "" || !ctx.FileExists(cand) {
				return model.ImportTarget{}, false
			}
			return model.ImportTarget{Kind: model.TargetFile, Path: cand, Module: cfamModuleOf(cand, ctx)}, true
		}
		if local {
			if t, ok := try(fromDir); ok {
				return t
			}
		}
		for _, d := range cfamCompileIncludeDirs(ctx, fromDir) {
			if t, ok := try(d); ok {
				return t
			}
		}
		for d := fromDir; ; d = cfamParentDir(d) {
			if t, ok := try(path.Join(d, "include")); ok {
				return t
			}
			if d == "" {
				break
			}
		}
		if local {
			for d := cfamParentDir(fromDir); fromDir != ""; d = cfamParentDir(d) {
				if t, ok := try(d); ok {
					return t
				}
				if d == "" {
					break
				}
			}
		}
	}
	if local {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: inner}
	}
	return model.ImportTarget{Kind: model.TargetExternal, Module: inner}
}

// ---------------------------------------------------------------- compile_commands.json

// cfamCCCacheTTL bounds how long parsed include directories are reused, so a
// long-running process notices an edited compile_commands.json.
const cfamCCCacheTTL = 5 * time.Second

// cfamCCMaxEntries caps the compile commands inspected per file.
const cfamCCMaxEntries = 20000

type cfamCCKey struct {
	ctx  any
	path string
}

type cfamCCEntry struct {
	dirs []string
	at   time.Time
}

var cfamCCCache = struct {
	sync.Mutex
	m map[cfamCCKey]cfamCCEntry
}{m: map[cfamCCKey]cfamCCEntry{}}

// cfamCompileIncludeDirs returns the repo-relative include directories listed
// in the compile_commands.json nearest to dir (or build/ at the root).
func cfamCompileIncludeDirs(ctx model.RepoContext, dir string) []string {
	file, ok := model.FindUp(ctx, dir, "compile_commands.json")
	if !ok {
		if !ctx.FileExists("build/compile_commands.json") {
			return nil
		}
		file = "build/compile_commands.json"
	}
	cacheable := reflect.TypeOf(ctx).Comparable()
	key := cfamCCKey{path: file}
	if cacheable {
		key.ctx = ctx
		cfamCCCache.Lock()
		e, hit := cfamCCCache.m[key]
		cfamCCCache.Unlock()
		if hit && time.Since(e.at) < cfamCCCacheTTL {
			return e.dirs
		}
	}
	b, err := ctx.ReadFile(file)
	var dirs []string
	if err == nil {
		dirs = cfamParseCompileCommands(b, ctx)
	}
	if cacheable {
		cfamCCCache.Lock()
		if len(cfamCCCache.m) > 64 {
			clear(cfamCCCache.m)
		}
		cfamCCCache.m[key] = cfamCCEntry{dirs: dirs, at: time.Now()}
		cfamCCCache.Unlock()
	}
	return dirs
}

type cfamCCCommand struct {
	Directory string   `json:"directory"`
	File      string   `json:"file"`
	Command   string   `json:"command"`
	Arguments []string `json:"arguments"`
}

// cfamParseCompileCommands extracts include directories and maps them to
// repo-relative paths. The repository's absolute location is unknown, so
// it is derived from the first entry whose absolute file path ends in an
// existing repo-relative path. Malformed input yields nil.
func cfamParseCompileCommands(b []byte, ctx model.RepoContext) []string {
	var cmds []cfamCCCommand
	if json.Unmarshal(b, &cmds) != nil {
		return nil
	}
	if len(cmds) > cfamCCMaxEntries {
		cmds = cmds[:cfamCCMaxEntries]
	}
	root, ok := "", false
	for _, c := range cmds {
		if root, ok = cfamRepoRootOf(c, ctx); ok {
			break
		}
	}
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range cmds {
		args := c.Arguments
		if len(args) == 0 {
			args = strings.Fields(c.Command)
		}
		for i := 0; i < len(args); i++ {
			a := args[i]
			var dir string
			for _, flag := range []string{"-I", "-iquote", "-isystem", "-idirafter"} {
				if a == flag && i+1 < len(args) {
					dir = args[i+1]
					i++
					break
				}
				if strings.HasPrefix(a, flag) && len(a) > len(flag) {
					dir = a[len(flag):]
					break
				}
			}
			if dir == "" {
				continue
			}
			dir = strings.Trim(dir, `"'`)
			if !path.IsAbs(dir) {
				dir = path.Join(c.Directory, dir)
			}
			rel, inside := strings.CutPrefix(path.Clean(dir), root)
			if !inside || (rel != "" && rel[0] != '/') {
				continue
			}
			rel = model.CleanRel(strings.TrimPrefix(rel, "/"))
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	return out
}

// cfamRepoRootOf derives the absolute repository root from a compile command
// whose source file exists in the repository.
func cfamRepoRootOf(c cfamCCCommand, ctx model.RepoContext) (string, bool) {
	file := c.File
	if file == "" {
		return "", false
	}
	if !path.IsAbs(file) {
		file = path.Join(c.Directory, file)
	}
	file = path.Clean(file)
	for i := 0; i < len(file); i++ {
		if file[i] != '/' {
			continue
		}
		rel := file[i+1:]
		if rel != "" && ctx.FileExists(rel) {
			if i == 0 {
				return "", true
			}
			return file[:i], true
		}
	}
	return "", false
}
