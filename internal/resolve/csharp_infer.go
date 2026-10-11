package resolve

import (
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tscs "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

// C# receiver typing (T3). The receiver of x.M() is typed from the source:
// locals and parameters of the enclosing member (declared type, or the
// initializer of a var), this/base, fields and properties of the enclosing
// types (from their sigs), static access through a type name, namespace
// paths, object creation, casts, and method results (from sigs). Unknown
// capitalized roots that are not repository namespaces are external
// (Console, Assert, Math, ...).

// csParserState is the run-wide parser.
type csParserState struct{ parser *sitter.Parser }

func (p *csParserState) Close() { p.parser.Close() }

// csTrees caches parsed files for one batch.
type csTrees map[int64]*sitter.Tree

func (t csTrees) Close() {
	for _, tr := range t {
		if tr != nil {
			tr.Close()
		}
	}
}

func csTree(s *Session, f *File, src []byte) *sitter.Tree {
	trees, ok := s.BatchState(model.LangCSharp).(csTrees)
	if !ok {
		trees = csTrees{}
		s.SetBatchState(model.LangCSharp, trees)
	}
	if tr, ok := trees[f.ID]; ok {
		return tr
	}
	st := csStateOf(s)
	if st.infer == nil {
		p := sitter.NewParser()
		if err := p.SetLanguage(sitter.NewLanguage(tscs.Language())); err != nil {
			p.Close()
			trees[f.ID] = nil
			return nil
		}
		st.infer = &csParserState{parser: p}
	}
	tr := st.infer.parser.Parse(src, nil)
	trees[f.ID] = tr
	return tr
}

// cvKind classifies an inferred expression.
type cvKind uint8

const (
	cvUnknown   cvKind = iota // could not infer
	cvExternal                // a value or type outside the repository
	cvValue                   // a value of one of types
	cvStatic                  // the expression names one of types itself
	cvNamespace               // the expression is a namespace path (or unknown type)
)

type cval struct {
	kind  cvKind
	types []*Def
	path  string // cvNamespace
}

var (
	cvUnknownVal  = cval{kind: cvUnknown}
	cvExternalVal = cval{kind: cvExternal}
)

// csInfer infers expression types within one file.
type csInfer struct {
	s     *Session
	site  *Site
	src   []byte
	look  *csLookup
	depth int
}

// csReceiver classifies the C# reference at site (see LangResolver).
func csReceiver(s *Session, site *Site) Receiver {
	src := s.Source(site.File)
	if src == nil || site.Line == 0 {
		return Receiver{Kind: RecvUnknownShape}
	}
	tree := csTree(s, site.File, src)
	if tree == nil {
		return Receiver{Kind: RecvUnknownShape}
	}
	pt := sitter.Point{Row: uint(site.Line - 1), Column: uint(site.Col)}
	n := tree.RootNode().NamedDescendantForPointRange(pt, pt)
	if n == nil || n.Kind() != "identifier" || n.Utf8Text(src) != site.Name {
		return Receiver{Kind: RecvUnknownShape}
	}
	in := &csInfer{s: s, site: site, src: src, look: csLookupFor(s, site.File, site.Src)}
	if p := n.Parent(); p != nil && p.Kind() == "generic_name" {
		n = p
	}
	p := n.Parent()
	if p == nil {
		return Receiver{Kind: RecvNone}
	}
	switch p.Kind() {
	case "member_access_expression", "qualified_name":
		if nameIs(p, n) {
			left := p.ChildByFieldName("expression")
			if left == nil {
				left = p.ChildByFieldName("qualifier")
			}
			if left == nil {
				return Receiver{Kind: RecvUntyped}
			}
			return in.receiverOf(in.expr(left))
		}
	case "member_binding_expression":
		if ca := p.Parent(); ca != nil && ca.Kind() == "conditional_access_expression" {
			if cond := ca.ChildByFieldName("condition"); cond != nil {
				return in.receiverOf(in.expr(cond))
			}
		}
		return Receiver{Kind: RecvUntyped}
	}
	return in.bare(n)
}

func nameIs(p, n *sitter.Node) bool {
	nm := p.ChildByFieldName("name")
	return nm != nil && nm.StartByte() == n.StartByte() && nm.EndByte() == n.EndByte()
}

func (in *csInfer) receiverOf(v cval) Receiver {
	switch v.kind {
	case cvValue, cvStatic:
		if len(v.types) > 0 {
			return Receiver{Kind: RecvTyped, Types: v.types}
		}
		return Receiver{Kind: RecvUntyped}
	case cvExternal:
		return Receiver{Kind: RecvExternal}
	case cvNamespace:
		// A repository namespace: the name is a type looked up by name.
		// Anything else is an external type or namespace.
		if csKnownNamespace(in.s, v.path) {
			return Receiver{Kind: RecvNone}
		}
		return Receiver{Kind: RecvExternal}
	}
	return Receiver{Kind: RecvUntyped}
}

// bare handles a reference without receiver: a call through using static
// is typed by the imported type, everything else is left to the lexical
// tiers.
func (in *csInfer) bare(n *sitter.Node) Receiver {
	if in.site.Kind != model.EdgeCalls || len(in.look.info.statics) == 0 {
		return Receiver{Kind: RecvNone}
	}
	if p := n.Parent(); p == nil || p.Kind() != "invocation_expression" {
		return Receiver{Kind: RecvNone}
	}
	name := in.site.Name
	for d := in.site.Src; d != nil; d = in.s.Parent(d) {
		if d.Kind.IsType() && len(in.s.Members(d, name)) > 0 {
			return Receiver{Kind: RecvNone}
		}
		if slices.ContainsFunc(in.s.Children(d), func(c *Def) bool { return c.Name == name }) {
			return Receiver{Kind: RecvNone}
		}
	}
	var types []*Def
	for _, st := range in.look.info.statics {
		q, last := csSplitLast(st)
		for _, t := range in.look.qualified(q, last, 0, true).Defs {
			if len(in.s.Members(t, name)) > 0 {
				types = appendUnique(types, t)
			}
		}
	}
	if len(types) > 0 {
		return Receiver{Kind: RecvTyped, Types: types}
	}
	return Receiver{Kind: RecvNone}
}

// expr infers the type of an expression node.
func (in *csInfer) expr(n *sitter.Node) cval {
	if n == nil || in.depth > maxDepth {
		return cvUnknownVal
	}
	in.depth++
	defer func() { in.depth-- }()

	switch n.Kind() {
	case "this":
		if t := in.s.EnclosingType(in.site.Src); t != nil {
			return cval{kind: cvValue, types: []*Def{t}}
		}
		return cvUnknownVal
	case "base":
		return in.base()
	case "identifier":
		return in.ident(n)
	case "generic_name":
		if id := firstNamedOfKind(n, "identifier"); id != nil {
			return in.staticType("", id.Utf8Text(in.src))
		}
	case "alias_qualified_name": // global::X
		if nm := n.ChildByFieldName("name"); nm != nil {
			return in.expr(nm)
		}
	case "member_access_expression", "qualified_name":
		left := n.ChildByFieldName("expression")
		if left == nil {
			left = n.ChildByFieldName("qualifier")
		}
		nm := n.ChildByFieldName("name")
		if left == nil || nm == nil {
			return cvUnknownVal
		}
		return in.member(in.expr(left), csNodeName(nm, in.src))
	case "invocation_expression":
		return in.call(n.ChildByFieldName("function"))
	case "object_creation_expression", "cast_expression":
		return in.typeNode(n.ChildByFieldName("type"))
	case "as_expression":
		return in.typeNode(n.ChildByFieldName("right"))
	case "parenthesized_expression":
		if c := n.NamedChild(0); c != nil {
			return in.expr(c)
		}
	case "predefined_type", "string_literal", "verbatim_string_literal", "raw_string_literal",
		"interpolated_string_expression", "integer_literal", "real_literal", "boolean_literal",
		"character_literal", "typeof_expression", "array_creation_expression",
		"implicit_array_creation_expression", "tuple_expression":
		return cvExternalVal
	}
	return cvUnknownVal
}

// csNodeName returns the identifier of a name node (generic args dropped).
func csNodeName(n *sitter.Node, src []byte) string {
	if n.Kind() == "generic_name" {
		if id := firstNamedOfKind(n, "identifier"); id != nil {
			return id.Utf8Text(src)
		}
	}
	return n.Utf8Text(src)
}

func firstNamedOfKind(n *sitter.Node, kind string) *sitter.Node {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Kind() == kind {
			return c
		}
	}
	return nil
}

