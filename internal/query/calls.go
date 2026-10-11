package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// link is one edge of a walked call tree: id reached from parent.
type link struct {
	id, parent int64
	depth      int
	conf, line int
}

// walkCalls runs the recursive CTE behind callers (reverse) and callees:
// UNION dedupes (id, parent, depth), the depth column bounds the walk and
// LIMIT bounds its size. capped reports that the row limit was hit.
func (q *querier) walkCalls(ctx context.Context, root int64, reverse bool, depth, minConf int, kinds []model.EdgeKind, rowLimit int) (links []link, capped bool, err error) {
	next, join, extra := "e.dst", "e.src = w.id", " AND +e.dst IS NOT NULL"
	if reverse {
		next, join, extra = "e.src", "e.dst = w.id", ""
	}
	kl := edgeKindList(kinds)
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`WITH RECURSIVE walk(id, parent, depth) AS (
			SELECT ?1, 0, 0
			UNION
			SELECT %s, w.id, w.depth + 1 FROM walk w CROSS JOIN edges e ON %s
			WHERE w.depth < ?2 AND e.kind IN (%s) AND e.conf >= ?3%s
			LIMIT ?4
		) SELECT id, parent, depth FROM walk WHERE depth > 0`, next, join, kl, extra),
		root, depth, minConf, rowLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.id, &l.parent, &l.depth); err != nil {
			return nil, false, err
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	// The anchor row counts against LIMIT too.
	capped = len(links) >= rowLimit
	return links, capped, q.annotateLinks(ctx, links, reverse, minConf, kl)
}

// annotateLinks fills the best confidence and first call-site line of each
// (parent, id) pair.
func (q *querier) annotateLinks(ctx context.Context, links []link, reverse bool, minConf int, kl string) error {
	if len(links) == 0 {
		return nil
	}
	parents := map[int64]bool{}
	for _, l := range links {
		parents[l.parent] = true
	}
	ids := make([]int64, 0, len(parents))
	for id := range parents {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	col := "src"
	if reverse {
		col = "dst"
	}
	type pair struct{ src, dst int64 }
	best := map[pair][2]int{}
	err := forChunks(ids, func(chunk []int64) error {
		rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`SELECT src, dst, max(conf), min(line) FROM edges
			WHERE %s IN (%s) AND kind IN (%s) AND conf >= ? AND +dst IS NOT NULL GROUP BY src, dst`,
			col, placeholders(len(chunk)), kl), append(int64Args(chunk), minConf)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p pair
			var conf, line int
			if err := rows.Scan(&p.src, &p.dst, &conf, &line); err != nil {
				return err
			}
			best[p] = [2]int{conf, line}
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for i := range links {
		p := pair{src: links[i].parent, dst: links[i].id}
		if reverse {
			p = pair{src: links[i].id, dst: links[i].parent}
		}
		v := best[p]
		links[i].conf, links[i].line = v[0], v[1]
	}
	return nil
}

// treeRow is one rendered line of a symbol tree.
type treeRow struct {
	text string
	id   int64
}

// renderTree lays out links as an indented tree below root, children
// ordered by location. Every symbol is expanded once; repeats are marked.
// At most limit distinct symbols are rendered; it returns the rows and the
// number of distinct symbols left out.
func renderTree(root int64, links []link, syms map[int64]*sym, limit int, label func(l link, s *sym) string) ([]treeRow, int) {
	children := map[int64][]link{}
	distinct := map[int64]bool{}
	for _, l := range links {
		if syms[l.id] == nil {
			continue
		}
		children[l.parent] = append(children[l.parent], l)
		distinct[l.id] = true
	}
	for _, ls := range children {
		sort.SliceStable(ls, func(i, j int) bool {
			a, b := syms[ls[i].id], syms[ls[j].id]
			if a.Path != b.Path {
				return a.Path < b.Path
			}
			return a.Start < b.Start
		})
	}
	expanded := map[int64]bool{root: true}
	shown := map[int64]bool{}
	var rows []treeRow
	var walk func(parent int64, level int)
	walk = func(parent int64, level int) {
		for _, l := range children[parent] {
			if len(shown) >= limit && !shown[l.id] {
				continue
			}
			s := syms[l.id]
			text := strings.Repeat("  ", level) + label(l, s)
			if expanded[l.id] {
				rows = append(rows, treeRow{text: text + "  (see above)", id: l.id})
				shown[l.id] = true
				continue
			}
			expanded[l.id] = true
			shown[l.id] = true
			rows = append(rows, treeRow{text: text, id: l.id})
			walk(l.id, level+1)
		}
	}
	walk(root, 1)
	delete(distinct, root)
	delete(shown, root)
	return rows, len(distinct) - len(shown)
}

// Callers prints the tree of definitions calling symbol.
func (e *Engine) Callers(ctx context.Context, in CallsInput) (string, error) {
	return e.calls(ctx, "callers", in, true)
}

// Callees prints the tree of definitions symbol calls.
func (e *Engine) Callees(ctx context.Context, in CallsInput) (string, error) {
	return e.calls(ctx, "callees", in, false)
}

func (e *Engine) calls(ctx context.Context, tool string, in CallsInput, reverse bool) (string, error) {
	return e.run(ctx, tool, in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		s, err := q.symbolArg(ctx, o, in.Symbol)
		if s == nil || err != nil {
			return err
		}
		depth := orDefault(in.Depth, 1, 5)
		minConf := orDefault(in.MinConfidence, 50, 100)
		limit := orDefault(in.Limit, 50, 1000)
		kinds := []model.EdgeKind{model.EdgeCalls}
		if !s.Kind.IsCallable() {
			// Types, fields and variables are used rather than called.
			kinds = append(kinds, model.EdgeReferences)
		}
		links, capped, err := q.walkCalls(ctx, s.ID, reverse, depth, minConf, kinds, max(limit*4, 200))
		if err != nil {
			return err
		}
		ids := make([]int64, 0, len(links))
		for _, l := range links {
			ids = append(ids, l.id)
		}
		syms, err := q.symsByID(ctx, ids)
		if err != nil {
			return err
		}
		o.line(s.row())
		rows, rest := renderTree(s.ID, links, syms, limit, func(l link, t *sym) string {
			r := confMark(l.conf) + t.short()
			if l.line > 0 {
				r += fmt.Sprintf("  @%d", l.line)
			}
			return r
		})
		if len(rows) == 0 {
			o.linef("  no %s (min_confidence %d)", tool, minConf)
		}
		if capped && rest == 0 {
			rest = 1 // more rows exist beyond the walk limit
		}
		o.list(len(rows), func(i int) string { return rows[i].text }, rest)
		if !reverse {
			return q.unresolvedCalls(ctx, o, s.ID)
		}
		return nil
	})
}

// unresolvedCalls lists the names a definition calls that resolve to no
// indexed definition (library or dynamic calls).
func (q *querier) unresolvedCalls(ctx context.Context, o *output, id int64) error {
	rows, err := q.db.QueryContext(ctx, `SELECT DISTINCT m.name FROM edges e JOIN names m ON m.id = e.name_id
		WHERE e.src = ? AND e.dst IS NULL AND e.kind = ? ORDER BY m.name LIMIT 21`, id, int(model.EdgeCalls))
	if err != nil {
		return err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil || len(names) == 0 {
		return err
	}
	if len(names) > 20 {
		names = append(names[:20], "…")
	}
	o.line("unresolved: " + strings.Join(names, ", "))
	return nil
}
