package replay

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	// Agent adapters register themselves for the dispatcher, as in
	// internal/cli.
	_ "github.com/pragmataW/graphmaster/internal/adapters/claude"
	_ "github.com/pragmataW/graphmaster/internal/adapters/codex"
	_ "github.com/pragmataW/graphmaster/internal/adapters/cursor"
	_ "github.com/pragmataW/graphmaster/internal/adapters/opencode"
	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/hookrun"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// step is one hook invocation of a scenario.
type step struct {
	// fixture is the payload file under testdata/hooks.
	fixture string
	// agent overrides the --agent value (default: the fixture's agent),
	// e.g. a Cursor payload delivered to the Claude Code hook.
	agent string
	// vars are placeholder values on top of the scenario defaults.
	vars map[string]string
	// env is appended to the fixture's environment (KEY=VALUE).
	env []string
	// stdin, when set, replaces the fixture payload (placeholders are
	// expanded); it models malformed input.
	stdin *string
	// before changes the world (files, config, index) before the hook runs.
	before func(t *testing.T, w *world)
	// check holds assertions beyond the golden output.
	check func(t *testing.T, w *world, r result)
}

// scenario is an ordered list of hook invocations against one fresh
// materialized fixture repository and one temporary HOME.
type scenario struct {
	name  string
	setup func(t *testing.T, w *world)
	steps []step
	// spawns marks scenarios that start background builds; they only run
	// in-process, where the spawn is recorded instead of executed.
	spawns bool
}

// result is the answer of one hook invocation.
type result struct {
	stdout, stderr []byte
	code           int
}

// world is the state a scenario runs in.
type world struct {
	root    string // materialized fixture, symlinks resolved
	home    string
	scrub   *hookrun.Scrubber
	mu      sync.Mutex
	spawned []string
	results []result // previous steps, in order
}

func (w *world) spawn(root string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.spawned = append(w.spawned, root)
	return nil
}

func (w *world) spawns() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.spawned...)
}

func (w *world) path(rel string) string { return filepath.Join(w.root, filepath.FromSlash(rel)) }

// runner executes one hook: in-process through hookrun, or by exec'ing a
// built binary.
type runner func(t *testing.T, w *world, agent, event, dir string, stdin []byte, env []string) result

// inProcess runs the real dispatcher with the registered adapters, the way
// `graphmaster hook` does, but records background builds instead of
// spawning them and lifts the deadlines so a loaded machine cannot turn a
// decision into a fail-open.
func inProcess(t *testing.T, w *world, agent, event, dir string, stdin []byte, env []string) result {
	t.Helper()
	d := hookrun.New(hookrun.Options{
		Spawn:        w.spawn,
		LookupBudget: time.Minute,
		Timeout:      func(hookapi.Event, *config.Config) time.Duration { return 2 * time.Minute },
	})
	out := d.Run(context.Background(), hookrun.Request{Event: event, Agent: agent, Stdin: stdin, Env: env, WorkDir: dir})
	return result{stdout: out.Stdout, stderr: out.Stderr, code: out.ExitCode}
}

// newWorld materializes the fixture repository and a HOME.
func newWorld(t *testing.T) *world {
	t.Helper()
	root := testutil.Materialize(t)
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	return &world{root: root, home: home, scrub: hookrun.NewScrubber(home, root)}
}

// defaults are the placeholder values every step starts from.
func (w *world) defaults(sc scenario, i int) map[string]string {
	sid := "sess-" + sc.name
	return map[string]string{
		"REPO":            w.root,
		"HOME":            w.home,
		"SESSION_ID":      sid,
		"SESSIONID":       sid,
		"TRANSCRIPT_PATH": filepath.Join(w.home, ".claude", "projects", "replay", sid+".jsonl"),
		"REDACTED":        "redacted",
		"TOOL_USE_ID":     fmt.Sprintf("toolu_%02d", i+1),
		"CALLID":          fmt.Sprintf("call_%02d", i+1),
		"GENERATION_ID":   fmt.Sprintf("gen-%02d", i+1),
		"AGENT_ID":        "agent-explore-1",
	}
}

// baseEnv is the hermetic part of every hook environment.
func (w *world) baseEnv() []string {
	return []string{
		"HOME=" + w.home,
		"XDG_CONFIG_HOME=" + filepath.Join(w.home, ".config"),
	}
}

