package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// fileHit is a file whose path matched a search.
type fileHit struct {
	Path   string
	Lang   model.Lang
	LOC    int
	Status model.FileStatus
}

func (h fileHit) row() string {
	r := fmt.Sprintf("file %s  %s, %d loc", h.Path, h.Lang, h.LOC)
	if h.Status != model.StatusOK {
		r += ", " + h.Status.String()
	}
	return r
}

// searchFiles finds files whose path contains term (ASCII case-insensitive),
// shortest paths first.
func (q *querier) searchFiles(ctx context.Context, term, pathPrefix string, limit int) ([]fileHit, error) {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	rows, err := q.db.QueryContext(ctx, `SELECT path, lang, loc, status FROM files
		WHERE path LIKE ? ESCAPE '\' AND path LIKE ? ESCAPE '\'
		ORDER BY length(path), path LIMIT ?`,
		"%"+esc.Replace(term)+"%", esc.Replace(pathPrefix)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fileHit
	for rows.Next() {
		var h fileHit
		var lang, status int
		if err := rows.Scan(&h.Path, &lang, &h.LOC, &status); err != nil {
			return nil, err
		}
		h.Lang, h.Status = model.Lang(uint8(lang)), model.FileStatus(uint8(status))
		out = append(out, h)
	}
	return out, rows.Err()
}

// Search runs a full-text (token prefix) and trigram (substring) search
// over symbol names, plus a substring match over file paths.
func (e *Engine) Search(ctx context.Context, in SearchInput) (string, error) {
	return e.run(ctx, "search", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		query := strings.TrimSpace(in.Query)
		if query == "" {
			return invalidf("query is required")
		}
		limit := orDefault(in.Limit, 30, 500)
		f := &symFilter{pathPrefix: normPathPrefix(in.PathPrefix)}
		wantFiles, wantSyms := true, true
		if len(in.Kinds) > 0 {
			wantFiles, wantSyms = false, false
			for _, k := range in.Kinds {
				if strings.EqualFold(strings.TrimSpace(k), "file") {
					wantFiles = true
					continue
				}
				if err := f.addKind(k); err != nil {
					return err
				}
				wantSyms = true
			}
		}

		var files []fileHit
		if wantFiles && !strings.ContainsAny(query, " \t") {
			var err error
			if files, err = q.searchFiles(ctx, query, f.pathPrefix, limit+1); err != nil {
				return err
			}
		}
		var syms []ranked
		if wantSyms {
			var err error
			if syms, err = q.rankSymbols(ctx, query, false, f); err != nil {
				return err
			}
		}
		if len(files) == 0 && len(syms) == 0 {
			o.linef("nothing matches %q", query)
			return nil
		}

		// Files take at most a quarter of the limit when symbols compete.
		fileCap := len(files)
		if len(syms) > 0 {
			fileCap = min(fileCap, max(5, limit/4))
		}
		fileCap = min(fileCap, limit)
		symCap := min(len(syms), limit-fileCap)

		if symCap > 0 {
			o.linef("symbols (%d%s):", len(syms), plusIf(len(syms) >= candidatePool(f)))
			o.list(symCap, func(i int) string { return syms[i].row() }, len(syms)-symCap)
		}
		if fileCap > 0 && !o.full {
			o.linef("files (%d%s):", min(len(files), limit), plusIf(len(files) > limit))
			o.list(fileCap, func(i int) string { return files[i].row() }, len(files)-fileCap)
		}
		return nil
	})
}

// plusIf marks a count that is a lower bound.
func plusIf(b bool) string {
	if b {
		return "+"
	}
	return ""
}
