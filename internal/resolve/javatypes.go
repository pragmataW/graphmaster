package resolve

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Java receiver typing (T3). The type of a receiver expression is inferred
// from the referencing file's syntax tree: locals (closest preceding
// declaration in scope, var from its initializer), parameters, lambda,
// catch, resource and pattern variables, fields of the enclosing classes and
// their ancestors, this/super, new, casts, and field types / method return
// types read from the signatures of resolved members.

// javaState is the run-wide Java state: one parser.
type javaState struct{ parser *sitter.Parser }

func (j *javaState) Close() { j.parser.Close() }

// javaTrees caches parsed files for one batch.
type javaTrees map[int64]*sitter.Tree

func (t javaTrees) Close() {
	for _, tr := range t {
		if tr != nil {
			tr.Close()
		}
	}
}

func javaTree(s *Session, f *File, src []byte) *sitter.Tree {
	trees, ok := s.BatchState(model.LangJava).(javaTrees)
	if !ok {
		trees = javaTrees{}
		s.SetBatchState(model.LangJava, trees)
	}
	if tr, ok := trees[f.ID]; ok {
		return tr
	}
	st, ok := s.State(model.LangJava).(*javaState)
	if !ok {
		p := sitter.NewParser()
		if err := p.SetLanguage(sitter.NewLanguage(tsjava.Language())); err != nil {
			p.Close()
			trees[f.ID] = nil
			return nil
		}
		st = &javaState{parser: p}
		s.SetState(model.LangJava, st)
	}
	tr := st.parser.Parse(src, nil)
	trees[f.ID] = tr
	return tr
}

// jvKind classifies an inferred value.
type jvKind uint8

const (
	jvUnknown  jvKind = iota // could not infer
	jvExternal               // primitive, platform or out-of-repo type
	jvTypes                  // types holds the value's (static) type
)

type jval struct {
	kind  jvKind
	types []*Def
	dims  int // array dimensions of the value
}

var (
	jUnknown  = jval{kind: jvUnknown}
	jExternal = jval{kind: jvExternal}
)

// javaInfer infers expression types within one file.
type javaInfer struct {
	s     *Session
	r     javaResolver
	file  *File
	src   []byte
	depth int
}

// javaReceiver classifies the Java reference at site (see LangResolver).
func javaReceiver(s *Session, r javaResolver, site *Site) Receiver {
	if site.Kind == model.EdgeTests || site.Line == 0 {
		return Receiver{Kind: RecvNone}
	}
	src := s.Source(site.File)
	if src == nil {
		return Receiver{Kind: RecvUnknownShape}
	}
	tree := javaTree(s, site.File, src)
	if tree == nil {
		return Receiver{Kind: RecvUnknownShape}
	}
	pt := sitter.Point{Row: uint(site.Line - 1), Column: uint(site.Col)}
	n := tree.RootNode().NamedDescendantForPointRange(pt, pt)
	if n == nil || n.Utf8Text(src) != site.Name {
		return Receiver{Kind: RecvUnknownShape}
	}
	p := n.Parent()
	if p == nil {
		return Receiver{Kind: RecvNone}
	}
	in := &javaInfer{s: s, r: r, file: site.File, src: src}
	switch p.Kind() {
	case "method_invocation":
		if sameNode(p.ChildByFieldName("name"), n) {
			if obj := p.ChildByFieldName("object"); obj != nil {
				return in.receiverOf(in.expr(obj))
			}
			return in.implicitCall(n, site.Name)
		}
	case "field_access":
		if sameNode(p.ChildByFieldName("field"), n) {
			return in.receiverOf(in.expr(p.ChildByFieldName("object")))
		}
	case "method_reference":
		if first := p.NamedChild(0); first != nil && !sameNode(first, n) {
			if isJavaTypeNode(first) {
				return in.receiverOf(in.typeNode(first))
			}
			return in.receiverOf(in.expr(first))
		}
	}
	if n.Kind() == "type_identifier" {
		creation := p
		if creation.Kind() == "generic_type" {
			creation = creation.Parent()
		}
		if site.Kind == model.EdgeCalls && creation != nil && creation.Kind() == "object_creation_expression" {
			return in.constructor(site.Name)
		}
		return in.typeRef(n, site.Name)
	}
	if p.Kind() == "marker_annotation" || p.Kind() == "annotation" {
		return in.typeRef(n, site.Name)
	}
	return Receiver{Kind: RecvNone}
}

