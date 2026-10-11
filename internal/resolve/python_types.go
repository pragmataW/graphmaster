package resolve

import (
	"cmp"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Python receiver typing (T3). The type of an attribute's object is
// inferred from source: self/cls of a method, super(), annotated
// parameters, the closest preceding local (or module-level) assignment,
// constructor calls, return annotations (from sigs), class attribute
// annotations and self.attr assignments in the class's methods (walking
// base classes). Unannotated values stay unknown, which leaves the
// reference to T4/T5 among members.

// pyMaxScan bounds the nodes visited when searching a function body.
const pyMaxScan = 20000

// pyState is the run-wide Python state: one parser.
type pyState struct{ parser *sitter.Parser }

func (p *pyState) Close() { p.parser.Close() }

// pyBatch caches parsed files and their binding indexes for one batch.
type pyBatch struct {
	trees  map[int64]*sitter.Tree
	scopes map[pyScopeKey]map[string][]sitter.Node // scope -> name -> bindings in source order
	attrs  map[pyScopeKey]map[string]sitter.Node   // class -> self.<attr> assignment
}

// pyScopeKey identifies a scope or class node of a file.
type pyScopeKey struct {
	file       int64
	start, end uint
}

func (b *pyBatch) Close() {
	for _, tr := range b.trees {
		if tr != nil {
			tr.Close()
		}
	}
}

func pyBatchOf(s *Session) *pyBatch {
	b, ok := s.BatchState(model.LangPython).(*pyBatch)
	if !ok {
		b = &pyBatch{trees: map[int64]*sitter.Tree{}, scopes: map[pyScopeKey]map[string][]sitter.Node{},
			attrs: map[pyScopeKey]map[string]sitter.Node{}}
		s.SetBatchState(model.LangPython, b)
	}
	return b
}

// pyTree returns the parsed tree and source of f (cached per batch), or
// nil when the source is unavailable.
func pyTree(s *Session, f *File) (*sitter.Tree, []byte) {
	src := s.Source(f)
	if src == nil {
		return nil, nil
	}
	b := pyBatchOf(s)
	if tr, ok := b.trees[f.ID]; ok {
		return tr, src
	}
	var tr *sitter.Tree
	st, ok := s.State(model.LangPython).(*pyState)
	if !ok {
		p := sitter.NewParser()
		if err := p.SetLanguage(sitter.NewLanguage(tspython.Language())); err != nil {
			p.Close()
		} else {
			st = &pyState{parser: p}
			s.SetState(model.LangPython, st)
		}
	}
	if st != nil {
		tr = st.parser.Parse(src, nil)
	}
	b.trees[f.ID] = tr
	return tr, src
}

// pvKind classifies an inferred value.
type pvKind uint8

const (
	pvUnknown  pvKind = iota // could not infer
	pvExternal               // builtin or out-of-repo value
	pvTyped                  // an instance (or the class object) of types
	pvModule                 // an imported module
)

type pval struct {
	kind  pvKind
	types []*Def
	mod   model.ImportTarget
}

var (
	pvUnknownVal  = pval{kind: pvUnknown}
	pvExternalVal = pval{kind: pvExternal}
)

func pvTypes(ds []*Def) pval {
	if len(ds) == 0 {
		return pvUnknownVal
	}
	return pval{kind: pvTyped, types: ds}
}

// pyInfer infers expression types within one file.
type pyInfer struct {
	s     *Session
	r     pyResolver
	file  *File
	src   []byte
	root  *sitter.Node
	depth *int // shared across files to bound mutual recursion
}

// pyReceiver classifies the Python reference at site (see LangResolver).
func pyReceiver(s *Session, site *Site) Receiver {
	if site.Line == 0 {
		return Receiver{Kind: RecvUnknownShape}
	}
	tree, src := pyTree(s, site.File)
	if tree == nil {
		return Receiver{Kind: RecvUnknownShape}
	}
	root := tree.RootNode()
	pt := sitter.Point{Row: uint(site.Line - 1), Column: uint(site.Col)}
	n := root.NamedDescendantForPointRange(pt, pt)
	if n == nil || n.Kind() != "identifier" || n.Utf8Text(src) != site.Name {
		return Receiver{Kind: RecvUnknownShape}
	}
	p := n.Parent()
	if p == nil || p.Kind() != "attribute" {
		return Receiver{Kind: RecvNone}
	}
	if a := p.ChildByFieldName("attribute"); a == nil || a.StartByte() != n.StartByte() {
		return Receiver{Kind: RecvNone}
	}
	depth := 0
	in := &pyInfer{s: s, file: site.File, src: src, root: root, depth: &depth}
	v := in.expr(p.ChildByFieldName("object"))
	switch v.kind {
	case pvTyped:
		return Receiver{Kind: RecvTyped, Types: v.types}
	case pvExternal:
		return Receiver{Kind: RecvExternal}
	case pvModule:
		if !v.mod.Resolved() {
			return Receiver{Kind: RecvExternal}
		}
	}
	return Receiver{Kind: RecvUntyped}
}

func (in *pyInfer) text(n *sitter.Node) string { return n.Utf8Text(in.src) }

// forFile returns an inferrer for another file sharing the depth budget.
func (in *pyInfer) forFile(f *File) *pyInfer {
	if f == nil {
		return nil
	}
	if f.ID == in.file.ID {
		return in
	}
	tree, src := pyTree(in.s, f)
	if tree == nil {
		return nil
	}
	return &pyInfer{s: in.s, file: f, src: src, root: tree.RootNode(), depth: in.depth}
}

// expr infers the value of expression n.
func (in *pyInfer) expr(n *sitter.Node) pval {
	if n == nil || *in.depth > maxDepth {
		return pvUnknownVal
	}
	*in.depth++
	defer func() { *in.depth-- }()

	switch n.Kind() {
	case "identifier":
		return in.ident(n)
	case "attribute":
		a := n.ChildByFieldName("attribute")
		if a == nil {
			return pvUnknownVal
		}
		return in.member(in.expr(n.ChildByFieldName("object")), in.text(a))
	case "call":
		return in.call(n)
	case "parenthesized_expression", "await":
		if n.NamedChildCount() == 1 {
			return in.expr(n.NamedChild(0))
		}
	case "string", "concatenated_string", "integer", "float", "true", "false", "none",
		"list", "dictionary", "set", "tuple", "list_comprehension", "dictionary_comprehension",
		"set_comprehension", "generator_expression", "comparison_operator", "not_operator", "lambda":
		return pvExternalVal
	}
	return pvUnknownVal
}

// ident infers a bare name: self/cls, locals and parameters of the
// enclosing functions, module-level assignments, imports and classes.
func (in *pyInfer) ident(n *sitter.Node) pval {
	name := in.text(n)
	fn := pyEnclosingFunc(n)
	for f := fn; f != nil; f = pyEnclosingFunc(f) {
		if (name == "self" || name == "cls") && pyFirstParam(f, in.src) == name {
			if cls := in.classDef(pyClassOf(f)); cls != nil {
				return pvTypes([]*Def{cls})
			}
		}
		if v, ok := in.local(f, name, n); ok {
			return v
		}
	}
	// Module scope: inside a function every module-level binding counts.
	limit := n.StartByte()
	if fn != nil {
		limit = in.root.EndByte()
	}
	if v, ok := in.assigned(in.root, name, limit); ok {
		return v
	}
	name = pyDealias(in.s, in.file, name)
	if t, ok := in.s.Binding(in.file, name); ok {
		return pval{kind: pvModule, mod: t}
	}
	if t, ok := in.s.NamedImport(in.file, name); ok {
		if !t.Resolved() {
			return pvExternalVal
		}
		if tr := in.r.ResolveType(in.s, in.file, "", name); tr.Found() {
			return pvTypes(tr.Defs)
		}
		if !in.definedIn(t, name) {
			return pval{kind: pvModule, mod: t} // from pkg import submodule
		}
		return pvUnknownVal
	}
	tr := in.r.ResolveType(in.s, in.file, "", name)
	switch {
	case tr.Found():
		return pvTypes(tr.Defs)
	case tr.External:
		return pvExternalVal
	}
	return pvUnknownVal
}

// definedIn reports whether target t defines a top-level name.
func (in *pyInfer) definedIn(t model.ImportTarget, name string) bool {
	for _, d := range in.s.DefsNamed(name) {
		if d.ParentID == 0 && in.s.InTarget(d, t) {
			return true
		}
	}
	return false
}

// member infers obj.name.
func (in *pyInfer) member(obj pval, name string) pval {
	switch obj.kind {
	case pvExternal:
		return pvExternalVal
	case pvModule:
		if !obj.mod.Resolved() {
			return pvExternalVal
		}
		for _, d := range in.s.DefsNamed(name) {
			if d.ParentID != 0 || !in.s.InTarget(d, obj.mod) {
				continue
			}
			switch {
			case d.Kind.IsType():
				return pvTypes([]*Def{d})
			case d.Kind == model.KindVariable || d.Kind == model.KindConstant:
				return in.annotatedDef(d)
			}
			return pvUnknownVal
		}
	case pvTyped:
		for _, t := range obj.types {
			ms := in.s.Members(t, name)
			if len(ms) == 0 {
				continue
			}
			m := ms[0]
			switch {
			case m.Kind.IsType():
				return pvTypes([]*Def{m})
			case m.Kind == model.KindEnumMember:
				return pvTypes([]*Def{in.s.Parent(m)})
			case m.Kind == model.KindField || m.Kind == model.KindVariable || m.Kind == model.KindConstant:
				if v := in.annotatedDef(m); v.kind != pvUnknown {
					return v
				}
				continue // maybe assigned as self.<name> with a known type
			case strings.Contains(m.Sig, "@property") || strings.Contains(m.Sig, "@cached_property"):
				return in.returnOf(m, obj.types)
			}
			return pvUnknownVal // a bound method
		}
		for _, t := range obj.types {
			if v := in.selfAttr(t, name); v.kind != pvUnknown {
				return v
			}
		}
	}
	return pvUnknownVal
}

// call infers the result of a call expression.
func (in *pyInfer) call(n *sitter.Node) pval {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return pvUnknownVal
	}
	switch fn.Kind() {
	case "identifier":
		name := in.text(fn)
		if name == "super" {
			cls := in.classDef(pyClassOf(pyEnclosingFunc(n)))
			if cls == nil {
				return pvUnknownVal
			}
			if anc := in.s.Ancestors(cls); len(anc) > 0 {
				return pvTypes(anc)
			}
			return pvExternalVal
		}
		callee := in.ident(fn)
		switch callee.kind {
		case pvTyped: // constructor call
			return callee
		case pvExternal, pvModule:
			return pvExternalVal
		}
		for f := pyEnclosingFunc(n); f != nil; f = pyEnclosingFunc(f) {
			if _, local := in.local(f, name, fn); local {
				return pvUnknownVal // a callable value or nested function
			}
		}
		if f := in.function(name); f != nil {
			return in.returnOf(f, nil)
		}
	case "attribute":
		a := fn.ChildByFieldName("attribute")
		if a == nil {
			return pvUnknownVal
		}
		name := in.text(a)
		obj := in.expr(fn.ChildByFieldName("object"))
		switch obj.kind {
		case pvExternal:
			return pvExternalVal
		case pvModule:
			if !obj.mod.Resolved() {
				return pvExternalVal
			}
			for _, d := range in.s.DefsNamed(name) {
				if d.ParentID != 0 || !in.s.InTarget(d, obj.mod) {
					continue
				}
				if d.Kind.IsType() {
					return pvTypes([]*Def{d})
				}
				if d.Kind.IsCallable() {
					return in.returnOf(d, nil)
				}
			}
		case pvTyped:
			// The first type with a member of that name decides.
			for _, t := range obj.types {
				ms := in.s.Members(t, name)
				if len(ms) == 0 {
					continue
				}
				switch m := ms[0]; {
				case m.Kind.IsType():
					return pvTypes([]*Def{m})
				case m.Kind.IsCallable():
					return in.returnOf(m, obj.types)
				}
				return pvUnknownVal
			}
		}
	}
	return pvUnknownVal
}

