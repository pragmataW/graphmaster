package index

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// BenchmarkFreshenNoChange measures the no-change dirty check (PLAN target:
// < 150ms at 100k files) on a synthetic repository. GRAPHMASTER_BENCH_FILES
// overrides the size (default 100000; the initial build takes a while).
//
//	warm: repeated Freshen in one process (MCP server)
//	cold: Open + Freshen + Close per call (short-lived hook process)
func BenchmarkFreshenNoChange(b *testing.B) {
	n := 100_000
	if v, err := strconv.Atoi(os.Getenv("GRAPHMASTER_BENCH_FILES")); err == nil && v > 0 {
		n = v
	}
	dir := testutil.MaterializeSynth(b, 1, n, 15, []model.Lang{model.LangGo})
	// No resolver: resolution cost is not part of the dirty check.
	opts := Options{Logf: func(string, ...any) {}}
	ix, err := Open(context.Background(), dir, opts)
	if err != nil {
		b.Fatal(err)
	}
	start := time.Now()
	res := freshen(b, ix)
	b.Logf("initial build of %d files: %v (%+v)", n, time.Since(start), *res)
	// Rows recorded within the racy window are re-checked once.
	for i := 0; i < 3 && !res.Clean(); i++ {
		time.Sleep(RacyWindowForBench)
		res = freshen(b, ix)
	}
	if !res.Clean() {
		b.Fatalf("repository did not settle: %+v", res)
	}

	b.Run("warm", func(b *testing.B) {
		for b.Loop() {
			if res := freshen(b, ix); !res.Clean() {
				b.Fatalf("not clean: %+v", res)
			}
		}
	})
	_ = ix.Close()
	b.Run("cold", func(b *testing.B) {
		for b.Loop() {
			ix, err := Open(context.Background(), dir, opts)
			if err != nil {
				b.Fatal(err)
			}
			if res := freshen(b, ix); !res.Clean() {
				b.Fatalf("not clean: %+v", res)
			}
			_ = ix.Close()
		}
	})
}

// RacyWindowForBench lets files written just before the build age past the
// racy window.
const RacyWindowForBench = 2500 * time.Millisecond

// BenchmarkFullBuildSynth measures a cold full index with the real resolver
// (PLAN target: 1M LOC < 60s) on a synthetic repository spread over every
// supported language: GRAPHMASTER_BENCH_FULL_FILES files (default 10000) of
// GRAPHMASTER_BENCH_LOC lines each (default 100). Every iteration indexes
// into a fresh state directory, so nothing is served from cache.db.
func BenchmarkFullBuildSynth(b *testing.B) {
	files, loc := 10_000, 100
	if v, err := strconv.Atoi(os.Getenv("GRAPHMASTER_BENCH_FULL_FILES")); err == nil && v > 0 {
		files = v
	}
	if v, err := strconv.Atoi(os.Getenv("GRAPHMASTER_BENCH_LOC")); err == nil && v > 0 {
		loc = v
	}
	langs := model.AllLangs()
	dir := testutil.MaterializeSynth(b, 1, files, loc, langs)
	b.ResetTimer()
	for b.Loop() {
		res := &resolve.Resolver{}
		ix, err := Open(context.Background(), dir, Options{
			Logf: func(string, ...any) {}, Resolver: res, stateDir: b.TempDir(),
		})
		if err != nil {
			b.Fatal(err)
		}
		start := time.Now()
		r, err := ix.Freshen(context.Background(), time.Minute)
		if err != nil {
			b.Fatal(err)
		}
		el := time.Since(start)
		if !r.FullBuild || r.ParseErrors > 0 {
			b.Fatalf("unexpected build result: %+v", *r)
		}
		var nodes, edges, resolved int
		db := ix.Store().DB()
		_ = db.QueryRow(`SELECT count(*) FROM nodes`).Scan(&nodes)
		_ = db.QueryRow(`SELECT count(*), count(dst) FROM edges WHERE kind <> ?`, int(model.EdgeContains)).Scan(&edges, &resolved)
		b.Logf("%d files x %d LOC (%d LOC): %v (parsed %d; nodes %d, edges %d, resolved %d; resolver %+v)",
			files, loc, files*loc, el, r.Parsed, nodes, edges, resolved, res.Last)
		b.ReportMetric(float64(files*loc)/el.Seconds(), "LOC/s")
		_ = ix.Close()
	}
}
