package model

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"
)

func newTestRepo() *MemRepo {
	return NewMemRepo(map[string]string{
		"go.mod":                 "module example.com/x\n",
		"cmd/app/main.go":        "package main",
		"internal/a/a.go":        "package a",
		"internal/a/b.go":        "package a",
		"internal/a/sub/c.go":    "package sub",
		"web/package.json":       `{"name":"web"}`,
		"web/src/deep/x/file.ts": "export {}",
	})
}

func TestMemRepoFileExists(t *testing.T) {
	r := newTestRepo()
	for _, p := range []string{"go.mod", "internal/a/a.go", "./internal/a/b.go"} {
		if !r.FileExists(p) {
			t.Errorf("FileExists(%q) = false", p)
		}
	}
	for _, p := range []string{"internal/a", "nope.go", "", "../go.mod"} {
		if r.FileExists(p) {
			t.Errorf("FileExists(%q) = true", p)
		}
	}
	if !r.DirExists("internal/a") || !r.DirExists("") || !r.DirExists("web/src") {
		t.Fatal("DirExists missed real dirs")
	}
	if r.DirExists("internal/zz") || r.DirExists("go.mod") {
		t.Fatal("DirExists false positive")
	}
}

func TestMemRepoReadFile(t *testing.T) {
	r := newTestRepo()
	b, err := r.ReadFile("go.mod")
	if err != nil || string(b) != "module example.com/x\n" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if _, err := r.ReadFile("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
}

func TestMemRepoListDir(t *testing.T) {
	r := newTestRepo()
	got := r.ListDir("internal/a")
	want := []string{"a.go", "b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListDir = %v, want %v", got, want)
	}
	if got := r.ListDir("."); !reflect.DeepEqual(got, []string{"go.mod"}) {
		t.Fatalf("ListDir(root) = %v", got)
	}
	if got := r.ListDir("none"); len(got) != 0 {
		t.Fatalf("ListDir(none) = %v", got)
	}
}

func TestFindUp(t *testing.T) {
	r := newTestRepo()
	cases := []struct {
		dir, name, want string
		ok              bool
	}{
		{"internal/a/sub", "go.mod", "go.mod", true},
		{"web/src/deep/x", "package.json", "web/package.json", true},
		{"web", "package.json", "web/package.json", true},
		{"", "go.mod", "go.mod", true},
		{"cmd/app", "Cargo.toml", "", false},
	}
	for _, c := range cases {
		got, ok := FindUp(r, c.dir, c.name)
		if got != c.want || ok != c.ok {
			t.Errorf("FindUp(%q,%q) = %q,%v want %q,%v", c.dir, c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestCleanRel(t *testing.T) {
	cases := map[string]string{
		"":             "",
		".":            "",
		"./a/b":        "a/b",
		"a//b/":        "a/b",
		`a\b\c.go`:     "a/b/c.go",
		"a/../b":       "b",
		"../x":         "",
		"/abs/path":    "",
		"a/./b/../c":   "a/c",
		"a/b/../../..": "",
	}
	for in, want := range cases {
		if got := CleanRel(in); got != want {
			t.Errorf("CleanRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImportTargetKinds(t *testing.T) {
	if (ImportTarget{}).Resolved() {
		t.Fatal("zero target must be unresolved")
	}
	if !(ImportTarget{Kind: TargetFile, Path: "a.ts"}).Resolved() {
		t.Fatal("file target must be resolved")
	}
	if (ImportTarget{Kind: TargetExternal, Module: "fmt"}).Resolved() {
		t.Fatal("external targets are not in-repo resolutions")
	}
	for _, k := range []ImportTargetKind{TargetUnresolved, TargetFile, TargetModule, TargetExternal} {
		if k.String() == "" {
			t.Fatalf("empty string for %d", k)
		}
	}
}
