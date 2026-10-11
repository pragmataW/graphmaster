// Package hookrun runs graphmaster's short-lived hook processes: dispatch,
// dedupe, timeouts and payload recording.
package hookrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RecordEnv names the environment variable that enables payload capture.
// When set to a directory, every hook invocation dumps its raw stdin and a
// scrubbed env snapshot under <dir>/<agent>/<event>-<n>.json.
const RecordEnv = "GRAPHMASTER_RECORD"

// Placeholders substituted for machine-specific paths in recordings.
const (
	HomePlaceholder = "{{HOME}}"
	RepoPlaceholder = "{{REPO}}"
	redacted        = "{{REDACTED}}"
)

// maxRecordSeq bounds the file-number probe so a broken directory cannot
// spin forever.
const maxRecordSeq = 100000

// envPrefixes select the agent-related variables kept in a snapshot.
var envPrefixes = []string{"CLAUDE_", "CODEX_", "CURSOR_", "OPENCODE_"}

// envExact lists individual variables kept in a snapshot.
var envExact = map[string]bool{"TERM_PROGRAM": true}

// envValueAllow lists the variables whose values are kept. Every other kept
// variable is recorded by name only: agent processes inherit account ids,
// session ids and similar identifiers that must not land in fixtures, while
// the variable's presence is all agent detection needs.
var envValueAllow = map[string]bool{
	"TERM_PROGRAM":           true,
	"CLAUDE_PROJECT_DIR":     true,
	"CODEX_HOME":             true,
	"CLAUDE_CODE_ENTRYPOINT": true,
	"CURSOR_VERSION":         true,
	"CURSOR_AGENT":           true,
	"CODEX_SANDBOX":          true,
}

// secretMarkers flag variable names whose values must never be written.
var secretMarkers = []string{"TOKEN", "SECRET", "KEY", "PASSWORD", "PASSWD", "AUTH", "CREDENTIAL", "COOKIE"}

// Recording is the on-disk shape of one captured hook invocation.
type Recording struct {
	Agent string            `json:"agent"`
	Event string            `json:"event"`
	CWD   string            `json:"cwd"`
	Env   map[string]string `json:"env"`
	// Stdin holds the payload when it is valid JSON (the normal case).
	Stdin json.RawMessage `json:"stdin,omitempty"`
	// StdinText holds the payload verbatim when it is not valid JSON.
	StdinText string `json:"stdin_text,omitempty"`
}

// Recorder dumps hook payloads for building replay fixtures. A nil
// *Recorder is a valid no-op.
type Recorder struct {
	Dir  string // output root
	Home string // replaced by {{HOME}}; empty disables
	Repo string // replaced by {{REPO}}; empty disables
	// Environ is the environment to snapshot; nil means os.Environ().
	Environ []string
}

// RecorderFromEnv returns a Recorder when RecordEnv is set, else nil.
// repoRoot is the repository root resolved by the caller (may be empty).
func RecorderFromEnv(getenv func(string) string, repoRoot string) *Recorder {
	dir := strings.TrimSpace(getenv(RecordEnv))
	if dir == "" {
		return nil
	}
	home, _ := os.UserHomeDir()
	return &Recorder{Dir: dir, Home: home, Repo: repoRoot}
}

// Record writes one recording and returns its path. cwd defaults to the
// process working directory when empty. Agent and event names are
// sanitized into safe path components.
func (r *Recorder) Record(agent, event string, stdin []byte, cwd string) (string, error) {
	if r == nil {
		return "", nil
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	environ := r.Environ
	if environ == nil {
		environ = os.Environ()
	}
	s := NewScrubber(r.Home, r.Repo)
	rec := Recording{
		Agent: agent,
		Event: event,
		CWD:   s.Scrub(cwd),
		Env:   SnapshotEnv(environ, s),
	}
	payload := s.Scrub(string(stdin))
	if trimmed := bytes.TrimSpace([]byte(payload)); len(trimmed) > 0 && json.Valid(trimmed) {
		rec.Stdin = json.RawMessage(trimmed)
	} else {
		rec.StdinText = payload
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rec); err != nil {
		return "", fmt.Errorf("encode recording: %w", err)
	}

	dir := filepath.Join(r.Dir, safeName(agent))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create record dir: %w", err)
	}
	return writeNext(dir, safeName(event), buf.Bytes())
}

