package repo

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
)

// Entry kinds recorded in the directory cache.
const (
	kindFile    byte = 'f'
	kindDir     byte = 'd'
	kindSymlink byte = 'l'
)

// dirName is one directory entry as seen by readdir.
type dirName struct {
	name string
	kind byte
}

// cachedDir is the remembered listing of one directory.
type cachedDir struct {
	mtimeNS int64
	names   []dirName
	// ignoreSig is the stat of the directory's .gitignore ("" if none);
	// patterns are its parsed content, kept in memory only.
	ignoreSig string
	patterns  []ignorePattern
	parsed    bool
}

// DirRow is the persisted form of one cached directory (graph.db `dirs`).
// Names encodes the listing as repeated <kind byte><name>NUL records with
// kind one of 'f' (file), 'd' (directory), 'l' (symlink).
type DirRow struct {
	Path       string
	MtimeNS    int64
	IgnoreStat string
	Names      []byte
}

// DirCache implements git's untracked-cache idea: a directory whose mtime
// is unchanged since it was listed still has the same entries, so it need
// not be read again. Only directories whose mtime was safely older than the
// scan (racy-git rule) are cached. DirCache is safe for concurrent use.
type DirCache struct {
	walkMu sync.Mutex // serializes walks using this cache
	mu     sync.Mutex
	dirs   map[string]*cachedDir
}

// NewDirCache returns an empty cache.
func NewDirCache() *DirCache { return &DirCache{dirs: map[string]*cachedDir{}} }

// DirCacheFromRows restores a cache from persisted rows.
func DirCacheFromRows(rows []DirRow) (*DirCache, error) {
	c := NewDirCache()
	for _, r := range rows {
		names, err := decodeDirNames(r.Names)
		if err != nil {
			return nil, fmt.Errorf("dir cache row %q: %w", r.Path, err)
		}
		c.dirs[r.Path] = &cachedDir{mtimeNS: r.MtimeNS, names: names, ignoreSig: r.IgnoreStat}
	}
	return c, nil
}

// Len is the number of cached directories.
func (c *DirCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.dirs)
}

// Rows returns all entries sorted by path, for persistence.
func (c *DirCache) Rows() []DirRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]DirRow, 0, len(c.dirs))
	for p, d := range c.dirs {
		rows = append(rows, d.row(p))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows
}

func (d *cachedDir) row(path string) DirRow {
	return DirRow{Path: path, MtimeNS: d.mtimeNS, IgnoreStat: d.ignoreSig, Names: encodeDirNames(d.names)}
}

func (c *DirCache) get(path string) *cachedDir {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirs[path]
}

// replace swaps in the directories visited by a walk and reports which
// persisted rows changed or disappeared.
func (c *DirCache) replace(next map[string]*cachedDir) (changed []DirRow, removed []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p, d := range next {
		if old, ok := c.dirs[p]; !ok || old != d {
			changed = append(changed, d.row(p))
		}
	}
	for p := range c.dirs {
		if _, ok := next[p]; !ok {
			removed = append(removed, p)
		}
	}
	c.dirs = next
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	sort.Strings(removed)
	return changed, removed
}

func encodeDirNames(names []dirName) []byte {
	n := 0
	for _, e := range names {
		n += len(e.name) + 2
	}
	b := make([]byte, 0, n)
	for _, e := range names {
		b = append(b, e.kind)
		b = append(b, e.name...)
		b = append(b, 0)
	}
	return b
}

func decodeDirNames(b []byte) ([]dirName, error) {
	var out []dirName
	for len(b) > 0 {
		i := bytes.IndexByte(b, 0)
		if i < 1 {
			return nil, fmt.Errorf("malformed names record")
		}
		kind := b[0]
		if kind != kindFile && kind != kindDir && kind != kindSymlink {
			return nil, fmt.Errorf("unknown entry kind %q", kind)
		}
		out = append(out, dirName{name: string(b[1:i]), kind: kind})
		b = b[i+1:]
	}
	return out, nil
}
