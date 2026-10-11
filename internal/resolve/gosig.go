package resolve

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Go signature parsing. Stored sigs are source snippets (see parse's Go
// signature hook): functions "func (r *T) M(a int) (X, error)", interface
// methods "M(a int) X", fields "a, b pkg.T", variables "var x T = v". They
// are parsed by wrapping them into a tiny Go file; truncated sigs fail to
// parse cleanly and yield nothing. Results are cached for the run.

// goResults returns the result types of a function or method sig.
func goResults(s *Session, sig string) []string {
	return goSigCached(s, "r:"+sig, func(g *goState) []string {
		code := "package p\n" + sig + " {}\n"
		if !strings.HasPrefix(strings.TrimSpace(sig), "func") {
			code = "package p\ntype _ interface {\n" + sig + "\n}\n"
		}
		return goParseSnippet(g, code, func(root *sitter.Node, src []byte) []string {
			var decl *sitter.Node
			walk(root, func(n *sitter.Node) bool {
				switch n.Kind() {
				case "function_declaration", "method_declaration", "method_elem":
					decl = n
					return false
				}
				return decl == nil
			})
			if decl == nil {
				return nil
			}
			res := decl.ChildByFieldName("result")
			if res == nil {
				return nil
			}
			if res.Kind() != "parameter_list" {
				return []string{res.Utf8Text(src)}
			}
			var out []string
			for _, p := range namedChildren(res) {
				t := p.ChildByFieldName("type")
				if t == nil {
					continue
				}
				for k := max(1, len(fieldChildren(p, "name"))); k > 0; k-- {
					out = append(out, t.Utf8Text(src))
				}
			}
			return out
		})
	})
}

// goFieldType returns the type of a struct field from its sig. Every name
// of one field declaration ("a, b T") shares the type, so the field name
// itself is not needed.
func goFieldType(s *Session, sig, _ string) string {
	return first(goSigCached(s, "f:"+sig, func(g *goState) []string {
		return goParseSnippet(g, "package p\ntype _ struct {\n"+sig+"\n}\n", func(root *sitter.Node, src []byte) []string {
			var out []string
			walk(root, func(n *sitter.Node) bool {
				if n.Kind() == "field_declaration" {
					if t := n.ChildByFieldName("type"); t != nil {
						out = []string{t.Utf8Text(src)}
					}
					return false
				}
				return out == nil
			})
			return out
		})
	}))
}

// goVarType returns the declared (or composite-literal) type of variable
// name from a "var ..." / "const ..." sig.
func goVarType(s *Session, sig, name string) string {
	types := goSigCached(s, "v:"+sig, func(g *goState) []string {
		return goParseSnippet(g, "package p\n"+sig+"\n", func(root *sitter.Node, src []byte) []string {
			var spec *sitter.Node
			walk(root, func(n *sitter.Node) bool {
				if n.Kind() == "var_spec" || n.Kind() == "const_spec" {
					spec = n
					return false
				}
				return spec == nil
			})
			if spec == nil {
				return nil
			}
			names := fieldChildren(spec, "name")
			var out []string
			t := spec.ChildByFieldName("type")
			var vals []*sitter.Node
			if v := spec.ChildByFieldName("value"); v != nil {
				vals = namedChildren(v)
			}
			for i, id := range names {
				typ := ""
				switch {
				case t != nil:
					typ = t.Utf8Text(src)
				case len(vals) == len(names):
					typ = literalType(vals[i], src)
				}
				out = append(out, id.Utf8Text(src)+"\x00"+typ)
			}
			return out
		})
	})
	for _, nt := range types {
		if n, t, ok := strings.Cut(nt, "\x00"); ok && n == name {
			return t
		}
	}
	return ""
}

// literalType is the type of T{...} or &T{...}, else "".
func literalType(v *sitter.Node, src []byte) string {
	ptr := ""
	if v.Kind() == "unary_expression" {
		if op := v.ChildByFieldName("operator"); op != nil && op.Utf8Text(src) == "&" {
			ptr, v = "*", v.ChildByFieldName("operand")
		}
	}
	if v == nil || v.Kind() != "composite_literal" {
		return ""
	}
	if t := v.ChildByFieldName("type"); t != nil {
		return ptr + t.Utf8Text(src)
	}
	return ""
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func goSigCached(s *Session, key string, parse func(*goState) []string) []string {
	g := goStateOf(s)
	if g == nil {
		return nil
	}
	if v, ok := g.sigs[key]; ok {
		return v
	}
	v := parse(g)
	g.sigs[key] = v
	return v
}

// goParseSnippet parses code and runs extract on a clean tree.
func goParseSnippet(g *goState, code string, extract func(*sitter.Node, []byte) []string) []string {
	src := []byte(code)
	tree := g.parser.Parse(src, nil)
	if tree == nil {
		g.parser.Reset()
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil
	}
	return extract(root, src)
}

// Type text helpers.

// goDeref strips pointer stars, parentheses and spaces.
func goDeref(t string) string {
	for {
		t = strings.TrimSpace(t)
		switch {
		case strings.HasPrefix(t, "*"):
			t = t[1:]
		case strings.HasPrefix(t, "(") && matchClose(t, 0) == len(t)-1:
			t = t[1 : len(t)-1]
		default:
			return t
		}
	}
}

// goNamed splits a (possibly pointer / generic) named type into qualifier
// and name: "*store.Item" -> ("store", "Item"), "List[T]" -> ("", "List").
// Composite types (slices, maps, funcs, ...) are not named.
func goNamed(t string) (qual, name string, ok bool) {
	t = goDeref(t)
	for _, p := range []string{"[", "map[", "chan", "<-", "func", "struct", "interface"} {
		if strings.HasPrefix(t, p) {
			return "", "", false
		}
	}
	if i := strings.IndexByte(t, '['); i >= 0 {
		t = t[:i]
	}
	parts := strings.Split(t, ".")
	for _, p := range parts {
		if !isGoIdent(p) {
			return "", "", false
		}
	}
	switch len(parts) {
	case 1:
		return "", parts[0], true
	case 2:
		return parts[0], parts[1], true
	}
	return "", "", false
}

func isGoIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c < 0x80 && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// goElem returns the element type of a slice, array or map (its value).
func goElem(t string) (string, bool) {
	_, e, ok := goRange(t)
	if !ok || strings.HasPrefix(goDeref(t), "string") {
		return "", false
	}
	return e, true
}

// goRange returns the key and value types produced by ranging over t.
func goRange(t string) (key, val string, ok bool) {
	t = goDeref(t)
	switch {
	case strings.HasPrefix(t, "map["):
		end := matchClose(t, 3)
		if end < 0 {
			return "", "", false
		}
		return t[4:end], t[end+1:], true
	case strings.HasPrefix(t, "["):
		end := matchClose(t, 0)
		if end < 0 {
			return "", "", false
		}
		return "int", t[end+1:], true
	case t == "string":
		return "int", "rune", true
	}
	if e, ok := goChanElem(t); ok {
		return e, "", true
	}
	return "", "", false
}

// goChanElem returns the element type of a channel type.
func goChanElem(t string) (string, bool) {
	t = goDeref(t)
	for _, p := range []string{"<-chan", "chan<-", "chan"} {
		if rest, ok := strings.CutPrefix(t, p); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// matchClose returns the index of the bracket closing the one at open, or
// -1.
func matchClose(s string, open int) int {
	if open >= len(s) {
		return -1
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
