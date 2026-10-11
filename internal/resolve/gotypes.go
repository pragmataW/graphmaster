package resolve

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Go receiver typing (T3). The type of a selector operand is inferred from
// the enclosing function's source: receiver and parameters, local var and
// := declarations (closest preceding one in scope), composite literals,
// new/make, conversions, function and method results (from their sigs),
// struct fields (from field sigs, walking embedded types) and package-level
// variables. Types are carried as source text plus the file whose imports
// qualify them, e.g. ("*store.Item", order.go).

// goState is the run-wide Go state: one parser and a cache of parsed sigs.
type goState struct {
	parser *sitter.Parser
	sigs   map[string][]string
}

func (g *goState) Close() { g.parser.Close() }

// goBatch caches parsed files, and the local declarations of their
// function bodies, for one batch.
type goBatch struct {
	trees map[int64]*sitter.Tree
	decls map[goBodyKey][]goDecl
}

func (b *goBatch) Close() {
	for _, tr := range b.trees {
		if tr != nil {
			tr.Close()
		}
	}
}

func goBatchOf(s *Session) *goBatch {
	b, ok := s.BatchState(model.LangGo).(*goBatch)
	if !ok {
		b = &goBatch{trees: map[int64]*sitter.Tree{}, decls: map[goBodyKey][]goDecl{}}
		s.SetBatchState(model.LangGo, b)
	}
	return b
}

func goStateOf(s *Session) *goState {
	if g, ok := s.State(model.LangGo).(*goState); ok {
		return g
	}
	p := sitter.NewParser()
	if err := p.SetLanguage(sitter.NewLanguage(tsgo.Language())); err != nil {
		p.Close()
		return nil
	}
	g := &goState{parser: p, sigs: map[string][]string{}}
	s.SetState(model.LangGo, g)
	return g
}

// goTree returns the parsed tree of f (cached per batch), or nil.
func goTree(s *Session, f *File, src []byte) *sitter.Tree {
	trees := goBatchOf(s).trees
	if tr, ok := trees[f.ID]; ok {
		return tr
	}
	var tr *sitter.Tree
	if g := goStateOf(s); g != nil {
		tr = g.parser.Parse(src, nil)
	}
	trees[f.ID] = tr
	return tr
}

// gvKind classifies an inferred value.
type gvKind uint8

const (
	gvUnknown  gvKind = iota // could not infer
	gvExternal               // basic, composite or out-of-repo type
	gvType                   // typ (written in file) is the value's type
	gvPkg                    // the identifier names an imported package
)

type gval struct {
	kind gvKind
	typ  string
	file *File
	pkg  model.ImportTarget
}

var (
	unknownVal  = gval{kind: gvUnknown}
	externalVal = gval{kind: gvExternal}
)

func typedVal(typ string, f *File) gval {
	if strings.TrimSpace(typ) == "" || f == nil {
		return unknownVal
	}
	return gval{kind: gvType, typ: strings.TrimSpace(typ), file: f}
}

// goInfer infers expression types within one file.
type goInfer struct {
	s     *Session
	r     goResolver
	file  *File
	src   []byte
	depth int
}

// goReceiver classifies the Go reference at site (see LangResolver).
func goReceiver(s *Session, site *Site) Receiver {
	src := s.Source(site.File)
	if src == nil || site.Line == 0 {
		return Receiver{Kind: RecvUnknownShape}
	}
	tree := goTree(s, site.File, src)
	if tree == nil {
		return Receiver{Kind: RecvUnknownShape}
	}
	pt := sitter.Point{Row: uint(site.Line - 1), Column: uint(site.Col)}
	n := tree.RootNode().NamedDescendantForPointRange(pt, pt)
	if n == nil || n.Utf8Text(src) != site.Name {
		return Receiver{Kind: RecvUnknownShape}
	}
	p := n.Parent()
	if p == nil || p.Kind() != "selector_expression" {
		return Receiver{Kind: RecvNone}
	}
	if f := p.ChildByFieldName("field"); f == nil || f.StartByte() != n.StartByte() {
		return Receiver{Kind: RecvNone}
	}
	op := p.ChildByFieldName("operand")
	if op == nil {
		return Receiver{Kind: RecvUntyped}
	}
	in := &goInfer{s: s, file: site.File, src: src}
	return in.receiverOf(in.expr(op), n)
}

func (in *goInfer) receiverOf(v gval, at *sitter.Node) Receiver {
	switch v.kind {
	case gvExternal:
		return Receiver{Kind: RecvExternal}
	case gvType:
		q, name, ok := goNamed(v.typ)
		if !ok {
			return Receiver{Kind: RecvExternal}
		}
		if q == "" && v.file == in.file && in.isTypeParam(name, at) {
			return Receiver{Kind: RecvUntyped}
		}
		tr := in.r.ResolveType(in.s, v.file, q, name)
		switch {
		case tr.Found():
			return Receiver{Kind: RecvTyped, Types: tr.Defs}
		case tr.External:
			return Receiver{Kind: RecvExternal}
		}
	}
	return Receiver{Kind: RecvUntyped}
}

