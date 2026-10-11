package model

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

// RepoContext is the read-only view of the repository that language specs
// use for module identity and import resolution. All paths are
// repo-relative, slash-separated, and "" denotes the repo root.
// Implementations must be safe for concurrent use.
type RepoContext interface {
	// FileExists reports whether a regular, indexed-or-indexable file exists.
	FileExists(rel string) bool
	// DirExists reports whether a directory exists.
	DirExists(rel string) bool
	// ReadFile reads a small file such as go.mod, tsconfig.json or
	// composer.json. Implementations may cap the size. A missing file
	// returns an error wrapping fs.ErrNotExist.
	ReadFile(rel string) ([]byte, error)
	// ListDir returns the sorted base names of files (not directories)
	// directly inside dir.
	ListDir(dir string) []string
}

// CleanRel normalizes a repo-relative path: backslashes become slashes,
// "." and ".." are resolved, and the root becomes "". Paths that are
// absolute or escape the repository return "".
func CleanRel(p string) string {
	c, _ := cleanRel(p)
	return c
}

// cleanRel is CleanRel that also reports whether p stayed inside the repo.
func cleanRel(p string) (string, bool) {
	p = strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(p, "/") {
		return "", false
	}
	p = path.Clean(p)
	switch {
	case p == ".":
		return "", true
	case p == ".." || strings.HasPrefix(p, "../"):
		return "", false
	}
	return p, true
}

// FindUp searches dir and its ancestors (up to the repo root) for a file
// named name and returns its repo-relative path.
func FindUp(ctx RepoContext, dir, name string) (string, bool) {
	dir = CleanRel(dir)
	for {
		candidate := path.Join(dir, name)
		if ctx.FileExists(candidate) {
			return candidate, true
		}
		if dir == "" {
			return "", false
		}
		dir = parentDir(dir)
	}
}

func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// MemRepo is an in-memory RepoContext for tests.
type MemRepo struct {
	files map[string][]byte
	dirs  map[string]bool
}

var _ RepoContext = (*MemRepo)(nil)

// NewMemRepo builds a MemRepo from path -> content.
func NewMemRepo(files map[string]string) *MemRepo {
	m := &MemRepo{files: map[string][]byte{}, dirs: map[string]bool{"": true}}
	for p, c := range files {
		p = CleanRel(p)
		if p == "" {
			continue
		}
		m.files[p] = []byte(c)
		for d := parentDir(p); d != ""; d = parentDir(d) {
			m.dirs[d] = true
		}
	}
	return m
}

// FileExists implements RepoContext.
func (m *MemRepo) FileExists(rel string) bool {
	_, ok := m.files[CleanRel(rel)]
	return ok
}

// DirExists implements RepoContext.
func (m *MemRepo) DirExists(rel string) bool {
	p, ok := cleanRel(rel)
	return ok && m.dirs[p]
}

// ReadFile implements RepoContext.
func (m *MemRepo) ReadFile(rel string) ([]byte, error) {
	b, ok := m.files[CleanRel(rel)]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: rel, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), b...), nil
}

// ListDir implements RepoContext.
func (m *MemRepo) ListDir(dir string) []string {
	dir = CleanRel(dir)
	var out []string
	for p := range m.files {
		if parentDir(p) == dir {
			out = append(out, path.Base(p))
		}
	}
	sort.Strings(out)
	return out
}
