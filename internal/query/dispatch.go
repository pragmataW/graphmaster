package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/resolve"
)

// Virtual dispatch. A call through an interface or base-class method may
// land in any implementation, so the callers of a concrete method include
// the callers of the methods it implements or overrides, marked
// "~via <method>":
//
//   - Go: the derived method-level implements rows (resolve.DispatchQual)
//     link a concrete method to the interface methods it implements;
//   - languages with explicit inheritance (Java, C#, TypeScript, PHP,
//     Python, C++): a method of type T is linked to the method of the same
//     name in each supertype T extends or implements (resolved inheritance
//     edges), and transitively further up.
//
// Callees list the implementations of interface and base methods they
// call (dispatch down), capped at dispatchShown.

// dispatchShown is how many implementations a callee line lists.
const dispatchShown = 5

// virtualConf is the confidence shown for definitions reached only
// through virtual dispatch.
var virtualConf = int(resolve.ConfImplicit)

// dispatchUpSQL returns the recursive-CTE members that step from a method
// in walk w to the methods it may be called through, at the same depth.
// sel renders the selected columns given the reached id expression; the
// query's ?3 parameter is the minimum confidence.
func dispatchUpSQL(sel func(id string) string) string {
	const conf = "?3"
	return fmt.Sprintf(`
		UNION
		SELECT %s FROM walk w CROSS JOIN edges e ON e.src = w.id
		WHERE e.kind = %d AND e.qual = '%s' AND e.conf >= %s
		UNION
		SELECT %s FROM walk w CROSS JOIN nodes m ON m.id = w.id
			CROSS JOIN edges h ON h.src = m.parent_id AND h.kind IN (%d, %d)
			CROSS JOIN nodes b ON b.parent_id = h.dst AND b.name_id = m.name_id
		WHERE m.kind = %d AND b.kind = %d AND h.name_id IS NOT NULL AND h.conf >= %s`,
		sel("e.dst"), model.EdgeImplements, resolve.DispatchQual, conf,
		sel("b.id"), model.EdgeExtends, model.EdgeImplements, model.KindMethod, model.KindMethod, conf)
}

// foldDispatch removes the dispatch links of a reverse walk (an interface
// or base method reached from the method below it) and hangs their
// callers below the nearest definition reached normally, recording the
// method they were reached through. Dispatch nodes that are also reached
// normally keep their callers.
func foldDispatch(links []link) []link {
	normal := map[int64]bool{}
	up := map[int64]int64{} // dispatch node -> the method it was reached from
	for _, l := range links {
		if l.via {
			if _, ok := up[l.id]; !ok {
				up[l.id] = l.parent
			}
		} else {
			normal[l.id] = true
		}
	}
	type pair struct{ id, parent int64 }
	at := map[pair]int{} // index in out
	var out []link
	for _, l := range links {
		if l.via {
			continue
		}
		p, through := l.parent, int64(0)
		for i := 0; i < 32 && !normal[p]; i++ {
			next, ok := up[p]
			if !ok {
				break
			}
			if through == 0 {
				through = p
			}
			p = next
		}
		if through != 0 {
			l.parent, l.through = p, through
		}
		// A definition reached both directly and through dispatch, or
		// through several methods, is listed once (directly if it can).
		k := pair{l.id, l.parent}
		if i, dup := at[k]; dup {
			if out[i].through != 0 && l.through == 0 {
				out[i] = l
			}
			continue
		}
		at[k] = len(out)
		out = append(out, l)
	}
	return out
}

// implementations returns, for each of ids that is an interface or base
// method, the methods implementing or overriding it (Go dispatch rows and
// same-named methods of direct subtypes), sorted by location.
func (q *querier) implementations(ctx context.Context, ids []int64) (map[int64][]*sym, error) {
	found := map[int64][]int64{}
	add := func(m, impl int64) {
		for _, x := range found[m] {
			if x == impl {
				return
			}
		}
		found[m] = append(found[m], impl)
	}
	err := forChunks(ids, func(chunk []int64) error {
		rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`SELECT e.dst, e.src FROM edges e
			WHERE e.dst IN (%[1]s) AND e.kind = %[2]d AND e.qual = '%[3]s'
			UNION
			SELECT m.id, b.id FROM nodes m CROSS JOIN edges h ON h.dst = m.parent_id AND h.kind IN (%[4]d, %[5]d)
				CROSS JOIN nodes b ON b.parent_id = h.src AND b.name_id = m.name_id
			WHERE m.id IN (%[1]s) AND m.kind = %[6]d AND b.kind = %[6]d AND h.name_id IS NOT NULL`,
			placeholders(len(chunk)), model.EdgeImplements, resolve.DispatchQual,
			model.EdgeExtends, model.EdgeImplements, model.KindMethod),
			append(int64Args(chunk), int64Args(chunk)...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m, impl int64
			if err := rows.Scan(&m, &impl); err != nil {
				return err
			}
			add(m, impl)
		}
		return rows.Err()
	})
	if err != nil || len(found) == 0 {
		return nil, err
	}
	var all []int64
	for _, impls := range found {
		all = append(all, impls...)
	}
	syms, err := q.symsByID(ctx, all)
	if err != nil {
		return nil, err
	}
	out := map[int64][]*sym{}
	for m, impls := range found {
		for _, id := range impls {
			if s := syms[id]; s != nil {
				out[m] = append(out[m], s)
			}
		}
		sortByLocation(out[m])
	}
	return out, nil
}

// implLine renders the implementations of a called method below its tree
// row (indent is the row's indentation).
func implLine(indent string, impls []*sym) string {
	names := make([]string, 0, dispatchShown)
	for i, s := range impls {
		if i == dispatchShown {
			break
		}
		names = append(names, s.displayQName())
	}
	r := indent + "  ~implemented by " + strings.Join(names, ", ")
	if n := len(impls) - len(names); n > 0 {
		r += fmt.Sprintf(" (+%d more)", n)
	}
	return r
}

// withImplementations inserts, below every row of a callee tree whose
// method is an interface or base method, the methods it dispatches to.
func (q *querier) withImplementations(ctx context.Context, rows []treeRow) ([]treeRow, error) {
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	impls, err := q.implementations(ctx, ids)
	if err != nil || len(impls) == 0 {
		return rows, err
	}
	out := make([]treeRow, 0, len(rows)+len(impls))
	for _, r := range rows {
		out = append(out, r)
		if is := impls[r.id]; len(is) > 0 && !strings.HasSuffix(r.text, "(see above)") {
			indent := r.text[:len(r.text)-len(strings.TrimLeft(r.text, " "))]
			out = append(out, treeRow{text: implLine(indent, is)})
		}
	}
	return out, nil
}