// base types base.X: the supertypes of the enclosing type (not its partial
// sibling parts).
func (in *csInfer) base() cval {
	t := in.s.EnclosingType(in.site.Src)
	if t == nil {
		return cvUnknownVal
	}
	anc := filter(in.s.Ancestors(t), func(a *Def) bool { return a.QName != t.QName })
	if len(anc) == 0 {
		return cvExternalVal
	}
	return cval{kind: cvValue, types: anc}
}

// ident types a bare identifier: local or parameter, member of an
// enclosing type, type name, else a namespace root (or external).
func (in *csInfer) ident(n *sitter.Node) cval {
	name := n.Utf8Text(in.src)
	if v, ok := in.local(n, name); ok {
		return v
	}
	for t := in.s.EnclosingType(in.site.Src); t != nil; t = in.s.EnclosingType(in.s.Parent(t)) {
		if v, ok := in.memberValue(t, name); ok {
			return v
		}
	}
	if v := in.staticType("", name); v.kind == cvStatic {
		return v
	}
	if r, _ := firstRune(name); r >= 'a' && r <= 'z' || r == '_' {
		return cvUnknownVal // likely an untracked local or inherited member
	}
	return cval{kind: cvNamespace, path: name}
}

func firstRune(s string) (byte, bool) {
	if s == "" {
		return 0, false
	}
	return s[0], true
}

