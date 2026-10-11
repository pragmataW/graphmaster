package resolve

import (
	"bytes"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// cResolver adds C scoping to the generic resolver:
//   - #include binds no qualifier roots (C has no qualified names);
//   - declarations (prototypes, extern variables) count as members, so bare
//     names resolve to definitions and a header prototype never competes
//     with its definition; a name that is only declared stays unresolved.
//     Definitions links a declaration to its definitions (declares edges);
//   - struct/union fields and scoped enum members are members, while the
//     enumerators of a plain enum live in the enclosing scope;
//   - receivers declared as locals or parameters (Vec2 r; r.x) are typed
//     from the enclosing function's source text (T3), following typedefs.
//
// Static functions of other files are not filtered out (best effort).
type cResolver struct{ Generic }

func init() { Register(model.LangC, cResolver{}) }

// DeclarationResolver is implemented by resolvers of languages that keep
// declarations apart from definitions (C, C++). Definitions returns the
// definitions a declaration (model.FlagDeclaration) declares, which are the
// targets of its "declares" edge.
//
// The indexer writes one unresolved declares edge per declaration; the
// framework resolves it through this interface (T2, or T5 when several
// definitions match).
type DeclarationResolver interface {
	Definitions(s *Session, decl *Def) []*Def
}

var (
	_ DeclarationResolver = cResolver{}
	_ DeclarationResolver = cppResolver{}
)

// ImportBindings implements LangResolver: includes bind nothing.
func (cResolver) ImportBindings(Import) []string { return nil }

// IsMember implements LangResolver.
func (cResolver) IsMember(s *Session, d *Def) bool { return cfamIsMember(s, d) }

// OwnerName implements LangResolver. C++ classes may be declared in .h
// files (parsed as C), so out-of-line C++ members are recognized here too.
func (cResolver) OwnerName(_ *Session, d *Def) string { return cfamOwnerName(d) }

// Receiver implements LangResolver.
func (r cResolver) Receiver(s *Session, site *Site) Receiver {
	return cfamReceiver(s, site, r, nil)
}

// Definitions implements DeclarationResolver.
func (cResolver) Definitions(s *Session, decl *Def) []*Def { return cfamDefinitions(s, decl) }

func cfamIsMember(s *Session, d *Def) bool {
	if d.Flags.Has(model.FlagDeclaration) {
		return true
	}
	p := s.Parent(d)
	if p == nil || !p.Kind.IsType() {
		return false
	}
	if d.Kind == model.KindEnumMember {
		return cfamIsScopedEnum(p)
	}
	return true
}

// cfamIsScopedEnum reports whether e is an enum class / enum struct.
func cfamIsScopedEnum(e *Def) bool {
	sig := strings.TrimPrefix(strings.TrimSpace(e.Sig), "typedef ")
	return strings.HasPrefix(sig, "enum class") || strings.HasPrefix(sig, "enum struct")
}

// cfamOwnerName returns the class of an out-of-line member definition from
// its signature: "void Canvas::add(...)" -> "Canvas". Nested definitions
// and free functions have no owner name.
func cfamOwnerName(d *Def) string {
	if d.ParentID != 0 || (d.Kind != model.KindMethod && d.Kind != model.KindConstructor) {
		return ""
	}
	return cfamSigOwner(d.Sig, d.Name)
}

// cfamSigOwner finds "Owner::name(" in sig and returns Owner without template
// arguments.
func cfamSigOwner(sig, name string) string {
	needle := "::" + name
	for from := 0; from < len(sig); {
		i := strings.Index(sig[from:], needle)
		if i < 0 {
			return ""
		}
		i += from
		rest := strings.TrimLeft(sig[i+len(needle):], " \t")
		if strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "<") {
			segs := cfamQualSegs(cfamTypeToken(sig[:i]))
			if len(segs) > 0 {
				return segs[len(segs)-1]
			}
			return ""
		}
		from = i + len(needle)
	}
	return ""
}

