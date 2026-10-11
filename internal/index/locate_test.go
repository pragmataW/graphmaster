package index

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/testutil"
)

func TestLocateGitRepoAndPrepare(t *testing.T) {
	dir := testutil.Materialize(t)
	sub := filepath.Join(dir, "go", "internal")
	loc, err := Locate(sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if loc.NoGit || loc.Root.WorkTree != mustEval(t, dir) {
		t.Fatalf("Locate = %+v, want git root %s", loc, dir)
	}
	for range 2 { // idempotent
		if err := loc.Prepare(); err != nil {
			t.Fatal(err)
		}
	}
	gi, err := os.ReadFile(filepath.Join(loc.Dir, ".gitignore"))
	if err != nil || string(gi) != "*\n" {
		t.Fatalf(".graphmaster/.gitignore = %q, %v", gi, err)
	}
	ex, err := os.ReadFile(loc.Root.InfoExcludePath())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(ex), excludeLine); n != 1 {
		t.Fatalf("info/exclude has %d %q lines:\n%s", n, excludeLine, ex)
	}
	// git must not see the state directory.
	if out := testutil.Git(t, dir, "status", "--porcelain", "--ignored=no", "--", DirName); out != "" {
		t.Fatalf("git status shows .graphmaster: %q", out)
	}
}

func TestLocateNonGit(t *testing.T) {
	dir := t.TempDir()
	if _, err := Locate(dir, false); !errors.Is(err, ErrNotGitRepo) {
		t.Fatalf("Locate(non-git) err = %v, want ErrNotGitRepo", err)
	}
	loc, err := Locate(dir, true)
	if err != nil || !loc.NoGit {
		t.Fatalf("Locate(force) = %+v, %v", loc, err)
	}
	if err := loc.Prepare(); err != nil {
		t.Fatal(err)
	}
	// Once forced, the directory is found without the flag, also from below.
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	loc2, err := Locate(sub, false)
	if err != nil || !loc2.NoGit || loc2.Root.WorkTree != loc.Root.WorkTree {
		t.Fatalf("Locate after force = %+v, %v", loc2, err)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
