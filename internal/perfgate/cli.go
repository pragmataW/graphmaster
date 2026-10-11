package perfgate

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Exit codes of Run.
const (
	ExitOK    = 0
	ExitFail  = 1 // a threshold was exceeded or a result is missing
	ExitUsage = 2 // bad flags, unreadable input or invalid thresholds
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// Run is the perfgate command line (cmd/perfgate and bench/check.sh):
//
//	perfgate [-thresholds bench/thresholds.json] [-profile plan]
//	         [-k8s results.jsonl] [-bench out.txt ...] [-partial]
//	         [-summary file] [-json file] [bench-output.txt ...]
//
// It prints one line per check and exits ExitFail when any check fails.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("perfgate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	thresholds := fs.String("thresholds", "bench/thresholds.json", "thresholds file")
	profile := fs.String("profile", "plan", "thresholds profile (plan, macos-ci, linux-ci, ...)")
	k8sPath := fs.String("k8s", "", "bench/k8s.sh RESULTS file (last JSON line is checked)")
	var benchPaths multiFlag
	fs.Var(&benchPaths, "bench", "`go test -bench` output file (repeatable)")
	summary := fs.String("summary", "", "append a markdown report to this file (e.g. $GITHUB_STEP_SUMMARY)")
	jsonOut := fs.String("json", "", "write the report as JSON to this file")
	partial := fs.Bool("partial", false, "skip limits of benchmarks absent from every -bench file (subset runs)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	benchPaths = append(benchPaths, fs.Args()...) // positional args are -bench files
	if *k8sPath == "" && len(benchPaths) == 0 {
		fmt.Fprintln(stderr, "perfgate: nothing to check: pass -k8s and/or -bench")
		return ExitUsage
	}
	rep, bench, err := run(*thresholds, *profile, *k8sPath, benchPaths)
	if err != nil {
		fmt.Fprintf(stderr, "perfgate: %v\n", err)
		return ExitUsage
	}
	if *partial {
		rep.DropAbsent(bench)
	}
	rep.WriteText(stdout)
	if *summary != "" {
		if err := appendFile(*summary, rep.WriteMarkdown); err != nil {
			fmt.Fprintf(stderr, "perfgate: summary: %v\n", err)
			return ExitUsage
		}
	}
	if *jsonOut != "" {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err == nil {
			err = os.WriteFile(*jsonOut, append(data, '\n'), 0o644)
		}
		if err != nil {
			fmt.Fprintf(stderr, "perfgate: json: %v\n", err)
			return ExitUsage
		}
	}
	if !rep.OK() {
		return ExitFail
	}
	return ExitOK
}

func run(thresholds, profile, k8sPath string, benchPaths []string) (*Report, BenchResults, error) {
	f, err := os.Open(thresholds)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := ParseConfig(f)
	_ = f.Close()
	if err != nil {
		return nil, nil, err
	}
	var k8s map[string]float64
	if k8sPath != "" {
		f, err := os.Open(k8sPath)
		if err != nil {
			return nil, nil, err
		}
		k8s, err = ReadK8s(f)
		_ = f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", k8sPath, err)
		}
	}
	var bench BenchResults
	if len(benchPaths) > 0 {
		bench = BenchResults{}
		for _, p := range benchPaths {
			f, err := os.Open(p)
			if err != nil {
				return nil, nil, err
			}
			r, err := ParseBench(f)
			_ = f.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", p, err)
			}
			bench.Merge(r)
		}
	}
	rep, err := cfg.Check(profile, k8s, bench)
	return rep, bench, err
}

func appendFile(path string, write func(io.Writer)) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	write(f)
	return nil
}
