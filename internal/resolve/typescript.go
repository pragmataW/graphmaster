package resolve

import (
	"database/sql"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

// tsResolver resolves TypeScript, TSX and JavaScript (one family: modules
// are files, see parse.jsFamily):
//   - only whole-module bindings (import * as ns, const m = require(...),
//     import m = require(...)) bind a qualifier; named and default imports
//     are Import.Names and handled by the framework (T1);
//   - type names resolve in the same file, then through named imports
//     (following barrel re-exports), wildcard re-exports and finally
//     globally (T4/T5); well-known globals (Promise, Map, HTMLElement...)
//     are external;
//   - every definition nested in another one, except in a namespace, is a
//     member: class members, object literal members and local helpers are
//     never visible as bare names from other files;
//   - receiver typing (T3) parses the file and infers the receiver from
//     this/super, parameter and variable annotations, `new T()`, `x as T`,
//     declared return types of called functions and methods, field types
//     and imported classes (static access).
type tsResolver struct{ Generic }

func init() {
	for _, l := range []model.Lang{model.LangTypeScript, model.LangTSX, model.LangJavaScript} {
		Register(l, tsResolver{})
	}
}

// ImportBindings implements LangResolver.
func (tsResolver) ImportBindings(imp Import) []string {
	if imp.Alias != "" {
		return []string{imp.Alias}
	}
	return nil
}

// IsMember implements LangResolver.
func (tsResolver) IsMember(s *Session, d *Def) bool {
	if p := s.Parent(d); p != nil {
		return p.Kind != model.KindNamespace && p.Kind != model.KindModule
	}
	return d.Kind == model.KindMethod // Foo.prototype.m = function
}

// tsMaxDepth bounds type inference recursion (alias chains, call chains,
// barrel re-exports).
const tsMaxDepth = 12

// ResolveType implements LangResolver.
func (r tsResolver) ResolveType(s *Session, from *File, qual, name string) TypeResult {
	return r.resolveType(s, from, qual, name, 0)
}

func (r tsResolver) resolveType(s *Session, from *File, qual, name string, depth int) TypeResult {
	if from == nil || name == "" {
		return TypeResult{}
	}
	types := filter(s.DefsNamed(name), func(d *Def) bool { return d.Kind.IsType() || d.Kind == model.KindNamespace })
	if qual != "" {
		root, rest := splitQual(qual)
		if t, ok := s.Binding(from, root); ok {
			if !t.Resolved() {
				return TypeResult{External: true}
			}
			if len(rest) == 0 {
				return typeResult(r.exported(s, t, name, depth), ConfImport, true)
			}
			return typeResult(filter(types, func(d *Def) bool {
				return s.InTarget(d, t) && s.ownedBy(r, d, rest)
			}), ConfImport, true)
		}
		if _, ok := s.NamedImport(from, root); !ok && tsGlobalValues[root] && !tsDeclaredIn(s, from, root) {
			return TypeResult{External: true} // React.FC with a global React, NodeJS.Timeout
		}
		return s.DefaultResolveType(from, qual, name)
	}
	if in := filter(types, func(d *Def) bool { return d.FileID == from.ID && !r.IsMember(s, d) }); len(in) > 0 {
		return typeResult(in, ConfLexical, false)
	}
	if t, ok := s.NamedImport(from, name); ok {
		if !t.Resolved() {
			return TypeResult{External: true}
		}
		return typeResult(r.exported(s, t, name, depth), ConfImport, true)
	}
	if tsIsGlobalType(name) {
		return TypeResult{External: true}
	}
	for _, im := range s.Imports(from) {
		if !im.Wildcard() {
			continue
		}
		if t := s.ImportTarget(from, im.Spec); t.Resolved() {
			if in := r.exported(s, t, name, depth); len(in) > 0 {
				return typeResult(in, ConfImport, false)
			}
		}
	}
	pool := filter(types, func(d *Def) bool { return sameFamily(s, d, from) && !r.IsMember(s, d) })
	if len(pool) == 0 || s.Common(name) {
		return TypeResult{}
	}
	return typeResult(pool, ConfUnique, false)
}

// exported returns the top-level type definitions named name in an import
// target, following re-exports (export { X } from, export * from) of barrel
// files.
func (r tsResolver) exported(s *Session, t model.ImportTarget, name string, depth int) []*Def {
	return r.exportedDefs(s, t, name, depth, func(d *Def) bool {
		return (d.Kind.IsType() || d.Kind == model.KindNamespace) && !r.IsMember(s, d)
	})
}

// ReExported implements ReExporter: definitions named name that import
// target t provides, following barrel re-exports.
func (r tsResolver) ReExported(s *Session, t model.ImportTarget, name string, keep func(*Def) bool) []*Def {
	return r.exportedDefs(s, t, name, 0, keep)
}

// exportedDefs returns the definitions named name in import target t that
// keep accepts, following re-exports of barrel files up to tsMaxDepth.
func (r tsResolver) exportedDefs(s *Session, t model.ImportTarget, name string, depth int, keep func(*Def) bool) []*Def {
	in := filter(s.DefsNamed(name), func(d *Def) bool { return keep(d) && s.InTarget(d, t) })
	if len(in) > 0 || depth >= tsMaxDepth || t.Kind != model.TargetFile {
		return in
	}
	f := tsFileByPath(s, t.Path)
	if f == nil {
		return nil
	}
	for _, im := range s.Imports(f) {
		if !im.Wildcard() && !containsStr(im.Names, name) {
			continue
		}
		if next := s.ImportTarget(f, im.Spec); next.Resolved() && next.Path != t.Path {
			if out := r.exportedDefs(s, next, name, depth+1, keep); len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// tsDeclaredIn reports whether file f defines a top-level symbol name.
func tsDeclaredIn(s *Session, f *File, name string) bool {
	for _, d := range s.DefsNamed(name) {
		if d.FileID == f.ID && d.ParentID == 0 {
			return true
		}
	}
	return false
}

// tsFileByPath returns the files row of a repo-relative path, or nil.
func tsFileByPath(s *Session, p string) *File {
	st := tsStateOf(s)
	if st == nil {
		return nil
	}
	id, ok := st.fileIDs[p]
	if !ok {
		err := s.rows(`SELECT id FROM files WHERE path = ?`, func(r *sql.Rows) error { return r.Scan(&id) }, p)
		s.fail(err)
		st.fileIDs[p] = id
	}
	if id == 0 {
		return nil
	}
	return s.File(id)
}

// ---------------------------------------------------------------- globals

// tsGlobalValues are global objects whose members never resolve into the
// repository unless a file declares or imports the same name.
var tsGlobalValues = map[string]bool{
	"console": true, "Math": true, "JSON": true, "Object": true, "Array": true, "Promise": true,
	"Number": true, "String": true, "Boolean": true, "Date": true, "Reflect": true, "Symbol": true,
	"Intl": true, "window": true, "document": true, "globalThis": true, "process": true,
	"Buffer": true, "navigator": true, "localStorage": true, "sessionStorage": true, "location": true,
	"history": true, "performance": true, "crypto": true, "require": true, "module": true,
	"exports": true, "Error": true, "RegExp": true, "Map": true, "Set": true, "WeakMap": true,
	"WeakSet": true, "BigInt": true, "Atomics": true, "Proxy": true, "URL": true, "NodeJS": true,
	"React": true, "JSX": true,
}

// tsGlobalTypes are standard library and DOM types.
var tsGlobalTypes = map[string]bool{
	"Promise": true, "PromiseLike": true, "Array": true, "ReadonlyArray": true, "Map": true,
	"ReadonlyMap": true, "Set": true, "ReadonlySet": true, "WeakMap": true, "WeakSet": true,
	"Record": true, "Partial": true, "Required": true, "Readonly": true, "Pick": true, "Omit": true,
	"Exclude": true, "Extract": true, "NonNullable": true, "ReturnType": true, "Parameters": true,
	"InstanceType": true, "Awaited": true, "Date": true, "RegExp": true, "Error": true,
	"TypeError": true, "RangeError": true, "Function": true, "Object": true, "String": true,
	"Number": true, "Boolean": true, "Symbol": true, "BigInt": true, "Iterable": true,
	"Iterator": true, "IterableIterator": true, "AsyncIterable": true, "AsyncIterator": true,
	"Generator": true, "AsyncGenerator": true, "ArrayLike": true, "ArrayBuffer": true,
	"Uint8Array": true, "Int32Array": true, "Float64Array": true, "DataView": true, "Buffer": true,
	"Element": true, "Node": true, "Event": true, "EventTarget": true, "MouseEvent": true,
	"KeyboardEvent": true, "Window": true, "Document": true, "Response": true, "Request": true,
	"Headers": true, "URL": true, "URLSearchParams": true, "FormData": true, "Blob": true,
	"File": true, "AbortController": true, "AbortSignal": true, "Console": true,
	"TemplateStringsArray": true, "PropertyKey": true, "ThisType": true, "Uppercase": true,
	"Lowercase": true, "Capitalize": true, "Uncapitalize": true, "NodeJS": true, "JSX": true,
}

func tsIsGlobalType(name string) bool {
	return tsGlobalTypes[name] || strings.HasPrefix(name, "HTML") || strings.HasPrefix(name, "SVG")
}

// ---------------------------------------------------------------- parse state

// tsState is the run-wide state: one parser per dialect and a path -> file
// id cache for following re-exports.
type tsState struct {
	parsers map[model.Lang]*sitter.Parser
	fileIDs map[string]int64
}

func (st *tsState) Close() {
	for _, p := range st.parsers {
		p.Close()
	}
}

// tsTrees caches parsed files for one batch.
type tsTrees map[int64]*sitter.Tree

func (t tsTrees) Close() {
	for _, tr := range t {
		if tr != nil {
			tr.Close()
		}
	}
}

func tsStateOf(s *Session) *tsState {
	if st, ok := s.State(model.LangTypeScript).(*tsState); ok {
		return st
	}
	st := &tsState{parsers: map[model.Lang]*sitter.Parser{}, fileIDs: map[string]int64{}}
	s.SetState(model.LangTypeScript, st)
	return st
}

func (st *tsState) parser(l model.Lang) *sitter.Parser {
	if p, ok := st.parsers[l]; ok {
		return p
	}
	var ptr = tsjs.Language()
	switch l {
	case model.LangTypeScript:
		ptr = tsts.LanguageTypescript()
	case model.LangTSX:
		ptr = tsts.LanguageTSX()
	}
	p := sitter.NewParser()
	if err := p.SetLanguage(sitter.NewLanguage(ptr)); err != nil {
		p.Close()
		p = nil
	}
	st.parsers[l] = p
	return p
}

// tsTree returns the parsed tree of f (cached per batch), or nil.
func tsTree(s *Session, f *File) (*sitter.Tree, []byte) {
	src := s.Source(f)
	if src == nil {
		return nil, nil
	}
	trees, ok := s.BatchState(model.LangTypeScript).(tsTrees)
	if !ok {
		trees = tsTrees{}
		s.SetBatchState(model.LangTypeScript, trees)
	}
	if tr, ok := trees[f.ID]; ok {
		return tr, src
	}
	var tr *sitter.Tree
	if p := tsStateOf(s).parser(f.Lang); p != nil {
		tr = p.Parse(src, nil)
	}
	trees[f.ID] = tr
	return tr, src
}

// tsDefNode returns the syntax node of definition d and its file.
func tsDefNode(s *Session, d *Def) (*sitter.Node, *tsCtx) {
	f := s.File(d.FileID)
	if f == nil {
		return nil, nil
	}
	tr, src := tsTree(s, f)
	if tr == nil || int(d.EndByte) > len(src) || d.StartByte > d.EndByte {
		return nil, nil
	}
	n := tr.RootNode().NamedDescendantForByteRange(uint(d.StartByte), uint(d.EndByte))
	for n != nil {
		p := n.Parent()
		if p == nil || p.StartByte() != n.StartByte() || p.EndByte() != n.EndByte() {
			break
		}
		n = p
	}
	return n, &tsCtx{file: f, src: src}
}

// ---------------------------------------------------------------- receiver

// Receiver implements LangResolver.
func (r tsResolver) Receiver(s *Session, site *Site) Receiver {
	if site.Kind == model.EdgeTests || site.Kind.IsInheritance() || site.Line == 0 {
		return Receiver{Kind: RecvNone}
	}
	tr, src := tsTree(s, site.File)
	if tr == nil {
		if src == nil {
			return Receiver{Kind: RecvUnknownShape}
		}
		return s.TextReceiver(site)
	}
	pt := sitter.Point{Row: uint(site.Line - 1), Column: uint(site.Col)}
	n := tr.RootNode().NamedDescendantForPointRange(pt, pt)
	if n == nil || n.StartPosition() != pt || n.Utf8Text(src) != site.Name {
		return s.TextReceiver(site)
	}
	p := n.Parent()
	if p == nil || p.Kind() != "member_expression" {
		return Receiver{Kind: RecvNone}
	}
	if prop := p.ChildByFieldName("property"); prop == nil || prop.StartByte() != n.StartByte() {
		return Receiver{Kind: RecvNone}
	}
	in := &tsInfer{s: s, r: r, seen: map[int64]bool{}}
	v := in.expr(&tsCtx{file: site.File, src: src}, p.ChildByFieldName("object"))
	switch v.kind {
	case tvTypes:
		return Receiver{Kind: RecvTyped, Types: v.types}
	case tvExternal:
		return Receiver{Kind: RecvExternal}
	}
	return Receiver{Kind: RecvUntyped}
}

// ---------------------------------------------------------------- inference

type tvKind uint8

const (
	tvUnknown  tvKind = iota // could not infer
	tvExternal               // builtin or out-of-repo type
	tvTypes                  // repository definitions whose members apply
)

// tsVal is an inferred value type: the definitions whose members a
// property access on the value selects (classes, interfaces, enums, type
// aliases, namespaces, or an object-literal constant).
type tsVal struct {
	kind  tvKind
	types []*Def
}

var (
	tsUnknown  = tsVal{kind: tvUnknown}
	tsExternal = tsVal{kind: tvExternal}
)

func tsTyped(ds []*Def) tsVal {
	if len(ds) == 0 {
		return tsUnknown
	}
	return tsVal{kind: tvTypes, types: ds}
}

// tsCtx is the file an expression or type node belongs to.
type tsCtx struct {
	file *File
	src  []byte
}

func (c *tsCtx) text(n *sitter.Node) string { return n.Utf8Text(c.src) }

// tsInfer infers expression types across files.
type tsInfer struct {
	s     *Session
	r     tsResolver
	depth int
	seen  map[int64]bool // definitions being evaluated (cycle guard)
}

func (in *tsInfer) enter() bool {
	if in.depth >= tsMaxDepth {
		return false
	}
	in.depth++
	return true
}

func (in *tsInfer) leave() { in.depth-- }

// expr infers the type of expression n in c.
func (in *tsInfer) expr(c *tsCtx, n *sitter.Node) tsVal {
	if n == nil || !in.enter() {
		return tsUnknown
	}
	defer in.leave()
	switch n.Kind() {
	case "this":
		return in.this(c, n)
	case "super":
		if cls := in.enclosingClass(c, n); cls != nil {
			if anc := in.s.Ancestors(cls); len(anc) > 0 {
				return tsTyped(anc)
			}
			return tsExternal
		}
	case "identifier":
		return in.ident(c, n, c.text(n))
	case "member_expression":
		prop := n.ChildByFieldName("property")
		if prop == nil {
			return tsUnknown
		}
		return in.member(in.expr(c, n.ChildByFieldName("object")), c.text(prop))
	case "call_expression":
		return in.call(c, n)
	case "new_expression":
		return in.typeExpr(c, n.ChildByFieldName("constructor"))
	case "parenthesized_expression", "non_null_expression", "await_expression":
		if k := n.NamedChild(0); k != nil {
			return in.expr(c, k)
		}
	case "as_expression", "satisfies_expression":
		if k := n.NamedChild(1); k != nil {
			return in.annotation(c, k)
		}
	case "string", "template_string", "number", "true", "false", "null", "undefined",
		"array", "regex", "arrow_function", "function_expression", "binary_expression",
		"unary_expression", "update_expression":
		return tsExternal
	}
	return tsUnknown
}

// typeExpr resolves the type named by a `new` target or a static receiver
// (Foo, ns.Foo).
func (in *tsInfer) typeExpr(c *tsCtx, n *sitter.Node) tsVal {
	if n == nil {
		return tsUnknown
	}
	switch n.Kind() {
	case "identifier":
		return in.typeName(c, "", c.text(n))
	case "member_expression":
		obj, prop := n.ChildByFieldName("object"), n.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return tsUnknown
		}
		return in.typeName(c, c.text(obj), c.text(prop))
	}
	return tsUnknown
}

// typeName resolves a type name written in c's file.
func (in *tsInfer) typeName(c *tsCtx, qual, name string) tsVal {
	if qual == "" && tsIsGlobalType(name) && !tsDeclaredIn(in.s, c.file, name) {
		if _, ok := in.s.NamedImport(c.file, name); !ok {
			return tsExternal
		}
	}
	tr := in.r.resolveType(in.s, c.file, qual, name, in.depth)
	switch {
	case tr.Found():
		return in.expandAliases(tr.Defs)
	case tr.External:
		return tsExternal
	}
	return tsUnknown
}

// expandAliases replaces aliases of other types (type A = B | C) by their
// targets; aliases of object types keep their own members.
func (in *tsInfer) expandAliases(ds []*Def) tsVal {
	var out []*Def
	for _, d := range ds {
		if d.Kind != model.KindType || in.seen[d.ID] {
			out = appendUnique(out, d)
			continue
		}
		n, c := tsDefNode(in.s, d)
		var v *sitter.Node
		if n != nil {
			v = n.ChildByFieldName("value")
		}
		if v == nil || v.Kind() == "object_type" {
			out = appendUnique(out, d)
			continue
		}
		in.seen[d.ID] = true
		t := in.annotation(c, v)
		delete(in.seen, d.ID)
		if t.kind != tvTypes {
			return t
		}
		out = appendUnique(out, t.types...)
	}
	return tsTyped(out)
}

// annotation infers the type denoted by a type annotation or type node.
func (in *tsInfer) annotation(c *tsCtx, n *sitter.Node) tsVal {
	if n == nil || !in.enter() {
		return tsUnknown
	}
	defer in.leave()
	switch n.Kind() {
	case "type_annotation", "parenthesized_type", "opting_type_annotation", "asserts_annotation":
		return in.annotation(c, n.NamedChild(0))
	case "type_identifier":
		return in.typeName(c, "", c.text(n))
	case "nested_type_identifier":
		mod, name := n.ChildByFieldName("module"), n.ChildByFieldName("name")
		if mod == nil || name == nil {
			return tsUnknown
		}
		return in.typeName(c, c.text(mod), c.text(name))
	case "generic_type":
		name := n.ChildByFieldName("name")
		if name == nil {
			return tsUnknown
		}
		switch c.text(name) {
		case "Promise", "PromiseLike", "Awaited", "Readonly", "Partial", "Required", "NonNullable":
			if args := n.ChildByFieldName("type_arguments"); args != nil {
				return in.annotation(c, args.NamedChild(0))
			}
		}
		return in.annotation(c, name)
	case "union_type":
		var out []*Def
		for _, m := range tsUnionMembers(n) {
			if k := m.Kind(); k == "literal_type" || k == "predefined_type" && c.text(m) != "string" && c.text(m) != "number" && c.text(m) != "boolean" {
				continue // null, undefined, void, never
			}
			v := in.annotation(c, m)
			if v.kind != tvTypes {
				return v
			}
			out = appendUnique(out, v.types...)
		}
		return tsTyped(out)
	case "this_type":
		if cls := in.enclosingClass(c, n); cls != nil {
			return tsTyped([]*Def{cls})
		}
	case "predefined_type", "literal_type", "array_type", "tuple_type", "function_type",
		"object_type", "template_literal_type", "constructor_type", "readonly_type":
		return tsExternal
	}
	return tsUnknown
}

// tsUnionMembers flattens nested union types.
func tsUnionMembers(n *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	for _, k := range namedChildren(n) {
		if k.Kind() == "union_type" {
			out = append(out, tsUnionMembers(k)...)
		} else {
			out = append(out, k)
		}
	}
	return out
}

// this types `this` by the class lexically enclosing n, stopping at
// non-arrow functions that rebind it.
func (in *tsInfer) this(c *tsCtx, n *sitter.Node) tsVal {
	if cls := in.enclosingClass(c, n); cls != nil {
		return tsTyped([]*Def{cls})
	}
	return tsUnknown
}

func (in *tsInfer) enclosingClass(c *tsCtx, n *sitter.Node) *Def {
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "function_declaration", "function_expression", "generator_function_declaration",
			"generator_function", "function":
			return nil
		case "class_declaration", "abstract_class_declaration", "class":
			name := p.ChildByFieldName("name")
			if name == nil {
				return nil
			}
			return in.defAt(c, c.text(name), p)
		}
	}
	return nil
}

// defAt finds the definition named name whose node is n in c's file.
func (in *tsInfer) defAt(c *tsCtx, name string, n *sitter.Node) *Def {
	for _, d := range in.s.DefsNamed(name) {
		if d.FileID == c.file.ID && uint(d.StartByte) == n.StartByte() && uint(d.EndByte) == n.EndByte() {
			return d
		}
	}
	return nil
}

// ident infers the value of identifier name used at n: the closest local
// declaration or parameter, a module-level declaration, an import, or a
// global.
func (in *tsInfer) ident(c *tsCtx, n *sitter.Node, name string) tsVal {
	for scope := n.Parent(); scope != nil; scope = scope.Parent() {
		switch scope.Kind() {
		case "function_declaration", "function_expression", "arrow_function", "method_definition",
			"generator_function_declaration", "generator_function", "function":
			if v, ok := in.param(c, scope, name); ok {
				return v
			}
		case "statement_block", "program", "switch_body", "class_static_block":
			if v, ok := in.blockDecl(c, scope, n, name); ok {
				return v
			}
		case "for_in_statement", "for_statement", "catch_clause":
			if tsDeclares(c, scope, name) {
				return tsUnknown
			}
		}
	}
	if t, ok := in.s.NamedImport(c.file, name); ok {
		if !t.Resolved() {
			return tsExternal
		}
		return in.importedValue(t, name)
	}
	if _, ok := in.s.Binding(c.file, name); ok {
		return tsUnknown // a whole module object
	}
	if tsGlobalValues[name] {
		return tsExternal
	}
	return tsUnknown
}

// tsDeclares reports whether a loop or catch clause binds name.
func tsDeclares(c *tsCtx, scope *sitter.Node, name string) bool {
	found := false
	for _, f := range []string{"left", "initializer", "parameter"} {
		if k := scope.ChildByFieldName(f); k != nil {
			walk(k, func(x *sitter.Node) bool {
				if x.Kind() == "identifier" && c.text(x) == name {
					found = true
				}
				return !found
			})
			if k.Kind() == "identifier" && c.text(k) == name {
				found = true
			}
		}
	}
	return found
}

// param types a parameter of function fn named name.
func (in *tsInfer) param(c *tsCtx, fn *sitter.Node, name string) (tsVal, bool) {
	if p := fn.ChildByFieldName("parameter"); p != nil && c.text(p) == name { // x => ...
		return tsUnknown, true
	}
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return tsVal{}, false
	}
	for _, p := range namedChildren(params) {
		pat := p.ChildByFieldName("pattern")
		switch p.Kind() {
		case "identifier":
			if c.text(p) == name {
				return tsUnknown, true
			}
			continue
		case "assignment_pattern":
			pat = p.ChildByFieldName("left")
		case "required_parameter", "optional_parameter":
		default:
			continue
		}
		if pat == nil {
			continue
		}
		typ := p.ChildByFieldName("type")
		switch pat.Kind() {
		case "identifier":
			if c.text(pat) == name {
				return in.annotation(c, typ), true
			}
		case "object_pattern":
			// ({ service }: Props) -> the type of Props.service.
			if prop, ok := tsPatternProperty(c, pat, name); ok {
				return in.member(in.annotation(c, typ), prop), true
			}
		case "array_pattern":
			if tsPatternBinds(c, pat, name) {
				return tsUnknown, true
			}
		}
	}
	return tsVal{}, false
}

