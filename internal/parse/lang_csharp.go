package parse

import (
	_ "embed"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tscs "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/csharp.scm
var csharpQuery string

// CSharpPartialQual is the qualifier of the synthetic embeds edge that links
// every part of a partial type to its sibling parts (see postProcess). It is
// not a valid C# identifier, so it never collides with a written qualifier.
const CSharpPartialQual = "<partial>"

// csharpSpec is the C# language spec. The module identity of a file is its
// project: the repo-relative path of the nearest enclosing .csproj (or "."
// without one). Namespaces are not definitions; they become the leading
// Scope segments of top-level types, so qnames read Namespace.Type.Member.
type csharpSpec struct{}

func init() { Register(csharpSpec{}) }

func (csharpSpec) Lang() model.Lang           { return model.LangCSharp }
func (csharpSpec) Extensions() []string       { return model.LangCSharp.Extensions() }
func (csharpSpec) Grammar() unsafe.Pointer    { return tscs.Language() }
func (csharpSpec) QuerySource() string        { return csharpQuery }
func (csharpSpec) ExtractorVersion() int      { return 1 }
func (csharpSpec) importSpec(s string) string { return csCleanName(s) }

// QName implements the indexer's QNamer: the container chain already starts
// with the namespace, so the project identity is left out.
func (csharpSpec) QName(module string, parts []string) string {
	if len(parts) == 0 {
		return module
	}
	return strings.Join(parts, ".")
}

// IsTestPath matches the usual C# test layouts: *Test.cs / *Tests.cs files
// and files below a test(s) directory or a *.Tests / *.Test project dir.
func (csharpSpec) IsTestPath(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	base := strings.TrimSuffix(path.Base(p), path.Ext(p))
	if strings.HasSuffix(base, "Test") || strings.HasSuffix(base, "Tests") {
		return true
	}
	segs := strings.Split(path.Dir(p), "/")
	for _, s := range segs {
		s = strings.ToLower(s)
		if s == "test" || s == "tests" {
			return true
		}
		// Project directories such as Shop.Tests, Shop.UnitTests.
		if i := strings.LastIndexByte(s, '.'); i >= 0 {
			if last := s[i+1:]; last == "test" || strings.HasSuffix(last, "tests") {
				return true
			}
		}
	}
	return false
}

// testTarget maps Test names to the tested member: "Add_ReturnsSum" ->
// "Add", "TestParse" -> "Parse", "ParseTests" -> "Parse".
func (csharpSpec) testTarget(name string) string {
	t := name
	for _, p := range []string{"Test", "test"} {
		if rest, ok := strings.CutPrefix(t, p); ok && rest != "" {
			t = rest
			break
		}
	}
	t = strings.TrimLeft(t, "_")
	if i := strings.IndexByte(t, '_'); i > 0 {
		t = t[:i]
	}
	for _, s := range []string{"Tests", "Test"} {
		if rest, ok := strings.CutSuffix(t, s); ok && rest != "" {
			t = rest
			break
		}
	}
	if t == "" {
		return name
	}
	return t
}

// isExported: public or protected members, plus interface and enum members
// (implicitly public).
func (csharpSpec) isExported(_ *model.Def, n *sitter.Node, src []byte) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c.Kind() != "modifier" {
			continue
		}
		switch c.Utf8Text(src) {
		case "public", "protected":
			return true
		}
	}
	if n.Kind() == "enum_member_declaration" {
		return true
	}
	if p := n.Parent(); p != nil && p.Kind() == "declaration_list" {
		if gp := p.Parent(); gp != nil && gp.Kind() == "interface_declaration" {
			return true
		}
	}
	return false
}

// csHeadStart is the first byte of a declaration after its attributes.
func csHeadStart(n *sitter.Node) uint {
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		switch c.Kind() {
		case "attribute_list", "preproc_if_in_attribute_list":
			continue
		}
		return c.StartByte()
	}
	return n.StartByte()
}

