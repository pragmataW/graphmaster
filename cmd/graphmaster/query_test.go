package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/query"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

func TestQueryCommand(t *testing.T) {
	dir := testutil.Materialize(t)
	t.Chdir(dir)

	// The first query builds the index itself.
	out, err := execute(t, "query", "find_symbol", "PlaceOrder", "--lang", "go", "--limit", "1")
	if err != nil {
		t.Fatalf("find_symbol: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "graphmaster find_symbol · fresh (+") {
		t.Errorf("banner:\n%s", out)
	}
	if !strings.Contains(out, "method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32") {
		t.Errorf("result row missing:\n%s", out)
	}

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"query", "callers", "OrderService.PlaceOrder", "--depth", "2"}, "  function server.main  go/cmd/server/main.go:14-24  @19"},
		{[]string{"query", "file-outline", "go/internal/store/store.go", "--include-private=false", "--depth", "1"}, "struct Memory  12-15"},
		{[]string{"query", "search", "user", "--kinds", "class,file", "--path-prefix", "ts/"}, "file ts/src/models/user.ts"},
		{[]string{"query", "impact", "--path", "go/internal/util/format.go"}, "affected tests: 2"},
		{[]string{"query", "traverse", "server.main", "--edge-kinds", "calls", "--max-depth", "1"}, "→calls function util.Mirror"},
		{[]string{"query", "status"}, "state: ready"},
		{[]string{"query", "repo_map", "--depth", "1", "--max-tokens", "64"}, "graphmaster repo_map · fresh"},
	}
	for _, c := range cases {
		out, err := execute(t, c.args...)
		if err != nil {
			t.Errorf("%v: %v\n%s", c.args, err, out)
			continue
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%v: output lacks %q:\n%s", c.args, c.want, out)
		}
	}
	// The CLI prints exactly what the tool returns.
	cli, err := execute(t, "query", "file_outline", "go/internal/util/format.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cli, "function Mirror  21-23  func Mirror(s string) string\n") {
		t.Errorf("unexpected outline:\n%s", cli)
	}
}

func TestQueryCommandErrors(t *testing.T) {
	dir := testutil.Materialize(t)
	t.Chdir(dir)
	if _, err := execute(t, "query", "imports", "--path", "a", "--module", "b"); !errors.Is(err, query.ErrInvalidArgument) {
		t.Errorf("conflicting args: err = %v", err)
	}
	if _, err := execute(t, "query", "status", "extra"); err == nil {
		t.Error("status accepted a positional argument")
	}
	if _, err := execute(t, "query", "nosuchtool"); err == nil || !strings.Contains(err.Error(), "find_symbol") {
		t.Errorf("unknown tool: err = %v", err)
	}
}

func TestQueryHasEveryTool(t *testing.T) {
	cmd := newQueryCmd()
	for _, tl := range query.Tools() {
		sub, _, err := cmd.Find([]string{tl.Name})
		if err != nil || sub.Name() != tl.Name {
			t.Errorf("query %s: %v", tl.Name, err)
		}
	}
}
