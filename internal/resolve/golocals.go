package resolve

import (
	"slices"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Go local scopes: finding the declaration an identifier refers to inside
// function bodies, and small tree-sitter traversal helpers.

// scopeKinds are the nodes that bound the visibility of a declaration.
var scopeKinds = map[string]bool{
	"block": true, "if_statement": true, "for_statement": true,
	"expression_switch_statement": true, "type_switch_statement": true, "select_statement": true,
	"expression_case": true, "type_case": true, "default_case": true, "communication_case": true,
	"func_literal": true, "function_declaration": true, "method_declaration": true, "source_file": true,
}

var funcKinds = map[string]bool{"function_declaration": true, "method_declaration": true, "func_literal": true}

func enclosing(n *sitter.Node, kinds map[string]bool) *sitter.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if kinds[p.Kind()] {
			return p
		}
	}
	return nil
}

func containsNode(outer, inner *sitter.Node) bool {
	return outer.StartByte() <= inner.StartByte() && inner.EndByte() <= outer.EndByte()
}

// localDecl finds the declaration of name visible at node at: for each
// enclosing function (innermost first), the closest preceding declaration
// in scope within its body, then its receiver, parameters and named
// results.
func (in *goInfer) localDecl(at *sitter.Node, name string) (gval, bool) {
	for fn := enclosing(at, funcKinds); fn != nil; fn = enclosing(fn, funcKinds) {
		body := fn.ChildByFieldName("body")
		if body == nil || !containsNode(body, at) {
			// at is in the signature (e.g. a parameter type).
			if v, ok := in.param(fn, name); ok {
				return v, true
			}
			continue
		}
		var decl *sitter.Node
		var idx int
		atStart, atEnd := at.StartByte(), at.EndByte()
		ds := in.bodyDecls(body)
		for i := range ds {
			d := &ds[i]
			if d.start >= atStart {
				break // declarations are in pre-order, so by start
			}
			if d.end > atStart || atStart < d.scopeStart || d.scopeEnd < atEnd {
				continue
			}
			if j := slices.Index(d.names, name); j >= 0 && name != "" {
				decl, idx = d.node, j
			}
		}
		if decl != nil {
			return in.declVal(decl, idx), true
		}
		// Parameters are shadowed by body declarations.
		if v, ok := in.param(fn, name); ok {
			return v, true
		}
	}
	return gval{}, false
}

// goDecl is a local declaration (short_var_declaration, var_spec or
// range_clause) of a function body, outside nested function literals.
type goDecl struct {
	node                 *sitter.Node
	start, end           uint
	scopeStart, scopeEnd uint     // declScope
	names                []string // declared identifiers ("" for others)
}

type goBodyKey struct {
	file       int64
	start, end uint
}

// bodyDecls returns the local declarations of body in pre-order, cached
// for the batch: scanning the body for every identifier made receiver
// typing quadratic in the size of a function.
func (in *goInfer) bodyDecls(body *sitter.Node) []goDecl {
	b := goBatchOf(in.s)
	k := goBodyKey{file: in.file.ID, start: body.StartByte(), end: body.EndByte()}
	if ds, ok := b.decls[k]; ok {
		return ds
	}
	ds := []goDecl{}
	walk(body, func(d *sitter.Node) bool {
		switch d.Kind() {
		case "func_literal":
			return false // its locals are not visible outside
		case "short_var_declaration", "var_spec", "range_clause":
			sc := declScope(d)
			ds = append(ds, goDecl{node: d, start: d.StartByte(), end: d.EndByte(),
				scopeStart: sc.StartByte(), scopeEnd: sc.EndByte(), names: declNames(in, d)})
		}
		return true
	})
	b.decls[k] = ds
	return ds
}

// declNames returns d's declared identifiers in order ("" for a
// non-identifier).
func declNames(in *goInfer, d *sitter.Node) []string {
	var ids []*sitter.Node
	switch d.Kind() {
	case "var_spec":
		ids = fieldChildren(d, "name")
	default: // short_var_declaration, range_clause
		if l := d.ChildByFieldName("left"); l != nil {
			ids = namedChildren(l)
		}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		if id.Kind() == "identifier" {
			out[i] = in.text(id)
		}
	}
	return out
}

