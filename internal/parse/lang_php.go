package parse

import (
	_ "embed"
	"encoding/json"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsphp "github.com/tree-sitter/tree-sitter-php/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/php.scm
var phpQuery string

// phpSpec is the PHP language spec. Module identity is the namespace in
// dotted form ("App\Model" -> "App.Model", "" for the global namespace);
// composer.json PSR-4/PSR-0 autoload maps namespaces to directories for
// import resolution. Names in references are resolved to fully qualified
// form at parse time (see postProcess), which PHP defines purely textually.
type phpSpec struct{}

func init() { Register(phpSpec{}) }

func (phpSpec) Lang() model.Lang         { return model.LangPHP }
func (phpSpec) Extensions() []string     { return model.LangPHP.Extensions() }
func (phpSpec) Grammar() unsafe.Pointer  { return tsphp.LanguagePHP() }
func (phpSpec) QuerySource() string      { return phpQuery }
func (phpSpec) ExtractorVersion() int    { return 1 }
func (phpSpec) IsTestPath(p string) bool { return strings.HasSuffix(path.Base(p), "Test.php") }

// testTarget maps PHPUnit method names to the tested member:
// testTotalSumsPrices -> totalSumsPrices, test_formats_money -> formats_money.
func (phpSpec) testTarget(name string) string {
	t := defaultTestTarget(name)
	if t == "" {
		return name
	}
	r, size := utf8.DecodeRuneInString(t)
	return string(unicode.ToLower(r)) + t[size:]
}

// isExported: everything is public unless declared private or protected.
func (phpSpec) isExported(_ *model.Def, n *sitter.Node, src []byte) bool {
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil || c.Kind() != "visibility_modifier" {
			continue
		}
		v := strings.ToLower(string(src[c.StartByte():c.EndByte()]))
		return v != "private" && v != "protected"
	}
	return true
}

// signature drops leading attributes (#[...]) from declarations with a body.
func (phpSpec) signature(_ model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	attrs := n.ChildByFieldName("attributes")
	if attrs == nil {
		return "", false
	}
	start := attrs.EndByte()
	end := n.EndByte()
	if body := n.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	} else {
		return firstLine(strings.TrimSpace(string(src[start:end]))), true
	}
	return string(src[start:end]), true
}