// member types left.name.
func (in *csInfer) member(left cval, name string) cval {
	switch left.kind {
	case cvNamespace:
		if v := in.staticType(left.path, name); v.kind == cvStatic {
			return v
		}
		return cval{kind: cvNamespace, path: left.path + "." + name}
	case cvValue, cvStatic:
		for _, t := range left.types {
			if v, ok := in.memberValue(t, name); ok {
				return v
			}
		}
		return cvUnknownVal
	}
	return left
}

// memberValue types the member called name of type t (fields, properties,
// constants, nested types); ok is false when t has no such member.
func (in *csInfer) memberValue(t *Def, name string) (cval, bool) {
	ms := in.s.Members(t, name)
	if len(ms) == 0 {
		return cval{}, false
	}
	for _, m := range ms {
		switch {
		case m.Kind.IsType():
			return cval{kind: cvStatic, types: csPartsAll(in.s, filter(ms, func(d *Def) bool { return d.Kind.IsType() }))}, true
		case m.Kind == model.KindEnumMember:
			if p := in.s.Parent(m); p != nil {
				return cval{kind: cvValue, types: []*Def{p}}, true
			}
		case m.Kind == model.KindField || m.Kind == model.KindProperty || m.Kind == model.KindConstant:
			return in.declared(m, csSigType(m.Sig, m.Name)), true
		}
	}
	return cvUnknownVal, true
}

// call types the result of invoking fn.
func (in *csInfer) call(fn *sitter.Node) cval {
	if fn == nil {
		return cvUnknownVal
	}
	var (
		methods []*Def
		name    string
	)
	switch fn.Kind() {
	case "identifier", "generic_name":
		name = csNodeName(fn, in.src)
		for t := in.s.EnclosingType(in.site.Src); t != nil && len(methods) == 0; t = in.s.EnclosingType(in.s.Parent(t)) {
			methods = in.s.Members(t, name)
		}
		if len(methods) == 0 {
			for d := in.site.Src; d != nil && len(methods) == 0; d = in.s.Parent(d) {
				methods = filter(in.s.Children(d), func(c *Def) bool { return c.Name == name })
			}
		}
	case "member_access_expression":
		left := in.expr(fn.ChildByFieldName("expression"))
		nm := fn.ChildByFieldName("name")
		if nm == nil {
			return cvUnknownVal
		}
		name = csNodeName(nm, in.src)
		switch left.kind {
		case cvValue, cvStatic:
			for _, t := range left.types {
				methods = appendUnique(methods, in.s.Members(t, name)...)
			}
		case cvExternal:
			return cvExternalVal
		case cvNamespace:
			if !csKnownNamespace(in.s, left.path) {
				return cvExternalVal
			}
		}
	default:
		return cvUnknownVal
	}
	for _, m := range methods {
		if m.Kind == model.KindMethod || m.Kind == model.KindFunction {
			return in.declared(m, csSigType(m.Sig, m.Name))
		}
	}
	return cvUnknownVal
}

