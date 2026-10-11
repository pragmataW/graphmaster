package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Traversal tuning.
const (
	// traverseFanout is how many neighbors of one node are kept; the rest
	// are summarized as a hub line.
	traverseFanout = 12
	// traverseHubDegree marks nodes with more edges than this as hubs that
	// are shown but not expanded (loggers, base classes, utilities).
	traverseHubDegree = 64
	// traverseMaxNodes bounds the explored subgraph.
	traverseMaxNodes = 400
)

// defaultTraverseKinds excludes structural edges (contains) and file-level
// ones (imports, includes) that have no symbol endpoints.
var defaultTraverseKinds = []model.EdgeKind{
	model.EdgeCalls, model.EdgeReferences, model.EdgeExtends, model.EdgeImplements,
	model.EdgeEmbeds, model.EdgeTests, model.EdgeDeclares,
}

// tnode is a node discovered by traverse.
type tnode struct {
	id, parent int64
	kind       model.EdgeKind
	in         bool // reached through an incoming edge
	conf       int
	score      float64
	degree     int
	hidden     int  // neighbors cut by the fanout limit
	hub        bool // not expanded because of its degree
}

// adjEdge is an edge seen from the expanding node.
type adjEdge struct {
	from, to int64
	kind     model.EdgeKind
	conf     int
	in       bool
}

// Traverse explores the graph around a symbol breadth-first in Go: one
// batched IN query per level, neighbors ranked by confidence × 1/degree
// so specific links win over hubs, and hubs summarized instead of expanded.
func (e *Engine) Traverse(ctx context.Context, in TraverseInput) (string, error) {
	return e.run(ctx, "traverse", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		out, inward, err := parseDirection(in.Direction)
		if err != nil {
			return err
		}
		kinds := defaultTraverseKinds
		if len(in.EdgeKinds) > 0 {
			kinds = nil
			for _, k := range in.EdgeKinds {
				ek, err := model.ParseEdgeKind(k)
				if err != nil {
					return invalidf("%v", err)
				}
				kinds = append(kinds, ek)
			}
		}
		s, err := q.symbolArg(ctx, o, in.Start)
		if s == nil || err != nil {
			return err
		}
		maxDepth := orDefault(in.MaxDepth, 4, 8)
		minConf := orDefault(in.MinConfidence, 50, 100)
		tr := &traversal{q: q, kinds: edgeKindList(kinds), minConf: minConf, out: out, in: inward}
		if err := tr.run(ctx, s, maxDepth); err != nil {
			return err
		}
		names := make([]string, len(kinds))
		for i, k := range kinds {
			names[i] = k.String()
		}
		o.line(s.row())
		o.linef("%s via %s, depth ≤%d, min_confidence %d: %d symbols",
			dirLabel(out, inward),
			strings.Join(names, ","), maxDepth, minConf, len(tr.nodes)-1)
		tr.render(o, s.ID)
		return nil
	})
}

// kindRank breaks ties between parallel edges: the more specific
// relationship is shown (Dog extends Animal rather than references it).
func kindRank(k model.EdgeKind) int {
	switch {
	case k.IsInheritance():
		return 0
	case k == model.EdgeCalls, k == model.EdgeTests, k == model.EdgeDeclares:
		return 1
	}
	return 2
}

func dirLabel(out, in bool) string {
	switch {
	case out && in:
		return "both"
	case in:
		return "in"
	}
	return "out"
}

func parseDirection(d string) (out, in bool, err error) {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "", "out":
		return true, false, nil
	case "in":
		return false, true, nil
	case "both":
		return true, true, nil
	}
	return false, false, invalidf("direction must be out, in or both, got %q", d)
}

type traversal struct {
	q        *querier
	kinds    string
	minConf  int
	out, in  bool
	nodes    map[int64]*tnode
	children map[int64][]*tnode
	syms     map[int64]*sym
}

// locLess orders node ids by source location, so ties never depend on
// row ids (which vary between builds).
func (t *traversal) locLess(a, b int64) bool {
	x, y := t.syms[a], t.syms[b]
	switch {
	case x == nil || y == nil:
		return x != nil
	case x.Path != y.Path:
		return x.Path < y.Path
	case x.Start != y.Start:
		return x.Start < y.Start
	}
	return x.QName < y.QName
}

