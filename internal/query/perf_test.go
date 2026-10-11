package query

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// queryP95Target is the PLAN latency target for one tool call.
const queryP95Target = 50 * time.Millisecond

// TestQueryLatency measures per-tool p95 latency on a synthetic repository
// and reports it. GRAPHMASTER_QUERY_PERF_FILES sets the size (default
// 3000 files); GRAPHMASTER_QUERY_PERF_STRICT=1 enforces the 50ms target
// (otherwise only a 10x safety ceiling fails, so -race and loaded CI
// machines do not flake).
func TestQueryLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("perf test")
	}
	files := 3000
	if v, err := strconv.Atoi(os.Getenv("GRAPHMASTER_QUERY_PERF_FILES")); err == nil && v > 0 {
		files = v
	}
	dir := testutil.MaterializeSynth(t, 7, files, 40, []model.Lang{model.LangGo, model.LangPython, model.LangTypeScript})
	start := time.Now()
	ix := openIndexed(t, dir)
	t.Logf("indexed %d synthetic files in %v", files, time.Since(start).Round(time.Millisecond))
	e := New(ix, Options{Freshener: fixedFreshener{}})

	samples := sampleSymbols(t, e, 40)
	type call struct{ tool, args string }
	var calls []call
	for _, s := range samples {
		q := strconv.Quote(s.QName)
		calls = append(calls,
			call{"find_symbol", fmt.Sprintf(`{"query":%q}`, s.Name)},
			call{"search", fmt.Sprintf(`{"query":%q}`, s.Name)},
			call{"file_outline", fmt.Sprintf(`{"path":%q}`, s.Path)},
			call{"read_symbol", fmt.Sprintf(`{"symbol":%s}`, q)},
			call{"callers", fmt.Sprintf(`{"symbol":%s,"depth":2}`, q)},
			call{"callees", fmt.Sprintf(`{"symbol":%s,"depth":2}`, q)},
			call{"impact", fmt.Sprintf(`{"symbol":%s}`, q)},
			call{"imports", fmt.Sprintf(`{"path":%q,"direction":"in"}`, s.Path)},
			call{"inheritance", fmt.Sprintf(`{"symbol":%s}`, q)},
			call{"tests_for", fmt.Sprintf(`{"symbol":%s}`, q)},
			call{"traverse", fmt.Sprintf(`{"start":%s}`, q)},
			call{"repo_map", `{}`},
		)
	}
	lat := map[string][]time.Duration{}
	for round := 0; round < 2; round++ { // round 0 warms caches
		for _, c := range calls {
			t0 := time.Now()
			out := runTool(t, e, c.tool, c.args)
			if round > 0 {
				lat[c.tool] = append(lat[c.tool], time.Since(t0))
			}
			for _, bad := range []string{"no symbol matches", "ambiguous symbol", "no indexed file"} {
				if strings.Contains(out, bad) {
					t.Fatalf("%s %s did not resolve:\n%s", c.tool, c.args, out)
				}
			}
		}
	}

	// Freshen cost on an unchanged tree, paid once per real call.
	var fr []time.Duration
	for i := 0; i < 20; i++ {
		t0 := time.Now()
		if _, err := ix.Freshen(context.Background(), time.Second); err != nil {
			t.Fatal(err)
		}
		fr = append(fr, time.Since(t0))
	}
	t.Logf("freshen (no change)  p50 %-8v p95 %v", pct(fr, 50), pct(fr, 95))

	strict := os.Getenv("GRAPHMASTER_QUERY_PERF_STRICT") == "1"
	for _, tl := range Tools() {
		ds := lat[tl.Name]
		if len(ds) == 0 {
			continue
		}
		p95 := pct(ds, 95)
		t.Logf("%-12s n=%-4d p50 %-8v p95 %-8v max %v", tl.Name, len(ds), pct(ds, 50), p95, pct(ds, 100))
		limit := 10 * queryP95Target
		if strict {
			limit = queryP95Target
		}
		if p95 > limit {
			t.Errorf("%s p95 %v exceeds %v", tl.Name, p95, limit)
		}
	}
}

// sampleSymbols picks n callable definitions spread over the repository.
func sampleSymbols(t *testing.T, e *Engine, n int) []*sym {
	t.Helper()
	q := e.querier()
	ss, err := q.symsWhere(context.Background(), fmt.Sprintf(`n.kind IN (%d, %d) ORDER BY f.path, n.start_line`,
		model.KindFunction, model.KindMethod))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) == 0 {
		t.Fatal("no symbols in synthetic repository")
	}
	step := max(len(ss)/n, 1)
	var out []*sym
	for i := 0; i < len(ss) && len(out) < n; i += step {
		out = append(out, ss[i])
	}
	return out
}

func pct(ds []time.Duration, p int) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	return s[max(min(i, len(s))-1, 0)].Round(10 * time.Microsecond)
}
