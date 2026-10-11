// Package perfgate compares benchmark results with performance thresholds.
//
// It reads two kinds of results: `go test -bench` output (benchmark name,
// iteration count, then value/unit pairs) and the JSON lines written by
// bench/k8s.sh (RESULTS). Thresholds live in a JSON file of named profiles
// (bench/thresholds.json), so CI runners and a developer machine can be held
// to different, documented limits.
package perfgate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Limit is one threshold value: a plain number in the metric's unit, or a
// Go duration string ("150ms") converted to the metric's time unit.
type Limit struct {
	Number   float64
	Duration time.Duration
}

// UnmarshalJSON accepts a number or a duration string.
func (l *Limit) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("limit %s: %w", s, err)
		}
		*l = Limit{Duration: d}
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("limit %s: want a number or a duration string", b)
	}
	*l = Limit{Number: f}
	return nil
}

// In returns the limit expressed in unit. A duration converts to
// nanoseconds for "ns/op", milliseconds for units ending in "ms" and seconds
// for "s" or units ending in "_s"; any other unit takes plain numbers only.
func (l *Limit) In(unit string) (float64, error) {
	if l.Duration == 0 {
		return l.Number, nil
	}
	switch {
	case unit == "ns/op":
		return float64(l.Duration.Nanoseconds()), nil
	case strings.HasSuffix(unit, "ms"):
		return float64(l.Duration) / float64(time.Millisecond), nil
	case unit == "s" || strings.HasSuffix(unit, "_s"):
		return l.Duration.Seconds(), nil
	}
	return 0, fmt.Errorf("duration limit %v on metric %q, which is not a time unit", l.Duration, unit)
}

func (l *Limit) String() string {
	if l.Duration != 0 {
		return l.Duration.String()
	}
	return strconv.FormatFloat(l.Number, 'g', -1, 64)
}

// Bound is an inclusive range; either side may be absent.
type Bound struct {
	Min *Limit `json:"min,omitempty"`
	Max *Limit `json:"max,omitempty"`
}

// Profile holds the limits of one environment.
type Profile struct {
	// K8s maps a bench/k8s.sh RESULTS field (full_s, noop_s, ...) to its bound.
	K8s map[string]Bound `json:"k8s"`
	// Bench maps a benchmark name (without the -GOMAXPROCS suffix) to its
	// metrics; a metric key is a unit ("ns/op", "go-edit-ms") or a
	// path.Match pattern over units ("*-ms").
	Bench map[string]map[string]Bound `json:"bench"`
}

// Config is the thresholds file.
type Config struct {
	Profiles map[string]Profile `json:"profiles"`
}

// ParseConfig decodes a thresholds file. Top-level keys other than
// "profiles" (such as "_doc") are ignored.
func ParseConfig(r io.Reader) (*Config, error) {
	var c Config
	if err := json.NewDecoder(r).Decode(&c); err != nil {
		return nil, fmt.Errorf("thresholds: %w", err)
	}
	if len(c.Profiles) == 0 {
		return nil, errors.New("thresholds: no profiles")
	}
	return &c, nil
}

// BenchResults maps benchmark name to unit to every reported value.
type BenchResults map[string]map[string][]float64

// Value returns the median of the values reported for name and unit.
func (r BenchResults) Value(name, unit string) (float64, bool) {
	vs := r[name][unit]
	if len(vs) == 0 {
		return 0, false
	}
	return median(vs), true
}

func median(vs []float64) float64 {
	s := slices.Clone(vs)
	slices.Sort(s)
	if n := len(s); n%2 == 0 {
		return (s[n/2-1] + s[n/2]) / 2
	}
	return s[len(s)/2]
}

// procSuffix is the -GOMAXPROCS suffix go test appends to benchmark names.
var procSuffix = regexp.MustCompile(`-\d+$`)

