package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/index"
)

// Argument names that hold a path, and those that hold a symbol which may
// be written as path:line.
var (
	pathFields   = map[string]bool{"path": true, "path_prefix": true}
	symbolFields = map[string]bool{"symbol": true, "start": true}
)

// pathArg is one path-valued argument of a tool input.
type pathArg struct {
	field *string
	p     string // the path part
	line  string // ":N" kept after the path for path:line symbols
}

func (a pathArg) path() string { return a.p }

func (a pathArg) set(p string) { *a.field = p + a.line }

// pathArgs returns the path arguments of the input struct pointed to by
// in: every non-empty path / path_prefix, and symbol / start arguments
// written as an absolute path:line.
func pathArgs(in any) []pathArg {
	v := reflect.ValueOf(in)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	v = v.Elem()
	var out []pathArg
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		fv := v.Field(i)
		if !f.IsExported() || fv.Kind() != reflect.String || fv.String() == "" {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		ptr := fv.Addr().Interface().(*string)
		switch {
		case pathFields[name]:
			out = append(out, pathArg{field: ptr, p: *ptr})
		case symbolFields[name]:
			if p, line, ok := splitPathLine(*ptr); ok && filepath.IsAbs(p) {
				out = append(out, pathArg{field: ptr, p: p, line: line})
			}
		}
	}
	return out
}

// splitPathLine splits "path:N" (N a positive number) into the path and
// ":N".
func splitPathLine(s string) (string, string, bool) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	if n, err := strconv.Atoi(s[i+1:]); err != nil || n <= 0 {
		return "", "", false
	}
	return s[:i], s[i:], true
}

// route picks the repository that answers a call and rewrites absolute
// path arguments in in (a pointer to a tool input) relative to it.
// Relative paths and calls without paths go to the start repository; an
// absolute path outside it selects the repository that contains it.
func (s *Server) route(in any) (string, error) {
	defRoot, defErr := s.defaultRoot()
	chosen := ""
	for _, a := range pathArgs(in) {
		if !filepath.IsAbs(a.path()) {
			continue
		}
		abs := canonical(a.path())
		root := ""
		if defErr == nil && within(abs, defRoot) {
			root = defRoot
		} else {
			loc, err := index.Locate(existingDir(abs), false)
			if err != nil {
				return "", err
			}
			root = loc.Root.WorkTree
		}
		if chosen != "" && chosen != root {
			return "", fmt.Errorf("%w: paths in different repositories (%s, %s); query one repository per call",
				errBadPath, chosen, root)
		}
		chosen = root
		a.set(relPath(root, abs, strings.HasSuffix(a.path(), "/")))
	}
	if chosen != "" {
		return chosen, nil
	}
	return defRoot, defErr
}

var errBadPath = errors.New("invalid path argument")

// defaultRoot locates the start repository, retrying on every call until
// it succeeds (the directory may be indexed with --force later).
func (s *Server) defaultRoot() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.defRoot != "" {
		return s.defRoot, nil
	}
	loc, err := index.Locate(s.dir, false)
	if err != nil {
		return "", err
	}
	s.defRoot = loc.Root.WorkTree
	return s.defRoot, nil
}

// canonical cleans p and resolves symlinks in its longest existing prefix,
// so paths compare with repository roots (which are symlink-free) even
// when p names a file that no longer exists.
func canonical(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for d := p; ; {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
		d = parent
	}
}

// existingDir returns the nearest existing directory at or above p.
func existingDir(p string) string {
	for d := p; ; {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return d
		}
		d = parent
	}
}

// within reports whether p is root or below it.
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// relPath returns abs relative to root in slash form ("" for the root
// itself), keeping a trailing slash when the argument had one.
func relPath(root, abs string, dirSlash bool) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if dirSlash {
		rel += "/"
	}
	return rel
}