// signature renders declarations without attributes, bodies, accessors or
// initializers: "public partial class Invoice", "public decimal Total()",
// "protected decimal Fee", "private readonly List<decimal> _lines".
// Fields and properties keep "<modifiers> <type> <name>" so the resolver
// can read declared types back from the sig.
func (csharpSpec) signature(_ model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	text := func(a, b uint) string {
		if b < a {
			b = a
		}
		return strings.TrimRight(strings.TrimSpace(string(src[a:b])), ";")
	}
	start := csHeadStart(n)
	switch n.Kind() {
	case "namespace_declaration", "file_scoped_namespace_declaration":
		name := n.ChildByFieldName("name")
		if name == nil {
			return "", false
		}
		sig := "namespace " + name.Utf8Text(src)
		if n.Kind() == "file_scoped_namespace_declaration" {
			sig += ";"
		}
		return sig, true
	case "field_declaration", "event_field_declaration":
		var decl *sitter.Node
		for i := uint(0); i < n.NamedChildCount(); i++ {
			if c := n.NamedChild(i); c.Kind() == "variable_declaration" {
				decl = c
				break
			}
		}
		if decl == nil {
			return "", false
		}
		end := decl.StartByte()
		if t := decl.ChildByFieldName("type"); t != nil {
			end = t.EndByte()
		}
		var names []string
		for i := uint(0); i < decl.NamedChildCount(); i++ {
			c := decl.NamedChild(i)
			if c.Kind() != "variable_declarator" {
				continue
			}
			if nm := c.ChildByFieldName("name"); nm != nil {
				names = append(names, nm.Utf8Text(src))
			}
		}
		return text(start, end) + " " + strings.Join(names, ", "), true
	case "property_declaration", "event_declaration":
		if name := n.ChildByFieldName("name"); name != nil {
			return text(start, name.EndByte()), true
		}
	case "enum_member_declaration":
		return firstLine(text(n.StartByte(), n.EndByte())), true
	}
	end := n.EndByte()
	if body := n.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	}
	return text(start, end), true
}

// csCleanName strips whitespace and a leading "global::" from a name.
func csCleanName(s string) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), "")
	return strings.TrimPrefix(s, "global::")
}

// postProcess folds namespaces into scopes and applies C# rules the query
// cannot express (see the individual steps).
func (csharpSpec) postProcess(f *model.Facts) {
	csFoldNamespaces(f)
	csFixKinds(f)
	csFixInherits(f)
	csAddPartialEmbeds(f)
	csFixImports(f)
	csFixRefs(f)
}

// csFoldNamespaces removes namespace defs: their full dotted path becomes
// the leading Scope of the defs they contain, a file-scoped namespace
// applies to every later top-level def, and the first namespace becomes
// Facts.Package. Indices in refs, inherits and tests are remapped.
func csFoldNamespaces(f *model.Facts) {
	n := len(f.Defs)
	isNS := make([]bool, n)
	prefix := make([][]string, n)
	var (
		fileNS    []string
		fileNSEnd uint32
		pkg       []string
		found     bool
	)
	inFileNS := func(d *model.Def) bool {
		return fileNS != nil && d.Parent == model.NoParent && d.StartByte >= fileNSEnd
	}
	for i := range f.Defs {
		d := &f.Defs[i]
		if d.Kind != model.KindNamespace {
			continue
		}
		isNS[i], found = true, true
		var base []string
		switch {
		case d.Parent != model.NoParent && isNS[d.Parent]:
			base = prefix[d.Parent]
		case inFileNS(d):
			base = fileNS
		}
		prefix[i] = append(append([]string(nil), base...), splitScope(d.Name)...)
		if pkg == nil {
			pkg = prefix[i]
		}
		if fileNS == nil && d.Parent == model.NoParent && strings.HasSuffix(d.Sig, ";") {
			fileNS, fileNSEnd = prefix[i], d.EndByte
		}
	}
	if !found {
		return
	}
	f.Package = strings.Join(pkg, ".")

	// up maps an old index to the new index of its nearest non-namespace
	// ancestor-or-self.
	old := f.Defs
	newIdx := make([]int32, n)
	up := func(i int32) int32 {
		for i != model.NoParent && isNS[i] {
			i = old[i].Parent
		}
		if i == model.NoParent {
			return model.NoParent
		}
		return newIdx[i]
	}
	defs := make([]model.Def, 0, n)
	for i := range f.Defs {
		if isNS[i] {
			newIdx[i] = model.NoParent
			continue
		}
		d := f.Defs[i]
		var scope []string
		switch {
		case d.Parent != model.NoParent && isNS[d.Parent]:
			scope = prefix[d.Parent]
		case inFileNS(&d):
			scope = fileNS
		}
		if len(scope) > 0 {
			d.Scope = append(append([]string(nil), scope...), d.Scope...)
		}
		d.Parent = up(d.Parent)
		newIdx[i] = int32(len(defs))
		defs = append(defs, d)
	}
	f.Defs = defs

	for i := range f.Refs {
		f.Refs[i].Enclosing = up(f.Refs[i].Enclosing)
	}
	inh := f.Inherits[:0]
	for _, in := range f.Inherits {
		if in.Child = up(in.Child); in.Child != model.NoParent {
			inh = append(inh, in)
		}
	}
	f.Inherits = inh
	tests := f.Tests[:0]
	for _, t := range f.Tests {
		if t.Def = up(t.Def); t.Def != model.NoParent {
			tests = append(tests, t)
		}
	}
	f.Tests = tests
}