// function finds the unique top-level function called name visible in the
// file: defined in it or brought in by a from-import.
func (in *pyInfer) function(name string) *Def {
	var out []*Def
	target, imported := in.s.NamedImport(in.file, name)
	for _, d := range in.s.DefsNamed(name) {
		if d.ParentID != 0 || !d.Kind.IsCallable() {
			continue
		}
		if d.FileID == in.file.ID || imported && in.s.InTarget(d, target) {
			out = append(out, d)
		}
	}
	if len(out) == 1 {
		return out[0]
	}
	return nil
}

// returnOf infers the result of calling d from its return annotation;
// self lists the receiver types for "Self" and the owner class name.
func (in *pyInfer) returnOf(d *Def, self []*Def) pval {
	ann := pyReturnAnnotation(d.Sig)
	if ann == "" {
		return pvUnknownVal
	}
	if a := pyUnquote(ann); a == "Self" || a == "typing.Self" {
		if len(self) > 0 {
			return pvTypes(self)
		}
		if owner := in.s.EnclosingType(in.s.Parent(d)); owner != nil {
			return pvTypes([]*Def{owner})
		}
		return pvUnknownVal
	}
	return in.annotation(ann, in.s.File(d.FileID))
}

// annotatedDef infers the value of an annotated field or variable from its
// sig ("count: int = 0").
func (in *pyInfer) annotatedDef(d *Def) pval {
	if ann := pyVarAnnotation(d.Sig, d.Name); ann != "" {
		return in.annotation(ann, in.s.File(d.FileID))
	}
	return pvUnknownVal
}