func (in *goInfer) text(n *sitter.Node) string { return n.Utf8Text(in.src) }

// expr infers the type of expression n.
func (in *goInfer) expr(n *sitter.Node) gval {
	if n == nil || in.depth > maxDepth {
		return unknownVal
	}
	in.depth++
	defer func() { in.depth-- }()

	switch n.Kind() {
	case "identifier":
		return in.ident(n)
	case "selector_expression":
		op := in.expr(n.ChildByFieldName("operand"))
		f := n.ChildByFieldName("field")
		if f == nil {
			return unknownVal
		}
		return in.selectMember(op, in.text(f))
	case "call_expression":
		return in.call(n, 0)
	case "composite_literal":
		return in.typeNode(n.ChildByFieldName("type"))
	case "type_assertion_expression":
		return in.typeNode(n.ChildByFieldName("type"))
	case "unary_expression":
		op := n.ChildByFieldName("operator")
		v := in.expr(n.ChildByFieldName("operand"))
		if op == nil || v.kind != gvType {
			if v.kind == gvUnknown {
				return v
			}
			return externalVal
		}
		switch in.text(op) {
		case "&":
			v.typ = "*" + v.typ
			return v
		case "*":
			v.typ = strings.TrimPrefix(strings.TrimSpace(v.typ), "*")
			return v
		case "<-":
			if e, ok := goChanElem(v.typ); ok {
				return typedVal(e, v.file)
			}
		}
		return externalVal
	case "parenthesized_expression":
		if n.NamedChildCount() == 1 {
			return in.expr(n.NamedChild(0))
		}
	case "index_expression":
		v := in.expr(n.ChildByFieldName("operand"))
		if v.kind != gvType {
			return v
		}
		if e, ok := goElem(v.typ); ok {
			return typedVal(e, v.file)
		}
		return externalVal
	case "slice_expression":
		return in.expr(n.ChildByFieldName("operand"))
	case "interpreted_string_literal", "raw_string_literal", "int_literal", "float_literal",
		"imaginary_literal", "rune_literal", "true", "false", "nil", "iota",
		"binary_expression", "func_literal":
		return externalVal
	}
	return unknownVal
}

func (in *goInfer) typeNode(t *sitter.Node) gval {
	if t == nil {
		return unknownVal
	}
	return typedVal(in.text(t), in.file)
}

// ident infers the type of a bare identifier: local declaration, imported
// package, or package-level variable.
func (in *goInfer) ident(n *sitter.Node) gval {
	name := in.text(n)
	if v, ok := in.localDecl(n, name); ok {
		return v
	}
	if t, ok := in.s.Binding(in.file, name); ok {
		return gval{kind: gvPkg, pkg: t}
	}
	vars := filter(in.s.DefsNamed(name), func(d *Def) bool {
		return (d.Kind == model.KindVariable || d.Kind == model.KindConstant) && d.ParentID == 0 &&
			in.sameGoPackage(d)
	})
	if len(vars) == 1 {
		return in.varVal(vars[0])
	}
	return unknownVal
}

func (in *goInfer) sameGoPackage(d *Def) bool {
	f := in.s.File(d.FileID)
	return f != nil && f.Lang == model.LangGo && in.s.SameModule(f, in.file)
}

// varVal is the declared type of a package-level variable.
func (in *goInfer) varVal(d *Def) gval {
	if t := goVarType(in.s, d.Sig, d.Name); t != "" {
		return typedVal(t, in.s.File(d.FileID))
	}
	return unknownVal
}

// selectMember infers op.field.
func (in *goInfer) selectMember(op gval, field string) gval {
	switch op.kind {
	case gvPkg:
		if !op.pkg.Resolved() {
			return externalVal
		}
		ds := filter(in.s.DefsNamed(field), func(d *Def) bool {
			return d.ParentID == 0 && in.s.InTarget(d, op.pkg) && !in.r.IsMember(in.s, d)
		})
		if len(ds) == 0 {
			return externalVal // bound package without such a definition
		}
		if d := ds[0]; len(ds) == 1 && (d.Kind == model.KindVariable || d.Kind == model.KindConstant) {
			return in.varVal(d)
		}
		return unknownVal
	case gvType:
		types, v, done := in.typeDefs(op)
		if done {
			return v
		}
		for _, t := range types {
			for _, m := range in.s.Members(t, field) {
				if m.Kind == model.KindField {
					if ft := goFieldType(in.s, m.Sig, m.Name); ft != "" {
						return typedVal(ft, in.s.File(m.FileID))
					}
				}
				return unknownVal // method value or untyped field
			}
			if v, ok := in.embedded(t, field); ok {
				return v
			}
		}
		return externalVal
	}
	return op
}

