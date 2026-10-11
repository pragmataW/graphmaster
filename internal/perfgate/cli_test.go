package perfgate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliThresholds = `{"profiles":{"ci":{
  "k8s":   {"full_s": {"max": 60}, "noop_s": {"max": "300ms"}},
  "bench": {"BenchmarkMultiFileEdit": {"ns/op": {"max": "1s"}}}
}}}`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunPass(t *testing.T) {
	th := writeTemp(t, "th.json", cliThresholds)
	k8s := writeTemp(t, "k8s.jsonl", `{"full_s":33.1,"noop_s":0.06}`+"\n")
	bench := writeTemp(t, "b.txt", "BenchmarkMultiFileEdit-10  3  400000000 ns/op\n")
	summary := filepath.Join(t.TempDir(), "summary.md")
	jsonOut := filepath.Join(t.TempDir(), "report.json")
	var out, errb bytes.Buffer
	code := Run([]string{"-thresholds", th, "-profile", "ci", "-k8s", k8s,
		"-summary", summary, "-json", jsonOut, bench}, &out, &errb) // positional bench file
	if code != ExitOK {
		t.Fatalf("code %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "all 3 checks passed") {
		t.Errorf("stdout:\n%s", out.String())
	}
	md, err := os.ReadFile(summary)
	if err != nil || !strings.Contains(string(md), "BenchmarkMultiFileEdit") {
		t.Errorf("summary %q, %v", md, err)
	}
	var rep Report
	data, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &rep); err != nil || len(rep.Checks) != 3 {
		t.Errorf("json report %s: %v", data, err)
	}
}

func TestRunFail(t *testing.T) {
	th := writeTemp(t, "th.json", cliThresholds)
	k8s := writeTemp(t, "k8s.jsonl", `{"full_s":33.1,"noop_s":0.45}`+"\n")
	var out, errb bytes.Buffer
	if code := Run([]string{"-thresholds", th, "-profile", "ci", "-k8s", k8s}, &out, &errb); code != ExitFail {
		t.Fatalf("code %d, want %d\n%s%s", code, ExitFail, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "FAIL k8s k8s noop_s: 0.45 > max 0.3") {
		t.Errorf("failure is not named clearly:\n%s", out.String())
	}
}

func TestRunMissingBenchmarkFails(t *testing.T) {
	th := writeTemp(t, "th.json", cliThresholds)
	bench := writeTemp(t, "b.txt", "--- FAIL: BenchmarkMultiFileEdit\nFAIL\n")
	var out, errb bytes.Buffer
	if code := Run([]string{"-thresholds", th, "-profile", "ci", "-bench", bench}, &out, &errb); code != ExitFail {
		t.Fatalf("code %d, want %d", code, ExitFail)
	}
	if !strings.Contains(out.String(), "no result") {
		t.Errorf("stdout:\n%s", out.String())
	}
}

func TestRunUsageErrors(t *testing.T) {
	th := writeTemp(t, "th.json", cliThresholds)
	for name, args := range map[string][]string{
		"no inputs":       {"-thresholds", th, "-profile", "ci"},
		"unknown profile": {"-thresholds", th, "-profile", "x", "-k8s", writeTemp(t, "k.jsonl", `{"full_s":1}`)},
		"missing file":    {"-thresholds", th, "-profile", "ci", "-bench", filepath.Join(t.TempDir(), "nope")},
		"bad flag":        {"-nope"},
		"bad thresholds":  {"-thresholds", writeTemp(t, "bad.json", "{"), "-k8s", th},
	} {
		var out, errb bytes.Buffer
		if code := Run(args, &out, &errb); code != ExitUsage {
			t.Errorf("%s: code %d, want %d (stderr %s)", name, code, ExitUsage, errb.String())
		}
	}
}

func TestRunPartial(t *testing.T) {
	th := writeTemp(t, "th.json", `{"profiles":{"ci":{"bench":{
	  "BenchmarkMultiFileEdit": {"ns/op": {"max": "1s"}},
	  "BenchmarkFullBuildSynth": {"ns/op": {"max": "60s"}}}}}}`)
	bench := writeTemp(t, "b.txt", "BenchmarkMultiFileEdit-10  3  400000000 ns/op\n")
	var out, errb bytes.Buffer
	if code := Run([]string{"-thresholds", th, "-profile", "ci", "-bench", bench}, &out, &errb); code != ExitFail {
		t.Fatalf("strict: code %d, want %d", code, ExitFail)
	}
	out.Reset()
	if code := Run([]string{"-thresholds", th, "-profile", "ci", "-bench", bench, "-partial"}, &out, &errb); code != ExitOK {
		t.Fatalf("partial: code %d, want %d\n%s", code, ExitOK, out.String())
	}
}