// annotation resolves a type annotation written in file f.
func (in *pyInfer) annotation(ann string, f *File) pval {
	if f == nil {
		return pvUnknownVal
	}
	a := pyUnquote(ann)
	if parts := pySplitTop(a, '|'); len(parts) > 1 {
		return in.union(parts, f)
	}
	if base, args, ok := pyGeneric(a); ok {
		switch pyLast(base) {
		case "Optional", "Annotated", "Final", "ClassVar", "Required", "NotRequired", "ReadOnly":
			if len(args) > 0 {
				return in.annotation(args[0], f)
			}
		case "Union":
			return in.union(args, f)
		case "type", "Type":
			if len(args) == 1 {
				return in.annotation(args[0], f)
			}
		}
		a = base
	}
	if a == "None" || a == "" {
		return pvExternalVal
	}
	if !isPyDottedName(a) {
		return pvUnknownVal
	}
	a = pyDealias(in.s, f, a)
	qual, name := "", a
	if i := strings.LastIndexByte(a, '.'); i >= 0 {
		qual, name = a[:i], a[i+1:]
	}
	tr := in.r.ResolveType(in.s, f, qual, name)
	switch {
	case tr.Found():
		return pvTypes(tr.Defs)
	case tr.External:
		return pvExternalVal
	}
	return pvUnknownVal
}

