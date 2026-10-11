package parse

import (
	_ "embed"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/java.scm
var javaQuery string

// javaSpec is the Java language spec. Module identity is the declared
// package ("" for the default package); qnames are package + class chain.
type javaSpec struct{}

func init() { Register(javaSpec{}) }

func (javaSpec) Lang() model.Lang        { return model.LangJava }
func (javaSpec) Extensions() []string    { return model.LangJava.Extensions() }
func (javaSpec) Grammar() unsafe.Pointer { return tsjava.Language() }
func (javaSpec) QuerySource() string     { return javaQuery }
func (javaSpec) ExtractorVersion() int   { return 1 }

// IsTestPath implements TestPathMatcher: Maven/Gradle test source sets and
// the common test class naming conventions.
func (javaSpec) IsTestPath(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(p, "src/test/") || strings.Contains(p, "/src/test/") {
		return true
	}
	base := strings.TrimSuffix(path.Base(p), ".java")
	for _, s := range []string{"Test", "Tests", "TestCase", "IT"} {
		if strings.HasSuffix(base, s) && len(base) > len(s) {
			return true
		}
	}
	return strings.HasPrefix(base, "Test") && len(base) > 4 && isUpperRune(base[4:])
}

// testTarget maps JUnit 3 style names to the tested member
// (testFetch -> fetch, test_fetch -> fetch). Descriptive JUnit 4/5 names
// carry no hint, so they get none (no tests edge).
func (javaSpec) testTarget(name string) string {
	rest, ok := strings.CutPrefix(name, "test")
	if !ok {
		return ""
	}
	rest = strings.TrimLeft(rest, "_")
	r, n := utf8.DecodeRuneInString(rest)
	if n == 0 || (!unicode.IsUpper(r) && !strings.HasPrefix(name, "test_")) {
		return ""
	}
	return string(unicode.ToLower(r)) + rest[n:]
}

func isUpperRune(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

// isExported: public declarations, interface/annotation members (implicitly
// public) and enum constants.
func (javaSpec) isExported(_ *model.Def, n *sitter.Node, src []byte) bool {
	switch n.Kind() {
	case "enum_constant":
		return true
	case "formal_parameter": // record component: public accessor
		return true
	}
	if javaHasModifier(n, src, "public") {
		return true
	}
	if p := n.Parent(); p != nil {
		switch p.Kind() {
		case "interface_body", "annotation_type_body":
			return !javaHasModifier(n, src, "private")
		}
	}
	return false
}

func javaModifiers(n *sitter.Node) *sitter.Node {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c != nil && c.Kind() == "modifiers" {
			return c
		}
	}
	return nil
}

func javaHasModifier(n *sitter.Node, src []byte, kw string) bool {
	m := javaModifiers(n)
	if m == nil {
		return false
	}
	for i := uint(0); i < m.ChildCount(); i++ {
		if c := m.Child(i); c != nil && !c.IsNamed() && c.Utf8Text(src) == kw {
			return true
		}
	}
	return false
}

// signature renders declarations without annotations: modifier keywords
// followed by the declaration text up to its body. Fields list their
// declarator names without initializers.
func (javaSpec) signature(_ model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	var parts []string
	start := n.StartByte()
	if m := javaModifiers(n); m != nil {
		for i := uint(0); i < m.ChildCount(); i++ {
			if c := m.Child(i); c != nil && !c.IsNamed() {
				parts = append(parts, c.Utf8Text(src))
			}
		}
		start = m.EndByte()
	}
	text := func(a, b uint) string {
		if b < a {
			return ""
		}
		return string(src[a:b])
	}
	switch n.Kind() {
	case "field_declaration", "constant_declaration":
		t := n.ChildByFieldName("type")
		if t == nil {
			return "", false
		}
		var names []string
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil || c.Kind() != "variable_declarator" {
				continue
			}
			if name := c.ChildByFieldName("name"); name != nil {
				s := name.Utf8Text(src)
				if dims := c.ChildByFieldName("dimensions"); dims != nil {
					s += dims.Utf8Text(src)
				}
				names = append(names, s)
			}
		}
		parts = append(parts, t.Utf8Text(src), strings.Join(names, ", "))
	case "enum_constant":
		end := n.EndByte()
		if body := n.ChildByFieldName("body"); body != nil {
			end = body.StartByte()
		}
		parts = append(parts, text(start, end))
	default:
		end := n.EndByte()
		if body := n.ChildByFieldName("body"); body != nil {
			end = body.StartByte()
		}
		parts = append(parts, strings.TrimSuffix(strings.TrimSpace(text(start, end)), ";"))
	}
	return strings.Join(parts, " "), true
}

// importSpec drops whitespace and comments a qualified name may contain.
func (javaSpec) importSpec(raw string) string { return javaCompactName(raw) }

// javaAnnotationNoise are java.lang annotations that can never name
// repository code; references to them are dropped.
var javaAnnotationNoise = map[string]bool{
	"Override": true, "Deprecated": true, "SuppressWarnings": true,
	"FunctionalInterface": true, "SafeVarargs": true,
}

