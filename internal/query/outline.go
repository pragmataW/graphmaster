package query

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pragmataW/graphmaster/internal/store"
)

// FileOutline lists a file's definitions as a tree with line ranges and
// signatures.
func (e *Engine) FileOutline(ctx context.Context, in FileOutlineInput) (string, error) {
	return e.run(ctx, "file_outline", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		path := normPathPrefix(in.Path)
		if path == "" {
			return invalidf("path is required")
		}
		depth := orDefault(in.Depth, 2, 20)
		private := in.IncludePrivate == nil || *in.IncludePrivate
		f, err := q.st.FileByPath(ctx, path)
		if errors.Is(err, store.ErrNotFound) {
			return q.fileNotFound(ctx, o, path)
		}
		if err != nil {
			return err
		}
		ss, err := q.symsWhere(ctx, `n.file_id = ? ORDER BY n.start_byte, n.id`, f.ID)
		if err != nil {
			return err
		}
		head := fmt.Sprintf("%s  %s, %d loc, %d symbols", f.Path, f.Lang, f.LOC, len(ss))
		if !f.Status.Indexed() {
			head += " (" + f.Status.String() + ", not parsed)"
		}
		o.line(head)

		children := map[int64][]*sym{}
		byID := map[int64]bool{}
		for _, s := range ss {
			byID[s.ID] = true
		}
		var roots []*sym
		for _, s := range ss {
			if s.ParentID != 0 && byID[s.ParentID] {
				children[s.ParentID] = append(children[s.ParentID], s)
			} else {
				roots = append(roots, s)
			}
		}
		var rows []string
		var walk func(list []*sym, level int)
		walk = func(list []*sym, level int) {
			for _, s := range list {
				if !private && !s.Exported() {
					continue
				}
				r := fmt.Sprintf("%s%s %s  %d-%d", strings.Repeat("  ", level), s.Kind, s.Name, s.Start, s.End)
				if s.Sig != "" {
					r += "  " + s.Sig
				}
				rows = append(rows, r)
				if level+1 < depth {
					walk(children[s.ID], level+1)
				}
			}
		}
		walk(roots, 0)
		o.list(len(rows), func(i int) string { return rows[i] }, 0)
		return nil
	})
}

// fileNotFound reports an unknown path with similarly named files.
func (q *querier) fileNotFound(ctx context.Context, o *output, path string) error {
	o.linef("no indexed file %s", path)
	base := path[strings.LastIndexByte(path, '/')+1:]
	hits, err := q.searchFiles(ctx, base, "", 5)
	if err != nil || len(hits) == 0 {
		return err
	}
	o.line("did you mean:")
	for _, h := range hits {
		if !o.line("  " + h.Path) {
			break
		}
	}
	return nil
}