// union infers X | None as X; other unions are unknown.
func (in *pyInfer) union(parts []string, f *File) pval {
	var rest []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "None" {
			rest = append(rest, p)
		}
	}
	if len(rest) == 1 {
		return in.annotation(rest[0], f)
	}
	return pvUnknownVal
}

// classDef maps a class_definition node of this file to its definition.
func (in *pyInfer) classDef(cls *sitter.Node) *Def {
	if cls == nil {
		return nil
	}
	nameNode := cls.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	start := cls.StartByte()
	if p := cls.Parent(); p != nil && p.Kind() == "decorated_definition" {
		start = p.StartByte()
	}
	for _, d := range in.s.DefsNamed(in.text(nameNode)) {
		if d.FileID == in.file.ID && d.Kind.IsType() && uint(d.StartByte) == start {
			return d
		}
	}
	return nil
}

// ---------------------------------------------------------------- scopes

// local infers name from the parameters or assignments of function fn
// (before node at). ok reports that fn binds name at all.
func (in *pyInfer) local(fn *sitter.Node, name string, at *sitter.Node) (pval, bool) {
	if params := fn.ChildByFieldName("parameters"); params != nil {
		for i := uint(0); i < params.NamedChildCount(); i++ {
			if v, ok := in.param(params.NamedChild(i), name); ok {
				return v, true
			}
		}
	}
	if body := fn.ChildByFieldName("body"); body != nil {
		return in.assigned(body, name, at.StartByte())
	}
	return pvUnknownVal, false
}

// param infers a parameter called name.
func (in *pyInfer) param(p *sitter.Node, name string) (pval, bool) {
	if p == nil {
		return pvUnknownVal, false
	}
	switch p.Kind() {
	case "identifier":
		return pvUnknownVal, in.text(p) == name
	case "typed_parameter":
		for i := uint(0); i < p.NamedChildCount(); i++ {
			c := p.NamedChild(i)
			switch {
			case c.Kind() == "identifier" && in.text(c) == name:
				return in.annotation(in.typeText(p), in.file), true
			case (c.Kind() == "list_splat_pattern" || c.Kind() == "dictionary_splat_pattern") && pyNameOf(c, in.src) == name:
				return pvExternalVal, true
			}
		}
	case "default_parameter", "typed_default_parameter":
		if n := p.ChildByFieldName("name"); n != nil && in.text(n) == name {
			if p.Kind() == "typed_default_parameter" {
				return in.annotation(in.typeText(p), in.file), true
			}
			return in.expr(p.ChildByFieldName("value")), true
		}
	case "list_splat_pattern", "dictionary_splat_pattern":
		return pvExternalVal, pyNameOf(p, in.src) == name
	}
	return pvUnknownVal, false
}

