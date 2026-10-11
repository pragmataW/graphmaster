package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pragmataW/graphmaster/internal/query"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// TestMCPCommand runs `graphmaster mcp` over pipes in place of stdio and
// drives it with an SDK client.
func TestMCPCommand(t *testing.T) {
	t.Chdir(testutil.Materialize(t))

	toServerR, toServerW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	root := newRootCmd()
	root.SetIn(toServerR)
	root.SetOut(toClientW)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"mcp"})
	done := make(chan error, 1)
	go func() {
		err := root.Execute()
		_ = toClientW.Close()
		done <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := gomcp.NewClient(&gomcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &gomcp.IOTransport{Reader: toClientR, Writer: toServerW}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if name := cs.InitializeResult().ServerInfo.Name; name != "graphmaster" {
		t.Errorf("server name = %q", name)
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != len(query.Tools()) {
		t.Errorf("listed %d tools, want %d", len(tools.Tools), len(query.Tools()))
	}
	res, err := cs.CallTool(ctx, &gomcp.CallToolParams{
		Name:      "find_symbol",
		Arguments: map[string]any{"query": "PlaceOrder", "lang": "go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Content[0].(*gomcp.TextContent).Text
	if res.IsError || !strings.HasPrefix(out, "graphmaster find_symbol · ") || !strings.Contains(out, "PlaceOrder") {
		t.Errorf("find_symbol:\n%s", out)
	}

	// Closing the client ends the server cleanly.
	if err := cs.Close(); err != nil {
		t.Logf("client close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("mcp command: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("mcp command did not exit after the client closed")
	}
}
