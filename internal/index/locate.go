package index

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/repo"
)

// DirName is the per-repository state directory.
const DirName = ".graphmaster"

// noGitDirName marks a directory indexed without git (`index --force`). The
// directory doubles as a fake git dir: it has no index, config or
// info/exclude, so discovery sees "nothing tracked, nothing excluded".
const noGitDirName = "nogit"

// excludeLine is appended to .git/info/exclude.
const excludeLine = "/" + DirName + "/"

// ErrNotGitRepo is returned when no git working tree (and no forced
// non-git index) encloses the start directory.
var ErrNotGitRepo = repo.ErrNotGitRepo

// NotGitHint is the user-facing advice for ErrNotGitRepo.
const NotGitHint = "not a git repository; run `graphmaster index --force`"

// Location is a located repository.
type Location struct {
	Root *repo.Root
	// Dir is the absolute .graphmaster directory.
	Dir string
	// NoGit is set for a directory indexed without git.
	NoGit bool
}

// Locate finds the repository enclosing start: a git working tree, or a
// directory previously indexed with --force. With allowNoGit, a directory
// outside any repository becomes its own (non-git) root.
func Locate(start string, allowNoGit bool) (*Location, error) {
	r, err := repo.FindRoot(start)
	if err == nil {
		return &Location{Root: r, Dir: filepath.Join(r.WorkTree, DirName)}, nil
	}
	if !errors.Is(err, repo.ErrNotGitRepo) {
		return nil, err
	}
	abs, aerr := filepath.Abs(start)
	if aerr != nil {
		return nil, aerr
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	for d := abs; ; {
		if fi, serr := os.Stat(filepath.Join(d, DirName, noGitDirName)); serr == nil && fi.IsDir() {
			return noGitLocation(d), nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if allowNoGit {
		return noGitLocation(abs), nil
	}
	return nil, fmt.Errorf("%w: %s; run `graphmaster index --force` to index it anyway", ErrNotGitRepo, abs)
}

func noGitLocation(dir string) *Location {
	gd := filepath.Join(dir, DirName, noGitDirName)
	return &Location{
		Root:  &repo.Root{WorkTree: dir, GitDir: gd, CommonDir: gd},
		Dir:   filepath.Join(dir, DirName),
		NoGit: true,
	}
}

// Prepare creates the .graphmaster directory with a catch-all .gitignore
// and registers it in .git/info/exclude (never in a tracked .gitignore).
// It is idempotent.
func (l *Location) Prepare() error {
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	gi := filepath.Join(l.Dir, ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(gi, []byte("*\n"), 0o644); err != nil {
			return err
		}
	}
	if l.NoGit {
		return os.MkdirAll(filepath.Join(l.Dir, noGitDirName), 0o755)
	}
	return ensureExcluded(l.Root.InfoExcludePath())
}

// ensureExcluded appends excludeLine to the info/exclude file unless an
// equivalent line is already present.
func ensureExcluded(path string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		switch strings.TrimSpace(sc.Text()) {
		case excludeLine, DirName, DirName + "/", "/" + DirName:
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if len(b) > 0 && b[len(b)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString(excludeLine + "\n")
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
