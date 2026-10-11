package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pragmataW/graphmaster/internal/query"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// fixtureCalls has one valid call per query tool on the fixture repository.
var fixtureCalls = map[string]string{
	"find_symbol":  `{"query":"FormatID"}`,
	"search":       `{"query":"order"}`,
	"repo_map":     `{"depth":1}`,
	"file_outline": `{"path":"go/internal/store/store.go"}`,
	"read_symbol":  `{"symbol":"OrderService.PlaceOrder"}`,
	"callers":      `{"symbol":"util.FormatID"}`,
	"callees":      `{"symbol":"example.com/fixture/cmd/server.main"}`,
	"impact":       `{"symbol":"util.FormatID"}`,
	"imports":      `{"path":"go/cmd/server/main.go"}`,
	"inheritance":  `{"symbol":"Animal"}`,
	"tests_for":    `{"symbol":"util.FormatID"}`,
	"traverse":     `{"start":"example.com/fixture/cmd/server.main"}`,
	"status":       `{}`,
}

func TestInitialize(t *testing.T) {
	cs, _ := connect(t, sharedFixture(t))
	init := cs.InitializeResult()
	if init.ServerInfo == nil || init.ServerInfo.Name != "graphmaster" {
		t.Errorf("server info = %+v, want name graphmaster", init.ServerInfo)
	}
	if init.Capabilities == nil || init.Capabilities.Tools == nil {
		t.Errorf("tools capability missing: %+v", init.Capabilities)
	}
	for _, w := range []string{"find_symbol", "read_symbol", "impact", "grep", "max_tokens"} {
		if !strings.Contains(init.Instructions, w) {
			t.Errorf("instructions lack %q:\n%s", w, init.Instructions)
		}
	}
}

func TestListTools(t *testing.T) {
	cs, _ := connect(t, sharedFixture(t))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := query.Tools()
	if len(res.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(res.Tools), len(want))
	}
	byName := map[string]*gomcp.Tool{}
	for _, tl := range res.Tools {
		byName[tl.Name] = tl
	}
	for _, q := range want {
		tl := byName[q.Name]
		if tl == nil {
			t.Errorf("tool %s not listed", q.Name)
			continue
		}
		if !strings.Contains(tl.Description, "Use ") {
			t.Errorf("%s: description does not say when to use it: %q", q.Name, tl.Description)
		}
		if tl.OutputSchema != nil {
			t.Errorf("%s: unexpected output schema", q.Name)
		}
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s: missing read-only annotation", q.Name)
		}
		schema := schemaOf(t, tl)
		if schema.Type != "object" {
			t.Errorf("%s: input schema type %q", q.Name, schema.Type)
		}
		if q.Name != "status" {
			if _, ok := schema.Properties["max_tokens"]; !ok {
				t.Errorf("%s: schema lacks max_tokens: %+v", q.Name, schema.Properties)
			}
		}
		if q.Primary != "" {
			if _, ok := schema.Properties[q.Primary]; !ok {
				t.Errorf("%s: schema lacks primary %q", q.Name, q.Primary)
			}
		}
	}
	// Typed inputs carry required fields.
	if s := schemaOf(t, byName["find_symbol"]); len(s.Required) != 1 || s.Required[0] != "query" {
		t.Errorf("find_symbol required = %v, want [query]", s.Required)
	}
}