func (in *pyInfer) typeText(n *sitter.Node) string {
	if t := n.ChildByFieldName("type"); t != nil {
		return in.text(t)
	}
	return ""
}

// assigned infers name from the last binding in scope (not descending
// into nested functions, classes or lambdas) that ends before byte limit.
// ok reports that the scope binds name.
func (in *pyInfer) assigned(scope *sitter.Node, name string, limit uint) (pval, bool) {
	binds := in.scopeBindings(scope)[name]
	for i := len(binds) - 1; i >= 0; i-- {
		if n := &binds[i]; n.EndByte() <= limit {
			return in.binding(n), true
		}
	}
	return pvUnknownVal, false
}

// scopeBindings indexes the bindings of a scope by name (cached per batch).
// Nested function and class definitions bind their name; their bodies are
// separate scopes.
func (in *pyInfer) scopeBindings(scope *sitter.Node) map[string][]sitter.Node {
	b := pyBatchOf(in.s)
	key := pyScopeKey{in.file.ID, scope.StartByte(), scope.EndByte()}
	if m, ok := b.scopes[key]; ok {
		return m
	}
	m := map[string][]sitter.Node{}
	stack := []*sitter.Node{scope}
	for visited := 0; len(stack) > 0 && visited < pyMaxScan; visited++ {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n != scope {
			switch n.Kind() {
			case "function_definition", "class_definition":
				if nm := n.ChildByFieldName("name"); nm != nil {
					m[in.text(nm)] = append(m[in.text(nm)], *n)
				}
				continue
			case "lambda":
				continue
			}
		}
		if name := pyBoundName(n, in.src); name != "" {
			m[name] = append(m[name], *n)
		}
		for i := int(n.NamedChildCount()) - 1; i >= 0; i-- {
			stack = append(stack, n.NamedChild(uint(i)))
		}
	}
	for _, ns := range m {
		slices.SortFunc(ns, func(a, b sitter.Node) int { return cmp.Compare(a.StartByte(), b.StartByte()) })
	}
	b.scopes[key] = m
	return m
}

// binding infers the value bound by node n (see pyBoundName).
func (in *pyInfer) binding(n *sitter.Node) pval {
	switch n.Kind() {
	case "assignment":
		if t := n.ChildByFieldName("type"); t != nil {
			return in.annotation(in.text(t), in.file)
		}
		return in.expr(n.ChildByFieldName("right"))
	case "named_expression":
		return in.expr(n.ChildByFieldName("value"))
	case "as_pattern": // with X() as name
		if n.NamedChildCount() > 0 {
			return in.expr(n.NamedChild(0))
		}
	case "class_definition":
		if d := in.classDef(n); d != nil {
			return pvTypes([]*Def{d})
		}
	}
	return pvUnknownVal
}

// selfAttr infers an instance attribute assigned as self.<name> in the
// methods of class t or its ancestors (breadth first).
func (in *pyInfer) selfAttr(t *Def, name string) pval {
	seen := map[int64]bool{}
	level := []*Def{t}
	for depth := 0; depth < maxDepth && len(level) > 0; depth++ {
		var next []*Def
		for _, c := range level {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if v, ok := in.selfAttrIn(c, name); ok {
				return v
			}
			next = append(next, in.s.Ancestors(c)...)
		}
		level = next
	}
	return pvUnknownVal
}

func (in *pyInfer) selfAttrIn(c *Def, name string) (pval, bool) {
	fi := in.forFile(in.s.File(c.FileID))
	if fi == nil {
		return pvUnknownVal, false
	}
	cls := pyNodeOfDef(fi.root, c)
	if cls == nil {
		return pvUnknownVal, false
	}
	as, ok := fi.classAttrs(cls)[name]
	if !ok {
		return pvUnknownVal, false
	}
	if t := as.ChildByFieldName("type"); t != nil {
		return fi.annotation(fi.text(t), fi.file), true
	}
	return fi.expr(as.ChildByFieldName("right")), true
}