// writeNext creates <dir>/<base>-<n>.json with the lowest free n >= 1.
// O_EXCL makes concurrent hook processes pick distinct numbers.
func writeNext(dir, base string, data []byte) (string, error) {
	for n := 1; n <= maxRecordSeq; n++ {
		path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", base, n))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create recording: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return "", fmt.Errorf("write recording: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close recording: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("record dir %s: no free sequence number", dir)
}

// safeName maps s to a path component of [A-Za-z0-9_-], never empty.
func safeName(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !isNameByte(c) || c == '.' {
			b[i] = '_'
		}
	}
	out := strings.Trim(string(b), "_-")
	if out == "" {
		return "unknown"
	}
	return out
}

// SnapshotEnv keeps the agent-related variables from environ, redacting
// secret-looking values and scrubbing paths in the rest.
func SnapshotEnv(environ []string, s *Scrubber) map[string]string {
	out := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !keepEnv(k) {
			continue
		}
		if isSecret(k) || !envValueAllow[k] {
			out[k] = redacted
			continue
		}
		out[k] = s.Scrub(v)
	}
	return out
}

func keepEnv(k string) bool {
	if envExact[k] {
		return true
	}
	for _, p := range envPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func isSecret(k string) bool {
	u := strings.ToUpper(k)
	for _, m := range secretMarkers {
		if strings.Contains(u, m) {
			return true
		}
	}
	return false
}

// Scrubber replaces machine-specific path roots with placeholders.
type Scrubber struct {
	rules []scrubRule // longest root first
}

type scrubRule struct{ root, token string }

// NewScrubber builds a scrubber for the home and repo roots. Each root is
// also matched in its symlink-resolved form and in its JSON-escaped form
// (doubled backslashes, for Windows paths inside JSON strings). Empty roots
// and filesystem roots are ignored.
func NewScrubber(home, repo string) *Scrubber {
	s := &Scrubber{}
	seen := map[string]bool{}
	add := func(root, token string) {
		for _, v := range rootVariants(root) {
			if !seen[v] {
				seen[v] = true
				s.rules = append(s.rules, scrubRule{v, token})
			}
		}
	}
	// Repo first so it wins ties; length sort below handles nesting.
	add(repo, RepoPlaceholder)
	add(home, HomePlaceholder)
	sort.SliceStable(s.rules, func(i, j int) bool { return len(s.rules[i].root) > len(s.rules[j].root) })
	return s
}

func rootVariants(root string) []string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	cands := []string{root}
	if c := filepath.Clean(root); c != root {
		cands = append(cands, c)
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		cands = append(cands, r)
	}
	var out []string
	for _, c := range cands {
		c = strings.TrimRight(c, `/\`)
		if c == "" || isVolumeRoot(c) {
			continue
		}
		out = append(out, c)
		if strings.Contains(c, `\`) {
			out = append(out, strings.ReplaceAll(c, `\`, `\\`))
		}
	}
	return out
}

// isVolumeRoot reports a bare drive such as "C:" (after trimming slashes).
func isVolumeRoot(p string) bool { return len(p) == 2 && p[1] == ':' }

// Scrub replaces every root occurrence that sits on path-component
// boundaries with its placeholder.
func (s *Scrubber) Scrub(in string) string {
	for _, r := range s.rules {
		in = replaceBounded(in, r.root, r.token)
	}
	return in
}

// replaceBounded replaces root in s only where the match is not glued to
// neighbouring path-name characters, so "/home/al" does not hit
// "/home/alice" and "/src/proj" does not hit "/src/project2".
func replaceBounded(s, root, token string) string {
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(s[i:], root)
		if j < 0 {
			break
		}
		start, end := i+j, i+j+len(root)
		before := start == 0 || (!isNameByte(s[start-1]) && s[start-1] != '/' && s[start-1] != '\\')
		after := end == len(s) || !isNameByte(s[end])
		if before && after {
			b.WriteString(s[i:start])
			b.WriteString(token)
		} else {
			b.WriteString(s[i : start+1])
			end = start + 1
		}
		i = end
	}
	if i == 0 {
		return s
	}
	b.WriteString(s[i:])
	return b.String()
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '-' || c == '.' || c >= 0x80
}
