package perfgate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const benchOut = `goos: darwin
goarch: arm64
pkg: github.com/pragmataW/graphmaster/internal/index
cpu: Apple M4
BenchmarkFreshenNoChange/warm-10         	      50	  21000000 ns/op
BenchmarkFreshenNoChange/cold-10         	      20	  90000000 ns/op
BenchmarkFreshenNoChange/cold-10         	      20	 120000000 ns/op
BenchmarkFreshenNoChange/cold-10         	      20	 100000000 ns/op
    bench_test.go:47: some log line
BenchmarkIncrementalSynth-10   	       1	9000000000 ns/op	        12.00 go-edit-ms	        40.00 java-add-ms
BenchmarkMultiFileEdit	       3	 400000000 ns/op	       120.0 refs/restore	   1024 B/op	      7 allocs/op
PASS
ok  	github.com/pragmataW/graphmaster/internal/index	12.3s
`

func TestParseBench(t *testing.T) {
	got, err := ParseBench(strings.NewReader(benchOut))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, unit string
		want       float64
	}{
		{"BenchmarkFreshenNoChange/warm", "ns/op", 21e6},
		{"BenchmarkFreshenNoChange/cold", "ns/op", 100e6}, // median of three runs
		{"BenchmarkIncrementalSynth", "go-edit-ms", 12},
		{"BenchmarkIncrementalSynth", "java-add-ms", 40},
		{"BenchmarkMultiFileEdit", "ns/op", 400e6}, // no GOMAXPROCS suffix
		{"BenchmarkMultiFileEdit", "refs/restore", 120},
		{"BenchmarkMultiFileEdit", "allocs/op", 7},
	}
	for _, c := range cases {
		v, ok := got.Value(c.name, c.unit)
		if !ok || v != c.want {
			t.Errorf("%s %s = %v, %v; want %v", c.name, c.unit, v, ok, c.want)
		}
	}
	if _, ok := got.Value("BenchmarkFreshenNoChange", "ns/op"); ok {
		t.Error("parent benchmark without a result line must not be reported")
	}
}

func TestParseBenchRejectsMalformedLine(t *testing.T) {
	_, err := ParseBench(strings.NewReader("BenchmarkX-4  10  12 ns/op 99\n"))
	if err == nil {
		t.Fatal("want error for odd value/unit pairs")
	}
}

func TestLimitUnmarshal(t *testing.T) {
	var b struct {
		A Limit `json:"a"`
		B Limit `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":60,"b":"150ms"}`), &b); err != nil {
		t.Fatal(err)
	}
	if b.A.Number != 60 || b.A.Duration != 0 {
		t.Errorf("a = %+v", b.A)
	}
	if b.B.Duration.String() != "150ms" {
		t.Errorf("b = %+v", b.B)
	}
	for _, unit := range []struct {
		unit string
		want float64
	}{{"ns/op", 150e6}, {"go-edit-ms", 150}, {"full_s", 0.15}, {"s", 0.15}} {
		v, err := b.B.In(unit.unit)
		if err != nil || v != unit.want {
			t.Errorf("150ms in %s = %v, %v; want %v", unit.unit, v, err, unit.want)
		}
	}
	if _, err := b.B.In("refs/restore"); err == nil {
		t.Error("a duration limit on a unitless metric must be an error")
	}
	if err := json.Unmarshal([]byte(`{"a":"soon"}`), &b); err == nil {
		t.Error("want error for an unparsable duration")
	}
}

