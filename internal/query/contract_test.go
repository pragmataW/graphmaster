package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

func TestBanner(t *testing.T) {
	fx := fixtureEngine(t)
	cases := []struct {
		fr   Freshener
		want string
	}{
		{fixedFreshener{index.Result{Added: 1, Modified: 1, Duration: 41 * time.Millisecond}}, "graphmaster status · fresh (+2 files, 41ms)"},
		{fixedFreshener{index.Result{Stale: true, Pending: 3}}, "graphmaster status · ⚠ stale: 3 files pending"},
		{fixedFreshener{index.Result{Stale: true, Building: true, Progress: 37}}, "graphmaster status · ⚠ building 37% (partial graph)"},
		{errFreshener{}, "graphmaster status · ⚠ freshen failed: boom"},
	}
	for _, c := range cases {
		e := New(fx.b, Options{Freshener: c.fr})
		out, err := e.Status(context.Background(), StatusInput{})
		if err != nil {
			t.Fatal(err)
		}
		if got := lines(out)[0]; got != c.want {
			t.Errorf("banner = %q, want %q", got, c.want)
		}
	}
}

type errFreshener struct{}

func (errFreshener) Freshen(context.Context, time.Duration) (*index.Result, error) {
	return nil, errors.New("boom")
}

func TestStatus(t *testing.T) {
	out := runTool(t, fixtureEngine(t), "status", `{}`)
	mustContain(t, out, "graphmaster status · fresh\n", "state: ready", "mode: balanced", "languages: ", "db size: ")
}

func TestTokenCap(t *testing.T) {
	e := fixtureEngine(t)
	calls := []struct{ tool, args string }{
		{"find_symbol", `{"query":"Item","limit":500,"max_tokens":%d}`},
		{"search", `{"query":"order","limit":500,"max_tokens":%d}`},
		{"repo_map", `{"depth":8,"max_tokens":%d}`},
		{"file_outline", `{"path":"go/internal/store/store.go","max_tokens":%d}`},
		{"read_symbol", `{"symbol":"OrderService.PlaceOrder","context_lines":40,"max_tokens":%d}`},
		{"callers", `{"symbol":"store.Item","max_tokens":%d}`},
		{"impact", `{"symbol":"shop.models.Item","max_tokens":%d}`},
		{"traverse", `{"start":"example.com/fixture/cmd/server.main","max_tokens":%d}`},
		{"status", `{"max_tokens":%d}`},
	}
	for _, c := range calls {
		for _, budget := range []int{1, 70, 120} {
			out := runTool(t, e, c.tool, fmt.Sprintf(c.args, budget))
			limit := max(budget, minMaxTokens) * bytesPerToken
			if len(out) > limit {
				t.Errorf("%s max_tokens=%d: %d bytes > %d\n%s", c.tool, budget, len(out), limit, out)
			}
			if !strings.HasPrefix(out, "graphmaster "+c.tool+" · ") {
				t.Errorf("%s: banner missing:\n%s", c.tool, out)
			}
		}
	}
	// Truncated lists say how much is missing.
	out := runTool(t, e, "find_symbol", `{"query":"Item","limit":500,"max_tokens":120}`)
	mustContain(t, out, "more (narrow path_prefix / raise max_tokens)")
	// The cap applies to oversized requests.
	big := runTool(t, e, "search", `{"query":"e","limit":500,"max_tokens":1000000}`)
	if len(big) > MaxTokensCap*bytesPerToken {
		t.Errorf("max_tokens cap not applied: %d bytes", len(big))
	}
	// Default budget.
	def := runTool(t, e, "search", `{"query":"e","limit":500}`)
	if len(def) > DefaultMaxTokens*bytesPerToken {
		t.Errorf("default budget exceeded: %d bytes", len(def))
	}
}

func TestInvalidArguments(t *testing.T) {
	e := fixtureEngine(t)
	for _, c := range []struct{ tool, args string }{
		{"find_symbol", `{}`},
		{"find_symbol", `{"query":"x","kind":"nonsense"}`},
		{"find_symbol", `{"query":"x","lang":"cobol"}`},
		{"search", `{"query":"  "}`},
		{"file_outline", `{}`},
		{"read_symbol", `{}`},
		{"impact", `{}`},
		{"impact", `{"symbol":"Put","changed":true}`},
		{"imports", `{"path":"a","module":"b"}`},
		{"imports", `{"path":"go/cmd/server/main.go","direction":"sideways"}`},
		{"inheritance", `{"symbol":"Animal","direction":"left"}`},
		{"traverse", `{"start":"main","edge_kinds":["teleports"]}`},
		{"traverse", `{"start":"main","direction":"up"}`},
		{"callers", `{"symbol":1}`},
	} {
		tl, _ := Lookup(c.tool)
		_, err := tl.RunJSON(context.Background(), e, []byte(c.args))
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s %s: err = %v, want ErrInvalidArgument", c.tool, c.args, err)
		}
	}
}

// TestFreshenBeforeAnswer uses the real indexer as freshener: an edit made
// after the first query is visible in the next answer.
func TestFreshenBeforeAnswer(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIndexed(t, dir)
	e := New(ix, Options{})

	out := runTool(t, e, "find_symbol", `{"query":"Brandnew","exact":true}`)
	mustContain(t, out, `no definitions match "Brandnew"`)
	mustContain(t, runTool(t, e, "repo_map", `{"path_prefix":"go/internal/util","depth":1}`), "go/internal/util/  2 files,")

	src := "package util\n\n// Brandnew is added by the test.\nfunc Brandnew() string { return FormatID(\"x\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "go/internal/util/brandnew.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out = runTool(t, e, "callers", `{"symbol":"util.FormatID"}`)
	if !strings.HasPrefix(out, "graphmaster callers · fresh (+1 files, ") {
		t.Errorf("banner does not report the reindex:\n%s", out)
	}
	mustContain(t, out, "function util.Brandnew  go/internal/util/brandnew.go:4-4")
	// The cached files table is refreshed after the reindex.
	mustContain(t, runTool(t, e, "repo_map", `{"path_prefix":"go/internal/util","depth":1}`), "go/internal/util/  3 files,")
}

func TestToolsRegistry(t *testing.T) {
	want := []string{"find_symbol", "search", "repo_map", "file_outline", "read_symbol", "callers",
		"callees", "impact", "imports", "inheritance", "tests_for", "traverse", "status"}
	got := Tools()
	if len(got) != len(want) {
		t.Fatalf("got %d tools, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name || got[i].Description == "" {
			t.Errorf("tool %d = %q (%q), want %q", i, got[i].Name, got[i].Description, name)
		}
		if _, ok := Lookup(name); !ok {
			t.Errorf("Lookup(%q) failed", name)
		}
	}
	if _, ok := Lookup("grep"); ok {
		t.Error("Lookup(grep) succeeded")
	}
}