// csFixKinds turns record structs into structs and const fields into
// constants.
func csFixKinds(f *model.Facts) {
	for i := range f.Defs {
		d := &f.Defs[i]
		switch d.Kind {
		case model.KindClass:
			if csHasWordPair(d.Sig, "record", "struct") {
				d.Kind = model.KindStruct
			}
		case model.KindField:
			if csHasModifier(d.Sig, "const") {
				d.Kind = model.KindConstant
			}
		}
	}
}

// csFixInherits: the base list of a class or record names at most one base
// class, first. It is an extends edge unless it looks like an interface
// (IName); everything else stays implements.
func csFixInherits(f *model.Facts) {
	seen := map[int32]bool{}
	for i := range f.Inherits {
		in := &f.Inherits[i]
		if seen[in.Child] {
			continue
		}
		seen[in.Child] = true
		if f.Defs[in.Child].Kind == model.KindClass && in.Kind == model.EdgeImplements && !csLooksLikeInterface(in.Name) {
			in.Kind = model.EdgeExtends
		}
	}
	if len(f.Inherits) == 0 {
		f.Inherits = nil
	}
}

// csAddPartialEmbeds links each top-level partial type to its other parts
// with an embeds edge named after itself and qualified CSharpPartialQual;
// the resolver resolves it to the sibling parts, which makes members of
// all parts reachable through the ordinary ancestor walk.
func csAddPartialEmbeds(f *model.Facts) {
	for i := range f.Defs {
		d := &f.Defs[i]
		if d.Parent != model.NoParent || !d.Kind.IsType() || !CSharpIsPartialSig(d.Sig) {
			continue
		}
		f.Inherits = append(f.Inherits, model.Inherit{
			Child: int32(i), Kind: model.EdgeEmbeds, Name: d.Name, Qualifier: CSharpPartialQual,
			Line: d.StartLine, Col: d.StartCol,
		})
	}
}

// csFixImports marks namespace usings as wildcard imports. The resolver
// reads wildcards from Names ("*"), next to the "global"/"static" markers
// captured by the query.
func csFixImports(f *model.Facts) {
	for i := range f.Imports {
		im := &f.Imports[i]
		if im.Wildcard {
			im.Names = append([]string{"*"}, im.Names...)
		}
	}
}

// csFixRefs drops file-level refs (usings, namespace names) and nameof().
func csFixRefs(f *model.Facts) {
	refs := f.Refs[:0]
	for _, r := range f.Refs {
		if r.Enclosing == model.NoParent {
			continue
		}
		if r.Kind == model.RefCall && r.Receiver == "" && r.Name == "nameof" {
			continue
		}
		refs = append(refs, r)
	}
	f.Refs = refs
}

// CSharpIsPartialSig reports whether a type signature carries the partial
// modifier ("public partial class X").
func CSharpIsPartialSig(sig string) bool {
	for _, w := range strings.Fields(sig) {
		switch w {
		case "partial":
			return true
		case "class", "struct", "interface", "record", "enum":
			return false
		}
	}
	return false
}

