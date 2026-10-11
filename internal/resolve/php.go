package resolve

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
)

// phpResolver adds PHP scoping to the generic resolver. The parse layer
// already applied PHP's textual name resolution, so every class name and
// every imported or qualified function name in the facts is fully
// qualified: the qualifier is "\Ns\Sub" ("\" for the global namespace) and
// the module identity of a file is its dotted namespace. Hence:
//   - imports bind no qualifier roots (names are already expanded);
//   - type names resolve by exact namespace match (T1) or not at all, so
//     vendor classes stay external instead of being guessed;
//   - unqualified function calls use the generic tiers (same namespace,
//     then global), mirroring PHP's runtime fallback;
//   - receivers: $this/self/static (enclosing type), parent (its
//     ancestors), typed parameters, `$x = new T`, `@var T $x`, catch
//     variables and typed properties ($this->p->m()) give T3.
type phpResolver struct{ Generic }

func init() { Register(model.LangPHP, phpResolver{}) }

// ImportBindings implements LangResolver: use imports never act as
// qualifier roots because the parse layer expanded them.
func (phpResolver) ImportBindings(Import) []string { return nil }

// ResolveType implements LangResolver. qual is an absolute namespace in
// facts form ("\A\B") or dotted form ("A.B"); "" and "\" denote the global
// namespace.
func (r phpResolver) ResolveType(s *Session, _ *File, qual, name string) TypeResult {
	if name == "" || strings.ContainsAny(name, `\.`) {
		return TypeResult{}
	}
	if phpSpecialType(name) {
		return TypeResult{External: true}
	}
	ns := normQual(qual)
	cands := filter(s.DefsNamed(name), func(d *Def) bool {
		if !d.Kind.IsType() || r.IsMember(s, d) {
			return false
		}
		f := s.File(d.FileID)
		return f != nil && f.Lang == model.LangPHP && s.Module(f) == ns
	})
	return typeResult(cands, ConfImport, true)
}

// phpSpecialType reports builtin and relative type names that never name a
// repository class by themselves.
func phpSpecialType(name string) bool {
	switch strings.ToLower(name) {
	case "self", "static", "parent", "array", "callable", "bool", "float", "int",
		"string", "iterable", "object", "mixed", "void", "null", "never", "false",
		"true", "var":
		return true
	}
	return false
}

// Receiver implements LangResolver by reading the source around the
// reference: "->", "?->" and "::" mark member access.
func (r phpResolver) Receiver(s *Session, site *Site) Receiver {
	src := s.Source(site.File)
	if src == nil {
		return Receiver{Kind: RecvUnknownShape}
	}
	off, ok := phpLineOffset(src, site.Line)
	off += int(site.Col)
	if !ok || off > len(src) || !bytes.HasPrefix(src[off:], []byte(site.Name)) {
		return Receiver{Kind: RecvUnknownShape}
	}
	p := off
	if p > 0 && src[p-1] == '$' { // static property: self::$x
		p--
	}
	p = phpSkipSpaceBack(src, p)
	switch {
	case bytes.HasSuffix(src[:p], []byte("::")):
		return r.scopeReceiver(s, site, src, p-2, off)
	case bytes.HasSuffix(src[:p], []byte("?->")):
		return r.valueReceiver(s, site, src, p-3, off)
	case bytes.HasSuffix(src[:p], []byte("->")):
		return r.valueReceiver(s, site, src, p-2, off)
	}
	return Receiver{Kind: RecvNone}
}

// scopeReceiver types X in X::name.
func (r phpResolver) scopeReceiver(s *Session, site *Site, src []byte, end, siteOff int) Receiver {
	tok, _ := phpTokenBefore(src, phpSkipSpaceBack(src, end))
	switch {
	case tok == "":
		return Receiver{Kind: RecvUntyped}
	case strings.HasPrefix(tok, "$"):
		return r.variableReceiver(s, site, src, tok[1:], siteOff)
	}
	return r.typeReceiver(s, site.File, site.Src, tok)
}

// valueReceiver types the expression ending at end in expr->name.
func (r phpResolver) valueReceiver(s *Session, site *Site, src []byte, end, siteOff int) Receiver {
	end = phpSkipSpaceBack(src, end)
	tok, start := phpTokenBefore(src, end)
	if tok == "" {
		return Receiver{Kind: RecvUntyped} // call result, index, ...
	}
	before := phpSkipSpaceBack(src, start)
	arrow := 0
	switch {
	case bytes.HasSuffix(src[:before], []byte("?->")):
		arrow = 3
	case bytes.HasSuffix(src[:before], []byte("->")):
		arrow = 2
	case bytes.HasSuffix(src[:before], []byte("::")):
		return Receiver{Kind: RecvUntyped}
	}
	if arrow > 0 {
		// One-level property chain: $this->prop->name.
		owner, ostart := phpTokenBefore(src, phpSkipSpaceBack(src, before-arrow))
		if owner != "$this" || strings.HasPrefix(tok, "$") {
			return Receiver{Kind: RecvUntyped}
		}
		if ob := phpSkipSpaceBack(src, ostart); bytes.HasSuffix(src[:ob], []byte(">")) || bytes.HasSuffix(src[:ob], []byte(":")) {
			return Receiver{Kind: RecvUntyped}
		}
		return r.propertyReceiver(s, site, tok)
	}
	if !strings.HasPrefix(tok, "$") {
		return Receiver{Kind: RecvUntyped}
	}
	return r.variableReceiver(s, site, src, tok[1:], siteOff)
}

