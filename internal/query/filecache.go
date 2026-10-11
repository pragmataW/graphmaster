package query

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/pragmataW/graphmaster/internal/model"
)

// metaFilesGen is the meta key internal/index rewrites in every transaction
// that changes files rows; equal values imply an unchanged files table.
const metaFilesGen = "files_gen"

// fileEntry is the part of a files row repo_map needs.
type fileEntry struct {
	id      int64
	path    string
	loc     int
	indexed bool
}

// fileCache keeps the files table in memory between calls of a
// long-lived engine (MCP server); loading 100k rows costs ~100ms while
// walking the cached slice costs a few.
type fileCache struct {
	mu    sync.Mutex
	gen   string
	files []fileEntry // sorted by path
}

// load returns every files row, reusing the cached copy while files_gen is
// unchanged. The label is read before the rows, so a concurrent write can
// only make the cache newer than its label, never older.
func (c *fileCache) load(ctx context.Context, q *querier) ([]fileEntry, error) {
	gen, _, err := q.st.Meta(ctx, metaFilesGen)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != "" && gen == c.gen {
		return c.files, nil
	}
	rows, err := q.db.QueryContext(ctx, `SELECT id, path, loc, status FROM files ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]fileEntry, 0, len(c.files))
	for rows.Next() {
		var (
			f      fileEntry
			status int
		)
		if err := rows.Scan(&f.id, &f.path, &f.loc, &status); err != nil {
			return nil, err
		}
		f.indexed = model.FileStatus(uint8(status)).Indexed()
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	c.gen, c.files = gen, out
	return out, nil
}

// under returns the entries whose path starts with prefix (a slice of the
// sorted list, found by binary search).
func under(files []fileEntry, prefix string) []fileEntry {
	lo := sort.Search(len(files), func(i int) bool { return files[i].path >= prefix })
	hi := lo + sort.Search(len(files)-lo, func(i int) bool { return !strings.HasPrefix(files[lo+i].path, prefix) })
	return files[lo:hi]
}