// staticType resolves qual.name as a type name in the site's context.
func (in *csInfer) staticType(qual, name string) cval {
	tr := in.look.types(qual, name, 0)
	if !tr.Found() {
		return cvUnknownVal
	}
	return cval{kind: cvStatic, types: tr.Defs}
}

// typeNode types a value whose type is written as node n in the site file.
func (in *csInfer) typeNode(n *sitter.Node) cval {
	if n == nil {
		return cvUnknownVal
	}
	return in.typeText(in.look, n.Utf8Text(in.src))
}

// declared types a value declared with type text typ by definition d (the
// text is resolved in d's file and namespace).
func (in *csInfer) declared(d *Def, typ string) cval {
	f := in.s.File(d.FileID)
	if f == nil || typ == "" {
		return cvUnknownVal
	}
	return in.typeText(csLookupFor(in.s, f, d), typ)
}

// csPredefined are C# keyword types and contextual type keywords.
var csPredefined = map[string]bool{
	"bool": true, "byte": true, "sbyte": true, "char": true, "decimal": true, "double": true,
	"float": true, "int": true, "uint": true, "long": true, "ulong": true, "short": true,
	"ushort": true, "nint": true, "nuint": true, "object": true, "string": true, "void": true,
	"dynamic": true,
}

// typeText resolves a written type to the value kind it produces.
func (in *csInfer) typeText(look *csLookup, typ string) cval {
	typ = strings.TrimSpace(typ)
	for _, p := range []string{"ref ", "readonly ", "scoped ", "out ", "in ", "params "} {
		typ = strings.TrimSpace(strings.TrimPrefix(typ, p))
	}
	typ = strings.TrimSuffix(typ, "?")
	switch {
	case typ == "" || typ == "var":
		return cvUnknownVal
	case strings.HasPrefix(typ, "(") || strings.HasSuffix(typ, "]") || strings.HasSuffix(typ, "*"):
		return cvExternalVal // tuples, arrays, pointers
	case csPredefined[typ]:
		return cvExternalVal
	}
	q, name := csSplitLast(csClean(typ))
	tr := look.types(q, name, 0)
	if !tr.Found() {
		return cvExternalVal
	}
	return cval{kind: cvValue, types: tr.Defs}
}

// csSigType extracts the declared type of a field, property or method from
// its sig ("private readonly List<decimal> _lines", "public decimal
// Total()", "decimal IPayment.Charge(decimal amount)").
func csSigType(sig, name string) string {
	at := -1
	for i := 0; i+len(name) <= len(sig); i++ {
		j := strings.Index(sig[i:], name)
		if j < 0 {
			break
		}
		k := i + j
		end := k + len(name)
		before := k > 0 && (sig[k-1] == ' ' || sig[k-1] == '.')
		after := end == len(sig) || strings.ContainsRune("(<,", rune(sig[end]))
		if before && after {
			at = k
			break
		}
		i = k
	}
	if at < 0 {
		return ""
	}
	head := strings.TrimSpace(sig[:at])
	if strings.HasSuffix(head, ".") { // explicit interface implementation
		if i := strings.LastIndexByte(head, ' '); i >= 0 {
			head = head[:i]
		} else {
			return ""
		}
	}
	words := strings.Fields(head)
	for len(words) > 0 && csModifiers[words[0]] {
		words = words[1:]
	}
	return strings.Join(words, " ")
}

var csModifiers = map[string]bool{
	"public": true, "private": true, "protected": true, "internal": true, "static": true,
	"readonly": true, "const": true, "volatile": true, "virtual": true, "override": true,
	"abstract": true, "sealed": true, "new": true, "extern": true, "unsafe": true,
	"async": true, "partial": true, "required": true, "event": true, "fixed": true, "file": true,
}