func sameNode(a, b *sitter.Node) bool {
	return a != nil && b != nil && a.StartByte() == b.StartByte() && a.EndByte() == b.EndByte()
}

func isJavaTypeNode(n *sitter.Node) bool {
	switch n.Kind() {
	case "type_identifier", "scoped_type_identifier", "generic_type", "array_type",
		"integral_type", "floating_point_type", "boolean_type":
		return true
	}
	return false
}

func (in *javaInfer) receiverOf(v jval) Receiver {
	switch {
	case v.kind == jvExternal || (v.kind == jvTypes && v.dims > 0):
		return Receiver{Kind: RecvExternal}
	case v.kind == jvTypes && len(v.types) > 0:
		return Receiver{Kind: RecvTyped, Types: v.types}
	}
	return Receiver{Kind: RecvUntyped}
}

// constructor classifies new T(...): T's constructors when it declares
// any, the nested type through its owner, else the type itself (RecvNone).
func (in *javaInfer) constructor(name string) Receiver {
	tr := in.r.ResolveType(in.s, in.file, "", name)
	switch {
	case tr.External:
		return Receiver{Kind: RecvExternal}
	case !tr.Found():
		return Receiver{Kind: RecvNone}
	}
	for _, t := range tr.Defs {
		for _, c := range in.s.Children(t) {
			if c.Kind == model.KindConstructor && c.Name == name {
				return Receiver{Kind: RecvTyped, Types: tr.Defs}
			}
		}
	}
	return in.nestedOwners(tr.Defs)
}

// typeRef classifies a bare type name: nested types are reached through
// their owner (they are members), top-level ones by the generic tiers.
func (in *javaInfer) typeRef(at *sitter.Node, name string) Receiver {
	if in.isTypeParam(name, at) {
		return Receiver{Kind: RecvExternal}
	}
	tr := in.r.ResolveType(in.s, in.file, "", name)
	switch {
	case tr.External:
		return Receiver{Kind: RecvExternal}
	case !tr.Found():
		return Receiver{Kind: RecvNone}
	}
	return in.nestedOwners(tr.Defs)
}

func (in *javaInfer) nestedOwners(types []*Def) Receiver {
	var owners []*Def
	for _, t := range types {
		p := in.s.Parent(t)
		if p == nil || !p.Kind.IsType() {
			return Receiver{Kind: RecvNone}
		}
		owners = appendUnique(owners, p)
	}
	return Receiver{Kind: RecvTyped, Types: owners}
}

// implicitCall classifies an unqualified call m(...): a method of an
// enclosing type (or its ancestors), else a statically imported method.
func (in *javaInfer) implicitCall(at *sitter.Node, name string) Receiver {
	for _, level := range in.thisTypes(at) {
		var with []*Def
		for _, t := range level {
			if len(filter(in.s.Members(t, name), isCallableDef)) > 0 {
				with = append(with, t)
			}
		}
		if len(with) > 0 {
			return Receiver{Kind: RecvTyped, Types: with}
		}
	}
	if owner := in.staticImportOwner(name); owner.kind != jvUnknown {
		return in.receiverOf(owner)
	}
	return Receiver{Kind: RecvNone}
}

// staticImportOwner finds the type a static import brings name from:
// import static a.B.name; or import static a.B.*; with B declaring name.
func (in *javaInfer) staticImportOwner(name string) jval {
	for _, im := range in.s.Imports(in.file) {
		if im.Wildcard() || !javaHasName(im.Names, name) {
			continue
		}
		i := strings.LastIndexByte(im.Spec, '.')
		if i <= 0 {
			continue
		}
		owner := in.typeText(im.Spec[:i], in.file, nil)
		if owner.kind == jvTypes && !in.hasMember(owner.types, name) {
			continue
		}
		return owner
	}
	for _, im := range in.s.Imports(in.file) {
		if !im.Wildcard() || in.s.ImportTarget(in.file, im.Spec).Kind != model.TargetFile {
			continue
		}
		if owner := in.typeText(im.Spec, in.file, nil); owner.kind == jvTypes && in.hasMember(owner.types, name) {
			return owner
		}
	}
	return jUnknown
}