// play runs sc with run and returns the rendered transcript.
func play(t *testing.T, sc scenario, run runner) string {
	t.Helper()
	w := newWorld(t)
	if sc.setup != nil {
		sc.setup(t, w)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# scenario %s\n", sc.name)
	for i, st := range sc.steps {
		rec, err := Load(st.fixture)
		if err != nil {
			t.Fatal(err)
		}
		vars := w.defaults(sc, i)
		base := w.defaults(sc, i)
		for k, v := range st.vars {
			// Step values may refer to the defaults, e.g. "{{REPO}}/a.go".
			if vars[k], err = Expand(v, base); err != nil {
				t.Fatalf("%s: var %s: %v", st.fixture, k, err)
			}
		}
		agent := rec.Agent
		if st.agent != "" {
			agent = st.agent
		}
		var stdin []byte
		if st.stdin != nil {
			s, err := Expand(*st.stdin, vars)
			if err != nil {
				t.Fatal(err)
			}
			stdin = []byte(s)
		} else if stdin, err = Stdin(rec, vars); err != nil {
			t.Fatalf("%s: %v", st.fixture, err)
		}
		fenv, err := Environ(rec, vars)
		if err != nil {
			t.Fatalf("%s: %v", st.fixture, err)
		}
		dir, err := Expand(rec.CWD, vars)
		if err != nil {
			t.Fatalf("%s: cwd: %v", st.fixture, err)
		}
		env := append(append(w.baseEnv(), fenv...), st.env...)
		if st.before != nil {
			st.before(t, w)
		}

		r := run(t, w, agent, rec.Event, dir, stdin, env)
		w.results = append(w.results, r)
		writeStep(&b, w, i, agent, rec.Event, st, r)
		if r.code != 0 {
			t.Errorf("step %d (%s): exit %d; hooks always exit 0", i+1, st.fixture, r.code)
		}
		if len(r.stdout) > 0 && (!bytes.HasSuffix(r.stdout, []byte("\n")) || bytes.Count(r.stdout, []byte("\n")) != 1) {
			t.Errorf("step %d (%s): stdout must be one JSON line: %q", i+1, st.fixture, r.stdout)
		}
		if st.check != nil {
			t.Run(fmt.Sprintf("step%d", i+1), func(t *testing.T) { st.check(t, w, r) })
		}
	}
	return b.String()
}

// writeStep renders one invocation for the golden file: the call, the exit
// code, the normalized stdout verbatim and, for a JSON object, its fields
// flattened for reading.
func writeStep(b *strings.Builder, w *world, i int, agent, event string, st step, r result) {
	fmt.Fprintf(b, "\n## %d. %s %s < %s\n", i+1, agent, event, st.fixture)
	if len(st.vars) > 0 {
		keys := make([]string, 0, len(st.vars))
		for k := range st.vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(b, "var %s=%s\n", k, strings.ReplaceAll(Normalize(st.vars[k], w.scrub), "\n", `\n`))
		}
	}
	for _, kv := range st.env {
		fmt.Fprintf(b, "env %s\n", Normalize(kv, w.scrub))
	}
	if st.stdin != nil {
		fmt.Fprintf(b, "stdin %q\n", Normalize(*st.stdin, w.scrub))
	}
	fmt.Fprintf(b, "exit %d\n", r.code)
	stdout := Normalize(string(r.stdout), w.scrub)
	if stdout == "" {
		b.WriteString("stdout (empty)\n")
	} else {
		fmt.Fprintf(b, "stdout %s", stdout)
		if !strings.HasSuffix(stdout, "\n") {
			b.WriteString("\n")
		}
		for _, f := range Fields([]byte(stdout)) {
			fmt.Fprintf(b, "  %s\n", f)
		}
	}
	if s := Normalize(string(r.stderr), w.scrub); s != "" {
		fmt.Fprintf(b, "stderr %q\n", s)
	}
}

func goldenPath(name string) string { return filepath.Join("testdata", "golden", name+".golden") }

// compareGolden checks got against the scenario's golden file, rewriting
// it under -update.
func compareGolden(t *testing.T, name, got string, mayUpdate bool) {
	t.Helper()
	p := goldenPath(name)
	if *update && mayUpdate {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s (run with -update to accept):\n%s", p, lineDiff(string(want), got))
	}
}

// lineDiff shows the first differing lines of want and got.
func lineDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	shown := 0
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w == g {
			continue
		}
		fmt.Fprintf(&b, "line %d\n  want: %s\n  got:  %s\n", i+1, w, g)
		if shown++; shown == 8 {
			b.WriteString("  ...\n")
			break
		}
	}
	return b.String()
}

// World helpers used by scenarios.

