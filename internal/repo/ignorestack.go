package repo

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Ignore evaluates git's exclude rules for one repository. Precedence,
// highest first (git's EXC_CMDL, EXC_DIRS, EXC_FILE groups):
//
//  1. config excludes (graphmaster's own list, like --exclude on the
//     command line),
//  2. per-directory .gitignore files, deepest directory first,
//  3. $GIT_COMMON_DIR/info/exclude, then core.excludesFile.
//
// Within each source the last matching line wins. A path inside an
// excluded directory is excluded regardless of later negations. Ignore is
// immutable and safe for concurrent use.
type Ignore struct {
	config     []ignorePattern
	files      [][]ignorePattern // core.excludesFile, info/exclude (low → high)
	ignoreCase bool
	root       string // worktree, for on-demand .gitignore reads in IsIgnored
}

// NewIgnore loads the repository-wide exclude sources. configExcludes are
// gitignore-syntax patterns relative to the repo root.
func NewIgnore(r *Root, cfg GitConfig, configExcludes []string) (*Ignore, error) {
	ig := &Ignore{
		config:     parsePatterns(configExcludes),
		ignoreCase: cfg.IgnoreCase,
		root:       r.WorkTree,
	}
	for _, f := range []string{cfg.ExcludesFile, r.InfoExcludePath()} {
		b, err := readOptionalFile(f)
		if err != nil {
			return nil, fmt.Errorf("read excludes %s: %w", f, err)
		}
		ig.files = append(ig.files, parseIgnore(b, ""))
	}
	return ig, nil
}

// IgnoreCase reports whether matching folds case (core.ignorecase).
func (ig *Ignore) IgnoreCase() bool { return ig.ignoreCase }

// ignoreFrame is one level of the per-directory .gitignore stack. Frames
// are immutable and shared between goroutines walking sibling subtrees.
type ignoreFrame struct {
	parent   *ignoreFrame
	patterns []ignorePattern
}

// push returns a child frame, or f itself when patterns is empty.
func (f *ignoreFrame) push(patterns []ignorePattern) *ignoreFrame {
	if len(patterns) == 0 {
		return f
	}
	return &ignoreFrame{parent: f, patterns: patterns}
}

// excludedAt decides whether path (whose parent directories are known not
// to be excluded) is excluded, given the .gitignore stack of its parent.
func (ig *Ignore) excludedAt(frame *ignoreFrame, p string, isDir bool) bool {
	base := path.Base(p)
	if v := matchList(ig.config, p, base, isDir, ig.ignoreCase); v != 0 {
		return v > 0
	}
	for f := frame; f != nil; f = f.parent {
		if v := matchList(f.patterns, p, base, isDir, ig.ignoreCase); v != 0 {
			return v > 0
		}
	}
	for i := len(ig.files) - 1; i >= 0; i-- {
		if v := matchList(ig.files[i], p, base, isDir, ig.ignoreCase); v != 0 {
			return v > 0
		}
	}
	return false
}

// IsIgnored reports whether the repo-relative path is excluded, reading the
// .gitignore files along its parent directories from disk. It is meant for
// one-off checks (e.g. paths named by hooks); the walker uses the cached
// stack instead.
func (ig *Ignore) IsIgnored(rel string, isDir bool) bool {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return false
	}
	parts := strings.Split(rel, "/")
	frame := ig.rootFrame()
	dir := ""
	for i, name := range parts {
		p := name
		if dir != "" {
			p = dir + "/" + name
		}
		last := i == len(parts)-1
		if ig.excludedAt(frame, p, isDir || !last) {
			return true
		}
		if last {
			break
		}
		dir = p
		b, err := readOptionalFile(ig.gitignorePath(dir))
		if err == nil && b != nil {
			frame = frame.push(parseIgnore(b, dir))
		}
	}
	return false
}

// rootFrame returns the frame for the worktree root's .gitignore.
func (ig *Ignore) rootFrame() *ignoreFrame {
	b, err := readOptionalFile(ig.gitignorePath(""))
	if err != nil || b == nil {
		return nil
	}
	return (*ignoreFrame)(nil).push(parseIgnore(b, ""))
}

func (ig *Ignore) gitignorePath(dir string) string {
	return filepath.Join(ig.root, filepath.FromSlash(dir), ".gitignore")
}

// ExcludedByConfig reports whether rel or any of its parent directories
// matches the config excludes alone. The indexer uses it to drop tracked
// files under vendor/, node_modules/ and the like, which git itself would
// still list.
func (ig *Ignore) ExcludedByConfig(rel string) bool {
	if len(ig.config) == 0 {
		return false
	}
	rel = strings.Trim(rel, "/")
	parts := strings.Split(rel, "/")
	p := ""
	for i, name := range parts {
		if p == "" {
			p = name
		} else {
			p += "/" + name
		}
		if matchList(ig.config, p, name, i < len(parts)-1, ig.ignoreCase) > 0 {
			return true
		}
	}
	return false
}
