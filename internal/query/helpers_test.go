package query

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixedFreshener returns a constant result so banners are deterministic.
type fixedFreshener struct{ res index.Result }

func (f fixedFreshener) Freshen(context.Context, time.Duration) (*index.Result, error) {
	r := f.res
	return &r, nil
}

// openIndexed indexes dir with the real resolver and returns the indexer.
func openIndexed(t testing.TB, dir string) *index.Indexer {
	t.Helper()
	ix, err := index.Open(context.Background(), dir, index.Options{
		Resolver: &resolve.Resolver{},
		Logf:     func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	if _, err := ix.Freshen(context.Background(), time.Minute); err != nil {
		t.Fatalf("Freshen: %v", err)
	}
	return ix
}

var (
	fixtureOnce sync.Once
	fixtureDir  string
	fixtureIx   *index.Indexer
	fixtureErr  error
)

// fixtureEngine returns an engine over the materialized fixture repository,
// shared by all tests of the package (read-only use only).
func fixtureEngine(t testing.TB) *Engine {
	t.Helper()
	fixtureOnce.Do(func() {
		fixtureDir, fixtureErr = os.MkdirTemp("", "gm-query-fixture-")
		if fixtureErr != nil {
			return
		}
		if fixtureErr = testutil.MaterializeTo(filepath.Join(fixtureDir, "repo")); fixtureErr != nil {
			return
		}
		fixtureIx, fixtureErr = index.Open(context.Background(), filepath.Join(fixtureDir, "repo"),
			index.Options{Resolver: &resolve.Resolver{}, Logf: func(string, ...any) {}})
		if fixtureErr != nil {
			return
		}
		_, fixtureErr = fixtureIx.Freshen(context.Background(), time.Minute)
	})
	if fixtureErr != nil {
		t.Fatalf("fixture: %v", fixtureErr)
	}
	return New(fixtureIx, Options{Freshener: fixedFreshener{}})
}

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if fixtureIx != nil {
		_ = fixtureIx.Close()
	}
	if fixtureDir != "" {
		_ = os.RemoveAll(fixtureDir)
	}
	os.Exit(code)
}

// runTool runs a registered tool with JSON arguments.
func runTool(t testing.TB, e *Engine, name, args string) string {
	t.Helper()
	tl, ok := Lookup(name)
	if !ok {
		t.Fatalf("unknown tool %s", name)
	}
	out, err := tl.RunJSON(context.Background(), e, []byte(args))
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

// checkGolden compares got with testdata/golden/<name>.txt.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch (run with -update to accept)\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// lines counts output lines (for assertions on structure).
func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}