func (in *javaInfer) hasMember(types []*Def, name string) bool {
	for _, t := range types {
		if len(in.s.Members(t, name)) > 0 {
			return true
		}
	}
	return false
}

func isCallableDef(d *Def) bool { return d.Kind.IsCallable() && d.Kind != model.KindConstructor }

func isFieldDef(d *Def) bool {
	switch d.Kind {
	case model.KindField, model.KindConstant, model.KindEnumMember, model.KindProperty:
		return true
	}
	return false
}

func (in *javaInfer) text(n *sitter.Node) string { return n.Utf8Text(in.src) }

// expr infers the type of expression n.
func (in *javaInfer) expr(n *sitter.Node) jval {
	if n == nil || in.depth > maxDepth {
		return jUnknown
	}
	in.depth++
	defer func() { in.depth-- }()

	switch n.Kind() {
	case "identifier":
		return in.ident(n, in.text(n))
	case "this":
		if levels := in.thisTypes(n); len(levels) > 0 {
			return jval{kind: jvTypes, types: levels[0]}
		}
		return jUnknown
	case "super":
		return in.super(n)
	case "parenthesized_expression":
		return in.expr(n.NamedChild(0))
	case "cast_expression":
		return in.typeNode(n.ChildByFieldName("type"))
	case "object_creation_expression":
		return in.typeNode(n.ChildByFieldName("type"))
	case "array_creation_expression":
		v := in.typeNode(n.ChildByFieldName("type"))
		if v.kind == jvTypes {
			v.dims++
		}
		return v
	case "array_access":
		v := in.expr(n.ChildByFieldName("array"))
		if v.kind == jvTypes {
			if v.dims == 0 {
				return jUnknown
			}
			v.dims--
		}
		return v
	case "field_access":
		f := n.ChildByFieldName("field")
		if f == nil {
			return jUnknown
		}
		obj := n.ChildByFieldName("object")
		if f.Kind() == "this" { // Outer.this
			return in.expr(obj)
		}
		return in.member(in.expr(obj), in.text(f), false)
	case "method_invocation":
		name := n.ChildByFieldName("name")
		if name == nil {
			return jUnknown
		}
		if obj := n.ChildByFieldName("object"); obj != nil {
			return in.member(in.expr(obj), in.text(name), true)
		}
		for _, level := range in.thisTypes(n) {
			if v := in.member(jval{kind: jvTypes, types: level}, in.text(name), true); v.kind != jvUnknown {
				return v
			}
		}
		return in.member(in.staticImportOwner(in.text(name)), in.text(name), true)
	case "ternary_expression":
		return in.expr(n.ChildByFieldName("consequence"))
	case "assignment_expression":
		return in.expr(n.ChildByFieldName("left"))
	case "string_literal", "text_block", "character_literal", "true", "false", "null_literal",
		"decimal_integer_literal", "hex_integer_literal", "octal_integer_literal", "binary_integer_literal",
		"decimal_floating_point_literal", "hex_floating_point_literal", "class_literal",
		"instanceof_expression", "binary_expression", "unary_expression", "update_expression":
		return jExternal
	}
	return jUnknown
}

// super is the supertypes of the innermost enclosing type.
func (in *javaInfer) super(at *sitter.Node) jval {
	levels := in.thisTypes(at)
	if len(levels) == 0 {
		return jUnknown
	}
	var anc []*Def
	for _, t := range levels[0] {
		anc = appendUnique(anc, in.s.Ancestors(t)...)
	}
	if len(anc) == 0 {
		return jExternal // superclass outside the repository (or Object)
	}
	return jval{kind: jvTypes, types: anc}
}