func declScope(d *sitter.Node) *sitter.Node {
	if s := enclosing(d, scopeKinds); s != nil {
		return s
	}
	return d
}

func (in *goInfer) declVal(d *sitter.Node, i int) gval {
	switch d.Kind() {
	case "var_spec":
		if t := d.ChildByFieldName("type"); t != nil {
			return in.typeNode(t)
		}
		return in.values(d.ChildByFieldName("value"), len(fieldChildren(d, "name")), i)
	case "short_var_declaration":
		left := d.ChildByFieldName("left")
		n := 0
		if left != nil {
			n = int(left.NamedChildCount())
		}
		return in.values(d.ChildByFieldName("right"), n, i)
	case "range_clause":
		v := in.expr(d.ChildByFieldName("right"))
		if v.kind != gvType {
			return v
		}
		k, e, ok := goRange(v.typ)
		if !ok {
			return externalVal
		}
		if i == 0 {
			return typedVal(k, v.file)
		}
		return typedVal(e, v.file)
	}
	return unknownVal
}

// values infers value i of "lhs(n) = list".
func (in *goInfer) values(list *sitter.Node, n, i int) gval {
	if list == nil {
		return unknownVal
	}
	vs := namedChildren(list)
	switch {
	case len(vs) == n && i < len(vs):
		return in.expr(vs[i])
	case len(vs) == 1:
		return in.exprAt(vs[0], i)
	}
	return unknownVal
}

// param finds name among fn's receiver, parameters and named results.
func (in *goInfer) param(fn *sitter.Node, name string) (gval, bool) {
	for _, field := range []string{"receiver", "parameters", "result"} {
		list := fn.ChildByFieldName(field)
		if list == nil || list.Kind() != "parameter_list" {
			continue
		}
		for _, p := range namedChildren(list) {
			for _, id := range fieldChildren(p, "name") {
				if in.text(id) != name {
					continue
				}
				t := p.ChildByFieldName("type")
				if t == nil {
					return unknownVal, true
				}
				if p.Kind() == "variadic_parameter_declaration" {
					return typedVal("[]"+in.text(t), in.file), true
				}
				return in.typeNode(t), true
			}
		}
	}
	return gval{}, false
}

// isTypeParam reports whether name is a type parameter in scope at n.
func (in *goInfer) isTypeParam(name string, n *sitter.Node) bool {
	for fn := enclosing(n, funcKinds); fn != nil; fn = enclosing(fn, funcKinds) {
		if tps := fn.ChildByFieldName("type_parameters"); tps != nil {
			for _, p := range namedChildren(tps) {
				for _, id := range fieldChildren(p, "name") {
					if in.text(id) == name {
						return true
					}
				}
			}
		}
		if recv := fn.ChildByFieldName("receiver"); recv != nil {
			found := false
			walk(recv, func(d *sitter.Node) bool {
				if d.Kind() == "type_arguments" {
					walk(d, func(t *sitter.Node) bool {
						if t.Kind() == "type_identifier" && in.text(t) == name {
							found = true
						}
						return !found
					})
					return false
				}
				return !found
			})
			if found {
				return true
			}
		}
	}
	return false
}

// walk visits n's named descendants in pre-order; visit returns false to
// skip a subtree.
func walk(n *sitter.Node, visit func(*sitter.Node) bool) {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && visit(c) {
			walk(c, visit)
		}
	}
}

func namedChildren(n *sitter.Node) []*sitter.Node {
	out := make([]*sitter.Node, 0, n.NamedChildCount())
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c != nil {
			out = append(out, c)
		}
	}
	return out
}

func fieldChildren(n *sitter.Node, field string) []*sitter.Node {
	var out []*sitter.Node
	for i := uint(0); i < n.ChildCount(); i++ {
		if n.FieldNameForChild(uint32(i)) == field {
			if c := n.Child(i); c != nil {
				out = append(out, c)
			}
		}
	}
	return out
}