// ParseBench reads `go test -bench` output; non-benchmark lines are skipped.
// Repeated runs (-count) of a benchmark accumulate.
func ParseBench(r io.Reader) (BenchResults, error) {
	out := BenchResults{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.HasPrefix(f[0], "Benchmark") {
			continue
		}
		if _, err := strconv.ParseInt(f[1], 10, 64); err != nil {
			continue // a log line that starts with a benchmark name
		}
		if len(f)%2 != 0 {
			return nil, fmt.Errorf("malformed benchmark line: %q", sc.Text())
		}
		name := procSuffix.ReplaceAllString(f[0], "")
		m := out[name]
		if m == nil {
			m = map[string][]float64{}
			out[name] = m
		}
		for i := 2; i < len(f); i += 2 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				return nil, fmt.Errorf("malformed benchmark value %q in %q", f[i], sc.Text())
			}
			m[f[i+1]] = append(m[f[i+1]], v)
		}
	}
	return out, sc.Err()
}

// Merge adds the values of other to r.
func (r BenchResults) Merge(other BenchResults) {
	for name, units := range other {
		if r[name] == nil {
			r[name] = map[string][]float64{}
		}
		for u, vs := range units {
			r[name][u] = append(r[name][u], vs...)
		}
	}
}

// ReadK8s returns the numeric fields of the last JSON line of a
// bench/k8s.sh RESULTS file (the script appends one line per run).
func ReadK8s(r io.Reader) (map[string]float64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var last string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	if last == "" {
		return nil, errors.New("k8s results: empty")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(last), &raw); err != nil {
		return nil, fmt.Errorf("k8s results: %w", err)
	}
	out := map[string]float64{}
	for k, v := range raw {
		if f, ok := v.(float64); ok {
			out[k] = f
		}
	}
	return out, nil
}

// Check is the outcome of one metric against its bound.
type Check struct {
	Source  string  // "k8s" or "bench"
	Name    string  // benchmark name, or "k8s"
	Metric  string  // unit or RESULTS field
	Value   float64 // median measured value
	Min     float64
	Max     float64
	HasMin  bool
	HasMax  bool
	Missing bool // no result for a configured limit
	OK      bool
}

// Report is the result of checking one profile.
type Report struct {
	Profile string
	Checks  []Check
}

// OK reports whether every check passed.
func (r *Report) OK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Failures returns the failed checks.
func (r *Report) Failures() []Check {
	var out []Check
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c)
		}
	}
	return out
}

// Check compares results with the limits of profile. A nil k8s or bench
// argument skips that source; a configured limit without a result in a
// checked source fails, so a skipped or crashed benchmark cannot pass.
func (c *Config) Check(profile string, k8s map[string]float64, bench BenchResults) (*Report, error) {
	p, ok := c.Profiles[profile]
	if !ok {
		names := make([]string, 0, len(c.Profiles))
		for n := range c.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown profile %q (have %s)", profile, strings.Join(names, ", "))
	}
	rep := &Report{Profile: profile}
	if k8s != nil {
		for _, field := range sortedKeys(p.K8s) {
			v, ok := k8s[field]
			chk, err := evaluate("k8s", "k8s", field, v, ok, p.K8s[field])
			if err != nil {
				return nil, err
			}
			rep.Checks = append(rep.Checks, chk)
		}
	}
	if bench != nil {
		for _, name := range sortedKeys(p.Bench) {
			metrics := p.Bench[name]
			for _, pat := range sortedKeys(metrics) {
				units, err := matchUnits(bench[name], pat)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", name, err)
				}
				if len(units) == 0 {
					chk, err := evaluate("bench", name, pat, 0, false, metrics[pat])
					if err != nil {
						return nil, fmt.Errorf("%s: %w", name, err)
					}
					rep.Checks = append(rep.Checks, chk)
					continue
				}
				for _, u := range units {
					v, ok := bench.Value(name, u)
					chk, err := evaluate("bench", name, u, v, ok, metrics[pat])
					if err != nil {
						return nil, fmt.Errorf("%s: %w", name, err)
					}
					rep.Checks = append(rep.Checks, chk)
				}
			}
		}
	}
	return rep, nil
}