// ident infers a simple name: a local or parameter, a field of an
// enclosing type, or a type name (static access).
func (in *javaInfer) ident(at *sitter.Node, name string) jval {
	if typ, val, dims, ok := in.lookupLocal(at, name); ok {
		var v jval
		switch {
		case typ != nil && in.text(typ) != "var":
			v = in.typeNode(typ)
		case val != nil:
			v = in.expr(val)
		default:
			return jUnknown
		}
		if v.kind == jvTypes {
			v.dims += dims
		}
		return v
	}
	for _, level := range in.thisTypes(at) {
		if v := in.member(jval{kind: jvTypes, types: level}, name, false); v.kind != jvUnknown {
			return v
		}
	}
	if isUpperRuneStr(name) {
		return in.typeText(name, in.file, nil)
	}
	return jUnknown
}

// member infers the type of v.name (a field, or a method's result).
func (in *javaInfer) member(v jval, name string, call bool) jval {
	switch {
	case v.kind == jvExternal:
		return jExternal
	case v.kind != jvTypes:
		return jUnknown
	case v.dims > 0:
		return jExternal // length, clone(), Object methods
	}
	var ms []*Def
	for _, t := range v.types {
		ms = appendUnique(ms, in.s.Members(t, name)...)
	}
	want := func(d *Def) bool { return isFieldDef(d) || d.Kind.IsType() } // Outer.Inner
	if call {
		want = isCallableDef
		if len(filter(ms, want)) == 0 {
			want = isFieldDef // record accessor x() of component x
		}
	}
	ms = filter(ms, want)
	var out []*Def
	dims, external := 0, len(ms) > 0
	for _, m := range ms {
		mv := in.memberType(m)
		if mv.kind != jvExternal {
			external = false
		}
		if mv.kind == jvTypes {
			out = appendUnique(out, mv.types...)
			dims = mv.dims
		}
	}
	switch {
	case len(out) > 0:
		return jval{kind: jvTypes, types: out, dims: dims}
	case external:
		return jExternal
	}
	return jUnknown
}

// memberType is the declared type of a field or the return type of a
// method, read from its signature in its own file.
func (in *javaInfer) memberType(m *Def) jval {
	if m.Kind.IsType() {
		return jval{kind: jvTypes, types: []*Def{m}}
	}
	if m.Kind == model.KindEnumMember {
		if p := in.s.Parent(m); p != nil && p.Kind.IsType() {
			return jval{kind: jvTypes, types: []*Def{p}}
		}
		return jUnknown
	}
	f := in.s.File(m.FileID)
	if f == nil || f.Lang != model.LangJava {
		return jUnknown
	}
	params := javaSigTypeParams(m.Sig)
	if owner := in.s.EnclosingType(in.s.Parent(m)); owner != nil {
		for p := range javaSigTypeParams(owner.Sig) {
			params[p] = true
		}
	}
	return in.typeText(javaDeclType(m.Sig), f, params)
}

// typeNode infers the type named by a type node of the current file.
func (in *javaInfer) typeNode(t *sitter.Node) jval {
	if t == nil {
		return jUnknown
	}
	_, name, _ := javaTypeName(in.text(t))
	if name != "" && in.isTypeParam(name, t) {
		return jUnknown
	}
	return in.typeText(in.text(t), in.file, nil)
}

// typeText resolves a type written in file f; params are type variables in
// scope there.
func (in *javaInfer) typeText(typ string, f *File, params map[string]bool) jval {
	qual, name, dims := javaTypeName(typ)
	switch {
	case name == "" || javaPrimitive[name]:
		return jExternal
	case qual == "" && params[name]:
		return jUnknown
	}
	tr := in.r.ResolveType(in.s, f, qual, name)
	switch {
	case tr.Found():
		return jval{kind: jvTypes, types: tr.Defs, dims: dims}
	case tr.External:
		return jExternal
	}
	return jUnknown
}

