// Package replay replays captured agent hook payloads (testdata/hooks)
// through the real hook dispatcher and compares what graphmaster answers
// with golden files. The non-test code here only loads fixtures, expands
// their placeholders and normalizes output; the scenarios live in the
// package tests.
package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/hookrun"
)

// HooksDir returns the absolute path of testdata/hooks.
func HooksDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("replay: cannot locate source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "hooks")
}

// Load reads one fixture. rel is relative to HooksDir, for example
// "claude/real-session-start-1.json".
func Load(rel string) (*hookrun.Recording, error) {
	b, err := os.ReadFile(filepath.Join(HooksDir(), filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	var rec hookrun.Recording
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("replay: %s: %w", rel, err)
	}
	if rec.Agent == "" || rec.Event == "" {
		return nil, fmt.Errorf("replay: %s: agent and event are required", rel)
	}
	if len(rec.Stdin) == 0 && rec.StdinText == "" {
		return nil, fmt.Errorf("replay: %s: no stdin or stdin_text", rel)
	}
	return &rec, nil
}

// placeholder matches {{NAME}} tokens as written by the recorder.
var placeholder = regexp.MustCompile(`\{\{([A-Z][A-Z0-9_]*)\}\}`)

// Placeholders returns the sorted distinct placeholder names in s.
func Placeholders(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// Expand replaces every {{NAME}} in s with vars[NAME]. A placeholder
// without a value is an error, so a typo in a fixture cannot silently
// reach the dispatcher.
func Expand(s string, vars map[string]string) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(s, func(tok string) string {
		name := tok[2 : len(tok)-2]
		v, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return tok
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("replay: no value for placeholder(s) %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// ExpandJSON is Expand for JSON text: values are escaped as JSON string
// content, which keeps Windows paths and quotes valid inside the payload.
func ExpandJSON(raw []byte, vars map[string]string) ([]byte, error) {
	esc := make(map[string]string, len(vars))
	for k, v := range vars {
		b, _ := json.Marshal(v) // strings always marshal
		esc[k] = string(b[1 : len(b)-1])
	}
	s, err := Expand(string(raw), esc)
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(s)) {
		return nil, errors.New("replay: expanded payload is not valid JSON")
	}
	return []byte(s), nil
}

// Stdin returns the fixture's payload with placeholders expanded.
func Stdin(rec *hookrun.Recording, vars map[string]string) ([]byte, error) {
	if len(rec.Stdin) > 0 {
		return ExpandJSON(rec.Stdin, vars)
	}
	s, err := Expand(rec.StdinText, vars)
	return []byte(s), err
}

// Environ returns the fixture's env snapshot (placeholders expanded) in
// os.Environ form, sorted by name.
func Environ(rec *hookrun.Recording, vars map[string]string) ([]string, error) {
	keys := make([]string, 0, len(rec.Env))
	for k := range rec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := Expand(rec.Env[k], vars)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		out = append(out, k+"="+v)
	}
	return out, nil
}

// durations match Go time.Duration strings such as 1.2s, 850ms or 3m4s.
const durationRE = `(?:\d+(?:\.\d+)?(?:h|m|s|ms|µs|us|ns))+`

// timings are the run-dependent parts of graphmaster's answers.
var timings = []struct {
	re   *regexp.Regexp
	repl string
}{
	// index.Result.Banner after a foreground build or refresh.
	{regexp.MustCompile(`fresh \(\+(\d+) files?, ` + durationRE + `\)`), "fresh (+$1 files, {{DURATION}})"},
	// A build that is still running when the answer is composed.
	{regexp.MustCompile(`building \d+%`), "building {{PERCENT}}%"},
}

// Normalize makes hook output comparable across runs: machine paths become
// {{REPO}} / {{HOME}} and timings become placeholders.
func Normalize(s string, scrub *hookrun.Scrubber) string {
	if scrub != nil {
		s = scrub.Scrub(s)
	}
	for _, t := range timings {
		s = t.re.ReplaceAllString(s, t.repl)
	}
	return s
}

// Fields flattens a JSON object into "path: value" lines (sorted by path)
// so multi-line strings such as deny reasons read naturally in a golden
// file. Non-object input yields nil.
func Fields(stdout []byte) []string {
	var v any
	if err := json.Unmarshal(stdout, &v); err != nil {
		return nil
	}
	if _, ok := v.(map[string]any); !ok {
		return nil
	}
	var out []string
	flatten("", v, &out)
	sort.Strings(out)
	return out
}

func flatten(path string, v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			flatten(p, e, out)
		}
	case []any:
		for i, e := range x {
			flatten(fmt.Sprintf("%s[%d]", path, i), e, out)
		}
	case string:
		if strings.Contains(x, "\n") {
			*out = append(*out, path+": |\n    "+strings.ReplaceAll(x, "\n", "\n    "))
			return
		}
		*out = append(*out, path+": "+x)
	default:
		b, _ := json.Marshal(x)
		*out = append(*out, path+": "+string(b))
	}
}
