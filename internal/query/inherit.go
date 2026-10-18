package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// inheritKinds are the type-hierarchy edges.
var inheritKinds = []model.EdgeKind{model.EdgeExtends, model.EdgeImplements, model.EdgeEmbeds}

// hierEdge is one inheritance edge seen from the node being expanded:
// other is the related type (0 when unresolved, then name says which).
type hierEdge struct {
	other int64
	kind  model.EdgeKind
	conf  int
	name  string
}

// hierarchyLevel loads the inheritance edges of ids: supertypes (up) or
// subtypes (down), deduplicated and without self-loops (partial types).
func (q *querier) hierarchyLevel(ctx context.Context, ids []int64, up bool) (map[int64][]hierEdge, error) {
	from, to := "e.dst", "e.src"
	if up {
		from, to = "e.src", "e.dst"
	}
	out := map[int64][]hierEdge{}
	type key struct {
		from, other int64
		kind        model.EdgeKind
		name        string
	}
	seen := map[key]int{}
	err := forChunks(ids, func(chunk []int64) error {
		rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s, coalesce(%s, 0), e.kind, e.conf, coalesce(m.name, '')
			FROM edges e LEFT JOIN names m ON m.id = e.name_id
			WHERE %s IN (%s) AND e.kind IN (%s)`, from, to, from, placeholders(len(chunk)), edgeKindList(inheritKinds)),
			int64Args(chunk)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				f    int64
				h    hierEdge
				kind int
			)
			if err := rows.Scan(&f, &h.other, &kind, &h.conf, &h.name); err != nil {
				return err
			}
			h.kind = model.EdgeKind(uint8(kind))
			if h.other == f || (!up && h.other == 0) {
				continue
			}
			if h.other != 0 {
				h.name = ""
			}
			k := key{f, h.other, h.kind, h.name}
			if i, dup := seen[k]; dup {
				if h.conf > out[f][i].conf {
					out[f][i].conf = h.conf
				}
				continue
			}
			seen[k] = len(out[f])
			out[f] = append(out[f], h)
		}
		return rows.Err()
	})
	return out, err
}

// downLabel names an edge seen from the supertype.
var downLabel = map[model.EdgeKind]string{
	model.EdgeExtends:    "extended by",
	model.EdgeImplements: "implemented by",
	model.EdgeEmbeds:     "embedded by",
}

// Inheritance prints the type hierarchy around a type: supertypes (up),
// subtypes (down) or both.
func (e *Engine) Inheritance(ctx context.Context, in InheritanceInput) (string, error) {
	return e.run(ctx, "inheritance", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		dir := strings.ToLower(strings.TrimSpace(in.Direction))
		switch dir {
		case "":
			dir = "both"
		case "up", "down", "both":
		default:
			return invalidf("direction must be up, down or both, got %q", in.Direction)
		}
		var ext externalUses
		if dir != "up" {
			// Subtypes of a library type: its unresolved extends/implements.
			ext = externalUses{kinds: []model.EdgeKind{model.EdgeExtends, model.EdgeImplements, model.EdgeEmbeds},
				verb: "extended or implemented by", limit: 100}
		}
		s, err := q.symbolArgExt(ctx, o, in.Symbol, ext)
		if s == nil || err != nil {
			return err
		}
		depth := orDefault(in.Depth, 3, 10)
		o.line(s.row())
		var rows []string
		for _, up := range []bool{true, false} {
			if (up && dir == "down") || (!up && dir == "up") {
				continue
			}
			part, err := q.hierarchyRows(ctx, s.ID, up, depth)
			if err != nil {
				return err
			}
			head := "supertypes:"
			if !up {
				head = "subtypes:"
			}
			if len(part) == 0 {
				head += " none"
			}
			rows = append(rows, head)
			rows = append(rows, part...)
		}
		o.list(len(rows), func(i int) string { return rows[i] }, 0)
		return nil
	})
}

// hierarchyRows walks the hierarchy level by level (one batched query per
// level) and renders it as an indented tree.
func (q *querier) hierarchyRows(ctx context.Context, root int64, up bool, depth int) ([]string, error) {
	edges := map[int64][]hierEdge{}
	frontier := []int64{root}
	visited := map[int64]bool{root: true}
	for level := 0; level < depth && len(frontier) > 0; level++ {
		lv, err := q.hierarchyLevel(ctx, frontier, up)
		if err != nil {
			return nil, err
		}
		var next []int64
		for _, id := range frontier {
			edges[id] = lv[id]
			for _, h := range lv[id] {
				if h.other != 0 && !visited[h.other] {
					visited[h.other] = true
					next = append(next, h.other)
				}
			}
		}
		frontier = next
	}
	ids := make([]int64, 0, len(visited))
	for id := range visited {
		ids = append(ids, id)
	}
	syms, err := q.symsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	var rows []string
	expanded := map[int64]bool{root: true}
	var walk func(id int64, level int)
	walk = func(id int64, level int) {
		list := edges[id]
		sort.SliceStable(list, func(i, j int) bool {
			a, b := syms[list[i].other], syms[list[j].other]
			switch {
			case a == nil || b == nil:
				return a != nil
			case a.Path != b.Path:
				return a.Path < b.Path
			}
			return a.Start < b.Start
		})
		for _, h := range list {
			label := h.kind.String()
			if !up {
				label = downLabel[h.kind]
			}
			ind := strings.Repeat("  ", level)
			t := syms[h.other]
			if t == nil {
				rows = append(rows, fmt.Sprintf("%s%s ? %s (unresolved)", ind, label, h.name))
				continue
			}
			r := ind + label + " " + confMark(h.conf) + t.short()
			if expanded[t.ID] {
				rows = append(rows, r+"  (see above)")
				continue
			}
			expanded[t.ID] = true
			rows = append(rows, r)
			walk(t.ID, level+1)
		}
	}
	walk(root, 1)
	return rows, nil
}