// thisTypes returns the enclosing types of at, innermost first; an
// anonymous class contributes its base type.
func (in *javaInfer) thisTypes(at *sitter.Node) [][]*Def {
	var out [][]*Def
	for p := at.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "class_declaration", "interface_declaration", "enum_declaration",
			"record_declaration", "annotation_type_declaration":
			if d := in.typeDefAt(p); d != nil {
				out = append(out, []*Def{d})
			}
		case "class_body":
			if c := p.Parent(); c != nil && c.Kind() == "object_creation_expression" {
				if v := in.typeNode(c.ChildByFieldName("type")); v.kind == jvTypes && v.dims == 0 {
					out = append(out, v.types)
				}
			}
		}
	}
	return out
}

// typeDefAt maps a type declaration node of the current file to its def.
func (in *javaInfer) typeDefAt(decl *sitter.Node) *Def {
	name := decl.ChildByFieldName("name")
	if name == nil {
		return nil
	}
	for _, d := range in.s.DefsNamed(in.text(name)) {
		if d.FileID == in.file.ID && d.StartByte == uint32(decl.StartByte()) && d.Kind.IsType() {
			return d
		}
	}
	return nil
}

// isTypeParam reports whether name is a type parameter in scope at n.
func (in *javaInfer) isTypeParam(name string, n *sitter.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		tps := p.ChildByFieldName("type_parameters")
		if tps == nil {
			continue
		}
		for _, tp := range namedChildren(tps) {
			if tp.Kind() != "type_parameter" {
				continue
			}
			for _, c := range namedChildren(tp) {
				if c.Kind() == "type_identifier" {
					if in.text(c) == name {
						return true
					}
					break
				}
			}
		}
	}
	return false
}

// lookupLocal finds the closest declaration of a local variable or
// parameter name visible at at. It returns the declared type node (nil when
// untyped, e.g. a lambda parameter), the initializer, and the extra array
// dimensions written on the declarator.
func (in *javaInfer) lookupLocal(at *sitter.Node, name string) (typ, val *sitter.Node, dims int, ok bool) {
	cur := at
	for p := at.Parent(); p != nil; cur, p = p, p.Parent() {
		switch p.Kind() {
		case "block", "constructor_body", "switch_block_statement_group", "switch_rule":
			found := false
			for _, c := range namedChildren(p) {
				if c.StartByte() >= cur.StartByte() {
					break
				}
				if c.Kind() == "local_variable_declaration" {
					if d := in.declarator(c, name); d != nil {
						typ, val, dims, found = c.ChildByFieldName("type"), d.ChildByFieldName("value"), in.dims(d), true
					}
				}
			}
			if found {
				return typ, val, dims, true
			}
		case "for_statement":
			for _, c := range fieldChildren(p, "init") {
				if c.Kind() == "local_variable_declaration" && !sameNode(c, cur) {
					if d := in.declarator(c, name); d != nil {
						return c.ChildByFieldName("type"), d.ChildByFieldName("value"), in.dims(d), true
					}
				}
			}
		case "enhanced_for_statement":
			if n := p.ChildByFieldName("name"); n != nil && in.text(n) == name && sameNode(p.ChildByFieldName("body"), cur) {
				return p.ChildByFieldName("type"), nil, 0, true
			}
		case "catch_clause":
			for _, c := range namedChildren(p) {
				if c.Kind() != "catch_formal_parameter" {
					continue
				}
				if n := c.ChildByFieldName("name"); n != nil && in.text(n) == name {
					for _, t := range namedChildren(c) {
						if t.Kind() == "catch_type" {
							return t.NamedChild(0), nil, 0, true
						}
					}
				}
			}
		case "try_with_resources_statement":
			if rs := p.ChildByFieldName("resources"); rs != nil && !sameNode(rs, cur) {
				if t, v, ok := in.resource(rs, name, nil); ok {
					return t, v, 0, true
				}
			}
		case "resource_specification":
			if t, v, ok := in.resource(p, name, cur); ok {
				return t, v, 0, true
			}
		case "lambda_expression":
			if t, ok := in.lambdaParam(p, name); ok {
				return t, nil, 0, true
			}
		case "method_declaration", "constructor_declaration":
			if t, d, ok := in.formalParam(p.ChildByFieldName("parameters"), name); ok {
				return t, nil, d, true
			}
		case "compact_constructor_declaration":
			if rec := p.Parent(); rec != nil && rec.Parent() != nil {
				if t, d, ok := in.formalParam(rec.Parent().ChildByFieldName("parameters"), name); ok {
					return t, nil, d, true
				}
			}
		case "if_statement", "while_statement":
			if cond := p.ChildByFieldName("condition"); cond != nil && !sameNode(cond, cur) {
				if t := in.patternVar(cond, name); t != nil {
					return t, nil, 0, true
				}
			}
		case "binary_expression": // a instanceof B b && b.m()
			if left := p.ChildByFieldName("left"); left != nil && !sameNode(left, cur) {
				if t := in.patternVar(left, name); t != nil {
					return t, nil, 0, true
				}
			}
		}
	}
	return nil, nil, 0, false
}