// variableReceiver types $name at the reference site.
func (r phpResolver) variableReceiver(s *Session, site *Site, src []byte, name string, siteOff int) Receiver {
	if name == "this" {
		if enc := s.EnclosingType(site.Src); enc != nil {
			return Receiver{Kind: RecvTyped, Types: []*Def{enc}}
		}
		return Receiver{Kind: RecvUntyped}
	}
	d := site.Src
	start := int(d.StartByte)
	if start < 0 || start > siteOff || siteOff > len(src) {
		return Receiver{Kind: RecvUntyped}
	}
	typ, ok := phpVariableType(src[start:siteOff], name)
	if !ok || typ == "" {
		return Receiver{Kind: RecvUntyped}
	}
	return r.typeReceiver(s, site.File, site.Src, typ)
}

// propertyReceiver types $this->prop from the property declaration or a
// promoted constructor parameter of the enclosing type (or its ancestors).
func (r phpResolver) propertyReceiver(s *Session, site *Site, prop string) Receiver {
	enc := s.EnclosingType(site.Src)
	if enc == nil {
		return Receiver{Kind: RecvUntyped}
	}
	for _, m := range s.Members(enc, prop) {
		if m.Kind != model.KindProperty {
			continue
		}
		if typ := phpDeclaredType(m.Sig, prop); typ != "" {
			return r.typeReceiver(s, s.File(m.FileID), m, typ)
		}
	}
	for _, c := range s.Members(enc, "__construct") {
		if typ := phpDeclaredType(c.Sig, prop); typ != "" {
			return r.typeReceiver(s, s.File(c.FileID), c, typ)
		}
	}
	return Receiver{Kind: RecvUntyped}
}

// typeReceiver resolves a type expression ("?A|B", "\N\C", "self") written
// in file f inside def at.
func (r phpResolver) typeReceiver(s *Session, f *File, at *Def, typ string) Receiver {
	if f == nil {
		return Receiver{Kind: RecvUntyped}
	}
	var (
		types    []*Def
		external bool
	)
	scope := phpScopeOf(s, f)
	for _, part := range strings.FieldsFunc(typ, func(r rune) bool { return r == '|' || r == '&' || r == '(' || r == ')' }) {
		part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "?"))
		if part == "" {
			continue
		}
		switch strings.ToLower(part) {
		case "self", "static":
			if enc := s.EnclosingType(at); enc != nil {
				types = appendUnique(types, enc)
			}
			continue
		case "parent":
			if enc := s.EnclosingType(at); enc != nil {
				anc := s.Ancestors(enc)
				types = appendUnique(types, anc...)
				external = external || len(anc) == 0
			}
			continue
		}
		if phpSpecialType(part) {
			external = true
			continue
		}
		fqn, ok := scope.Class(part)
		if !ok {
			continue
		}
		qual, name := parse.PHPSplitFQN(fqn)
		tr := r.ResolveType(s, f, qual, name)
		types = appendUnique(types, tr.Defs...)
		external = external || tr.External
	}
	switch {
	case len(types) > 0:
		return Receiver{Kind: RecvTyped, Types: types}
	case external:
		return Receiver{Kind: RecvExternal}
	}
	return Receiver{Kind: RecvUntyped}
}

