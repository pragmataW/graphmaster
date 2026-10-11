package index

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/repo"
)

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRepoCtxAnswers(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod":            "module example.com/x\n",
		"a/b.go":            "package a\n",
		"a/ignored_code.go": "package a\n",
		"a/notes.txt":       "hi\n",
	})
	root := &repo.Root{WorkTree: dir}
	rc := NewRepoCtx(root, map[string]struct{}{"a/b.go": {}})

	// Code files are answered from the candidate set, not the disk.
	if !rc.FileExists("a/b.go") || rc.FileExists("a/ignored_code.go") || rc.FileExists("a/missing.go") {
		t.Fatal("code-file existence must follow the candidate set")
	}
	if !rc.FileExists("./go.mod") || rc.FileExists("nope.toml") {
		t.Fatal("non-code existence must follow the filesystem")
	}
	if b, err := rc.ReadFile("go.mod"); err != nil || string(b) != "module example.com/x\n" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if _, err := rc.ReadFile("missing.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile(missing) err = %v", err)
	}
	if _, err := rc.ReadFile("../escape"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile(escape) err = %v", err)
	}
	if b, err := rc.ReadFile("a/b.go"); err != nil || string(b) != "package a\n" {
		t.Fatalf("ReadFile(code) = %q, %v", b, err)
	}
	if _, recorded := rc.Deps()["f:a/b.go"]; recorded {
		t.Fatal("reading a source file must not become a dependency")
	}
	if !rc.DirExists("a") || !rc.DirExists("") || rc.DirExists("zzz") || rc.DirExists("..") {
		t.Fatal("DirExists")
	}
	if got := rc.ListDir("a"); !slices.Equal(got, []string{"b.go", "ignored_code.go", "notes.txt"}) {
		t.Fatalf("ListDir = %v", got)
	}

	deps := rc.Deps()
	for _, k := range []string{"c:a/b.go", "c:a/ignored_code.go", "f:go.mod", "f:nope.toml", "d:a", "l:a"} {
		if _, ok := deps[k]; !ok {
			t.Errorf("dependency %q not recorded (have %v)", k, deps)
		}
	}
	code := map[string]struct{}{"a/b.go": {}}
	if k := verifyDeps(root, deps, code); k != "" {
		t.Fatalf("verifyDeps on unchanged tree reports %q", k)
	}

	// Each kind of change is detected.
	if k := verifyDeps(root, deps, map[string]struct{}{}); k != "c:a/b.go" {
		t.Errorf("code removal: got %q", k)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "go.mod"), future, future); err != nil {
		t.Fatal(err)
	}
	if k := verifyDeps(root, deps, code); k != "f:go.mod" {
		t.Errorf("go.mod change: got %q", k)
	}
	writeTree(t, dir, map[string]string{"nope.toml": "x"})
	if k := verifyDeps(root, deps, code); k != "f:go.mod" { // first in key order
		t.Errorf("got %q", k)
	}
}

func TestRepoCtxListChange(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"p/x.cs": ""})
	root := &repo.Root{WorkTree: dir}
	rc := NewRepoCtx(root, nil)
	_ = rc.ListDir("p")
	deps := rc.Deps()
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "p"), past, past)
	if k := verifyDeps(root, deps, nil); k != "l:p" {
		t.Fatalf("listing change: got %q", k)
	}
}

func TestRepoCtxOwnerViews(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"go.mod": "module x\n", "p/a.ts": "", "p/b.ts": ""})
	rc := NewRepoCtx(&repo.Root{WorkTree: dir}, map[string]struct{}{"p/a.ts": {}, "p/b.ts": {}})

	// The linker's view records nothing, not even on a first probe.
	un := rc.Unrecorded()
	if !un.FileExists("go.mod") || !un.FileExists("p/a.ts") || len(un.ListDir("p")) != 2 {
		t.Fatal("unrecorded view answers differ")
	}
	if d := rc.TakeNewDeps(); len(d) != 0 {
		t.Fatalf("unrecorded view recorded %v", d)
	}
	// Owner views attribute every answer, also when it comes from the
	// probe cache filled by another view.
	a, b := rc.For("p/a.ts"), rc.For("p/b.ts")
	if a != rc.For("p/a.ts") {
		t.Fatal("For must return a stable view per owner")
	}
	for _, v := range []model.RepoContext{a, b} {
		_ = v.FileExists("go.mod")
		_ = v.FileExists("p/c.ts")
		_ = v.ListDir("p")
		_ = v.DirExists("p")
		_, _ = v.ReadFile("go.mod")
	}
	_ = rc.FileExists("p/a.ts") // no owner
	deps := rc.TakeNewDeps()
	for _, k := range []string{"f:go.mod", "c:p/c.ts", "l:p", "d:p", "c:p/a.ts"} {
		if _, ok := deps[k]; !ok {
			t.Errorf("dependency %s not recorded: %v", k, deps)
		}
	}
	got := map[DepOwner]bool{}
	for _, o := range rc.TakeNewOwners() {
		if got[o] {
			t.Errorf("attribution %v reported twice", o)
		}
		got[o] = true
	}
	for _, owner := range []string{"p/a.ts", "p/b.ts"} {
		for _, k := range []string{"f:go.mod", "c:p/c.ts", "l:p", "d:p"} {
			if !got[DepOwner{Key: k, Owner: owner}] {
				t.Errorf("missing attribution %s <- %s", k, owner)
			}
		}
	}
	if !got[DepOwner{Key: "c:p/a.ts"}] {
		t.Error("an answer given without an owner must be attributed to no owner")
	}
	if len(rc.TakeNewOwners()) != 0 {
		t.Error("TakeNewOwners must drain")
	}
}
