package query

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// externalUses describes which unresolved edges answer a tool for a name
// that has no definition in the repository (a library function such as
// useState, or an interface from a dependency such as
// Psr\Http\Server\MiddlewareInterface).
type externalUses struct {
	kinds []model.EdgeKind
	verb  string // "uses", "implemented or extended by"
	limit int
}

// symbolArgExt is symbolArg with a fallback for library names: when arg
// names no definition but unresolved edges of the given kinds use it, it
// lists them and returns nil, as symbolArg does after writing a message.
func (q *querier) symbolArgExt(ctx context.Context, o *output, arg string, ext externalUses) (*sym, error) {
	ss, err := q.resolveSymbol(ctx, arg)
	if err != nil {
		return nil, err
	}
	switch len(ss) {
	case 1:
		return ss[0], nil
	case 0:
		found, err := q.externalUsers(ctx, o, arg, ext)
		if found || err != nil {
			return nil, err
		}
		return nil, q.notFound(ctx, o, arg)
	}
	if s := codeOverTests(ss); s != nil {
		return s, nil
	}
	writeAmbiguous(o, arg, ss)
	return nil, nil
}

// externalUsers writes the definitions whose unresolved edges name arg's
// last segment, and reports whether there were any.
func (q *querier) externalUsers(ctx context.Context, o *output, arg string, ext externalUses) (bool, error) {
	name := lastSegment(strings.TrimSpace(arg))
	if name == "" || len(ext.kinds) == 0 {
		return false, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ext.kinds)), ",")
	args := []any{name}
	for _, k := range ext.kinds {
		args = append(args, int(k))
	}
	limit := max(ext.limit, 1)
	args = append(args, limit+1)
	// One row per using definition (or per file for top-level uses).
	const from = ` FROM edges e JOIN names m ON m.id = e.name_id
		WHERE m.name = ? AND e.dst IS NULL AND e.kind IN (`
	rows, err := q.db.QueryContext(ctx, `SELECT coalesce(e.src, 0) AS s, f.path, group_concat(e.line)
		FROM edges e JOIN names m ON m.id = e.name_id JOIN files f ON f.id = e.file_id
		WHERE m.name = ? AND e.dst IS NULL AND e.kind IN (`+ph+`)
		GROUP BY s, e.file_id ORDER BY f.path, min(e.line) LIMIT ?`, args...)
	if err != nil {
		return false, err
	}
	type use struct {
		src   int64
		path  string
		lines string
	}
	var uses []use
	for rows.Next() {
		var u use
		if err := rows.Scan(&u.src, &u.path, &u.lines); err != nil {
			rows.Close()
			return false, err
		}
		uses = append(uses, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(uses) == 0 {
		return false, err
	}
	more := 0
	if len(uses) > limit {
		uses = uses[:limit]
		var total int
		if err := q.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1`+from+ph+`)
			GROUP BY coalesce(e.src, 0), e.file_id)`, args[:len(args)-1]...).Scan(&total); err != nil {
			return false, err
		}
		more = total - limit
	}
	ids := make([]int64, 0, len(uses))
	for _, u := range uses {
		if u.src != 0 {
			ids = append(ids, u.src)
		}
	}
	syms, err := q.symsByID(ctx, ids)
	if err != nil {
		return false, err
	}
	o.linef("external symbol %q: not defined in this repository; %s:", name, ext.verb)
	o.list(len(uses), func(i int) string {
		u := uses[i]
		lines := sortedLines(u.lines)
		if s := syms[u.src]; s != nil {
			return fmt.Sprintf("  %s  @%s", s.short(), lines)
		}
		return fmt.Sprintf("  %s  (top level) @%s", u.path, lines)
	}, more)
	return true, nil
}

// sortedLines orders a group_concat list of line numbers: "14,13" -> "13,14".
func sortedLines(csv string) string {
	parts := strings.Split(csv, ",")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	nums = slices.Compact(nums)
	out := make([]string, len(nums))
	for i, n := range nums {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, ",")
}
