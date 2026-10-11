package e2e

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpClient starts `graphmaster mcp` in root and connects to it over stdio.
func mcpClient(t *testing.T, root string) *gomcp.ClientSession {
	t.Helper()
	cmd := exec.Command(binary(t), "mcp")
	cmd.Dir = root
	cmd.Env = childEnv(t.TempDir())
	client := gomcp.NewClient(&gomcp.Implementation{Name: "e2e", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cs, err := client.Connect(ctx, &gomcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callText calls a tool and returns its text output.
func callText(t *testing.T, cs *gomcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := cs.CallTool(ctx, &gomcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		tc, ok := c.(*gomcp.TextContent)
		if !ok {
			t.Fatalf("%s: non-text content %T", name, c)
		}
		b.WriteString(tc.Text)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, b.String())
	}
	return b.String()
}

func requireContains(t *testing.T, label, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("%s lacks %q:\n%s", label, w, got)
		}
	}
}

func TestMCPOverStdio(t *testing.T) {
	binary(t)
	root := fixture(t)
	cs := mcpClient(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"find_symbol", "search", "repo_map", "file_outline", "read_symbol", "callers", "callees",
		"impact", "imports", "inheritance", "tests_for", "traverse", "status"} {
		if !slices.Contains(names, want) {
			t.Errorf("tool %s not listed (have %v)", want, names)
		}
	}

	// The first call builds the index of an unindexed repository.
	requireContains(t, "find_symbol", callText(t, cs, "find_symbol", map[string]any{"query": "PlaceOrder"}),
		"graphmaster find_symbol", "OrderService.PlaceOrder", "go/internal/service/order.go:24")
	requireContains(t, "callers", callText(t, cs, "callers", map[string]any{"symbol": "example.com/fixture/internal/store.NewMemory"}),
		"TestPlaceOrder", "NewAudited")
	requireContains(t, "impact", callText(t, cs, "impact", map[string]any{"symbol": "example.com/fixture/internal/store.NewMemory"}),
		"go/internal/service/order_test.go", "affected tests")
	requireContains(t, "read_symbol", callText(t, cs, "read_symbol", map[string]any{"symbol": "example.com/fixture/internal/store.NewMemory"}),
		"func NewMemory() *Memory")

	// Edit a file behind the server's back; the next call must see it.
	writeFile(t, filepath.Join(root, "go", "internal", "store", "pool.go"),
		"package store\n\n// NewPoolQuokka builds a pooled store.\nfunc NewPoolQuokka() *Memory { return NewMemory() }\n")
	requireContains(t, "find_symbol after edit", callText(t, cs, "find_symbol", map[string]any{"query": "NewPoolQuokka"}),
		"fresh", "go/internal/store/pool.go:4")
	requireContains(t, "callers after edit", callText(t, cs, "callers", map[string]any{"symbol": "example.com/fixture/internal/store.NewMemory"}),
		"NewPoolQuokka")
	requireContains(t, "read_symbol after edit", callText(t, cs, "read_symbol", map[string]any{"symbol": "NewPoolQuokka"}),
		"return NewMemory()")
}