// tsPatternProperty returns the property an object pattern binds to name:
// { name }, { prop: name } or { name = default }.
func tsPatternProperty(c *tsCtx, pat *sitter.Node, name string) (string, bool) {
	for _, k := range namedChildren(pat) {
		switch k.Kind() {
		case "shorthand_property_identifier_pattern":
			if c.text(k) == name {
				return name, true
			}
		case "object_assignment_pattern":
			if l := k.ChildByFieldName("left"); l != nil && c.text(l) == name {
				return name, true
			}
		case "pair_pattern":
			key, val := k.ChildByFieldName("key"), k.ChildByFieldName("value")
			if key != nil && val != nil && val.Kind() == "identifier" && c.text(val) == name {
				return c.text(key), true
			}
		}
	}
	return "", false
}

func tsPatternBinds(c *tsCtx, pat *sitter.Node, name string) bool {
	found := false
	walk(pat, func(x *sitter.Node) bool {
		if (x.Kind() == "identifier" || x.Kind() == "shorthand_property_identifier_pattern") && c.text(x) == name {
			found = true
		}
		return !found
	})
	return found
}

// blockDecl finds the declaration of name among the statements of block:
// the closest one before `at` (module level: any), unwrapping exports.
func (in *tsInfer) blockDecl(c *tsCtx, block, at *sitter.Node, name string) (tsVal, bool) {
	module := block.Kind() == "program"
	var found *sitter.Node
	for _, st := range namedChildren(block) {
		if !module && st.StartByte() > at.StartByte() {
			break
		}
		if st.Kind() == "export_statement" {
			if d := st.ChildByFieldName("declaration"); d != nil {
				st = d
			}
		}
		switch st.Kind() {
		case "lexical_declaration", "variable_declaration":
			for _, decl := range namedChildren(st) {
				if decl.Kind() != "variable_declarator" {
					continue
				}
				if nm := decl.ChildByFieldName("name"); nm != nil {
					if nm.Kind() == "identifier" && c.text(nm) == name {
						found = decl
					} else if nm.Kind() != "identifier" && tsPatternBinds(c, nm, name) {
						found = nil
						return tsUnknown, true
					}
				}
			}
		case "class_declaration", "abstract_class_declaration", "enum_declaration",
			"interface_declaration", "internal_module", "function_declaration":
			if nm := st.ChildByFieldName("name"); nm != nil && c.text(nm) == name {
				found = st
			}
		case "expression_statement":
			if k := st.NamedChild(0); k != nil && k.Kind() == "internal_module" {
				if nm := k.ChildByFieldName("name"); nm != nil && c.text(nm) == name {
					found = k
				}
			}
		}
	}
	if found == nil {
		return tsVal{}, false
	}
	switch found.Kind() {
	case "variable_declarator":
		return in.declarator(c, found, name), true
	case "function_declaration":
		return tsExternal, true
	}
	if d := in.defAt(c, name, found); d != nil {
		return tsTyped([]*Def{d}), true // static access: Foo.bar, Enum.Member
	}
	return tsUnknown, true
}

