package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pragmataW/graphmaster/internal/testutil"
)

var (
	fixtureOnce sync.Once
	fixtureTmp  string
	fixtureRoot string
	fixtureErr  error
)

// sharedFixture materializes the fixture repository once per package run.
// Tests using it must not modify it.
func sharedFixture(t testing.TB) string {
	t.Helper()
	fixtureOnce.Do(func() {
		fixtureTmp, fixtureErr = os.MkdirTemp("", "gm-mcp-fixture-")
		if fixtureErr != nil {
			return
		}
		fixtureRoot = filepath.Join(fixtureTmp, "repo")
		fixtureErr = testutil.MaterializeTo(fixtureRoot)
	})
	if fixtureErr != nil {
		t.Fatalf("fixture: %v", fixtureErr)
	}
	return fixtureRoot
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtureTmp != "" {
		_ = os.RemoveAll(fixtureTmp)
	}
	os.Exit(code)
}

// connect starts a Server for dir and returns a connected client session.
func connect(t testing.TB, dir string) (*gomcp.ClientSession, *Server) {
	t.Helper()
	s, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	st, ct := gomcp.NewInMemoryTransports()
	ss, err := s.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := gomcp.NewClient(&gomcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, s
}

// call invokes a tool with JSON arguments and returns the result.
func call(t testing.TB, cs *gomcp.ClientSession, name, args string) *gomcp.CallToolResult {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		t.Fatalf("bad test args %s: %v", args, err)
	}
	res, err := cs.CallTool(context.Background(), &gomcp.CallToolParams{Name: name, Arguments: raw})
	if err != nil {
		t.Fatalf("CallTool %s %s: %v", name, args, err)
	}
	return res
}

// text returns the single TextContent of res, failing on anything else.
func text(t testing.TB, res *gomcp.CallToolResult) string {
	t.Helper()
	if res.StructuredContent != nil {
		t.Errorf("unexpected structuredContent: %v", res.StructuredContent)
	}
	if len(res.Content) != 1 {
		t.Fatalf("got %d content blocks, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*gomcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *TextContent", res.Content[0])
	}
	return tc.Text
}

// ok calls a tool and returns its text, failing on a tool error.
func ok(t testing.TB, cs *gomcp.ClientSession, name, args string) string {
	t.Helper()
	res := call(t, cs, name, args)
	out := text(t, res)
	if res.IsError {
		t.Fatalf("%s %s: tool error:\n%s", name, args, out)
	}
	return out
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

// body drops the banner line so outputs from different calls compare.
func body(s string) string {
	_, rest, _ := strings.Cut(s, "\n")
	return rest
}

// writeFile writes rel under root, creating directories.
func writeFile(t testing.TB, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newGitRepo creates a small committed Go repository.
func newGitRepo(t testing.TB, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		writeFile(t, root, rel, content)
	}
	testutil.Git(t, root, "init", "-q", "-b", "main")
	testutil.Git(t, root, "add", "-A")
	testutil.Git(t, root, "commit", "-q", "-m", "init")
	return root
}