// typeDefs resolves the named type of v. done reports that v cannot be
// typed further and res is the result to return.
func (in *goInfer) typeDefs(v gval) (types []*Def, res gval, done bool) {
	q, name, ok := goNamed(v.typ)
	if !ok {
		return nil, externalVal, true
	}
	tr := in.r.ResolveType(in.s, v.file, q, name)
	switch {
	case tr.Found():
		return tr.Defs, gval{}, false
	case tr.External:
		return nil, externalVal, true
	}
	return nil, unknownVal, true
}

// embedded finds an embedded field called field in t or its embedded
// types (breadth first); its type is the embedded type itself.
func (in *goInfer) embedded(t *Def, field string) (gval, bool) {
	seen := map[int64]bool{}
	level := []*Def{t}
	for depth := 0; depth < maxDepth && len(level) > 0; depth++ {
		var next []*Def
		for _, typ := range level {
			if seen[typ.ID] {
				continue
			}
			seen[typ.ID] = true
			f := in.s.File(typ.FileID)
			for _, ref := range in.s.Inherits(typ) {
				if ref.Kind != model.EdgeEmbeds {
					continue
				}
				if ref.Name == field {
					typ := ref.Name
					if ref.Qual != "" {
						typ = ref.Qual + "." + ref.Name
					}
					return typedVal(typ, f), true
				}
				next = append(next, in.r.ResolveType(in.s, f, ref.Qual, ref.Name).Defs...)
			}
		}
		level = next
	}
	return gval{}, false
}

// call infers result i of call expression n.
func (in *goInfer) call(n *sitter.Node, i int) gval {
	fn := n.ChildByFieldName("function")
	for fn != nil && (fn.Kind() == "index_expression" || fn.Kind() == "generic_type") {
		// Explicit instantiation F[int](x): use the generic function.
		if op := fn.ChildByFieldName("operand"); op != nil {
			fn = op
		} else {
			fn = fn.ChildByFieldName("type")
		}
	}
	if fn == nil {
		return unknownVal
	}
	switch fn.Kind() {
	case "identifier":
		name := in.text(fn)
		if _, local := in.localDecl(fn, name); local {
			return unknownVal // function value
		}
		if goBuiltinFuncs[name] {
			return in.builtin(name, n, i)
		}
		fns := filter(in.s.DefsNamed(name), func(d *Def) bool {
			return d.ParentID == 0 && !in.r.IsMember(in.s, d) && in.sameGoPackage(d)
		})
		return in.callee(fns, name, i)
	case "selector_expression":
		op := in.expr(fn.ChildByFieldName("operand"))
		f := fn.ChildByFieldName("field")
		if f == nil {
			return unknownVal
		}
		field := in.text(f)
		switch op.kind {
		case gvPkg:
			if !op.pkg.Resolved() {
				return externalVal
			}
			fns := filter(in.s.DefsNamed(field), func(d *Def) bool {
				return d.ParentID == 0 && in.s.InTarget(d, op.pkg) && !in.r.IsMember(in.s, d)
			})
			if len(fns) == 0 {
				return externalVal
			}
			return in.callee(fns, in.text(fn), i)
		case gvType:
			types, v, done := in.typeDefs(op)
			if done {
				return v
			}
			var ms []*Def
			for _, t := range types {
				ms = appendUnique(ms, in.s.Members(t, field)...)
			}
			if len(ms) == 0 {
				return externalVal // promoted from an out-of-repo type
			}
			return in.callee(ms, "", i)
		}
		return op
	}
	return unknownVal
}

// callee returns result i of the unique callable among fns, or a
// conversion when fns is a type (typeText is how the call names it).
func (in *goInfer) callee(fns []*Def, typeText string, i int) gval {
	if callables := best(fns, func(k model.NodeKind) int {
		if k.IsCallable() {
			return 0
		}
		return -1
	}); len(callables) == 1 {
		rs := goResults(in.s, callables[0].Sig)
		if i < len(rs) {
			return typedVal(rs[i], in.s.File(callables[0].FileID))
		}
		return unknownVal
	}
	if typeText != "" && i == 0 {
		if types := filter(fns, func(d *Def) bool { return d.Kind.IsType() }); len(types) == 1 {
			return typedVal(typeText, in.file)
		}
	}
	return unknownVal
}

func (in *goInfer) builtin(name string, call *sitter.Node, i int) gval {
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 || i != 0 {
		return externalVal
	}
	arg := args.NamedChild(0)
	switch name {
	case "new":
		return typedVal("*"+in.text(arg), in.file)
	case "make":
		return typedVal(in.text(arg), in.file)
	case "append", "min", "max":
		return in.expr(arg)
	}
	return externalVal
}

// exprAt infers value i of a multi-value expression.
func (in *goInfer) exprAt(n *sitter.Node, i int) gval {
	if n.Kind() == "call_expression" {
		return in.call(n, i)
	}
	if i == 0 {
		return in.expr(n)
	}
	return externalVal // the ok of v, ok := m[k] / x.(T) / <-ch
}
