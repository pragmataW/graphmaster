package index

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// incrLangs are the languages BenchmarkIncrementalSynth edits, with the
// synthetic path of file j in package 0.
var incrLangs = []struct {
	lang model.Lang
	path func(j int) string
}{
	{model.LangGo, func(j int) string { return "go/p000/m" + pad4(j) + ".go" }},
	{model.LangPython, func(j int) string { return "py/synth/p000/m" + pad4(j) + ".py" }},
	{model.LangTypeScript, func(j int) string { return "ts/src/p000/m" + pad4(j) + ".ts" }},
	{model.LangJavaScript, func(j int) string { return "js/src/p000/m" + pad4(j) + ".js" }},
	{model.LangRust, func(j int) string { return "rust/src/p000/m" + pad4(j) + ".rs" }},
	{model.LangJava, func(j int) string { return "java/src/synth/p000/C" + pad4(j) + ".java" }},
	{model.LangPHP, func(j int) string { return "php/src/P000/C" + pad4(j) + ".php" }},
	{model.LangCSharp, func(j int) string { return "cs/P000/C" + pad4(j) + ".cs" }},
	{model.LangC, func(j int) string { return "c/p000/m" + pad4(j) + ".c" }},
	{model.LangCPP, func(j int) string { return "cpp/p000/m" + pad4(j) + ".cpp" }},
}

func pad4(j int) string {
	s := strconv.Itoa(j)
	return strings.Repeat("0", max(0, 4-len(s))) + s
}

// BenchmarkIncrementalSynth measures incremental Freshen with the real
// resolver on the synthetic repository of BenchmarkFullBuildSynth
// (GRAPHMASTER_BENCH_FULL_FILES x GRAPHMASTER_BENCH_LOC, default 10000 x
// 100 = 1M LOC): per language a body edit of a widely imported file, its
// deletion and its restoration (an imported-file add). Times are reported
// as ms metrics per scenario; GRAPHMASTER_BENCH_LANGS limits the languages
// (comma-separated names), GRAPHMASTER_BENCH_VERIFY=1 compares the graph
// with a from-scratch build after every step (slow; for small sizes).
func BenchmarkIncrementalSynth(b *testing.B) {
	files, loc := 10_000, 100
	if v, err := strconv.Atoi(os.Getenv("GRAPHMASTER_BENCH_FULL_FILES")); err == nil && v > 0 {
		files = v
	}
	if v, err := strconv.Atoi(os.Getenv("GRAPHMASTER_BENCH_LOC")); err == nil && v > 0 {
		loc = v
	}
	var only []string
	if v := os.Getenv("GRAPHMASTER_BENCH_LANGS"); v != "" {
		only = strings.Split(v, ",")
	}
	dir := testutil.MaterializeSynth(b, 1, files, loc, model.AllLangs())
	res := &resolve.Resolver{}
	ix, err := Open(context.Background(), dir, Options{Logf: func(string, ...any) {}, Resolver: res, Now: futureClock})
	if err != nil {
		b.Fatal(err)
	}
	defer ix.Close()
	start := time.Now()
	if _, err := ix.Freshen(context.Background(), 5*time.Minute); err != nil {
		b.Fatal(err)
	}
	b.Logf("initial build: %v", time.Since(start))

	verify := os.Getenv("GRAPHMASTER_BENCH_VERIFY") == "1"
	step := func(name string) time.Duration {
		t0 := time.Now()
		r, err := ix.Freshen(context.Background(), time.Minute)
		if err != nil {
			b.Fatal(err)
		}
		el := time.Since(t0)
		if verify {
			b.StopTimer()
			requireSameDump(b, name, dump(b, ix), scratchDump(b, dir, Options{Resolver: &resolve.Resolver{}}))
			b.StartTimer()
		}
		b.Logf("%-28s %8v  changed=%d parsed=%d rebuilt=%q  resolver names=%d refs=%d deps=%d",
			name, el.Round(time.Millisecond), r.Changed(), r.Parsed, r.Rebuilt, res.Last.Names, res.Last.Refs, res.Last.Dependents)
		return el
	}
	b.ResetTimer()
	for b.Loop() {
		for _, l := range incrLangs {
			if len(only) > 0 && !slices.Contains(only, l.lang.String()) {
				continue
			}
			rel := l.path(1)
			abs := filepath.Join(dir, filepath.FromSlash(rel))
			orig, err := os.ReadFile(abs)
			if err != nil {
				b.Fatal(err)
			}
			edited := strings.Replace(string(orig), "n * 3", "n * 4", 1)
			writeFile(b, dir, rel, edited)
			b.ReportMetric(float64(step(l.lang.String()+" edit").Milliseconds()), l.lang.String()+"-edit-ms")
			removeFile(b, dir, rel)
			b.ReportMetric(float64(step(l.lang.String()+" delete").Milliseconds()), l.lang.String()+"-del-ms")
			writeFile(b, dir, rel, string(orig))
			b.ReportMetric(float64(step(l.lang.String()+" add").Milliseconds()), l.lang.String()+"-add-ms")
		}
	}
}