// buildIndex indexes the repository in the foreground, as a finished
// `graphmaster index` would have.
func buildIndex(t *testing.T, w *world) {
	t.Helper()
	ctx := context.Background()
	ix, err := index.Open(ctx, w.root, index.Options{Resolver: &resolve.Resolver{}})
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if _, err := ix.Freshen(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b)+content)
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// writeLedger adds a Go file long enough (> steer.ReadOutlineMinLines)
// for a whole-file Read to be answered with an outline.
func writeLedger(t *testing.T, w *world) {
	t.Helper()
	var b strings.Builder
	b.WriteString("package ledger\n\n// Ledger records postings.\ntype Ledger struct{ entries []int }\n")
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "\n// Post%02d adds posting %d.\nfunc (l *Ledger) Post%02d(v int) int {\n\tl.entries = append(l.entries, v+%d)\n\treturn len(l.entries)\n}\n", i, i, i, i)
	}
	writeFile(t, w.path("go/internal/ledger/ledger.go"), b.String())
}

// installCursorHooks writes a ~/.cursor/hooks.json with graphmaster's
// native hooks, as `graphmaster install` does for Cursor.
func installCursorHooks(t *testing.T, w *world) {
	t.Helper()
	writeFile(t, filepath.Join(w.home, ".cursor", "hooks.json"), `{
  "version": 1,
  "hooks": {
    "preToolUse": [{"command": "graphmaster hook pre-tool-use --agent cursor", "matcher": "Shell|Read|Grep"}]
  }
}
`)
}

func readLog(t *testing.T, w *world) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.root, index.DirName, hookrun.LogFileName))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func strPtr(s string) *string { return &s }

// Assertions used by scenario checks.

func wantSpawns(n int) func(*testing.T, *world, result) {
	return func(t *testing.T, w *world, _ result) {
		t.Helper()
		got := w.spawns()
		if len(got) != n {
			t.Fatalf("background builds = %v, want %d", got, n)
		}
		for _, r := range got {
			if r != w.root {
				t.Fatalf("background build of %s, want %s", r, w.root)
			}
		}
	}
}

func wantContains(subs ...string) func(*testing.T, *world, result) {
	return func(t *testing.T, _ *world, r result) {
		t.Helper()
		for _, s := range subs {
			if !strings.Contains(string(r.stdout), s) {
				t.Fatalf("stdout lacks %q:\n%s", s, r.stdout)
			}
		}
	}
}

func wantEmpty(t *testing.T, _ *world, r result) {
	t.Helper()
	if len(r.stdout) != 0 || len(r.stderr) != 0 {
		t.Fatalf("want no output, got stdout %q stderr %q", r.stdout, r.stderr)
	}
}

// wantSameAsStep asserts a replayed duplicate: byte-identical stdout.
func wantSameAsStep(n int) func(*testing.T, *world, result) {
	return func(t *testing.T, w *world, r result) {
		t.Helper()
		prev := w.results[n-1]
		if !bytes.Equal(prev.stdout, r.stdout) {
			t.Fatalf("duplicate must replay step %d:\nfirst  %s\nsecond %s", n, prev.stdout, r.stdout)
		}
	}
}

func wantLog(sub string) func(*testing.T, *world, result) {
	return func(t *testing.T, w *world, _ result) {
		t.Helper()
		if l := readLog(t, w); !strings.Contains(l, sub) {
			t.Fatalf(".graphmaster/log lacks %q:\n%s", sub, l)
		}
	}
}

func all(checks ...func(*testing.T, *world, result)) func(*testing.T, *world, result) {
	return func(t *testing.T, w *world, r result) {
		t.Helper()
		for _, c := range checks {
			c(t, w, r)
		}
	}
}

func wantNoDeny(t *testing.T, _ *world, r result) {
	t.Helper()
	if bytes.Contains(r.stdout, []byte(`"deny"`)) {
		t.Fatalf("want no deny, got %s", r.stdout)
	}
}

// wantSymbol asserts how many graph nodes are named name: post-tool-use
// must have updated the graph before any later search freshens it.
func wantSymbol(name string, n int) func(*testing.T, *world, result) {
	return func(t *testing.T, w *world, _ result) {
		t.Helper()
		ctx := context.Background()
		st, err := store.Open(ctx, filepath.Join(w.root, index.DirName), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		var got int
		if err := st.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes JOIN names ON names.id = nodes.name_id WHERE names.name = ?`, name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != n {
			t.Fatalf("graph has %d nodes named %s, want %d", got, name, n)
		}
	}
}

func wantStdout(want string) func(*testing.T, *world, result) {
	return func(t *testing.T, _ *world, r result) {
		t.Helper()
		if string(r.stdout) != want {
			t.Fatalf("stdout = %q, want %q", r.stdout, want)
		}
	}
}