// declarator types a variable declarator: its annotation, else its value.
// An object literal bound at module level types as the constant itself, so
// its members (object literal methods) are selectable.
func (in *tsInfer) declarator(c *tsCtx, decl *sitter.Node, name string) tsVal {
	if t := decl.ChildByFieldName("type"); t != nil {
		return in.annotation(c, t)
	}
	v := decl.ChildByFieldName("value")
	if v == nil {
		return tsUnknown
	}
	if v.Kind() == "object" {
		if d := in.defAt(c, name, decl); d != nil {
			return tsTyped([]*Def{d})
		}
		return tsExternal
	}
	if v.Kind() == "call_expression" { // const x = require("./x")
		if fn := v.ChildByFieldName("function"); fn != nil && c.text(fn) == "require" {
			return tsUnknown
		}
	}
	return in.expr(c, v)
}

// importedValue types a name imported from target t: a class or other type
// (static access), or a variable defined there.
func (in *tsInfer) importedValue(t model.ImportTarget, name string) tsVal {
	if types := in.r.exported(in.s, t, name, in.depth); len(types) > 0 {
		return tsTyped(types)
	}
	for _, d := range in.s.DefsNamed(name) {
		if d.ParentID == 0 && in.s.InTarget(d, t) &&
			(d.Kind == model.KindConstant || d.Kind == model.KindVariable) {
			return in.valueOf(d)
		}
	}
	return tsUnknown
}

