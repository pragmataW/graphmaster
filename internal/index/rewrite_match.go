package index

import (
	"github.com/pragmataW/graphmaster/internal/model"
)

// defIdent is what must stay equal for a definition to keep its node:
// everything but its position and parent (checked separately).
type defIdent struct {
	kind             model.NodeKind
	flags            model.DefFlags
	name, qname, sig string
}

// matchDefs pairs facts' definitions with the stored nodes they keep:
// match[j] is the index in nodes of the node definition j keeps, or -1 for
// a new definition. A definition keeps a node of equal identity (the k-th
// definition of an identity keeps the k-th such node in id order) whose
// parent is kept as its parent. Nodes not in match are removed.
//
// Matching ignores order: extractors do not emit definitions in source
// order, and definitions added by earlier in-place rewrites carry ids
// after the others. Id order matters only between definitions sharing a
// name (see sameNameOrder).
func matchDefs(nodes []storedNode, lang model.Lang, module string, f *model.Facts) []int {
	byIdent := make(map[defIdent][]int, len(nodes)) // nodes are in id order
	for i := range nodes {
		n := &nodes[i]
		k := defIdent{kind: n.kind, flags: n.flags, name: n.name, qname: n.qname, sig: n.sig}
		byIdent[k] = append(byIdent[k], i)
	}
	match := make([]int, len(f.Defs))
	for j := range f.Defs {
		d := &f.Defs[j]
		k := defIdent{kind: d.Kind, flags: d.Flags, name: d.Name, sig: d.Sig,
			qname: QName(lang, module, f.QNameParts(int32(j)))}
		match[j] = -1
		if q := byIdent[k]; len(q) > 0 {
			match[j], byIdent[k] = q[0], q[1:]
		}
	}
	// A kept definition's parent must be kept as its parent. Parents
	// precede children, so a parent's match is final when its children
	// are checked.
	for j := range f.Defs {
		k := match[j]
		if k < 0 {
			continue
		}
		var want int64
		if p := f.Defs[j].Parent; p >= 0 && int(p) < j {
			if match[p] < 0 {
				match[j] = -1
				continue
			}
			want = nodes[match[p]].id
		}
		if nodes[k].parent != want {
			match[j] = -1
		}
	}
	return match
}

// sameNameOrder reports whether, after the rewrite, the ids of the file's
// definitions sharing a name increase in source order, as in a full build:
// kept definitions keep their ids and new ones are numbered after every
// existing node.
func sameNameOrder(nodes []storedNode, match []int, f *model.Facts) bool {
	var maxID int64
	for _, n := range nodes {
		maxID = max(maxID, n.id)
	}
	last := map[string]int64{}
	for j := range f.Defs {
		id := maxID + 1 + int64(j)
		if match[j] >= 0 {
			id = nodes[match[j]].id
		}
		name := f.Defs[j].Name
		if prev, ok := last[name]; ok && id <= prev {
			return false
		}
		last[name] = id
	}
	return true
}

// identTokens returns the identifier-like tokens of s.
func identTokens(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		ident := i < len(s) && (s[i] == '_' || s[i] == '$' || s[i] >= 0x80 ||
			'a' <= s[i] && s[i] <= 'z' || 'A' <= s[i] && s[i] <= 'Z' || '0' <= s[i] && s[i] <= '9')
		switch {
		case ident && start < 0:
			start = i
		case !ident && start >= 0:
			out = append(out, s[start:i])
			start = -1
		}
	}
	return out
}