func TestReadK8sTakesLastLine(t *testing.T) {
	in := `{"version":"v1.34.0","full_s":50.1,"noop_s":0.2}
{"version":"v1.34.0","full_s":41.5,"noop_s":0.15,"edit_s":1.2,"restore_s":0.9,"db_bytes":123}

`
	got, err := ReadK8s(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if got["full_s"] != 41.5 || got["db_bytes"] != 123 {
		t.Errorf("got %v", got)
	}
	if _, ok := got["version"]; ok {
		t.Error("non-numeric fields must be skipped")
	}
	if _, err := ReadK8s(strings.NewReader("\n")); err == nil {
		t.Error("want error for empty results")
	}
	if _, err := ReadK8s(strings.NewReader("{not json")); err == nil {
		t.Error("want error for malformed results")
	}
}

func testConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := ParseConfig(strings.NewReader(`{
	  "_doc": "ignored",
	  "profiles": {
	    "ci": {
	      "k8s":   {"full_s": {"max": 60}, "noop_s": {"max": "300ms"}, "db_bytes": {"max": 1000}},
	      "bench": {
	        "BenchmarkFreshenNoChange/cold": {"ns/op": {"max": "150ms"}},
	        "BenchmarkIncrementalSynth":     {"*-ms": {"max": 30}},
	        "BenchmarkMultiFileEdit":        {"refs/restore": {"min": 1, "max": 500}}
	      }
	    }
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCheckPassesAndFails(t *testing.T) {
	cfg := testConfig(t)
	bench, err := ParseBench(strings.NewReader(benchOut))
	if err != nil {
		t.Fatal(err)
	}
	k8s := map[string]float64{"full_s": 72.4, "noop_s": 0.12, "db_bytes": 900}
	rep, err := cfg.Check("ci", k8s, bench)
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{}
	for _, c := range rep.Checks {
		if !c.OK {
			failed[c.Source+" "+c.Name+" "+c.Metric] = true
		}
	}
	want := map[string]bool{
		"k8s k8s full_s": true, // 72.4 > 60
		"bench BenchmarkIncrementalSynth java-add-ms": true, // 40 > 30
	}
	if len(failed) != len(want) {
		t.Errorf("failed = %v, want %v", failed, want)
	}
	for k := range want {
		if !failed[k] {
			t.Errorf("missing failure %q (failed: %v)", k, failed)
		}
	}
	if rep.OK() {
		t.Error("report must fail")
	}
	var buf bytes.Buffer
	rep.WriteText(&buf)
	out := buf.String()
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "full_s") || !strings.Contains(out, "72.4 > max 60") {
		t.Errorf("text report lacks a clear failure:\n%s", out)
	}
	buf.Reset()
	rep.WriteMarkdown(&buf)
	if !strings.Contains(buf.String(), "| k8s |") {
		t.Errorf("markdown report:\n%s", buf.String())
	}
}

func TestCheckMissingResultsFail(t *testing.T) {
	cfg := testConfig(t)
	rep, err := cfg.Check("ci", map[string]float64{}, BenchResults{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() {
		t.Fatal("missing results must fail")
	}
	for _, c := range rep.Checks {
		if c.OK || !c.Missing {
			t.Errorf("check %+v should be a missing failure", c)
		}
	}
	// A nil source is not checked: a k8s-only run ignores bench limits.
	rep, err = cfg.Check("ci", map[string]float64{"full_s": 1, "noop_s": 0.1, "db_bytes": 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Errorf("k8s-only check should pass: %+v", rep.Checks)
	}
}

func TestCheckUnknownProfile(t *testing.T) {
	if _, err := testConfig(t).Check("nope", nil, nil); err == nil || !strings.Contains(err.Error(), "ci") {
		t.Errorf("want error listing known profiles, got %v", err)
	}
}

func TestCheckDurationOnUnitlessMetricIsConfigError(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader(`{"profiles":{"p":{"bench":{"BenchmarkMultiFileEdit":{"refs/restore":{"max":"1s"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	bench, _ := ParseBench(strings.NewReader(benchOut))
	if _, err := cfg.Check("p", nil, bench); err == nil {
		t.Error("want config error")
	}
}

// TestRepoThresholds keeps bench/thresholds.json loadable and its profiles
// covering every gate the nightly workflow runs.
func TestRepoThresholds(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "bench", "thresholds.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := ParseConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan", "macos-ci", "linux-ci"} {
		p, ok := cfg.Profiles[name]
		if !ok {
			t.Fatalf("profile %q missing", name)
		}
		for _, k := range []string{"full_s", "noop_s", "edit_s", "restore_s"} {
			if _, ok := p.K8s[k]; !ok {
				t.Errorf("%s: k8s %s has no limit", name, k)
			}
		}
		for _, b := range []string{"BenchmarkFreshenNoChange/cold", "BenchmarkFullBuildSynth", "BenchmarkIncrementalSynth", "BenchmarkMultiFileEdit"} {
			if _, ok := p.Bench[b]; !ok {
				t.Errorf("%s: %s has no limit", name, b)
			}
		}
		// Every limit must convert into its metric's unit.
		if _, err := cfg.Check(name, map[string]float64{}, BenchResults{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	plan := cfg.Profiles["plan"]
	if v, _ := plan.K8s["noop_s"].Max.In("noop_s"); v != 0.3 {
		t.Errorf("plan noop_s max = %v, want the 0.3s target", v)
	}
	if v, _ := plan.Bench["BenchmarkFreshenNoChange/cold"]["ns/op"].Max.In("ns/op"); v != 150e6 {
		t.Errorf("plan no-change dirty check max = %v ns, want 150ms", v)
	}
}

func TestDropAbsent(t *testing.T) {
	cfg := testConfig(t)
	bench, err := ParseBench(strings.NewReader("BenchmarkMultiFileEdit-10  3  400000000 ns/op\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := cfg.Check("ci", nil, bench)
	if err != nil {
		t.Fatal(err)
	}
	rep.DropAbsent(bench)
	if len(rep.Checks) != 1 {
		t.Fatalf("checks = %+v, want only BenchmarkMultiFileEdit", rep.Checks)
	}
	// The benchmark ran but lacks the metric: still a failure.
	if c := rep.Checks[0]; c.Name != "BenchmarkMultiFileEdit" || c.OK || !c.Missing {
		t.Errorf("check = %+v", c)
	}
}