// phpScopeOf rebuilds the name scope of f from its namespace (module) and
// use imports. Include paths are skipped.
func phpScopeOf(s *Session, f *File) *parse.PHPNameScope {
	scope := &parse.PHPNameScope{Namespace: strings.ReplaceAll(s.Module(f), ".", `\`)}
	for _, im := range s.Imports(f) {
		if strings.ContainsAny(im.Spec, "/.") {
			continue
		}
		scope.AddUse("", im.Spec, im.Alias)
	}
	return scope
}

// ---------------------------------------------------------------- text scanning

// phpTypeExpr matches a declared type token.
const phpTypeExpr = `([?\\\w|&\x80-\xff]+)`

// phpModifiers skips modifiers of promoted parameters and properties.
const phpModifiers = `(?:(?:public|private|protected|readonly|static|var)\s+)*`

// phpVariableType finds the type of $name from its latest binding in body
// (the enclosing definition up to the reference): an assignment of `new T`,
// a typed parameter (also of closures and catch clauses) or an
// `@var T $name` annotation. ok is false when no binding is found; typ is ""
// when the latest binding has an unknown type.
func phpVariableType(body []byte, name string) (typ string, ok bool) {
	v := regexp.QuoteMeta(name)
	bestPos := -1
	consider := func(pos int, t string) {
		if pos > bestPos {
			bestPos, typ = pos, t
		}
	}
	// Typed declarations: (Type $x, ... / , Type $x / catch (Type $x).
	declRe := regexp.MustCompile(`[(,]\s*` + phpModifiers + phpTypeExpr + `\s+(?:&\s*)?(?:\.\.\.\s*)?\$` + v + `\b`)
	for _, m := range declRe.FindAllSubmatchIndex(body, -1) {
		if t := string(body[m[2]:m[3]]); !phpModifierWord(t) {
			consider(m[1], t)
		}
	}
	docRe := regexp.MustCompile(`@var\s+` + phpTypeExpr + `\s+\$` + v + `\b`)
	for _, m := range docRe.FindAllSubmatchIndex(body, -1) {
		consider(m[1], string(body[m[2]:m[3]]))
	}
	// Assignments and foreach bindings.
	useRe := regexp.MustCompile(`\$` + v + `\b`)
	for _, m := range useRe.FindAllIndex(body, -1) {
		before := strings.TrimRight(string(body[:m[0]]), " \t\r\n&")
		if strings.HasSuffix(before, "=>") || phpEndsWithWord(before, "as") {
			consider(m[1], "") // foreach value: unknown element type
			continue
		}
		rest := bytes.TrimLeft(body[m[1]:], " \t\r\n")
		if len(rest) == 0 || rest[0] != '=' || (len(rest) > 1 && (rest[1] == '=' || rest[1] == '>')) {
			continue
		}
		t := phpNewType(bytes.TrimLeft(rest[1:], " \t\r\n"))
		if t == "" && strings.HasSuffix(before, "*/") {
			// /** @var T $x */ $x = factory();
			if i := strings.LastIndex(before, "/*"); i >= 0 {
				if dm := docRe.FindStringSubmatch(before[i:]); dm != nil {
					t = dm[1]
				}
			}
		}
		consider(m[1], t)
	}
	return typ, bestPos >= 0
}

// phpNewType returns T from "new T(...)", or "".
func phpNewType(rhs []byte) string {
	rest, ok := bytes.CutPrefix(rhs, []byte("new"))
	if !ok || len(rest) == 0 || !strings.ContainsRune(" \t\n\\", rune(rest[0])) {
		return ""
	}
	rest = bytes.TrimLeft(rest, " \t\r\n")
	i := 0
	for i < len(rest) && (isIdentByte(rest[i]) || rest[i] == '\\') && rest[i] != '$' {
		i++
	}
	t := string(rest[:i])
	if strings.EqualFold(t, "class") {
		return ""
	}
	return t
}

// phpDeclaredType extracts the type preceding $name in a declaration
// signature ("private Cart $cart;", "__construct(private A $a, B $b)").
func phpDeclaredType(sig, name string) string {
	re := regexp.MustCompile(phpTypeExpr + `\s+(?:&\s*)?(?:\.\.\.\s*)?\$` + regexp.QuoteMeta(name) + `\b`)
	m := re.FindStringSubmatch(sig)
	if m == nil || phpModifierWord(m[1]) {
		return ""
	}
	return m[1]
}

func phpModifierWord(s string) bool {
	switch strings.ToLower(s) {
	case "public", "private", "protected", "readonly", "static", "var", "function", "fn", "global":
		return true
	}
	return false
}

func phpEndsWithWord(s, word string) bool {
	if len(s) < len(word) || !strings.EqualFold(s[len(s)-len(word):], word) {
		return false
	}
	return len(s) == len(word) || !isIdentByte(s[len(s)-len(word)-1])
}

// phpTokenBefore returns the identifier-like token ($ and \ included) that
// ends at end, and its start.
func phpTokenBefore(src []byte, end int) (string, int) {
	if end > len(src) {
		end = len(src)
	}
	i := end
	for i > 0 && (isIdentByte(src[i-1]) || src[i-1] == '\\') {
		i--
	}
	return string(src[i:end]), i
}

func phpSkipSpaceBack(src []byte, p int) int {
	if p > len(src) {
		p = len(src)
	}
	for p > 0 && (src[p-1] == ' ' || src[p-1] == '\t' || src[p-1] == '\n' || src[p-1] == '\r') {
		p--
	}
	return p
}

// phpLineOffset returns the byte offset where 1-based line n starts.
func phpLineOffset(src []byte, n uint32) (int, bool) {
	if n == 0 {
		return 0, false
	}
	off := 0
	for i := uint32(1); i < n; i++ {
		j := bytes.IndexByte(src[off:], '\n')
		if j < 0 {
			return 0, false
		}
		off += j + 1
	}
	return off, true
}