// importSpec turns the captured text into Import.Spec. Use clauses are kept
// verbatim (postProcess parses them); require/include arguments yield a
// path only when literal: 'a/b.php', "a/b.php", __DIR__ . '/a.php' (-> ./a.php)
// or dirname(__DIR__) . '/a.php' (-> ../a.php). Anything dynamic yields "".
func (phpSpec) importSpec(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "{" {
		return `\` // braced global namespace marker (see postProcess)
	}
	if isPHPUseClause(raw) {
		return compactText(raw, 1<<10)
	}
	return phpIncludePath(raw)
}

// isPHPUseClause reports whether s looks like a use clause ("A\B as C",
// "function A\b") rather than an include expression.
func isPHPUseClause(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isPHPNameRune(r) && r != '\\' && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func isPHPNameRune(r rune) bool {
	return r == '_' || r >= 0x80 || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// phpIncludePath extracts a literal include path from an expression text.
func phpIncludePath(expr string) string {
	expr = strings.TrimSpace(expr)
	for strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
		expr = strings.TrimSpace(expr[1 : len(expr)-1])
	}
	if lit, ok := phpStringLiteral(expr); ok {
		return lit
	}
	// <dir expression> . 'literal'
	dot := strings.LastIndexByte(expr, '.')
	for dot >= 0 {
		lit, ok := phpStringLiteral(strings.TrimSpace(expr[dot+1:]))
		if ok {
			up, ok := phpDirLevels(strings.TrimSpace(expr[:dot]))
			if !ok {
				return ""
			}
			rel := strings.TrimPrefix(lit, "/")
			if rel == "" {
				return ""
			}
			if up == 0 {
				return "./" + rel
			}
			return strings.Repeat("../", up) + rel
		}
		dot = strings.LastIndexByte(expr[:dot], '.')
	}
	return ""
}

// phpStringLiteral unquotes a string literal without interpolation.
func phpStringLiteral(s string) (string, bool) {
	if len(s) < 2 || (s[0] != '\'' && s[0] != '"') || s[len(s)-1] != s[0] {
		return "", false
	}
	body := s[1 : len(s)-1]
	if strings.IndexByte(body, s[0]) >= 0 && !strings.Contains(body, `\`+string(s[0])) {
		return "", false // two literals concatenated, e.g. 'a' . 'b'
	}
	if s[0] == '"' && strings.ContainsAny(body, "${") {
		return "", false
	}
	return body, body != ""
}

// phpDirLevels recognizes __DIR__, dirname(__FILE__) and dirname(__DIR__[, n])
// and returns how many levels above the file's directory they point.
func phpDirLevels(e string) (int, bool) {
	e = strings.Join(strings.Fields(e), "")
	switch e {
	case "__DIR__", "dirname(__FILE__)":
		return 0, true
	}
	inner, ok := strings.CutPrefix(e, "dirname(")
	if !ok || !strings.HasSuffix(inner, ")") {
		return 0, false
	}
	inner = inner[:len(inner)-1]
	arg, levelStr, hasLevel := strings.Cut(inner, ",")
	levels := 1
	if hasLevel {
		n, err := strconv.Atoi(levelStr)
		if err != nil || n < 1 || n > 16 {
			return 0, false
		}
		levels = n
	}
	switch {
	case arg == "__DIR__":
		return levels, true
	case arg == "__FILE__":
		return levels - 1, true
	case strings.HasPrefix(arg, "dirname("):
		up, ok := phpDirLevels(arg)
		return up + levels, ok
	}
	return 0, false
}

// ---------------------------------------------------------------- names

// PHPNameScope resolves PHP class and function names textually, as the
// language does: fully qualified names stand, a leading segment matching a
// use import is replaced by it, everything else is relative to the current
// namespace. Unqualified function names without a use import are left
// unresolved because PHP falls back to the global namespace at runtime.
// The zero value is the global namespace without imports.
type PHPNameScope struct {
	// Namespace is the current namespace without a leading backslash.
	Namespace string
	classes   map[string]string // lower(local name) -> FQN
	functions map[string]string
}

// AddUse records an import of fqn (leading backslash optional) under its
// alias or last segment. kind is "", "function" or "const".
func (s *PHPNameScope) AddUse(kind, fqn, alias string) {
	fqn = strings.TrimPrefix(fqn, `\`)
	if fqn == "" {
		return
	}
	local := alias
	if local == "" {
		local = phpLastSegment(fqn)
	}
	m := &s.classes
	if kind == "function" || kind == "const" {
		m = &s.functions
	}
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[strings.ToLower(local)] = fqn
}

// Class returns the fully qualified name (without leading backslash) of a
// class-like name. ok is false for self, static and parent.
func (s *PHPNameScope) Class(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || isPHPRelativeScope(name) {
		return "", false
	}
	if fqn, ok := s.qualified(name); ok {
		return fqn, true
	}
	if fqn, ok := s.classes[strings.ToLower(name)]; ok {
		return fqn, true
	}
	return phpJoin(s.Namespace, name), true
}

// Function returns the fully qualified name of a function or constant
// name; ok is false for an unqualified name without a use import.
func (s *PHPNameScope) Function(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	if fqn, ok := s.qualified(name); ok {
		return fqn, true
	}
	fqn, ok := s.functions[strings.ToLower(name)]
	return fqn, ok
}

// qualified resolves fully qualified, namespace-relative and qualified
// names; ok is false for unqualified names.
func (s *PHPNameScope) qualified(name string) (string, bool) {
	if rest, ok := strings.CutPrefix(name, `\`); ok {
		return rest, true
	}
	first, rest, ok := strings.Cut(name, `\`)
	if !ok {
		return "", false
	}
	if strings.EqualFold(first, "namespace") {
		return phpJoin(s.Namespace, rest), true
	}
	if fqn, ok := s.classes[strings.ToLower(first)]; ok {
		return fqn + `\` + rest, true
	}
	return phpJoin(s.Namespace, name), true
}

func isPHPRelativeScope(name string) bool {
	switch strings.ToLower(name) {
	case "self", "static", "parent":
		return true
	}
	return false
}

func phpJoin(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + `\` + name
}

func phpLastSegment(fqn string) string {
	if i := strings.LastIndexByte(fqn, '\\'); i >= 0 {
		return fqn[i+1:]
	}
	return fqn
}

// PHPSplitFQN splits a fully qualified name into the qualifier form stored
// in facts ("\" + namespace, "\" for the global namespace) and the name.
func PHPSplitFQN(fqn string) (qualifier, name string) {
	fqn = strings.TrimPrefix(fqn, `\`)
	if i := strings.LastIndexByte(fqn, '\\'); i >= 0 {
		return `\` + fqn[:i], fqn[i+1:]
	}
	return `\`, fqn
}

// PHPNamespaceModule converts a namespace ("App\Model", optionally with a
// leading backslash) to the dotted module identity ("App.Model").
func PHPNamespaceModule(ns string) string {
	return strings.Trim(strings.ReplaceAll(ns, `\`, "."), ".")
}

// ---------------------------------------------------------------- postProcess

// phpBuiltinTypes are type names that never refer to a class.
var phpBuiltinTypes = map[string]bool{
	"self": true, "static": true, "parent": true, "array": true, "callable": true,
	"bool": true, "float": true, "int": true, "string": true, "iterable": true,
	"object": true, "mixed": true, "void": true, "null": true, "never": true,
	"false": true, "true": true,
}

// phpBuiltinFuncs lists frequently called internal functions; unqualified
// calls to them are dropped (like Go builtins) unless imported.
var phpBuiltinFuncs = map[string]bool{
	"array_filter": true, "array_key_exists": true, "array_keys": true, "array_map": true,
	"array_merge": true, "array_pop": true, "array_push": true, "array_search": true,
	"array_shift": true, "array_slice": true, "array_values": true, "count": true,
	"define": true, "defined": true, "dirname": true, "explode": true, "file_exists": true,
	"file_get_contents": true, "implode": true, "in_array": true, "intdiv": true,
	"is_array": true, "is_int": true, "is_null": true, "is_numeric": true, "is_string": true,
	"json_decode": true, "json_encode": true, "max": true, "min": true, "preg_match": true,
	"preg_replace": true, "printf": true, "round": true, "sprintf": true, "str_contains": true,
	"str_replace": true, "str_starts_with": true, "strlen": true, "strtolower": true,
	"strtoupper": true, "substr": true, "trim": true, "ucfirst": true, "var_dump": true,
	"func_get_args": true, "get_class": true, "htmlspecialchars": true, "is_object": true,
	"method_exists": true, "number_format": true, "sort": true, "array_unique": true,
	"strpos": true,
}

// postProcess applies PHP name resolution: imports get their full spec,
// alias and kind; reference and inheritance names are rewritten to
// fully qualified form (Qualifier "\A\B", Name "C"); static class scopes
// become qualifiers; builtin types and functions are dropped. Files with
// several namespaces resolve each name in the namespace (and with the use
// imports) of its line.
func (phpSpec) postProcess(f *model.Facts) {
	scopes := phpScopes{{line: 0, scope: PHPNameScope{Namespace: strings.TrimPrefix(f.Package, `\`)}}}
	imps := f.Imports[:0]
	for _, im := range f.Imports {
		switch {
		case strings.EqualFold(im.Alias, "namespace"):
			ns := strings.Trim(im.Spec, `\`)
			if len(scopes) == 1 && len(scopes[0].used) == 0 && scopes[0].line == 0 {
				scopes[0] = phpScopeAt{line: im.Line, scope: PHPNameScope{Namespace: ns}}
			} else {
				scopes = append(scopes, phpScopeAt{line: im.Line, scope: PHPNameScope{Namespace: ns}})
			}
			continue
		case !isPHPUseClause(im.Spec):
			im.Alias = "" // include path
		default:
			kind, fqn, alias := parsePHPUse(im.Spec, im.Alias)
			if fqn == "" {
				continue
			}
			im.Spec, im.Alias, im.Names = fqn, alias, nil
			s := scopes.at(im.Line)
			s.scope.AddUse(kind, fqn, alias)
			s.used = append(s.used, fqn)
		}
		imps = append(imps, im)
	}
	f.Imports = imps
	if len(f.Imports) == 0 {
		f.Imports = nil
	}

	refs := f.Refs[:0]
	for _, r := range f.Refs {
		if phpResolveRef(&scopes.at(r.Line).scope, &r) {
			refs = append(refs, r)
		}
	}
	f.Refs = refs
	if len(f.Refs) == 0 {
		f.Refs = nil
	}

	inh := f.Inherits[:0]
	for _, in := range f.Inherits {
		text := in.Name
		if in.Qualifier != "" {
			text = in.Qualifier + `\` + in.Name
		}
		fqn, ok := scopes.at(in.Line).scope.Class(text)
		if !ok {
			continue
		}
		in.Qualifier, in.Name = PHPSplitFQN(fqn)
		if in.Name != "" {
			inh = append(inh, in)
		}
	}
	f.Inherits = inh
	if len(f.Inherits) == 0 {
		f.Inherits = nil
	}
}

// phpScopeAt is the name scope of a namespace starting at line.
type phpScopeAt struct {
	line  uint32
	scope PHPNameScope
	used  []string
}

// phpScopes are the namespaces of a file in line order.
type phpScopes []phpScopeAt

// at returns the scope of the namespace containing line.
func (ss phpScopes) at(line uint32) *phpScopeAt {
	i := sort.Search(len(ss), func(i int) bool { return ss[i].line > line }) - 1
	if i < 0 {
		i = 0
	}
	return &ss[i]
}

// parsePHPUse parses a use clause ("function A\b as c") and, for clauses of
// a group use, the whole declaration captured as alias.
func parsePHPUse(clause, decl string) (kind, fqn, alias string) {
	clause = strings.TrimSpace(clause)
	kind, clause = cutPHPUseKind(clause)
	if f := strings.Fields(clause); len(f) == 3 && strings.EqualFold(f[1], "as") {
		clause, alias = f[0], f[2]
	} else if len(f) == 1 {
		clause = f[0]
	} else {
		return "", "", ""
	}
	if prefix, ok := strings.CutPrefix(strings.TrimSpace(decl), "use"); ok && strings.Contains(decl, "{") {
		var gk string
		gk, prefix = cutPHPUseKind(strings.TrimSpace(prefix))
		if kind == "" {
			kind = gk
		}
		if i := strings.IndexByte(prefix, '{'); i >= 0 {
			prefix = prefix[:i]
		}
		prefix = strings.Trim(strings.TrimSpace(prefix), `\`)
		if prefix != "" {
			clause = prefix + `\` + strings.TrimPrefix(clause, `\`)
		}
	}
	return kind, strings.TrimPrefix(clause, `\`), alias
}

func cutPHPUseKind(s string) (kind, rest string) {
	for _, k := range []string{"function", "const"} {
		if len(s) > len(k) && strings.EqualFold(s[:len(k)], k) && unicode.IsSpace(rune(s[len(k)])) {
			return k, strings.TrimSpace(s[len(k):])
		}
	}
	return "", s
}

// phpResolveRef rewrites one reference; false drops it.
func phpResolveRef(scope *PHPNameScope, r *model.Ref) bool {
	if strings.EqualFold(r.Name, "class") {
		return false // Foo::class
	}
	switch {
	case r.Qualifier != "":
		// The whole qualified name was captured: strip the name segment.
		q := r.Qualifier
		if q != r.Name && !strings.HasSuffix(q, `\`+r.Name) {
			return false // truncated text
		}
		var fqn string
		var ok bool
		if r.Kind == model.RefCall {
			fqn, ok = scope.Function(q)
		} else {
			fqn, ok = scope.Class(q)
		}
		if !ok {
			return false
		}
		r.Qualifier, r.Name = PHPSplitFQN(fqn)
	case r.Receiver != "":
		if !isPHPClassName(r.Receiver) || isPHPRelativeScope(r.Receiver) {
			return true // a value ($x, call result) or self/static/parent
		}
		fqn, ok := scope.Class(r.Receiver)
		if !ok {
			return false
		}
		r.Qualifier, r.Receiver = `\`+fqn, ""
	case r.Kind == model.RefType:
		if phpBuiltinTypes[strings.ToLower(r.Name)] {
			return false
		}
		fqn, ok := scope.Class(r.Name)
		if !ok {
			return false
		}
		r.Qualifier, r.Name = PHPSplitFQN(fqn)
	case r.Kind == model.RefCall:
		if fqn, ok := scope.Function(r.Name); ok {
			r.Qualifier, r.Name = PHPSplitFQN(fqn)
		} else if phpBuiltinFuncs[strings.ToLower(r.Name)] {
			return false
		}
	}
	return r.Name != ""
}

// isPHPClassName reports whether s is a (possibly qualified) name.
func isPHPClassName(s string) bool {
	if s == "" || s == `\` {
		return false
	}
	for _, r := range s {
		if !isPHPNameRune(r) && r != '\\' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- modules

// ModuleOf returns the dotted namespace of the file: the declared one from
// facts, else the first namespace declaration found in the source, else the
// namespace PSR-4/PSR-0 autoloading assigns to the file's directory.
func (phpSpec) ModuleOf(p string, facts *model.Facts, ctx model.RepoContext) string {
	if facts != nil {
		return PHPNamespaceModule(facts.Package)
	}
	if ctx == nil {
		return ""
	}
	p = model.CleanRel(p)
	if src, err := ctx.ReadFile(p); err == nil {
		return PHPNamespaceModule(sniffPHPNamespace(src))
	}
	cfg, dir, ok := phpComposerFor(ctx, path.Dir(p))
	if !ok {
		return ""
	}
	return PHPNamespaceModule(cfg.namespaceOf(dir, model.CleanRel(path.Dir(p))))
}

// sniffPHPNamespace finds the first namespace declaration in src textually.
func sniffPHPNamespace(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(line, "<?php"))
		rest, ok := strings.CutPrefix(line, "namespace")
		if !ok || rest == "" || !unicode.IsSpace(rune(rest[0])) {
			continue
		}
		rest = strings.TrimSpace(rest)
		end := strings.IndexFunc(rest, func(r rune) bool { return !isPHPNameRune(r) && r != '\\' })
		if end < 0 {
			end = len(rest)
		}
		if tail := strings.TrimSpace(rest[end:]); end > 0 && (strings.HasPrefix(tail, ";") || strings.HasPrefix(tail, "{")) {
			return strings.Trim(rest[:end], `\`)
		}
		if strings.HasPrefix(rest, "{") {
			return "" // braced global namespace
		}
	}
	return ""
}

// ResolveImport maps a use import to the class file (or namespace
// directory) PSR-4/PSR-0 autoloading assigns to it, and an include path to
// the file relative to the including file, the composer root or the repo
// root. Composer's vendor/ is external.
func (phpSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	if spec == "" {
		return model.ImportTarget{Kind: model.TargetUnresolved}
	}
	if isPHPUseClause(spec) {
		return phpResolveUse(strings.TrimPrefix(spec, `\`), fromFile, ctx)
	}
	return phpResolveInclude(spec, fromFile, ctx)
}

func phpResolveInclude(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	ext := model.ImportTarget{Kind: model.TargetExternal, Module: spec}
	if strings.HasPrefix(spec, "/") || ctx == nil {
		return ext
	}
	dir := model.CleanRel(path.Dir(model.CleanRel(fromFile)))
	var cands []string
	add := func(base string) {
		p := path.Join(base, spec)
		if model.CleanRel(p) == "" { // escapes the repository
			return
		}
		cands = append(cands, model.CleanRel(p))
	}
	add(dir)
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		if comp, ok := model.FindUp(ctx, dir, "composer.json"); ok {
			add(model.CleanRel(path.Dir(comp)))
		}
		add("")
	}
	for _, c := range cands {
		if isVendorPath(c) {
			return ext
		}
		if ctx.FileExists(c) {
			return model.ImportTarget{Kind: model.TargetFile, Path: c}
		}
	}
	return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
}

func isVendorPath(p string) bool {
	return strings.HasPrefix(p, "vendor/") || strings.Contains(p, "/vendor/")
}

func phpResolveUse(fqn, fromFile string, ctx model.RepoContext) model.ImportTarget {
	nsMod := PHPNamespaceModule(phpNamespaceOf(fqn))
	if ctx == nil {
		return model.ImportTarget{Kind: model.TargetModule, Module: nsMod}
	}
	cfg, root, ok := phpComposerFor(ctx, path.Dir(model.CleanRel(fromFile)))
	if !ok || len(cfg.entries) == 0 {
		// No autoload information: the namespace is the target.
		return model.ImportTarget{Kind: model.TargetModule, Module: nsMod}
	}
	matched := false
	for _, e := range cfg.entries {
		rel, ok := e.relPath(fqn)
		if !ok {
			continue
		}
		matched = true
		for _, d := range e.dirs {
			base := model.CleanRel(path.Join(root, d))
			if f := model.CleanRel(path.Join(base, rel+".php")); ctx.FileExists(f) {
				return model.ImportTarget{Kind: model.TargetFile, Path: f, Module: nsMod}
			}
			if rel != "" {
				if dp := model.CleanRel(path.Join(base, rel)); ctx.DirExists(dp) {
					return model.ImportTarget{Kind: model.TargetModule, Path: dp, Module: PHPNamespaceModule(fqn)}
				}
			}
			// A function or constant import: its namespace directory.
			if dp := model.CleanRel(path.Join(base, path.Dir(rel))); ctx.DirExists(dp) {
				return model.ImportTarget{Kind: model.TargetModule, Path: dp, Module: nsMod}
			}
		}
	}
	if matched {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: PHPNamespaceModule(fqn)}
	}
	return model.ImportTarget{Kind: model.TargetExternal, Module: fqn}
}

func phpNamespaceOf(fqn string) string {
	if i := strings.LastIndexByte(fqn, '\\'); i >= 0 {
		return fqn[:i]
	}
	return ""
}

// ---------------------------------------------------------------- composer

// phpAutoload is one PSR-4 or PSR-0 mapping.
type phpAutoload struct {
	prefix string   // namespace prefix with trailing "\" ("" = any)
	dirs   []string // relative to the composer.json directory
	psr0   bool
}

// relPath maps fqn to its path below a mapped directory (without .php).
func (e phpAutoload) relPath(fqn string) (string, bool) {
	if !strings.HasPrefix(fqn, e.prefix) {
		return "", false
	}
	rest := fqn
	if !e.psr0 {
		rest = fqn[len(e.prefix):]
	}
	return strings.ReplaceAll(rest, `\`, "/"), true
}

type phpComposer struct {
	entries []phpAutoload // longest prefix first
}

// namespaceOf inverts the mapping for a directory (both repo-relative).
func (c *phpComposer) namespaceOf(root, dir string) string {
	best, bestLen := "", -1
	for _, e := range c.entries {
		for _, d := range e.dirs {
			base := model.CleanRel(path.Join(root, d))
			var rel string
			switch {
			case base == dir:
			case base == "" || strings.HasPrefix(dir, base+"/"):
				rel = strings.TrimPrefix(strings.TrimPrefix(dir, base), "/")
			default:
				continue
			}
			if len(base) <= bestLen {
				continue
			}
			ns := strings.ReplaceAll(rel, "/", `\`)
			if !e.psr0 {
				ns = phpJoin(strings.TrimSuffix(e.prefix, `\`), ns)
			}
			best, bestLen = strings.Trim(ns, `\`), len(base)
		}
	}
	return best
}

var phpComposerCache sync.Map // composer.json path + "\x00" + content -> *phpComposer

// phpComposerFor finds the nearest composer.json above dir and parses its
// autoload sections; root is the composer.json directory.
func phpComposerFor(ctx model.RepoContext, dir string) (*phpComposer, string, bool) {
	file, ok := model.FindUp(ctx, model.CleanRel(dir), "composer.json")
	if !ok {
		return nil, "", false
	}
	b, err := ctx.ReadFile(file)
	if err != nil {
		return nil, "", false
	}
	root := model.CleanRel(path.Dir(file))
	key := file + "\x00" + string(b)
	if v, ok := phpComposerCache.Load(key); ok {
		return v.(*phpComposer), root, true
	}
	c := parsePHPComposer(b)
	phpComposerCache.Store(key, c)
	return c, root, true
}

func parsePHPComposer(b []byte) *phpComposer {
	type autoload struct {
		PSR4 map[string]json.RawMessage `json:"psr-4"`
		PSR0 map[string]json.RawMessage `json:"psr-0"`
	}
	var doc struct {
		Autoload    autoload `json:"autoload"`
		AutoloadDev autoload `json:"autoload-dev"`
	}
	c := &phpComposer{}
	if json.Unmarshal(b, &doc) != nil {
		return c
	}
	for _, a := range []autoload{doc.Autoload, doc.AutoloadDev} {
		for prefix, raw := range a.PSR4 {
			c.add(prefix, raw, false)
		}
		for prefix, raw := range a.PSR0 {
			c.add(prefix, raw, true)
		}
	}
	sort.SliceStable(c.entries, func(i, j int) bool {
		a, b := c.entries[i], c.entries[j]
		if len(a.prefix) != len(b.prefix) {
			return len(a.prefix) > len(b.prefix)
		}
		if a.prefix != b.prefix {
			return a.prefix < b.prefix
		}
		return !a.psr0 && b.psr0
	})
	return c
}

func (c *phpComposer) add(prefix string, raw json.RawMessage, psr0 bool) {
	var dirs []string
	var one string
	if json.Unmarshal(raw, &one) == nil {
		dirs = []string{one}
	} else if json.Unmarshal(raw, &dirs) != nil {
		return
	}
	e := phpAutoload{prefix: strings.TrimPrefix(prefix, `\`), psr0: psr0}
	for _, d := range dirs {
		clean := model.CleanRel(d)
		if clean == "" && strings.Trim(d, "./") != "" {
			continue // escapes the repository or is absolute
		}
		e.dirs = append(e.dirs, clean)
	}
	sort.Strings(e.dirs)
	if len(e.dirs) > 0 {
		c.entries = append(c.entries, e)
	}
}