// member types property name of a value.
func (in *tsInfer) member(v tsVal, name string) tsVal {
	if v.kind != tvTypes {
		return v
	}
	var out []*Def
	for _, t := range v.types {
		for _, m := range in.s.Members(t, name) {
			mv := in.valueOf(m)
			if mv.kind != tvTypes {
				return mv
			}
			out = appendUnique(out, mv.types...)
		}
	}
	if len(out) == 0 {
		return tsExternal // inherited from outside the repository, or dynamic
	}
	return tsTyped(out)
}

// valueOf types the value of a definition used as an expression.
func (in *tsInfer) valueOf(d *Def) tsVal {
	switch {
	case d.Kind.IsType() || d.Kind == model.KindNamespace:
		return tsTyped([]*Def{d})
	case d.Kind == model.KindEnumMember:
		return tsExternal
	case d.Kind.IsCallable():
		return tsExternal // a function value
	}
	if in.seen[d.ID] || !in.enter() {
		return tsUnknown
	}
	in.seen[d.ID] = true
	defer func() { delete(in.seen, d.ID); in.leave() }()
	n, c := tsDefNode(in.s, d)
	if n == nil {
		return tsUnknown
	}
	switch n.Kind() {
	case "variable_declarator":
		return in.declarator(c, n, d.Name)
	case "public_field_definition", "field_definition", "property_signature",
		"required_parameter", "optional_parameter":
		if t := n.ChildByFieldName("type"); t != nil {
			return in.annotation(c, t)
		}
		if v := n.ChildByFieldName("value"); v != nil {
			return in.expr(c, v)
		}
	}
	return tsUnknown
}

