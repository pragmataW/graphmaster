package ui

import (
	"context"
	"net/http"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Search bounds.
const (
	defaultSearchLimit = 20
	maxSearchLimit     = 100
	maxFileHits        = 10
)

// search answers /api/search?q=&limit=: exact name matches first, then
// full-text token prefix matches, then substring matches; plus files
// whose path contains q.
func (s *Server) search(ctx context.Context, d *data, r *http.Request, banner string) (any, error) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return nil, badRequest("q is required")
	}
	limit, err := intParam(r, "limit", defaultSearchLimit, maxSearchLimit)
	if err != nil {
		return nil, err
	}

	var ids []int64
	seen := map[int64]bool{}
	addIDs := func(more []int64) {
		for _, id := range more {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	exact, err := queryInt64s(ctx, d, `SELECT n.id FROM nodes n
		WHERE n.name_id = (SELECT id FROM names WHERE name = ?) ORDER BY n.id LIMIT ?`, q, limit+1)
	if err != nil {
		return nil, err
	}
	addIDs(exact)
	if len(ids) <= limit {
		fts, err := d.st.SearchSymbols(ctx, q, limit+1)
		if err != nil {
			return nil, err
		}
		addIDs(fts)
	}
	if len(ids) <= limit {
		sub, err := d.st.SearchSubstring(ctx, q, limit+1)
		if err != nil {
			return nil, err
		}
		addIDs(sub)
	}

	res := SearchResponse{Banner: banner, Query: q, Symbols: []Symbol{}, More: len(ids) > limit}
	ids = ids[:min(len(ids), limit)]
	syms, err := d.symsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if sym := syms[id]; sym != nil {
			res.Symbols = append(res.Symbols, *sym)
		}
	}
	if res.Files, err = d.searchFiles(ctx, q); err != nil {
		return nil, err
	}
	return res, nil
}

// searchFiles finds files whose path contains term, shortest paths first.
func (d *data) searchFiles(ctx context.Context, term string) ([]FileInfo, error) {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	rows, err := d.db.QueryContext(ctx, `SELECT f.id, f.path, f.lang, f.loc, f.status,
			(SELECT count(*) FROM nodes n WHERE n.file_id = f.id)
		FROM files f WHERE f.path LIKE ? ESCAPE '\' ORDER BY length(f.path), f.path LIMIT ?`,
		"%"+esc.Replace(term)+"%", maxFileHits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileInfo{}
	for rows.Next() {
		var f FileInfo
		var lang, status int
		if err := rows.Scan(&f.ID, &f.Path, &lang, &f.LOC, &status, &f.Symbols); err != nil {
			return nil, err
		}
		f.Lang = model.Lang(uint8(lang)).String()
		f.Status = model.FileStatus(uint8(status)).String()
		out = append(out, f)
	}
	return out, rows.Err()
}

func queryInt64s(ctx context.Context, d *data, q string, args ...any) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
