package hookrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestScrubberReplacesRepoBeforeHome(t *testing.T) {
	s := NewScrubber("/home/alice", "/home/alice/src/proj")
	cases := []struct{ in, want string }{
		{"/home/alice/src/proj/main.go", "{{REPO}}/main.go"},
		{"/home/alice/src/proj", "{{REPO}}"},
		{"/home/alice/.claude/settings.json", "{{HOME}}/.claude/settings.json"},
		{"/home/alice", "{{HOME}}"},
		{`{"cwd":"/home/alice/src/proj","t":"/home/alice/x"}`, `{"cwd":"{{REPO}}","t":"{{HOME}}/x"}`},
		// Path-component boundary: a sibling sharing the prefix is not the repo.
		{"/home/alice/src/project2/a.go", "{{HOME}}/src/project2/a.go"},
		{"/home/alicex/file", "/home/alicex/file"},
		{"/other/place", "/other/place"},
		{"", ""},
	}
	for _, c := range cases {
		if got := s.Scrub(c.in); got != c.want {
			t.Errorf("Scrub(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestScrubberJSONEscapedWindowsPaths(t *testing.T) {
	s := NewScrubber(`C:\Users\bob`, `C:\Users\bob\code\app`)
	in := `{"cwd":"C:\\Users\\bob\\code\\app","p":"C:\\Users\\bob\\AppData"}`
	want := `{"cwd":"{{REPO}}","p":"{{HOME}}\\AppData"}`
	if got := s.Scrub(in); got != want {
		t.Errorf("Scrub = %q, want %q", got, want)
	}
}

func TestScrubberEmptyRootsAreIgnored(t *testing.T) {
	s := NewScrubber("", "")
	if got := s.Scrub("/a/b"); got != "/a/b" {
		t.Errorf("got %q", got)
	}
	// "/" must never become a scrub root (it would mangle every path).
	s = NewScrubber("/", "")
	if got := s.Scrub("/a/b"); got != "/a/b" {
		t.Errorf("root dir scrubbed: %q", got)
	}
}

func TestScrubberResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	s := NewScrubber("", link)
	for _, p := range []string{link + "/x", resolved + "/x"} {
		if got := s.Scrub(p); got != "{{REPO}}/x" {
			t.Errorf("Scrub(%q) = %q", p, got)
		}
	}
}

func TestSnapshotEnvFiltersAndRedacts(t *testing.T) {
	environ := []string{
		"CLAUDE_PROJECT_DIR=/home/alice/src/proj",
		"CLAUDE_CODE_OAUTH_TOKEN=sk-secret",
		"CODEX_HOME=/home/alice/.codex",
		"CURSOR_TRACE_ID=abc",
		"OPENCODE_API_KEY=hunter2",
		"OPENCODE=1",
		"TERM_PROGRAM=vscode",
		"TERM_PROGRAM_VERSION=1.2",
		"PATH=/usr/bin",
		"HOME=/home/alice",
		"ANTHROPIC_API_KEY=nope",
		"malformed",
	}
	s := NewScrubber("/home/alice", "/home/alice/src/proj")
	got := SnapshotEnv(environ, s)
	want := map[string]string{
		"CLAUDE_PROJECT_DIR":      "{{REPO}}",
		"CLAUDE_CODE_OAUTH_TOKEN": redacted,
		"CODEX_HOME":              "{{HOME}}/.codex",
		"CURSOR_TRACE_ID":         redacted, // identifiers keep only their name
		"OPENCODE_API_KEY":        redacted,
		"TERM_PROGRAM":            "vscode",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestRecordWritesNumberedFiles(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{
		Dir:     dir,
		Home:    "/home/alice",
		Repo:    "/home/alice/src/proj",
		Environ: []string{"CLAUDE_PROJECT_DIR=/home/alice/src/proj", "PATH=/bin"},
	}
	payload := []byte(`{"session_id":"s1","cwd":"/home/alice/src/proj","tool_input":{"path":"/home/alice/src/proj/a.go"}}`)

	p1, err := r.Record("claude", "PreToolUse", payload, "/home/alice/src/proj/sub")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := r.Record("claude", "PreToolUse", payload, "/home/alice/src/proj")
	if err != nil {
		t.Fatal(err)
	}
	p3, err := r.Record("codex", "session-start", []byte("not json /home/alice/x"), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		filepath.Join(dir, "claude", "PreToolUse-1.json"),
		filepath.Join(dir, "claude", "PreToolUse-2.json"),
		filepath.Join(dir, "codex", "session-start-1.json"),
	}
	for i, p := range []string{p1, p2, p3} {
		if p != wantPaths[i] {
			t.Errorf("path %d = %q, want %q", i, p, wantPaths[i])
		}
	}

	rec := readRecording(t, p1)
	if rec.Agent != "claude" || rec.Event != "PreToolUse" {
		t.Errorf("agent/event = %q/%q", rec.Agent, rec.Event)
	}
	if rec.CWD != "{{REPO}}/sub" {
		t.Errorf("cwd = %q", rec.CWD)
	}
	if rec.Env["CLAUDE_PROJECT_DIR"] != "{{REPO}}" || len(rec.Env) != 1 {
		t.Errorf("env = %v", rec.Env)
	}
	var stdin map[string]any
	if err := json.Unmarshal(rec.Stdin, &stdin); err != nil {
		t.Fatalf("stdin not embedded as JSON: %v (%s)", err, rec.Stdin)
	}
	if stdin["cwd"] != "{{REPO}}" {
		t.Errorf("stdin cwd = %v", stdin["cwd"])
	}
	if raw, _ := os.ReadFile(p1); strings.Contains(string(raw), "/home/alice") {
		t.Errorf("home path leaked:\n%s", raw)
	}

	rec3 := readRecording(t, p3)
	if rec3.Stdin != nil || rec3.StdinText != "not json {{HOME}}/x" {
		t.Errorf("non-JSON stdin: raw=%s text=%q", rec3.Stdin, rec3.StdinText)
	}
}

func TestRecordSanitizesNames(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{Dir: dir}
	p, err := r.Record("../evil", "pre/tool use", []byte(`{}`), "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "evil", "pre_tool_use-1.json")
	if p != want {
		t.Errorf("path = %q, want %q", p, want)
	}
	p, err = r.Record("", "", []byte(``), "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "unknown", "unknown-1.json"); p != want {
		t.Errorf("path = %q, want %q", p, want)
	}
}

func TestRecordConcurrentUniqueNames(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{Dir: dir}
	const n = 16
	var wg sync.WaitGroup
	paths := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := r.Record("cursor", "stop", []byte(`{}`), "")
			if err != nil {
				t.Error(err)
				return
			}
			paths <- p
		}()
	}
	wg.Wait()
	close(paths)
	seen := map[string]bool{}
	for p := range paths {
		if seen[p] {
			t.Errorf("duplicate path %s", p)
		}
		seen[p] = true
	}
	if len(seen) != n {
		t.Errorf("got %d unique files, want %d", len(seen), n)
	}
}

func TestRecorderFromEnv(t *testing.T) {
	if r := RecorderFromEnv(func(string) string { return "" }, "/repo"); r != nil {
		t.Errorf("expected nil recorder when %s unset", RecordEnv)
	}
	r := RecorderFromEnv(func(k string) string {
		if k == RecordEnv {
			return "/tmp/rec"
		}
		return ""
	}, "/repo")
	if r == nil || r.Dir != "/tmp/rec" || r.Repo != "/repo" {
		t.Fatalf("recorder = %+v", r)
	}
	// A nil recorder is a safe no-op so callers need no guard.
	var nilRec *Recorder
	if p, err := nilRec.Record("claude", "stop", nil, ""); p != "" || err != nil {
		t.Errorf("nil Record = %q, %v", p, err)
	}
}

func readRecording(t *testing.T, path string) Recording {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec Recording
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return rec
}

func TestRecordScrubsEmails(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{Dir: dir, Home: "/home/alice", Repo: "/home/alice/src/proj"}
	path, err := r.Record("cursor", "pre-tool-use", []byte(`{"user_email":"alice@example.com","tool_name":"Grep"}`), "/home/alice/src/proj")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "alice@example.com") || !strings.Contains(string(b), `"user_email": "{{EMAIL}}"`) {
		t.Fatalf("email not scrubbed:\n%s", b)
	}
}