// matchUnits returns the units of got that pat names, sorted.
func matchUnits(got map[string][]float64, pat string) ([]string, error) {
	if _, err := path.Match(pat, ""); err != nil {
		return nil, fmt.Errorf("metric pattern %q: %w", pat, err)
	}
	var out []string
	for u := range got {
		if ok, _ := path.Match(pat, u); ok {
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out, nil
}

func evaluate(source, name, metric string, v float64, present bool, b Bound) (Check, error) {
	chk := Check{Source: source, Name: name, Metric: metric, Value: v, Missing: !present, OK: present}
	if b.Min != nil {
		m, err := b.Min.In(metric)
		if err != nil {
			return chk, err
		}
		chk.Min, chk.HasMin = m, true
		if present && v < m {
			chk.OK = false
		}
	}
	if b.Max != nil {
		m, err := b.Max.In(metric)
		if err != nil {
			return chk, err
		}
		chk.Max, chk.HasMax = m, true
		if present && v > m {
			chk.OK = false
		}
	}
	if !chk.HasMin && !chk.HasMax {
		return chk, fmt.Errorf("%s %s: bound has neither min nor max", name, metric)
	}
	return chk, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// verdict describes the outcome of c in words.
func (c *Check) verdict() string {
	switch {
	case c.Missing:
		return "no result (benchmark skipped, failed or not run?)"
	case c.HasMax && c.Value > c.Max:
		return fmt.Sprintf("%s > max %s", c.format(c.Value), c.format(c.Max))
	case c.HasMin && c.Value < c.Min:
		return fmt.Sprintf("%s < min %s", c.format(c.Value), c.format(c.Min))
	}
	return "ok"
}

func (c *Check) limits() string {
	var parts []string
	if c.HasMin {
		parts = append(parts, ">= "+c.format(c.Min))
	}
	if c.HasMax {
		parts = append(parts, "<= "+c.format(c.Max))
	}
	return strings.Join(parts, ", ")
}

// value renders the measured value.
func (c *Check) value() string {
	if c.Missing {
		return "-"
	}
	return c.format(c.Value)
}

// format renders f in the check's unit, with a readable duration for ns/op.
func (c *Check) format(f float64) string {
	if c.Metric == "ns/op" {
		return num(f) + " (" + time.Duration(f).Round(10*time.Microsecond).String() + ")"
	}
	return num(f)
}

func num(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', 6, 64)
}

// WriteText prints one line per check and a summary naming every failure.
func (r *Report) WriteText(w io.Writer) {
	for _, c := range r.Checks {
		status := "ok  "
		if !c.OK {
			status = "FAIL"
		}
		fmt.Fprintf(w, "%s %-5s %-32s %-19s %-28s limit %s\n", status, c.Source, c.Name, c.Metric, c.value(), c.limits())
	}
	fails := r.Failures()
	if len(fails) == 0 {
		fmt.Fprintf(w, "perfgate: profile %s: all %d checks passed\n", r.Profile, len(r.Checks))
		return
	}
	fmt.Fprintf(w, "perfgate: profile %s: %d of %d checks FAILED:\n", r.Profile, len(fails), len(r.Checks))
	for _, c := range fails {
		fmt.Fprintf(w, "  FAIL %s %s %s: %s\n", c.Source, c.Name, c.Metric, c.verdict())
	}
}

// WriteMarkdown renders the report as a table (for $GITHUB_STEP_SUMMARY).
func (r *Report) WriteMarkdown(w io.Writer) {
	fmt.Fprintf(w, "### perfgate: profile `%s`\n\n", r.Profile)
	fmt.Fprintln(w, "| | source | benchmark | metric | value | limit | verdict |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
	for _, c := range r.Checks {
		mark := "pass"
		if !c.OK {
			mark = "**FAIL**"
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s |\n", mark, c.Source, c.Name, c.Metric, c.value(), c.limits(), c.verdict())
	}
	fmt.Fprintln(w)
}

// DropAbsent removes bench checks of benchmarks that have no result at all
// in bench, for runs of a subset of the gates. Missing metrics of a
// benchmark that did run still fail.
func (r *Report) DropAbsent(bench BenchResults) {
	r.Checks = slices.DeleteFunc(r.Checks, func(c Check) bool {
		return c.Source == "bench" && c.Missing && len(bench[c.Name]) == 0
	})
}