// classAttrs indexes the first "self.<attr> = ..." assignment per attribute
// in the methods of class cls, __init__ first (cached per batch).
func (in *pyInfer) classAttrs(cls *sitter.Node) map[string]sitter.Node {
	b := pyBatchOf(in.s)
	key := pyScopeKey{in.file.ID, cls.StartByte(), cls.EndByte()}
	if m, ok := b.attrs[key]; ok {
		return m
	}
	m := map[string]sitter.Node{}
	b.attrs[key] = m
	body := cls.ChildByFieldName("body")
	if body == nil {
		return m
	}
	var methods []*sitter.Node
	for i := uint(0); i < body.NamedChildCount(); i++ {
		fn := body.NamedChild(i)
		if fn.Kind() == "decorated_definition" {
			fn = fn.ChildByFieldName("definition")
		}
		if fn == nil || fn.Kind() != "function_definition" {
			continue
		}
		if nm := fn.ChildByFieldName("name"); nm != nil && in.text(nm) == "__init__" {
			methods = append([]*sitter.Node{fn}, methods...)
		} else {
			methods = append(methods, fn)
		}
	}
	for _, fn := range methods {
		self := pyFirstParam(fn, in.src)
		if body := fn.ChildByFieldName("body"); self != "" && body != nil {
			in.collectSelfAssignments(body, self, m)
		}
	}
	return m
}

// collectSelfAssignments records "self.attr = ..." in a method body (not
// nested scopes), keeping the first per attribute.
func (in *pyInfer) collectSelfAssignments(body *sitter.Node, self string, m map[string]sitter.Node) {
	var found []sitter.Node
	stack := []*sitter.Node{body}
	for visited := 0; len(stack) > 0 && visited < pyMaxScan; visited++ {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch n.Kind() {
		case "function_definition", "class_definition", "lambda":
			continue
		case "assignment":
			if l := n.ChildByFieldName("left"); l != nil && l.Kind() == "attribute" {
				if obj := l.ChildByFieldName("object"); obj != nil && in.text(obj) == self {
					found = append(found, *n)
				}
			}
		}
		for i := int(n.NamedChildCount()) - 1; i >= 0; i-- {
			stack = append(stack, n.NamedChild(uint(i)))
		}
	}
	slices.SortFunc(found, func(a, b sitter.Node) int { return cmp.Compare(a.StartByte(), b.StartByte()) })
	for _, n := range found {
		attr := n.ChildByFieldName("left").ChildByFieldName("attribute")
		if attr == nil {
			continue
		}
		if name := in.text(attr); name != "" {
			if _, dup := m[name]; !dup {
				m[name] = n
			}
		}
	}
}

// pyDealias rewrites the root of a dotted name bound by a from-import
// alias ("from m import User as Account") to the imported name, matching
// the parse layer's de-aliasing of references.
func pyDealias(s *Session, f *File, dotted string) string {
	root, rest, dot := strings.Cut(dotted, ".")
	for _, im := range s.Imports(f) {
		if im.Alias == root && len(im.Names) == 1 && im.Names[0] != "*" {
			if dot {
				return im.Names[0] + "." + rest
			}
			return im.Names[0]
		}
	}
	return dotted
}

// ---------------------------------------------------------------- syntax helpers

// pyBoundName returns the bare name bound by n, or "": an assignment or
// walrus to a name, a with/except "as" target, or a for target.
func pyBoundName(n *sitter.Node, src []byte) string {
	var target *sitter.Node
	switch n.Kind() {
	case "assignment", "for_statement", "for_in_clause":
		target = n.ChildByFieldName("left")
	case "named_expression":
		target = n.ChildByFieldName("name")
	case "as_pattern":
		if a := n.ChildByFieldName("alias"); a != nil && a.NamedChildCount() == 1 {
			target = a.NamedChild(0)
		}
	}
	if target == nil || target.Kind() != "identifier" {
		return ""
	}
	return target.Utf8Text(src)
}

// pyEnclosingFunc returns the closest function_definition strictly above n.
func pyEnclosingFunc(n *sitter.Node) *sitter.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "function_definition" {
			return p
		}
	}
	return nil
}