func (in *javaInfer) declarator(decl *sitter.Node, name string) *sitter.Node {
	for _, d := range fieldChildren(decl, "declarator") {
		if n := d.ChildByFieldName("name"); n != nil && in.text(n) == name {
			return d
		}
	}
	return nil
}

func (in *javaInfer) dims(n *sitter.Node) int {
	if d := n.ChildByFieldName("dimensions"); d != nil {
		return strings.Count(in.text(d), "[")
	}
	return 0
}

// resource finds a try-with-resources variable declared before stop.
func (in *javaInfer) resource(spec *sitter.Node, name string, stop *sitter.Node) (typ, val *sitter.Node, ok bool) {
	for _, r := range namedChildren(spec) {
		if stop != nil && r.StartByte() >= stop.StartByte() {
			break
		}
		if r.Kind() != "resource" {
			continue
		}
		if n := r.ChildByFieldName("name"); n != nil && in.text(n) == name {
			return r.ChildByFieldName("type"), r.ChildByFieldName("value"), true
		}
	}
	return nil, nil, false
}

func (in *javaInfer) lambdaParam(l *sitter.Node, name string) (*sitter.Node, bool) {
	ps := l.ChildByFieldName("parameters")
	if ps == nil {
		return nil, false
	}
	switch ps.Kind() {
	case "identifier":
		return nil, in.text(ps) == name
	case "inferred_parameters":
		for _, c := range namedChildren(ps) {
			if in.text(c) == name {
				return nil, true
			}
		}
	case "formal_parameters":
		t, _, ok := in.formalParam(ps, name)
		return t, ok
	}
	return nil, false
}

// formalParam finds a parameter; varargs count as one array dimension.
func (in *javaInfer) formalParam(ps *sitter.Node, name string) (*sitter.Node, int, bool) {
	if ps == nil {
		return nil, 0, false
	}
	for _, p := range namedChildren(ps) {
		switch p.Kind() {
		case "formal_parameter":
			if n := p.ChildByFieldName("name"); n != nil && in.text(n) == name {
				return p.ChildByFieldName("type"), in.dims(p), true
			}
		case "spread_parameter":
			for _, c := range namedChildren(p) {
				if c.Kind() != "variable_declarator" {
					continue
				}
				if n := c.ChildByFieldName("name"); n != nil && in.text(n) == name {
					for _, t := range namedChildren(p) {
						if isJavaTypeNode(t) {
							return t, 1, true
						}
					}
					return nil, 1, true
				}
			}
		}
	}
	return nil, 0, false
}

// patternVar finds a pattern variable (x instanceof T name) in a condition.
func (in *javaInfer) patternVar(cond *sitter.Node, name string) *sitter.Node {
	var found *sitter.Node
	visit := func(n *sitter.Node) bool {
		if found != nil {
			return false
		}
		switch n.Kind() {
		case "lambda_expression", "class_body":
			return false
		case "instanceof_expression":
			if v := n.ChildByFieldName("name"); v != nil && in.text(v) == name {
				found = n.ChildByFieldName("right")
				return false
			}
		}
		return true
	}
	if visit(cond) {
		walk(cond, visit)
	}
	return found
}