func (t *traversal) run(ctx context.Context, start *sym, maxDepth int) error {
	t.nodes = map[int64]*tnode{start.ID: {id: start.ID, score: 1}}
	t.children = map[int64][]*tnode{}
	t.syms = map[int64]*sym{start.ID: start}
	frontier := []int64{start.ID}
	for d := 1; d <= maxDepth && len(frontier) > 0 && len(t.nodes) < traverseMaxNodes; d++ {
		edges, err := t.adjacent(ctx, frontier)
		if err != nil {
			return err
		}
		var fresh []int64
		freshSet := map[int64]bool{}
		for _, e := range edges {
			if t.nodes[e.to] == nil && !freshSet[e.to] {
				freshSet[e.to] = true
				fresh = append(fresh, e.to)
			}
		}
		deg, err := t.degrees(ctx, fresh)
		if err != nil {
			return err
		}
		syms, err := t.q.symsByID(ctx, fresh)
		if err != nil {
			return err
		}
		for id, s := range syms {
			t.syms[id] = s
		}
		// Rank each node's new neighbors and keep the best few.
		byFrom := map[int64][]adjEdge{}
		for _, e := range edges {
			if freshSet[e.to] && t.syms[e.to] != nil {
				byFrom[e.from] = append(byFrom[e.from], e)
			}
		}
		prio := func(e adjEdge) float64 {
			return t.nodes[e.from].score * float64(e.conf) / 100 / float64(max(deg[e.to], 1))
		}
		better := func(a, b adjEdge) bool {
			if pa, pb := prio(a), prio(b); pa != pb {
				return pa > pb
			}
			if ra, rb := kindRank(a.kind), kindRank(b.kind); ra != rb {
				return ra < rb
			}
			if a.to != b.to {
				return t.locLess(a.to, b.to)
			}
			return t.locLess(a.from, b.from)
		}
		best := map[int64]adjEdge{}
		for _, from := range frontier {
			list := byFrom[from]
			sort.SliceStable(list, func(i, j int) bool { return better(list[i], list[j]) })
			seen := map[int64]bool{}
			kept := 0
			for _, e := range list {
				if seen[e.to] {
					continue
				}
				seen[e.to] = true
				if kept >= traverseFanout {
					t.nodes[from].hidden++
					continue
				}
				kept++
				if b, ok := best[e.to]; !ok || better(e, b) {
					best[e.to] = e
				}
			}
		}
		next := make([]int64, 0, len(best))
		for id := range best {
			next = append(next, id)
		}
		sort.Slice(next, func(i, j int) bool {
			return better(best[next[i]], best[next[j]])
		})
		frontier = frontier[:0:0]
		for _, id := range next {
			if len(t.nodes) >= traverseMaxNodes {
				break
			}
			e := best[id]
			n := &tnode{id: id, parent: e.from, kind: e.kind, in: e.in, conf: e.conf,
				score: prio(e), degree: deg[id], hub: deg[id] > traverseHubDegree}
			t.nodes[id] = n
			t.children[e.from] = append(t.children[e.from], n)
			if !n.hub {
				frontier = append(frontier, id)
			}
		}
	}
	return nil
}

// adjacent loads the edges of the frontier in the traversal direction(s).
func (t *traversal) adjacent(ctx context.Context, frontier []int64) ([]adjEdge, error) {
	var out []adjEdge
	load := func(col, other string, in bool) error {
		return forChunks(frontier, func(chunk []int64) error {
			rows, err := t.q.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s, %s, kind, max(conf) FROM edges
				WHERE %s IN (%s) AND kind IN (%s) AND +dst IS NOT NULL AND conf >= ? AND src <> dst
				GROUP BY %s, %s, kind`, col, other, col, placeholders(len(chunk)), t.kinds, col, other),
				append(int64Args(chunk), t.minConf)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				e := adjEdge{in: in}
				var kind int
				if err := rows.Scan(&e.from, &e.to, &kind, &e.conf); err != nil {
					return err
				}
				e.kind = model.EdgeKind(uint8(kind))
				out = append(out, e)
			}
			return rows.Err()
		})
	}
	if t.out {
		if err := load("src", "dst", false); err != nil {
			return nil, err
		}
	}
	if t.in {
		if err := load("dst", "src", true); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].from != out[j].from {
			return out[i].from < out[j].from
		}
		return out[i].to < out[j].to
	})
	return out, nil
}

// degrees counts the edges of ids in the traversal direction(s).
func (t *traversal) degrees(ctx context.Context, ids []int64) (map[int64]int, error) {
	deg := map[int64]int{}
	count := func(col string) error {
		return forChunks(ids, func(chunk []int64) error {
			rows, err := t.q.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s, count(*) FROM edges
				WHERE %s IN (%s) AND kind IN (%s) AND +dst IS NOT NULL GROUP BY %s`,
				col, col, placeholders(len(chunk)), t.kinds, col), int64Args(chunk)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var n int
				if err := rows.Scan(&id, &n); err != nil {
					return err
				}
				deg[id] += n
			}
			return rows.Err()
		})
	}
	if t.out {
		if err := count("src"); err != nil {
			return nil, err
		}
	}
	if t.in {
		if err := count("dst"); err != nil {
			return nil, err
		}
	}
	return deg, nil
}

// render prints the discovered tree, best-ranked branches first.
func (t *traversal) render(o *output, root int64) {
	total := len(t.nodes) - 1
	shown := 0
	var walk func(id int64, level int) bool
	walk = func(id int64, level int) bool {
		kids := t.children[id]
		sort.SliceStable(kids, func(i, j int) bool {
			a, b := kids[i], kids[j]
			if a.score != b.score {
				return a.score > b.score
			}
			if ra, rb := kindRank(a.kind), kindRank(b.kind); ra != rb {
				return ra < rb
			}
			return t.locLess(a.id, b.id)
		})
		ind := strings.Repeat("  ", level)
		for _, n := range kids {
			s := t.syms[n.id]
			if s == nil {
				continue
			}
			arrow := "→"
			if n.in {
				arrow = "←"
			}
			r := fmt.Sprintf("%s%s%s %s%s", ind, arrow, n.kind, confMark(n.conf), s.short())
			if n.hub {
				r += fmt.Sprintf("  [hub: %d edges, not expanded]", n.degree)
			}
			if !o.line(r) {
				return false
			}
			shown++
			if !walk(n.id, level+1) {
				return false
			}
		}
		if h := t.nodes[id].hidden; h > 0 {
			return o.linef("%s… +%d more neighbors (hub)", ind, h)
		}
		return true
	}
	walk(root, 1)
	if total == 0 {
		o.line("  no neighbors")
	}
	o.more(total - shown)
}