type schemaShape struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func schemaOf(t *testing.T, tl *gomcp.Tool) schemaShape {
	t.Helper()
	b, err := json.Marshal(tl.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var s schemaShape
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCallEveryTool(t *testing.T) {
	cs, _ := connect(t, sharedFixture(t))
	for _, q := range query.Tools() {
		args, found := fixtureCalls[q.Name]
		if !found {
			t.Errorf("no fixture call for tool %s", q.Name)
			continue
		}
		out := ok(t, cs, q.Name, args)
		if !strings.HasPrefix(out, "graphmaster "+q.Name+" · ") {
			t.Errorf("%s: banner missing:\n%s", q.Name, out)
		}
		if len(strings.Split(strings.TrimSpace(out), "\n")) < 2 {
			t.Errorf("%s: empty answer:\n%s", q.Name, out)
		}
	}
	mustContain(t, ok(t, cs, "find_symbol", fixtureCalls["find_symbol"]), "util.FormatID", "go/internal/util/format.go:")
	mustContain(t, ok(t, cs, "status", `{}`), "state: ready")
}

// TestSameTextAsQuery checks that the server adds nothing to the query
// package's text.
func TestSameTextAsQuery(t *testing.T) {
	root := sharedFixture(t)
	cs, s := connect(t, root)
	args := fixtureCalls["file_outline"]
	got := ok(t, cs, "file_outline", args)
	e, err := s.engine(context.Background(), mustLocate(t, root))
	if err != nil {
		t.Fatal(err)
	}
	tl, _ := query.Lookup("file_outline")
	want, err := tl.RunJSON(context.Background(), e, []byte(args))
	if err != nil {
		t.Fatal(err)
	}
	if body(got) != body(want) {
		t.Errorf("MCP text differs from query text\n--- mcp\n%s\n--- query\n%s", got, want)
	}
}

func TestToolErrors(t *testing.T) {
	cs, _ := connect(t, sharedFixture(t))
	for _, c := range []struct{ tool, args, want string }{
		{"find_symbol", `{}`, "query"},
		{"find_symbol", `{"query":"x","kind":"nonsense"}`, "graphmaster find_symbol · error: invalid argument: "},
		{"impact", `{}`, "invalid argument"},
		{"traverse", `{"start":"main","direction":"up"}`, "invalid argument"},
		{"callers", `{"symbol":1}`, "symbol"},
	} {
		res := call(t, cs, c.tool, c.args)
		out := text(t, res)
		if !res.IsError {
			t.Errorf("%s %s: IsError = false:\n%s", c.tool, c.args, out)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%s %s: error %q lacks %q", c.tool, c.args, out, c.want)
		}
	}
	// The session survives errors.
	ok(t, cs, "status", `{}`)
}

func TestNotGitRepository(t *testing.T) {
	dir := t.TempDir()
	cs, _ := connect(t, dir)
	res := call(t, cs, "status", `{}`)
	out := text(t, res)
	if !res.IsError {
		t.Fatalf("IsError = false:\n%s", out)
	}
	mustContain(t, out, "graphmaster status · error: not a git repository", "graphmaster index --force")
	// Tools are still listed and the server keeps answering.
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// Once the directory becomes a repository, the next call succeeds.
	writeFile(t, dir, "main.go", "package main\n\nfunc Hello() {}\n")
	testutil.Git(t, dir, "init", "-q", "-b", "main")
	testutil.Git(t, dir, "add", "-A")
	testutil.Git(t, dir, "commit", "-q", "-m", "init")
	mustContain(t, ok(t, cs, "find_symbol", `{"query":"Hello"}`), "Hello", "main.go:3")
}

// TestFreshAfterEdit edits the working tree between two calls of the same
// session and expects the second answer to reflect the edit.
func TestFreshAfterEdit(t *testing.T) {
	dir := testutil.Materialize(t)
	cs, _ := connect(t, dir)

	mustContain(t, ok(t, cs, "find_symbol", `{"query":"Brandnew","exact":true}`), `no definitions match "Brandnew"`)
	before := ok(t, cs, "callers", `{"symbol":"util.FormatID"}`)
	if strings.Contains(before, "Brandnew") {
		t.Fatalf("Brandnew present before the edit:\n%s", before)
	}

	writeFile(t, dir, "go/internal/util/brandnew.go",
		"package util\n\n// Brandnew is added by the test.\nfunc Brandnew() string { return FormatID(\"x\") }\n")
	after := ok(t, cs, "callers", `{"symbol":"util.FormatID"}`)
	if !strings.HasPrefix(after, "graphmaster callers · fresh (+1 files, ") {
		t.Errorf("banner does not report the reindex:\n%s", after)
	}
	mustContain(t, after, "util.Brandnew  go/internal/util/brandnew.go:4-4")
	mustContain(t, ok(t, cs, "find_symbol", `{"query":"Brandnew","exact":true}`), "go/internal/util/brandnew.go:4")
}

func TestAbsolutePathInsideRoot(t *testing.T) {
	root := sharedFixture(t)
	cs, _ := connect(t, root)
	rel := ok(t, cs, "file_outline", `{"path":"go/internal/store/store.go"}`)
	abs := ok(t, cs, "file_outline", jsonArgs(t, map[string]any{"path": filepath.Join(root, "go/internal/store/store.go")}))
	if body(rel) != body(abs) {
		t.Errorf("absolute path answer differs\n--- rel\n%s\n--- abs\n%s", rel, abs)
	}
	got := ok(t, cs, "find_symbol", jsonArgs(t, map[string]any{"query": "FormatID", "path_prefix": filepath.Join(root, "go")}))
	mustContain(t, got, "go/internal/util/format.go:")
}

// TestPathOutsideRoot passes a path from another repository; the server
// detects that repository and answers from its own index.
func TestPathOutsideRoot(t *testing.T) {
	cs, s := connect(t, sharedFixture(t))
	other := newGitRepo(t, map[string]string{
		"go.mod":      "module example.com/other\n\ngo 1.22\n",
		"lib/lib.go":  "package lib\n\n// Elsewhere lives in another repository.\nfunc Elsewhere() int { return helper() }\n\nfunc helper() int { return 1 }\n",
		"lib/more.go": "package lib\n\nfunc More() int { return Elsewhere() }\n",
	})
	out := ok(t, cs, "file_outline", jsonArgs(t, map[string]any{"path": filepath.Join(other, "lib/lib.go")}))
	mustContain(t, out, "Elsewhere", "helper")

	out = ok(t, cs, "find_symbol", jsonArgs(t, map[string]any{"query": "Elsewhere", "path_prefix": filepath.Join(other, "lib")}))
	mustContain(t, out, "lib/lib.go:4")

	out = ok(t, cs, "callers", jsonArgs(t, map[string]any{"symbol": filepath.Join(other, "lib/lib.go") + ":4"}))
	mustContain(t, out, "More", "lib/more.go:3")

	// Calls without a path still use the start repository.
	mustContain(t, ok(t, cs, "find_symbol", `{"query":"FormatID"}`), "go/internal/util/format.go:")
	if n := s.openRoots(); n != 2 {
		t.Errorf("open roots = %d, want 2", n)
	}
}

func TestPathsInDifferentRepositories(t *testing.T) {
	root := sharedFixture(t)
	cs, _ := connect(t, root)
	other := newGitRepo(t, map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	res := call(t, cs, "impact", jsonArgs(t, map[string]any{
		"symbol": filepath.Join(other, "a.go") + ":3",
		"path":   filepath.Join(root, "go/cmd/server/main.go"),
	}))
	if !res.IsError {
		t.Fatalf("IsError = false:\n%s", text(t, res))
	}
	mustContain(t, text(t, res), "different repositories")
}

func jsonArgs(t testing.TB, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