// javaPrimitive lists primitive types and void.
var javaPrimitive = javaSetOf("boolean", "byte", "char", "short", "int", "long", "float", "double", "void")

// javaTypeName reduces a written type to (qualifier, simple name, array
// dimensions): annotations and generic arguments are dropped
// ("@NonNull java.util.List<String>[]" -> "java.util", "List", 1).
func javaTypeName(typ string) (qual, name string, dims int) {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(typ); i++ {
		c := typ[i]
		switch {
		case c == '<':
			depth++
		case c == '>':
			if depth > 0 {
				depth--
			}
		case depth > 0:
		case c == '@': // annotation: skip its name and arguments
			i++
			for i < len(typ) && (isIdentByte(typ[i]) || typ[i] == '.') {
				i++
			}
			for i < len(typ) && typ[i] == ' ' {
				i++
			}
			if i < len(typ) && typ[i] == '(' {
				if end := strings.IndexByte(typ[i:], ')'); end >= 0 {
					i += end
				} else {
					i = len(typ)
				}
			} else {
				i--
			}
		case c == '[':
			dims++
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ']':
		default:
			b.WriteByte(c)
		}
	}
	s := b.String()
	if strings.HasSuffix(s, "...") {
		s, dims = strings.TrimSuffix(s, "..."), dims+1
	}
	s = strings.Trim(s, ".")
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[:i], s[i+1:], dims
	}
	return "", s, dims
}

// javaModifierWords are the keywords a signature may start with.
var javaModifierWords = javaSetOf("public", "protected", "private", "abstract", "static", "final",
	"strictfp", "default", "synchronized", "native", "transient", "volatile", "sealed", "non-sealed")

// javaDeclType extracts the declared type of a field signature or the
// return type of a method signature ("public static <T> List<T> of(...)"
// -> "List<T>"). It returns "" when there is none (constructors).
func javaDeclType(sig string) string {
	s := strings.TrimSpace(sig)
	for {
		word := s
		if i := strings.IndexAny(s, " \t<("); i >= 0 {
			word = s[:i]
		}
		if !javaModifierWords[word] || word == "" {
			break
		}
		s = strings.TrimSpace(s[len(word):])
	}
	if strings.HasPrefix(s, "<") { // method type parameters
		if end := javaMatchAngle(s); end > 0 {
			s = strings.TrimSpace(s[end:])
		}
	}
	// The type runs to the first space outside <...> that is followed by
	// the declared name.
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
		case '(':
			if depth == 0 {
				return "" // constructor: Name(...)
			}
		case ' ', '\t':
			if depth == 0 {
				rest := strings.TrimSpace(s[i:])
				if strings.HasPrefix(rest, "[") || strings.HasPrefix(rest, "...") || strings.HasPrefix(rest, "@") {
					continue
				}
				return s[:i]
			}
		}
	}
	return ""
}

// javaMatchAngle returns the offset after the <...> group starting s.
func javaMatchAngle(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// javaSigTypeParams collects the type variables a signature declares: the
// names starting a top-level <...> entry before the parameter list
// ("class Box<T extends X>" -> T, "<K, V> Map<K, V> m(...)" -> K, V).
func javaSigTypeParams(sig string) map[string]bool {
	out := map[string]bool{}
	if i := strings.IndexByte(sig, '('); i >= 0 {
		sig = sig[:i]
	}
	depth := 0
	start := false
	for i := 0; i < len(sig); i++ {
		c := sig[i]
		switch {
		case c == '<':
			depth++
			start = depth == 1
		case c == '>':
			depth--
		case c == ',' && depth == 1:
			start = true
		case start && isIdentByte(c):
			j := i
			for j < len(sig) && isIdentByte(sig[j]) {
				j++
			}
			out[sig[i:j]] = true
			i, start = j-1, false
		case c != ' ' && c != '\t':
			start = false
		}
	}
	return out
}

func isUpperRuneStr(s string) bool { return s != "" && s[0] >= 'A' && s[0] <= 'Z' }
