package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Impact bounds; the defaults match the impact query tool.
const (
	defaultImpactDepth   = 3
	maxImpactDepth       = 6
	defaultImpactMinConf = 30
	impactWalkLimit      = 5000
)

// impactKinds are the dependency edges followed backwards: anything that
// may break when its target changes (same set as the impact tool).
var impactKinds = []model.EdgeKind{
	model.EdgeCalls, model.EdgeReferences, model.EdgeExtends, model.EdgeImplements,
	model.EdgeEmbeds, model.EdgeTests, model.EdgeDeclares,
}

// impact answers /api/impact?id=|path=&depth=&min_conf=: the seeds (a
// symbol with its nested definitions, or every definition of a file) and
// the reverse transitive closure over dependency edges, closest first.
func (s *Server) impact(ctx context.Context, d *data, r *http.Request, banner string) (any, error) {
	q := r.URL.Query()
	hasID, path := strings.TrimSpace(q.Get("id")) != "", normPath(q.Get("path"))
	if hasID == (path != "") {
		return nil, badRequest("pass exactly one of id or path")
	}
	depth, err := intParam(r, "depth", defaultImpactDepth, maxImpactDepth)
	if err != nil {
		return nil, err
	}
	minConf, err := intParam(r, "min_conf", defaultImpactMinConf, 100)
	if err != nil {
		return nil, err
	}
	var seeds []int64
	if hasID {
		id, err := idParam(r)
		if err != nil {
			return nil, err
		}
		if _, err := d.symByID(ctx, id); err != nil {
			return nil, err
		}
		seeds, err = queryInt64s(ctx, d, `WITH RECURSIVE sub(id) AS (
			SELECT ? UNION SELECT n.id FROM sub CROSS JOIN nodes n ON n.parent_id = sub.id)
			SELECT id FROM sub ORDER BY id`, id)
		if err != nil {
			return nil, err
		}
	} else {
		info, err := d.fileInfo(ctx, path)
		if err != nil {
			return nil, err
		}
		if seeds, err = queryInt64s(ctx, d, `SELECT id FROM nodes WHERE file_id = ? ORDER BY id`, info.ID); err != nil {
			return nil, err
		}
	}

	reached, capped, err := d.reverseClosure(ctx, seeds, depth, minConf)
	if err != nil {
		return nil, err
	}
	all := append(append([]int64{}, seeds...), keysByDepth(reached)...)
	syms, err := d.symsByID(ctx, all)
	if err != nil {
		return nil, err
	}

	res := ImpactResponse{Seeds: []string{}, Depth: depth, MinConf: minConf, Capped: capped}
	files := map[int64]bool{}
	for id := range reached {
		if sym := syms[id]; sym != nil {
			res.Affected++
			files[sym.FileID] = true
			if sym.Test {
				res.Tests++
			}
		}
	}
	res.Files = len(files)

	g := newGraph(d.max)
	for _, id := range all {
		sym := syms[id]
		if sym == nil {
			continue
		}
		nd := symNode(sym, "")
		cls := "impact"
		if dep, ok := reached[id]; ok {
			nd.Depth = dep
		} else {
			cls = "seed"
		}
		if !g.node(nd, symClasses(sym, cls)...) {
			break
		}
		if cls == "seed" {
			res.Seeds = append(res.Seeds, nd.ID)
		}
	}
	if !g.full() {
		edges, err := d.edgesAmong(ctx, all, impactKinds)
		if err != nil {
			return nil, err
		}
		g.addSymEdges(edges)
	}
	res.GraphResponse = g.response(banner)
	return res, nil
}

// reverseClosure walks impactKinds edges backwards from seeds (recursive
// CTE with UNION dedupe and a row LIMIT) and returns every non-seed
// definition reached with its minimal depth.
func (d *data) reverseClosure(ctx context.Context, seeds []int64, depth, minConf int) (map[int64]int, bool, error) {
	out := map[int64]int{}
	if len(seeds) == 0 {
		return out, false, nil
	}
	seedJSON, err := json.Marshal(seeds)
	if err != nil {
		return nil, false, err
	}
	kinds := make([]string, len(impactKinds))
	for i, k := range impactKinds {
		kinds[i] = fmt.Sprint(int(k))
	}
	limit := len(seeds) + impactWalkLimit
	rows, err := d.db.QueryContext(ctx, `WITH RECURSIVE walk(id, depth) AS (
			SELECT value, 0 FROM json_each(?1)
			UNION
			SELECT e.src, w.depth + 1 FROM walk w CROSS JOIN edges e ON e.dst = w.id
			WHERE w.depth < ?2 AND e.kind IN (`+strings.Join(kinds, ",")+`) AND e.conf >= ?3
			LIMIT ?4
		) SELECT id, depth FROM walk`, string(seedJSON), depth, minConf, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	isSeed := make(map[int64]bool, len(seeds))
	for _, id := range seeds {
		isSeed[id] = true
	}
	n := 0
	for rows.Next() {
		var id int64
		var dep int
		if err := rows.Scan(&id, &dep); err != nil {
			return nil, false, err
		}
		n++
		if isSeed[id] {
			continue
		}
		if old, ok := out[id]; !ok || dep < old {
			out[id] = dep
		}
	}
	return out, n >= limit, rows.Err()
}

// keysByDepth orders the reached ids by depth, then id.
func keysByDepth(m map[int64]int) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]] != m[out[j]] {
			return m[out[i]] < m[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