// call types the result of a call expression from the callee's declared
// return type.
func (in *tsInfer) call(c *tsCtx, n *sitter.Node) tsVal {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return tsUnknown
	}
	var callees []*Def
	switch fn.Kind() {
	case "member_expression":
		prop := fn.ChildByFieldName("property")
		recv := in.expr(c, fn.ChildByFieldName("object"))
		if recv.kind != tvTypes || prop == nil {
			return recv
		}
		for _, t := range recv.types {
			callees = appendUnique(callees, in.s.Members(t, c.text(prop))...)
		}
	case "identifier":
		callees = in.function(c, c.text(fn))
	default:
		return tsUnknown
	}
	var out []*Def
	for _, d := range callees {
		v := in.returnOf(d)
		if v.kind != tvTypes {
			return v
		}
		out = appendUnique(out, v.types...)
	}
	return tsTyped(out)
}

// function finds the function definitions a bare call of name in c's file
// refers to: same-file top-level definitions, else named imports.
func (in *tsInfer) function(c *tsCtx, name string) []*Def {
	cands := filter(in.s.DefsNamed(name), func(d *Def) bool { return d.Kind.IsCallable() && d.ParentID == 0 })
	if local := filter(cands, func(d *Def) bool { return d.FileID == c.file.ID }); len(local) > 0 {
		return local
	}
	if t, ok := in.s.NamedImport(c.file, name); ok && t.Resolved() {
		return filter(cands, func(d *Def) bool { return in.s.InTarget(d, t) })
	}
	return nil
}

// returnOf types the declared result of a function or method definition.
func (in *tsInfer) returnOf(d *Def) tsVal {
	if !d.Kind.IsCallable() || in.seen[d.ID] || !in.enter() {
		return tsUnknown
	}
	in.seen[d.ID] = true
	defer func() { delete(in.seen, d.ID); in.leave() }()
	n, c := tsDefNode(in.s, d)
	if n == nil {
		return tsUnknown
	}
	fn := n
	switch n.Kind() {
	case "variable_declarator", "public_field_definition", "field_definition", "pair":
		fn = n.ChildByFieldName("value")
	case "assignment_expression":
		fn = n.ChildByFieldName("right")
	}
	if fn == nil {
		return tsUnknown
	}
	if rt := fn.ChildByFieldName("return_type"); rt != nil {
		return in.annotation(c, rt)
	}
	if fn.Kind() == "arrow_function" { // const make = () => new Foo()
		if body := fn.ChildByFieldName("body"); body != nil && body.Kind() != "statement_block" {
			return in.expr(c, body)
		}
	}
	return tsUnknown
}