// postProcess fills import names, turns type-like receivers into
// qualifiers and drops references that cannot name repository types.
func (javaSpec) postProcess(f *model.Facts) {
	f.Package = javaCompactName(f.Package)
	for i := range f.Imports {
		im := &f.Imports[i]
		if im.Wildcard {
			im.Names = []string{"*"}
			continue
		}
		if j := strings.LastIndexByte(im.Spec, '.'); j >= 0 && j+1 < len(im.Spec) {
			im.Names = []string{im.Spec[j+1:]}
		} else {
			im.Names = []string{im.Spec}
		}
	}

	quals := map[string]bool{}
	for i := range f.Refs {
		r := &f.Refs[i]
		if r.Qualifier != "" {
			r.Qualifier = javaCleanQualifier(r.Qualifier)
		}
		if r.Qualifier == "" && r.Receiver != "" && javaTypeLike(r.Receiver) {
			r.Qualifier, r.Receiver = javaCleanQualifier(r.Receiver), ""
		}
		if r.Qualifier != "" {
			quals[r.Qualifier] = true
		}
	}

	refs := f.Refs[:0]
	for _, r := range f.Refs {
		// Field accesses that spell a package path of a qualified name
		// (com.x.Util.m(): com.x, com.x.Util) are not member accesses.
		if r.Kind == model.RefMember && r.Receiver != "" && javaPathPrefix(quals, r.Receiver+"."+r.Name) {
			if !isUpperRune(r.Name) {
				continue
			}
			r.Kind, r.Qualifier, r.Receiver = model.RefType, r.Receiver, ""
		}
		if r.Kind == model.RefType {
			// Lowercase "types" are package segments of qualified names
			// (java.util.List) or the var keyword.
			if !isUpperRune(r.Name) || (r.Qualifier == "" && javaAnnotationNoise[r.Name]) {
				continue
			}
		}
		refs = append(refs, r)
	}
	f.Refs = refs
}

// javaPathPrefix reports whether path is one of quals or a dotted prefix of
// one.
func javaPathPrefix(quals map[string]bool, path string) bool {
	if quals[path] {
		return true
	}
	for q := range quals {
		if strings.HasPrefix(q, path+".") {
			return true
		}
	}
	return false
}

// javaTypeLike reports whether a receiver expression is a type or package
// path rather than a value: a dotted identifier chain whose last segment is
// capitalized (Strings, Map.Entry, com.x.Util) but not a CONSTANT.
func javaTypeLike(s string) bool {
	if s == "" {
		return false
	}
	segs := strings.Split(s, ".")
	for _, seg := range segs {
		if seg == "" || seg == "this" || seg == "super" || !isJavaIdent(seg) {
			return false
		}
	}
	last := segs[len(segs)-1]
	r, _ := utf8.DecodeRuneInString(last)
	if !unicode.IsUpper(r) {
		return false
	}
	return !javaConstantName(last)
}

