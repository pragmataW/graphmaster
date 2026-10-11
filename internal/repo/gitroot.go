package repo

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotGitRepo is returned when no enclosing git working tree is found.
var ErrNotGitRepo = errors.New("not a git repository")

// Root describes a located git working tree.
type Root struct {
	// WorkTree is the absolute, symlink-resolved top-level directory.
	WorkTree string
	// GitDir is the per-worktree git directory (holds index, HEAD).
	GitDir string
	// CommonDir is the directory shared by all worktrees (holds config,
	// info/exclude, objects). It equals GitDir for the main worktree.
	CommonDir string
}

// FindRoot locates the git working tree containing start. A ".git" file
// (linked worktree or submodule checkout) is followed via its "gitdir:" line
// and the shared directory via "commondir". Inside a submodule checkout the
// submodule itself is the root; from the superproject's point of view a
// submodule is an opaque gitlink.
func FindRoot(start string) (*Root, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	for dir := abs; ; {
		gitDir, err := resolveDotGit(dir)
		if err != nil {
			return nil, err
		}
		if gitDir != "" {
			return &Root{WorkTree: dir, GitDir: gitDir, CommonDir: commonDir(gitDir)}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("%w (or any parent up to /): %s", ErrNotGitRepo, abs)
		}
		dir = parent
	}
}

// resolveDotGit returns the git dir that dir/.git designates, or "" if dir
// has no (valid) .git entry.
func resolveDotGit(dir string) (string, error) {
	dotGit := filepath.Join(dir, ".git")
	fi, err := os.Stat(dotGit)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", err
	case fi.IsDir():
		if !isGitDir(dotGit) {
			return "", nil
		}
		return dotGit, nil
	}
	gitDir, err := readGitFile(dotGit)
	if err != nil {
		return "", err
	}
	if !isGitDir(gitDir) {
		return "", fmt.Errorf("%s: gitdir %s is not a git directory", dotGit, gitDir)
	}
	return gitDir, nil
}

// readGitFile parses a ".git" file of the form "gitdir: <path>". Relative
// paths are resolved against the file's directory.
func readGitFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line, _, _ := bytes.Cut(b, []byte("\n"))
	rest, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("gitdir:"))
	if !ok {
		return "", fmt.Errorf("%s: invalid gitfile format", path)
	}
	target := strings.TrimSpace(string(rest))
	if target == "" {
		return "", fmt.Errorf("%s: empty gitdir", path)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), nil
}

// commonDir follows gitDir/commondir when present (linked worktrees).
func commonDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	cd := strings.TrimSpace(string(b))
	if cd == "" {
		return gitDir
	}
	if !filepath.IsAbs(cd) {
		cd = filepath.Join(gitDir, cd)
	}
	return filepath.Clean(cd)
}

// isGitDir is a cheap validity check mirroring git's: HEAD must exist.
func isGitDir(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, "HEAD"))
	return err == nil
}

// IndexPath is the path of this worktree's index file.
func (r *Root) IndexPath() string { return filepath.Join(r.GitDir, "index") }

// InfoExcludePath is the repository-wide info/exclude file.
func (r *Root) InfoExcludePath() string { return filepath.Join(r.CommonDir, "info", "exclude") }

// ConfigPath is the repository config file.
func (r *Root) ConfigPath() string { return filepath.Join(r.CommonDir, "config") }

// Abs converts a repo-relative slash path to an absolute OS path.
func (r *Root) Abs(rel string) string {
	if rel == "" {
		return r.WorkTree
	}
	return filepath.Join(r.WorkTree, filepath.FromSlash(rel))
}

// Rel converts an absolute OS path inside the worktree to a repo-relative
// slash path. ok is false for paths outside the worktree.
func (r *Root) Rel(abs string) (rel string, ok bool) {
	rel, err := filepath.Rel(r.WorkTree, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	switch {
	case rel == ".":
		return "", true
	case rel == ".." || strings.HasPrefix(rel, "../"):
		return "", false
	}
	return rel, true
}
