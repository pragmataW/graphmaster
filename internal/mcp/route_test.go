package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/query"
)

func mustLocate(t testing.TB, dir string) string {
	t.Helper()
	loc, err := index.Locate(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return loc.Root.WorkTree
}

func TestPathArgs(t *testing.T) {
	cases := []struct {
		in   any
		want []string
	}{
		{&query.StatusInput{}, nil},
		{&query.FindSymbolInput{Query: "x"}, nil},
		{&query.FindSymbolInput{Query: "/abs", PathPrefix: "go/"}, []string{"go/"}},
		{&query.CallsInput{Symbol: "pkg.Foo"}, nil},
		{&query.CallsInput{Symbol: "go/a.go:3"}, nil},
		{&query.CallsInput{Symbol: "/r/go/a.go:3"}, []string{"/r/go/a.go"}},
		{&query.CallsInput{Symbol: "/r/go/a.go"}, nil},
		{&query.ImpactInput{Symbol: "/r/a.go:1", Path: "/r/b.go"}, []string{"/r/a.go", "/r/b.go"}},
		{&query.TraverseInput{Start: "/r/a.go:7"}, []string{"/r/a.go"}},
		{&query.ImportsInput{Path: "go/cmd", Module: "/not/a/path"}, []string{"go/cmd"}},
	}
	for _, c := range cases {
		var got []string
		for _, a := range pathArgs(c.in) {
			got = append(got, a.path())
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%+v: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRoute(t *testing.T) {
	a := newGitRepo(t, map[string]string{"x/a.go": "package x\n"})
	b := newGitRepo(t, map[string]string{"lib/lib.go": "package lib\n"})
	plain := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Dir: a})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rootA, rootB := mustLocate(t, a), mustLocate(t, b)

	cases := []struct {
		in       any
		wantRoot string
		check    func(in any) string // returns the rewritten field
		want     string
	}{
		{&query.FileOutlineInput{Path: "x/a.go"}, rootA, outlinePath, "x/a.go"},
		{&query.FileOutlineInput{Path: filepath.Join(a, "x/a.go")}, rootA, outlinePath, "x/a.go"},
		{&query.FileOutlineInput{Path: filepath.Join(link, "x/a.go")}, rootA, outlinePath, "x/a.go"},
		{&query.FileOutlineInput{Path: filepath.Join(a, "x/deleted.go")}, rootA, outlinePath, "x/deleted.go"},
		{&query.RepoMapInput{PathPrefix: a}, rootA, func(in any) string { return in.(*query.RepoMapInput).PathPrefix }, ""},
		{&query.RepoMapInput{PathPrefix: filepath.Join(a, "x") + "/"}, rootA, func(in any) string { return in.(*query.RepoMapInput).PathPrefix }, "x/"},
		{&query.FileOutlineInput{Path: filepath.Join(b, "lib/lib.go")}, rootB, outlinePath, "lib/lib.go"},
		{&query.CallsInput{Symbol: filepath.Join(b, "lib/lib.go") + ":4"}, rootB, func(in any) string { return in.(*query.CallsInput).Symbol }, "lib/lib.go:4"},
		{&query.StatusInput{}, rootA, nil, ""},
	}
	for _, c := range cases {
		root, err := s.route(c.in)
		if err != nil {
			t.Errorf("%+v: %v", c.in, err)
			continue
		}
		if root != c.wantRoot {
			t.Errorf("%+v: root %q, want %q", c.in, root, c.wantRoot)
		}
		if c.check != nil {
			if got := c.check(c.in); got != c.want {
				t.Errorf("%+v: rewritten to %q, want %q", c.in, got, c.want)
			}
		}
	}

	if _, err := s.route(&query.FileOutlineInput{Path: filepath.Join(plain, "z.go")}); !errors.Is(err, index.ErrNotGitRepo) {
		t.Errorf("non-git path: err = %v, want ErrNotGitRepo", err)
	}
	if _, err := s.route(&query.ImpactInput{Symbol: filepath.Join(b, "lib/lib.go") + ":1", Path: filepath.Join(a, "x/a.go")}); err == nil {
		t.Error("paths in two repositories: no error")
	}
}

func outlinePath(in any) string { return in.(*query.FileOutlineInput).Path }

func TestRouteNoGitStartDir(t *testing.T) {
	s, err := New(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.route(&query.StatusInput{}); !errors.Is(err, index.ErrNotGitRepo) {
		t.Errorf("err = %v, want ErrNotGitRepo", err)
	}
	// An absolute path into a repository still works.
	b := newGitRepo(t, map[string]string{"lib/lib.go": "package lib\n"})
	in := &query.FileOutlineInput{Path: filepath.Join(b, "lib/lib.go")}
	root, err := s.route(in)
	if err != nil || root != mustLocate(t, b) || in.Path != "lib/lib.go" {
		t.Errorf("route = %q, %v (path %q)", root, err, in.Path)
	}
}

func TestPanicBecomesToolError(t *testing.T) {
	res := safeCall("find_symbol", func() (string, error) { panic("kaboom") })
	if !res.IsError {
		t.Fatal("IsError = false")
	}
	tc := res.Content[0].(*gomcp.TextContent)
	if !strings.Contains(tc.Text, "graphmaster find_symbol · error: internal error: kaboom") {
		t.Errorf("text = %q", tc.Text)
	}
}

func TestErrorText(t *testing.T) {
	notGit := safeCall("status", func() (string, error) {
		return "", errors.Join(index.ErrNotGitRepo, errors.New("/tmp/x"))
	})
	tc := notGit.Content[0].(*gomcp.TextContent)
	if !strings.HasPrefix(tc.Text, "graphmaster status · error: "+index.NotGitHint+" (") {
		t.Errorf("text = %q", tc.Text)
	}
	canceled := safeCall("status", func() (string, error) { return "", context.Canceled })
	if !canceled.IsError {
		t.Error("canceled: IsError = false")
	}
}