// csPartsAll expands type defs to all their partial parts.
func csPartsAll(s *Session, ts []*Def) []*Def {
	var out []*Def
	for _, t := range ts {
		out = appendUnique(out, csParts(s, t)...)
	}
	return out
}

// local finds the closest declaration of name preceding n inside the
// enclosing member and returns its inferred type (cvUnknown for a local
// whose type cannot be inferred). ok is false when name is not a local.
func (in *csInfer) local(n *sitter.Node, name string) (cval, bool) {
	scope := n.Parent()
	for scope != nil && !csIsMemberNode(scope.Kind()) {
		scope = scope.Parent()
	}
	if scope == nil {
		return cval{}, false
	}
	var best *sitter.Node
	pos := n.StartByte()
	var walk func(c *sitter.Node, depth int)
	walk = func(c *sitter.Node, depth int) {
		if depth > 200 || c.StartByte() >= pos {
			return
		}
		if decl := csDeclName(c); decl != nil && decl.Utf8Text(in.src) == name && decl.StartByte() < pos && csInScope(c, pos) {
			if best == nil || c.StartByte() > best.StartByte() {
				best = c
			}
		}
		for i := uint(0); i < c.NamedChildCount(); i++ {
			walk(c.NamedChild(i), depth+1)
		}
	}
	walk(scope, 0)
	if best == nil {
		return cval{}, false
	}
	return in.localType(best), true
}

// csInScope reports whether the scope introduced by declaration node c
// (its nearest enclosing block, loop, lambda, catch or member) contains pos.
func csInScope(c *sitter.Node, pos uint) bool {
	s := c
	if c.Kind() != "foreach_statement" {
		s = c.Parent()
	}
	for s != nil {
		switch k := s.Kind(); k {
		case "block", "switch_section", "for_statement", "foreach_statement", "using_statement",
			"fixed_statement", "catch_clause", "lambda_expression", "anonymous_method_expression",
			"local_function_statement":
			return s.StartByte() <= pos && pos < s.EndByte()
		default:
			if csIsMemberNode(k) {
				return s.StartByte() <= pos && pos < s.EndByte()
			}
		}
		s = s.Parent()
	}
	return true
}

// csIsMemberNode reports node kinds that delimit local scopes.
func csIsMemberNode(kind string) bool {
	switch kind {
	case "method_declaration", "constructor_declaration", "destructor_declaration",
		"operator_declaration", "conversion_operator_declaration", "property_declaration",
		"indexer_declaration", "event_declaration", "accessor_declaration",
		"field_declaration", "class_declaration", "struct_declaration", "record_declaration":
		return true
	}
	return false
}

// csDeclName returns the declared name node of a local declaration node.
func csDeclName(c *sitter.Node) *sitter.Node {
	switch c.Kind() {
	case "variable_declarator", "parameter", "declaration_pattern", "catch_declaration",
		"local_function_statement":
		return c.ChildByFieldName("name")
	case "declaration_expression":
		return c.ChildByFieldName("name")
	case "foreach_statement":
		if l := c.ChildByFieldName("left"); l != nil && l.Kind() == "identifier" {
			return l
		}
	case "implicit_parameter":
		return c
	}
	return nil
}

// localType infers the type of a local declaration node.
func (in *csInfer) localType(decl *sitter.Node) cval {
	var typ *sitter.Node
	switch decl.Kind() {
	case "variable_declarator":
		if vd := decl.Parent(); vd != nil && vd.Kind() == "variable_declaration" {
			typ = vd.ChildByFieldName("type")
		}
		if typ != nil && typ.Kind() == "implicit_type" {
			// var x = <init>: the initializer is the declarator's last
			// named child, unless that is the name itself.
			name := decl.ChildByFieldName("name")
			if k := decl.NamedChildCount(); k > 0 {
				if c := decl.NamedChild(k - 1); name == nil || c.StartByte() != name.StartByte() {
					return in.expr(c)
				}
			}
			return cvUnknownVal
		}
	case "local_function_statement", "implicit_parameter":
		return cvUnknownVal
	default:
		typ = decl.ChildByFieldName("type")
	}
	if typ == nil || typ.Kind() == "implicit_type" {
		return cvUnknownVal
	}
	return in.typeNode(typ)
}
