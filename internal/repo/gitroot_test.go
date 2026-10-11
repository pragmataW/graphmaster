package repo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindRootPlainRepo(t *testing.T) {
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{"a/b/c.go": "package b\n"})
	r, err := FindRoot(filepath.Join(dir, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if r.WorkTree != dir {
		t.Fatalf("WorkTree = %s, want %s", r.WorkTree, dir)
	}
	if r.GitDir != filepath.Join(dir, ".git") || r.CommonDir != r.GitDir {
		t.Fatalf("unexpected dirs %+v", r)
	}
	if rel, ok := r.Rel(filepath.Join(dir, "a", "b", "c.go")); !ok || rel != "a/b/c.go" {
		t.Fatalf("Rel = %q %v", rel, ok)
	}
	if _, ok := r.Rel(filepath.Dir(dir)); ok {
		t.Fatal("Rel outside worktree must fail")
	}
	if got := r.Abs("a/b/c.go"); got != filepath.Join(dir, "a", "b", "c.go") {
		t.Fatalf("Abs = %s", got)
	}
}

func TestFindRootWorktree(t *testing.T) {
	main := newRepo(t)
	writeFiles(t, main, map[string]string{"f.go": "package f\n"})
	git(t, main, "add", ".")
	git(t, main, "commit", "-qm", "init")
	wt := filepath.Join(filepath.Dir(main), filepath.Base(main)+"-wt")
	git(t, main, "worktree", "add", "-q", wt)
	t.Cleanup(func() { _ = os.RemoveAll(wt) })

	r, err := FindRoot(wt)
	if err != nil {
		t.Fatal(err)
	}
	if r.WorkTree != wt {
		t.Fatalf("WorkTree = %s", r.WorkTree)
	}
	wantGitDir := strings.TrimSpace(git(t, wt, "rev-parse", "--absolute-git-dir"))
	wantCommon := strings.TrimSpace(git(t, wt, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if r.GitDir != wantGitDir {
		t.Fatalf("GitDir = %s, want %s", r.GitDir, wantGitDir)
	}
	if r.CommonDir != wantCommon {
		t.Fatalf("CommonDir = %s, want %s", r.CommonDir, wantCommon)
	}
	if r.InfoExcludePath() != filepath.Join(wantCommon, "info", "exclude") {
		t.Fatalf("InfoExcludePath = %s", r.InfoExcludePath())
	}
	if r.IndexPath() != filepath.Join(wantGitDir, "index") {
		t.Fatalf("IndexPath = %s", r.IndexPath())
	}
}

func TestFindRootSubmodule(t *testing.T) {
	sub := newRepo(t)
	writeFiles(t, sub, map[string]string{"s.go": "package s\n"})
	git(t, sub, "add", ".")
	git(t, sub, "commit", "-qm", "sub")

	super := newRepo(t)
	git(t, super, "submodule", "add", "-q", sub, "ext/sub")
	inside := filepath.Join(super, "ext", "sub")

	r, err := FindRoot(inside)
	if err != nil {
		t.Fatal(err)
	}
	if r.WorkTree != inside {
		t.Fatalf("inside a submodule the submodule is the root, got %s", r.WorkTree)
	}
	want := strings.TrimSpace(git(t, inside, "rev-parse", "--absolute-git-dir"))
	if r.GitDir != want {
		t.Fatalf("GitDir = %s, want %s", r.GitDir, want)
	}
	// From the superproject the root is the superproject.
	r2, err := FindRoot(filepath.Join(super, "ext"))
	if err != nil || r2.WorkTree != super {
		t.Fatalf("superproject root = %+v, %v", r2, err)
	}
}

func TestFindRootNotARepo(t *testing.T) {
	dir := t.TempDir()
	_, err := FindRoot(dir)
	if err == nil {
		t.Skip("temp dir is inside a git repository on this machine")
	}
	if !errors.Is(err, ErrNotGitRepo) {
		t.Fatalf("err = %v, want ErrNotGitRepo", err)
	}
}

func TestFindRootBadGitFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".git": "nonsense\n"})
	if _, err := FindRoot(dir); err == nil || errors.Is(err, ErrNotGitRepo) {
		t.Fatalf("malformed .git file must be a hard error, got %v", err)
	}
}

func TestLoadGitConfig(t *testing.T) {
	dir := newRepo(t)
	home, _ := os.UserHomeDir()
	r, err := FindRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := LoadGitConfig(r)
	if cfg.ExcludesFile != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git", "ignore") {
		t.Fatalf("default ExcludesFile = %s", cfg.ExcludesFile)
	}
	git(t, dir, "config", "core.excludesFile", "~/my-ignore")
	git(t, dir, "config", "core.ignorecase", "true")
	cfg = LoadGitConfig(r)
	if cfg.ExcludesFile != filepath.Join(home, "my-ignore") || !cfg.IgnoreCase {
		t.Fatalf("cfg = %+v", cfg)
	}
	git(t, dir, "config", "core.ignorecase", "false")
	if LoadGitConfig(r).IgnoreCase {
		t.Fatal("ignorecase=false not honored")
	}
}
