package ui

import (
	"strconv"
	"strings"
)

// graph accumulates the elements of one response under the element cap.
// Nodes must be added before the edges and children that refer to them.
type graph struct {
	max       int
	elems     []Element
	ids       map[string]bool
	truncated bool
}

func newGraph(max int) *graph {
	return &graph{max: max, elems: []Element{}, ids: map[string]bool{}}
}

// has reports whether an element id was added.
func (g *graph) has(id string) bool { return g.ids[id] }

// full reports that no further element fits.
func (g *graph) full() bool { return len(g.elems) >= g.max }

func (g *graph) add(group string, d ElemData, classes []string) bool {
	if g.ids[d.ID] {
		return true
	}
	if g.full() {
		g.truncated = true
		return false
	}
	g.ids[d.ID] = true
	g.elems = append(g.elems, Element{Group: group, Data: d, Classes: strings.Join(classes, " ")})
	return true
}

// node adds a node; a parent that is not in the graph is dropped so the
// element stays valid. It returns false when the cap was reached.
func (g *graph) node(d ElemData, classes ...string) bool {
	if d.Parent != "" && !g.ids[d.Parent] {
		d.Parent = ""
	}
	return g.add("nodes", d, classes)
}

// edge adds an edge between two present nodes; edges with a missing end
// are skipped silently. It returns false only when the cap was reached.
func (g *graph) edge(d ElemData, classes ...string) bool {
	if !g.ids[d.Source] || !g.ids[d.Target] {
		return true
	}
	return g.add("edges", d, classes)
}

// response packs the elements with a banner.
func (g *graph) response(banner string) GraphResponse {
	return GraphResponse{Banner: banner, Elements: g.elems, Truncated: g.truncated}
}

func symID(id int64) string  { return "n" + strconv.FormatInt(id, 10) }
func fileID(id int64) string { return "f" + strconv.FormatInt(id, 10) }

// symNode is the element data of a symbol; parent is the compound parent.
func symNode(s *Symbol, parent string) ElemData {
	return ElemData{
		ID: symID(s.ID), Parent: parent, Label: s.Name, Kind: s.Kind, Ref: s.ID,
		QName: s.QName, Path: s.Path, Lang: s.Lang, Start: s.Start, End: s.End,
	}
}

// symClasses are the style classes of a symbol node.
func symClasses(s *Symbol, extra ...string) []string {
	out := append([]string{"sym", "k-" + s.Kind}, extra...)
	if s.Test {
		out = append(out, "test")
	}
	if s.Exported {
		out = append(out, "exported")
	}
	return out
}

// edgeData is the element data of an aggregated symbol edge.
func edgeData(e edgeAgg) ElemData {
	return ElemData{
		ID:     "e" + strconv.FormatInt(e.src, 10) + "-" + strconv.FormatInt(e.dst, 10) + "-" + e.kind.String(),
		Source: symID(e.src), Target: symID(e.dst), Kind: e.kind.String(), Conf: e.conf, Count: e.count,
	}
}

// edgeClasses marks low-confidence edges like the query tools' "~".
func edgeClasses(e edgeAgg) []string {
	out := []string{"k-" + e.kind.String()}
	if e.conf < 80 {
		out = append(out, "weak")
	}
	return out
}

// addSymEdges adds aggregated symbol edges until the cap.
func (g *graph) addSymEdges(edges []edgeAgg) {
	for _, e := range edges {
		if !g.edge(edgeData(e), edgeClasses(e)...) {
			return
		}
	}
}