// cfamTypeToken returns the trailing qualified-name token of s, including
// template arguments: "std::string geo::Box<T>" -> "geo::Box<T>".
func cfamTypeToken(s string) string {
	s = strings.TrimRight(s, " \t")
	i := len(s)
	for i > 0 {
		c := s[i-1]
		switch {
		case isIdentByte(c) || c == ':':
			i--
		case c == '>':
			depth := 0
			j := i - 1
			for ; j >= 0; j-- {
				if s[j] == '>' {
					depth++
				} else if s[j] == '<' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if j < 0 {
				return s[i:]
			}
			i = j
		default:
			return s[i:]
		}
	}
	return s[i:]
}

// cfamQualSegs splits a C/C++ qualified name on "::" and "." outside
// template arguments, which are dropped: "a::B<c::D>::e" -> [a B e].
func cfamQualSegs(q string) []string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(q); i++ {
		switch c := q[i]; {
		case c == '<':
			depth++
		case c == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(b.String(), "::", "."), ".") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cfamChain returns the container chain of d (module identity stripped)
// from its qname, e.g. [geo Canvas add].
func cfamChain(s *Session, d *Def) []string {
	q := strings.ReplaceAll(d.QName, "::", ".")
	if m := s.Module(s.File(d.FileID)); m != "" {
		q = strings.TrimPrefix(q, m+".")
	}
	return cfamQualSegs(q)
}

// cfamChainEndsWith reports whether d's containers (its chain without its own
// name) end with segs.
func cfamChainEndsWith(s *Session, d *Def, segs []string) bool {
	if len(segs) == 0 {
		return true
	}
	chain := cfamChain(s, d)
	if len(chain) == 0 {
		return false
	}
	chain = chain[:len(chain)-1]
	if len(chain) < len(segs) {
		return false
	}
	off := len(chain) - len(segs)
	for i, seg := range segs {
		if chain[off+i] != seg {
			return false
		}
	}
	return true
}

// cfamDefinitions returns the definitions declared by decl: same name and
// container chain, same language family, not declarations themselves;
// static functions only from the declaring file.
func cfamDefinitions(s *Session, decl *Def) []*Def {
	if decl == nil || !decl.Flags.Has(model.FlagDeclaration) {
		return nil
	}
	df := s.File(decl.FileID)
	if df == nil {
		return nil
	}
	key := strings.Join(cfamChain(s, decl), ".")
	var out []*Def
	for _, d := range s.DefsNamed(decl.Name) {
		if d.ID == decl.ID || d.Flags.Has(model.FlagDeclaration) || d.Kind.IsCallable() != decl.Kind.IsCallable() {
			continue
		}
		f := s.File(d.FileID)
		if f == nil || langFamily(f.Lang) != langFamily(df.Lang) {
			continue
		}
		if d.Kind == model.KindFunction && d.Flags.Has(model.FlagStatic) && f.ID != df.ID {
			continue
		}
		if strings.Join(cfamChain(s, d), ".") == key {
			out = append(out, d)
		}
	}
	return out
}

// ---------------------------------------------------------------- receivers

// cfamOwnerTypesFunc returns the class(es) whose scope a definition's body is
// in (C++ out-of-line members); nil for C.
type cfamOwnerTypesFunc func(s *Session, d *Def) []*Def

// cfamReceiver types member accesses: the generic text scan decides the
// shape; untyped receivers are typed from a local declaration, "this", or
// a field of the enclosing class (C++).
func cfamReceiver(s *Session, site *Site, lr LangResolver, owners cfamOwnerTypesFunc) Receiver {
	rv := s.TextReceiver(site)
	if rv.Kind != RecvUntyped {
		return rv
	}
	src := s.Source(site.File)
	root, ptr := cfamReceiverRoot(src, site)
	if root == "" {
		return rv
	}
	if root == "this" {
		if owners != nil {
			if ts := owners(s, site.Src); len(ts) > 0 {
				return Receiver{Kind: RecvTyped, Types: ts}
			}
		}
		return rv
	}
	typ, ok := cfamLocalVarType(src, site, root)
	if !ok && owners != nil {
		typ, ok = cfamFieldType(s, site, root, owners)
	}
	if !ok {
		return rv
	}
	return cfamTypeReceiver(s, site.File, lr, typ, ptr, 0)
}

// cfamTypeReceiver resolves a type text to receiver types, following typedef
// and using aliases a few levels deep; ptr unwraps smart pointers (->).
func cfamTypeReceiver(s *Session, f *File, lr LangResolver, typ string, ptr bool, depth int) Receiver {
	if ptr {
		typ = cfamUnwrapSmartPointer(typ)
	}
	segs := cfamQualSegs(typ)
	if len(segs) == 0 || depth > 3 {
		return Receiver{Kind: RecvUntyped}
	}
	name, qual := segs[len(segs)-1], strings.Join(segs[:len(segs)-1], "::")
	tr := lr.ResolveType(s, f, qual, name)
	if tr.External {
		return Receiver{Kind: RecvExternal}
	}
	if !tr.Found() {
		return Receiver{Kind: RecvUntyped}
	}
	var types []*Def
	for _, d := range tr.Defs {
		if d.Kind != model.KindType {
			types = appendUnique(types, d)
			continue
		}
		if target := cfamAliasTarget(d.Sig); target != "" {
			r := cfamTypeReceiver(s, s.File(d.FileID), lr, target, ptr, depth+1)
			if r.Kind == RecvExternal && len(tr.Defs) == 1 {
				return r
			}
			types = appendUnique(types, r.Types...)
		}
	}
	if len(types) == 0 {
		return Receiver{Kind: RecvUntyped}
	}
	return Receiver{Kind: RecvTyped, Types: types}
}

// cfamAliasTarget returns the aliased type of a typedef/using signature:
// "typedef struct Node Node_t" -> "Node", "using P = std::unique_ptr<T>"
// -> "std::unique_ptr<T>".
func cfamAliasTarget(sig string) string {
	sig = strings.TrimSpace(sig)
	if rest, ok := strings.CutPrefix(sig, "using "); ok {
		if _, target, ok := strings.Cut(rest, "="); ok {
			return strings.TrimSpace(target)
		}
		return ""
	}
	rest, ok := strings.CutPrefix(sig, "typedef ")
	if !ok {
		return ""
	}
	fields := strings.Fields(cfamStripElaborated(rest))
	if len(fields) < 2 || strings.ContainsAny(rest, "(*[") {
		return ""
	}
	return strings.Join(fields[:len(fields)-1], " ")
}

// cfamStripElaborated removes struct/class/union/enum/const/volatile keywords.
func cfamStripElaborated(t string) string {
	var out []string
	for _, w := range strings.Fields(t) {
		switch w {
		case "struct", "class", "union", "enum", "const", "volatile", "typename", "static", "mutable", "constexpr", "inline":
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// cfamUnwrapSmartPointer maps std::unique_ptr<T> / shared_ptr<T> / weak_ptr<T>
// accessed through -> to T.
func cfamUnwrapSmartPointer(typ string) string {
	t := strings.TrimSpace(typ)
	for _, p := range []string{"std::unique_ptr<", "std::shared_ptr<", "std::weak_ptr<", "unique_ptr<", "shared_ptr<"} {
		if strings.HasPrefix(t, p) && strings.HasSuffix(t, ">") {
			inner := t[len(p) : len(t)-1]
			if i := strings.IndexByte(inner, ','); i >= 0 {
				inner = inner[:i]
			}
			return strings.TrimSpace(inner)
		}
	}
	return typ
}

// cfamReceiverRoot returns the identifier the member access at site is applied
// to ("r" in r.x, "this" in this->m) and whether it used "->". Chains and
// call results return "".
func cfamReceiverRoot(src []byte, site *Site) (string, bool) {
	line := lineAt(src, site.Line)
	if line == nil || int(site.Col) > len(line) {
		return "", false
	}
	before := bytes.TrimRight(line[:site.Col], " \t")
	ptr := false
	switch {
	case bytes.HasSuffix(before, []byte("->")):
		before, ptr = before[:len(before)-2], true
	case bytes.HasSuffix(before, []byte(".")):
		before = before[:len(before)-1]
	default:
		return "", false
	}
	before = bytes.TrimRight(before, " \t")
	i := len(before)
	for i > 0 && isIdentByte(before[i-1]) {
		i--
	}
	if i > 0 && bytes.ContainsAny(before[i-1:i], ".>:)]") {
		return "", false
	}
	return string(before[i:]), ptr
}

// cfamDeclKeywords are words that can precede an identifier without declaring
// it.
var cfamDeclKeywords = map[string]bool{
	"return": true, "else": true, "case": true, "goto": true, "sizeof": true, "delete": true,
	"new": true, "throw": true, "co_return": true, "co_yield": true, "co_await": true,
	"typedef": true, "using": true, "namespace": true, "do": true, "operator": true, "default": true,
}

// cfamLocalVarType finds the declared type of root in the enclosing function
// before the reference: "geo::Canvas canvas;", "Vec2 *p,", "const T& t)".
// "auto x = std::make_unique<T>(...)" and "auto x = new T(...)" yield T or
// a smart pointer to it.
func cfamLocalVarType(src []byte, site *Site, root string) (string, bool) {
	if src == nil || site.Src == nil {
		return "", false
	}
	end := cfamLineOffset(src, site.Line) + int(site.Col)
	start := int(site.Src.StartByte)
	if start < 0 || end > len(src) || start >= end {
		return "", false
	}
	text := src[start:end]
	for pos := len(text); pos > 0; {
		i := bytes.LastIndex(text[:pos], []byte(root))
		if i < 0 {
			return "", false
		}
		pos = i
		j := i + len(root)
		if (i > 0 && isIdentByte(text[i-1])) || (j < len(text) && isIdentByte(text[j])) {
			continue
		}
		after := bytes.TrimLeft(text[j:], " \t\r\n")
		if len(after) == 0 || !bytes.ContainsAny(after[:1], ";=,)[({:") || bytes.HasPrefix(after, []byte("::")) ||
			bytes.HasPrefix(after, []byte("==")) {
			continue
		}
		before := bytes.TrimRight(text[:i], " \t\r\n*&")
		before = bytes.TrimSuffix(before, []byte("const"))
		before = bytes.TrimRight(before, " \t\r\n*&")
		tok := cfamTypeToken(string(before))
		if tok == "" || cfamDeclKeywords[tok] || strings.HasSuffix(tok, ":") {
			continue
		}
		// The token must start a declaration, not end an expression.
		prev := bytes.TrimRight(before[:len(before)-len(tok)], " \t\r\n")
		if len(prev) > 0 && !bytes.ContainsAny(prev[len(prev)-1:], ";{}(,") && !cfamEndsWithSpecifier(prev) {
			continue
		}
		if tok == "auto" {
			return cfamAutoInitType(after)
		}
		return tok, true
	}
	return "", false
}

// cfamEndsWithSpecifier reports whether b ends with a declaration specifier such as
// const, static or struct.
func cfamEndsWithSpecifier(b []byte) bool {
	i := len(b)
	for i > 0 && isIdentByte(b[i-1]) {
		i--
	}
	switch string(b[i:]) {
	case "const", "static", "struct", "class", "union", "enum", "volatile", "register",
		"constexpr", "mutable", "typename", "extern", "thread_local":
		return true
	}
	return false
}

// cfamAutoInitType infers the type of "auto x = <init>" for make_unique,
// make_shared and new expressions.
func cfamAutoInitType(after []byte) (string, bool) {
	if len(after) == 0 || after[0] != '=' {
		return "", false
	}
	init := strings.TrimSpace(string(after[1:]))
	for _, p := range []string{"std::make_unique<", "std::make_shared<", "make_unique<", "make_shared<"} {
		if rest, ok := strings.CutPrefix(init, p); ok {
			if i := strings.IndexAny(rest, ">("); i > 0 {
				return "std::unique_ptr<" + strings.TrimSpace(rest[:i]) + ">", true
			}
		}
	}
	if rest, ok := strings.CutPrefix(init, "new "); ok {
		if t := cfamTypeToken(strings.TrimSpace(strings.SplitN(rest, "(", 2)[0])); t != "" {
			return t, true
		}
	}
	return "", false
}

// cfamFieldType types root as a field of the class owning the enclosing
// definition, from the field's signature ("std::vector<int> items_").
func cfamFieldType(s *Session, site *Site, root string, owners cfamOwnerTypesFunc) (string, bool) {
	types := owners(s, site.Src)
	if enc := s.EnclosingType(site.Src); enc != nil {
		types = appendUnique(types, enc)
	}
	for _, t := range types {
		for _, m := range s.Members(t, root) {
			if m.Kind != model.KindField && m.Kind != model.KindVariable && m.Kind != model.KindConstant {
				continue
			}
			sig := strings.TrimSpace(m.Sig)
			if i := strings.LastIndex(sig, root); i > 0 {
				if tok := cfamTypeToken(cfamStripElaborated(strings.TrimRight(sig[:i], " \t*&"))); tok != "" {
					return tok, true
				}
			}
		}
	}
	return "", false
}

// cfamLineOffset returns the byte offset of the 1-based line n.
func cfamLineOffset(src []byte, n uint32) int {
	off := 0
	for i := uint32(1); i < n; i++ {
		j := bytes.IndexByte(src[off:], '\n')
		if j < 0 {
			return len(src)
		}
		off += j + 1
	}
	return off
}