// javaConstantName reports an ALL_CAPS name of two or more characters.
func javaConstantName(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

func isJavaIdent(s string) bool {
	for i, r := range s {
		if r == '_' || r == '$' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return s != ""
}

// javaCleanQualifier removes whitespace and generic arguments from a
// qualifier (Map<K, V>.Entry -> Map.Entry).
func javaCleanQualifier(q string) string {
	q = javaCompactName(q)
	if !strings.ContainsRune(q, '<') {
		return q
	}
	var b strings.Builder
	depth := 0
	for _, r := range q {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// javaCompactName removes whitespace and comments from a qualified name.
func javaCompactName(s string) string {
	s = stripJavaComments(s)
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), "")
}

func stripJavaComments(s string) string {
	if !strings.Contains(s, "/") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '/' && i+1 < len(s) {
			switch s[i+1] {
			case '/':
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			case '*':
				end := strings.Index(s[i+2:], "*/")
				if end < 0 {
					return b.String()
				}
				i += end + 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ModuleOf returns the package of the file: from facts when available,
// otherwise by scanning the source's package declaration. The default
// package is "".
func (javaSpec) ModuleOf(p string, facts *model.Facts, ctx model.RepoContext) string {
	if facts != nil {
		return facts.Package
	}
	if ctx == nil {
		return ""
	}
	src, err := ctx.ReadFile(model.CleanRel(p))
	if err != nil {
		return ""
	}
	return JavaPackageOf(src)
}

// JavaPackageOf scans the package declaration at the top of a Java source
// file without parsing it. It returns "" for the default package.
func JavaPackageOf(src []byte) string {
	s := string(src)
	i := 0
	skip := func() {
		for i < len(s) {
			switch {
			case s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\f':
				i++
			case strings.HasPrefix(s[i:], "\xef\xbb\xbf"):
				i += len("\xef\xbb\xbf")
			case strings.HasPrefix(s[i:], "//"):
				for i < len(s) && s[i] != '\n' {
					i++
				}
			case strings.HasPrefix(s[i:], "/*"):
				end := strings.Index(s[i+2:], "*/")
				if end < 0 {
					i = len(s)
					return
				}
				i += end + 4
			default:
				return
			}
		}
	}
	for steps := 0; steps < 64; steps++ { // package annotations (package-info.java)
		skip()
		if i >= len(s) || s[i] != '@' {
			break
		}
		i++
		for i < len(s) && (isIdentByte(s[i]) || s[i] == '.' || s[i] == ' ') {
			i++
		}
		skip()
		if i < len(s) && s[i] == '(' {
			i = skipJavaParens(s, i)
		}
	}
	rest, ok := strings.CutPrefix(s[i:], "package")
	if !ok || rest == "" || isIdentByte(rest[0]) {
		return ""
	}
	end := strings.IndexByte(rest, ';')
	if end < 0 {
		return ""
	}
	name := javaCompactName(rest[:end])
	for _, seg := range strings.Split(name, ".") {
		if !isJavaIdent(seg) {
			return ""
		}
	}
	return name
}

// skipJavaParens returns the offset after the parenthesized group starting
// at s[i] == '(', skipping string and character literals.
func skipJavaParens(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch c := s[i]; c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		case '"', '\'':
			for i++; i < len(s) && s[i] != c; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		}
	}
	return len(s)
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// javaExternalPrefixes are platform packages that never live in a
// repository.
var javaExternalPrefixes = []string{"java.", "javax.", "jdk.", "sun.", "com.sun.", "jakarta."}

// JavaExternalName reports whether a qualified name belongs to the Java
// platform (java.*, javax.*, ...).
func JavaExternalName(name string) bool {
	for _, p := range javaExternalPrefixes {
		if strings.HasPrefix(name, p) || name == strings.TrimSuffix(p, ".") {
			return true
		}
	}
	return false
}

// ResolveImport maps an import to the source file declaring it (single-type
// or static import; nested types map to their top-level file), or to the
// package directory (on-demand import). Source roots are derived from the
// importing file's package, plus the sibling Maven/Gradle source sets
// (src/main/java, src/test/java). Platform packages are external. Imports
// not found under those roots are assumed to name the package their spec
// starts with (lowercase segments), which may live in another module of
// the repository; resolvers match it by package identity.
func (javaSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	spec = javaCompactName(spec)
	segs := strings.Split(spec, ".")
	if spec == "" || JavaExternalName(spec) {
		return model.ImportTarget{Kind: model.TargetExternal, Module: spec}
	}
	if ctx != nil {
		for _, root := range javaSourceRoots(fromFile, ctx) {
			for k := len(segs); k >= 1; k-- {
				file := path.Join(root, path.Join(segs[:k]...)+".java")
				if ctx.FileExists(file) {
					return model.ImportTarget{Kind: model.TargetFile, Path: model.CleanRel(file), Module: strings.Join(segs[:k-1], ".")}
				}
			}
			if dir := model.CleanRel(path.Join(root, path.Join(segs...))); dir != "" && ctx.DirExists(dir) {
				return model.ImportTarget{Kind: model.TargetModule, Path: dir, Module: spec}
			}
		}
	}
	pkg := javaSpecPackage(segs)
	if pkg == "" {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	return model.ImportTarget{Kind: model.TargetModule, Module: pkg}
}

// javaSpecPackage returns the leading lowercase segments of a qualified
// name (the conventional package part), or the whole name when no segment
// is capitalized.
func javaSpecPackage(segs []string) string {
	for i, s := range segs {
		if isUpperRune(s) {
			return strings.Join(segs[:i], ".")
		}
	}
	return strings.Join(segs, ".")
}

// javaSourceRoots returns the source roots to search for imports written in
// fromFile: the root implied by its package and directory, then sibling
// source sets of a src/<set>/java layout.
func javaSourceRoots(fromFile string, ctx model.RepoContext) []string {
	fromFile = model.CleanRel(fromFile)
	dir := model.CleanRel(path.Dir(fromFile))
	var root string
	found := false
	if src, err := ctx.ReadFile(fromFile); err == nil {
		pkgPath := strings.ReplaceAll(JavaPackageOf(src), ".", "/")
		switch {
		case pkgPath == "":
			root, found = dir, true
		case dir == pkgPath:
			root, found = "", true
		case strings.HasSuffix(dir, "/"+pkgPath):
			root, found = strings.TrimSuffix(dir, "/"+pkgPath), true
		}
	}
	if !found {
		return nil
	}
	roots := []string{root}
	// .../src/<set>/java -> .../src/main/java, .../src/test/java
	segs := strings.Split(root, "/")
	if n := len(segs); n >= 3 && segs[n-3] == "src" && segs[n-1] == "java" {
		base := strings.Join(segs[:n-2], "/")
		for _, set := range []string{"main", "test"} {
			if r := base + "/" + set + "/java"; r != root && ctx.DirExists(r) {
				roots = append(roots, r)
			}
		}
	}
	return roots
}