// pyClassOf returns the class whose body directly contains function fn.
func pyClassOf(fn *sitter.Node) *sitter.Node {
	if fn == nil {
		return nil
	}
	p := fn.Parent()
	if p != nil && p.Kind() == "decorated_definition" {
		p = p.Parent()
	}
	if p == nil || p.Kind() != "block" {
		return nil
	}
	if c := p.Parent(); c != nil && c.Kind() == "class_definition" {
		return c
	}
	return nil
}

// pyFirstParam returns the name of fn's first parameter.
func pyFirstParam(fn *sitter.Node, src []byte) string {
	params := fn.ChildByFieldName("parameters")
	if params == nil || params.NamedChildCount() == 0 {
		return ""
	}
	p := params.NamedChild(0)
	switch p.Kind() {
	case "identifier":
		return p.Utf8Text(src)
	case "typed_parameter":
		if p.NamedChildCount() > 0 && p.NamedChild(0).Kind() == "identifier" {
			return p.NamedChild(0).Utf8Text(src)
		}
	case "default_parameter", "typed_default_parameter":
		if n := p.ChildByFieldName("name"); n != nil {
			return n.Utf8Text(src)
		}
	}
	return ""
}

func pyNameOf(n *sitter.Node, src []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Kind() == "identifier" {
			return c.Utf8Text(src)
		}
	}
	return ""
}

// pyNodeOfDef finds the class_definition node of type def d in root.
func pyNodeOfDef(root *sitter.Node, d *Def) *sitter.Node {
	n := root.NamedDescendantForByteRange(uint(d.StartByte), uint(d.EndByte))
	for ; n != nil; n = n.Parent() {
		if n.StartByte() < uint(d.StartByte) {
			return nil
		}
		switch n.Kind() {
		case "decorated_definition":
			if c := n.ChildByFieldName("definition"); c != nil && c.Kind() == "class_definition" {
				return c
			}
		case "class_definition":
			if n.StartByte() == uint(d.StartByte) || n.Parent() != nil && n.Parent().Kind() == "decorated_definition" {
				return n
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- sig parsing

// pyReturnAnnotation extracts T from "[@dec ]def f(...) -> T".
func pyReturnAnnotation(sig string) string {
	if strings.HasSuffix(sig, "…") {
		return ""
	}
	i := strings.Index(sig, "def ")
	if i < 0 {
		return ""
	}
	open := strings.IndexByte(sig[i:], '(')
	if open < 0 {
		return ""
	}
	end := matchClose(sig, i+open)
	if end < 0 {
		return ""
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(sig[end+1:]), "->")
	if !ok {
		return ""
	}
	return strings.TrimSpace(rest)
}

// pyVarAnnotation extracts T from "name: T = value".
func pyVarAnnotation(sig, name string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(sig), name)
	if !ok {
		return ""
	}
	rest, ok = strings.CutPrefix(strings.TrimSpace(rest), ":")
	if !ok || strings.HasSuffix(sig, "…") {
		return ""
	}
	if parts := pySplitTop(rest, '='); len(parts) > 0 {
		rest = parts[0]
	}
	return strings.TrimSpace(rest)
}

// pySplitTop splits s on sep outside brackets and quotes.
func pySplitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// pyGeneric splits "Base[a, b]" into its base and top-level arguments.
func pyGeneric(s string) (base string, args []string, ok bool) {
	open := strings.IndexByte(s, '[')
	if open <= 0 || !strings.HasSuffix(s, "]") || matchClose(s, open) != len(s)-1 {
		return "", nil, false
	}
	for _, a := range pySplitTop(s[open+1:len(s)-1], ',') {
		if a = strings.TrimSpace(a); a != "" {
			args = append(args, a)
		}
	}
	return strings.TrimSpace(s[:open]), args, true
}

func pyUnquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func pyLast(dotted string) string { return dotted[strings.LastIndexByte(dotted, '.')+1:] }

// isPyDottedName reports whether s is an identifier chain such as
// "models.Item".
func isPyDottedName(s string) bool {
	if s == "" {
		return false
	}
	for _, seg := range strings.Split(s, ".") {
		if seg == "" || seg[0] >= '0' && seg[0] <= '9' {
			return false
		}
		for i := 0; i < len(seg); i++ {
			if !isIdentByte(seg[i]) || seg[i] == '$' {
				return false
			}
		}
	}
	return true
}