func csHasModifier(sig, mod string) bool {
	for _, w := range strings.Fields(sig) {
		if w == mod {
			return true
		}
	}
	return false
}

func csHasWordPair(sig, a, b string) bool {
	ws := strings.Fields(sig)
	for i := 0; i+1 < len(ws); i++ {
		if ws[i] == a && ws[i+1] == b {
			return true
		}
	}
	return false
}

// csLooksLikeInterface applies the IName convention.
func csLooksLikeInterface(name string) bool {
	if len(name) < 2 || name[0] != 'I' {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name[1:])
	return unicode.IsUpper(r)
}

// ModuleOf returns the project of the file: the nearest .csproj up the
// directory tree, or "." when the file belongs to no project.
func (csharpSpec) ModuleOf(p string, _ *model.Facts, ctx model.RepoContext) string {
	if proj := csProjectOf(ctx, path.Dir(model.CleanRel(p))); proj != "" {
		return proj
	}
	return "."
}

// csBCLRoots are namespace roots that never live in a repository.
var csBCLRoots = map[string]bool{"System": true, "Microsoft": true, "Windows": true}

// ResolveImport maps a using to the project that declares the namespace:
// the importing file's own project when the namespace starts with its name
// (file name, RootNamespace or AssemblyName), otherwise a sibling project
// laid out as <dir>/<Name>/<Name>.csproj or <dir>/src/<Name>/<Name>.csproj
// next to an ancestor directory. System.* and Microsoft.* are external.
func (csharpSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	spec = csCleanName(spec)
	if i := strings.IndexByte(spec, '<'); i >= 0 {
		spec = spec[:i]
	}
	root, _, _ := strings.Cut(spec, ".")
	if csBCLRoots[root] {
		return model.ImportTarget{Kind: model.TargetExternal, Module: spec}
	}
	if ctx == nil || spec == "" {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	start := model.CleanRel(path.Dir(model.CleanRel(fromFile)))
	if own := csProjectOf(ctx, start); own != "" {
		for _, name := range csProjectNames(ctx, own) {
			if spec == name || strings.HasPrefix(spec, name+".") {
				return model.ImportTarget{Kind: model.TargetModule, Path: model.CleanRel(path.Dir(own)), Module: own}
			}
		}
	}
	segs := strings.Split(spec, ".")
	for dir := start; ; dir = csParentDir(dir) {
		for k := len(segs); k > 0; k-- {
			name := strings.Join(segs[:k], ".")
			for _, cand := range []string{path.Join(dir, name, name+".csproj"), path.Join(dir, "src", name, name+".csproj")} {
				if ctx.FileExists(cand) {
					return model.ImportTarget{Kind: model.TargetModule, Path: path.Dir(cand), Module: cand}
				}
			}
		}
		if dir == "" {
			break
		}
	}
	return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
}

// csProjectOf returns the first .csproj (by name) in dir or its nearest
// ancestor that has one.
func csProjectOf(ctx model.RepoContext, dir string) string {
	if ctx == nil {
		return ""
	}
	dir = model.CleanRel(dir)
	for {
		for _, name := range ctx.ListDir(dir) {
			if strings.HasSuffix(strings.ToLower(name), ".csproj") {
				return path.Join(dir, name)
			}
		}
		if dir == "" {
			return ""
		}
		dir = csParentDir(dir)
	}
}

func csParentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// csProjectNames returns the namespace roots a project declares: its file
// name and its RootNamespace / AssemblyName properties.
func csProjectNames(ctx model.RepoContext, proj string) []string {
	base := path.Base(proj)
	names := []string{base[:len(base)-len(path.Ext(base))]}
	b, err := ctx.ReadFile(proj)
	if err != nil {
		return names
	}
	s := string(b)
	for _, tag := range []string{"RootNamespace", "AssemblyName"} {
		open, end := "<"+tag+">", "</"+tag+">"
		i := strings.Index(s, open)
		if i < 0 {
			continue
		}
		rest := s[i+len(open):]
		j := strings.Index(rest, end)
		if j < 0 {
			continue
		}
		if v := strings.TrimSpace(rest[:j]); v != "" && !strings.ContainsAny(v, "$(") {
			names = append(names, v)
		}
	}
	return names
}
