package hookrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/adapters"
	_ "github.com/pragmataW/graphmaster/internal/adapters/claude"
	_ "github.com/pragmataW/graphmaster/internal/adapters/codex"
	_ "github.com/pragmataW/graphmaster/internal/adapters/opencode"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// Shared, indexed copy of the fixture repository. Session and dedupe state
// live in graph.db, so every test uses its own session ids (uniqueSID).
var (
	fixtureOnce sync.Once
	fixtureTmp  string
	fixtureRoot string
	fixtureErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtureTmp != "" {
		_ = os.RemoveAll(fixtureTmp)
	}
	os.Exit(code)
}

func sharedFixture(t *testing.T) string {
	t.Helper()
	fixtureOnce.Do(func() {
		if fixtureTmp, fixtureErr = os.MkdirTemp("", "gm-hookrun-fixture-"); fixtureErr != nil {
			return
		}
		fixtureRoot = filepath.Join(fixtureTmp, "repo")
		if fixtureErr = testutil.MaterializeTo(fixtureRoot); fixtureErr != nil {
			return
		}
		if r, err := filepath.EvalSymlinks(fixtureRoot); err == nil {
			fixtureRoot = r
		}
		ctx := context.Background()
		ix, err := index.Open(ctx, fixtureRoot, index.Options{Resolver: &resolve.Resolver{}})
		if err != nil {
			fixtureErr = err
			return
		}
		defer ix.Close()
		_, fixtureErr = ix.Freshen(ctx, time.Minute)
	})
	if fixtureErr != nil {
		t.Fatalf("fixture: %v", fixtureErr)
	}
	return fixtureRoot
}

var sidSeq atomic.Int64

// uniqueSID is a session id unique within the process: session state lives
// in the shared graph.db and must not leak between -count repetitions.
func uniqueSID(t *testing.T) string {
	return fmt.Sprintf("%s-%d", t.Name(), sidSeq.Add(1))
}

// miniRepo is a small unindexed git repository with a Go symbol Foo.
func miniRepo(t *testing.T) string {
	t.Helper()
	root := gitDir(t)
	writeFile(t, root, "go.mod", "module example.com/mini\n\ngo 1.22\n")
	writeFile(t, root, "foo.go", "package mini\n\n// Foo greets.\nfunc Foo() string { return Bar() }\n\n// Bar is called by Foo.\nfunc Bar() string { return \"bar\" }\n")
	writeFile(t, root, "README.md", "# mini\n\nFoo docs.\n")
	return root
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// spawnRecorder records background builds instead of starting processes.
type spawnRecorder struct {
	mu    sync.Mutex
	roots []string
}

func (s *spawnRecorder) spawn(root string) error {
	s.mu.Lock()
	s.roots = append(s.roots, root)
	s.mu.Unlock()
	return nil
}

func (s *spawnRecorder) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.roots...)
}

// harness runs real-adapter hook invocations, one fresh Dispatcher per
// call as in production (one process per hook).
type harness struct {
	t     *testing.T
	root  string
	home  string
	env   []string
	spawn *spawnRecorder
}

func newHarness(t *testing.T, root string, env ...string) *harness {
	home := t.TempDir()
	return &harness{t: t, root: root, home: home, env: testEnv(home, env...), spawn: &spawnRecorder{}}
}

func (h *harness) hook(agent, event string, payload any) adapters.HookOutput {
	h.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatal(err)
	}
	d := New(Options{LookupBudget: 10 * time.Second, Spawn: h.spawn.spawn})
	out := d.Run(context.Background(), Request{Event: event, Agent: agent, Stdin: b, Env: h.env, WorkDir: h.root})
	if out.ExitCode != 0 {
		h.t.Fatalf("%s %s: exit %d", agent, event, out.ExitCode)
	}
	if p := filepath.Join(h.root, index.DirName, LogFileName); fileExists(p) {
		h.t.Logf("hook log:\n%s", readFile(h.t, p))
	}
	return out
}

// Payload builders (shapes of Claude Code, Codex, Cursor and the OpenCode plugin).

func claudeTool(sid, id, cwd, event, tool string, input map[string]any) map[string]any {
	return map[string]any{
		"session_id": sid, "cwd": cwd, "hook_event_name": event,
		"tool_name": tool, "tool_input": input, "tool_use_id": id,
	}
}

func claudeGrep(sid, id, cwd, pattern string, extra ...string) map[string]any {
	in := map[string]any{"pattern": pattern}
	for i := 0; i+1 < len(extra); i += 2 {
		in[extra[i]] = extra[i+1]
	}
	return claudeTool(sid, id, cwd, "PreToolUse", "Grep", in)
}

func codexBash(sid, id, cwd, event, command string) map[string]any {
	return map[string]any{
		"session_id": sid, "cwd": cwd, "hook_event_name": event, "tool_name": "Bash",
		"tool_input": map[string]any{"command": command}, "tool_use_id": id,
	}
}

func cursorGrep(sid, gen, id, root, pattern string) map[string]any {
	return map[string]any{
		"conversation_id": sid, "generation_id": gen, "cursor_version": "1.7.2",
		"hook_event_name": "preToolUse", "tool_name": "Grep", "tool_use_id": id,
		"tool_input": map[string]any{"pattern": pattern}, "workspace_roots": []string{root},
	}
}

// specific is the hookSpecificOutput of Claude Code and Codex.
type specific struct {
	Specific struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func decodeSpecific(t *testing.T, out adapters.HookOutput) specific {
	t.Helper()
	var s specific
	if len(out.Stdout) == 0 {
		return s
	}
	if err := json.Unmarshal(out.Stdout, &s); err != nil {
		t.Fatalf("stdout %q: %v", out.Stdout, err)
	}
	return s
}

func requireDeny(t *testing.T, label string, out adapters.HookOutput, wantInReason string) {
	t.Helper()
	s := decodeSpecific(t, out)
	if s.Specific.PermissionDecision != "deny" {
		t.Fatalf("%s: want deny, got %s", label, out.Stdout)
	}
	if !strings.Contains(s.Specific.PermissionDecisionReason, wantInReason) {
		t.Fatalf("%s: reason lacks %q:\n%s", label, wantInReason, s.Specific.PermissionDecisionReason)
	}
}

func requireNotDenied(t *testing.T, label string, out adapters.HookOutput) {
	t.Helper()
	if s := decodeSpecific(t, out); s.Specific.PermissionDecision != "" {
		t.Fatalf("%s: want no permission decision, got %s", label, out.Stdout)
	}
}

func openStore(t *testing.T, root string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(root, index.DirName), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func countSymbol(t *testing.T, root, name string) int {
	t.Helper()
	var n int
	err := openStore(t, root).DB().QueryRow(
		`SELECT COUNT(*) FROM nodes JOIN names ON names.id = nodes.name_id WHERE names.name = ?`, name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestClaudeGrepDenyThenRetryAllow(t *testing.T) {
	root := sharedFixture(t)
	h := newHarness(t, root)
	sid := uniqueSID(t)
	requireDeny(t, "first grep", h.hook("claude", "pre-tool-use", claudeGrep(sid, "t1", root, "PlaceOrder")), "PlaceOrder")
	requireNotDenied(t, "retry", h.hook("claude", "pre-tool-use", claudeGrep(sid, "t2", root, "PlaceOrder")))
}

func TestClaudeGrepNonCodeAllowed(t *testing.T) {
	root := sharedFixture(t)
	h := newHarness(t, root)
	requireNotDenied(t, ".md glob", h.hook("claude", "pre-tool-use", claudeGrep(uniqueSID(t), "t1", root, "PlaceOrder", "glob", "*.md")))
	requireNotDenied(t, ".md file", h.hook("claude", "pre-tool-use", claudeGrep(uniqueSID(t), "t2", root, "PlaceOrder", "path", "README.md")))
}

func TestStrictModeDeniesTextSearch(t *testing.T) {
	root := sharedFixture(t)
	balanced := newHarness(t, root)
	requireNotDenied(t, "balanced text", balanced.hook("claude", "pre-tool-use", claudeGrep(uniqueSID(t), "t1", root, "out of stock")))
	strict := newHarness(t, root, "GRAPHMASTER_MODE=strict")
	sid := uniqueSID(t)
	requireDeny(t, "strict text", strict.hook("claude", "pre-tool-use", claudeGrep(sid, "t1", root, "out of stock")), "strict")
	// strict never lets the retry through either
	requireDeny(t, "strict retry", strict.hook("claude", "pre-tool-use", claudeGrep(sid, "t2", root, "out of stock")), "strict")
}

func TestOffModeAllows(t *testing.T) {
	root := sharedFixture(t)
	h := newHarness(t, root, "GRAPHMASTER_MODE=off")
	requireNotDenied(t, "off", h.hook("claude", "pre-tool-use", claudeGrep(uniqueSID(t), "t1", root, "PlaceOrder")))
}

func TestCodexShellSearch(t *testing.T) {
	root := miniRepo(t)
	h := newHarness(t, root)
	sid := uniqueSID(t)
	// SessionStart builds the index synchronously for Codex.
	if out := h.hook("codex", "session-start", map[string]any{"session_id": sid, "cwd": root, "hook_event_name": "SessionStart"}); len(out.Stdout) == 0 {
		t.Fatal("codex session start must return context")
	}
	requireDeny(t, "rg Foo", h.hook("codex", "pre-tool-use", codexBash(sid, "c1", root, "PreToolUse", "rg Foo")), "Foo")
	requireNotDenied(t, "ps | grep", h.hook("codex", "pre-tool-use", codexBash(sid, "c2", root, "PreToolUse", "ps aux | grep x")))
	requireNotDenied(t, "retry", h.hook("codex", "pre-tool-use", codexBash(sid, "c3", root, "PreToolUse", "rg Foo")))
	if got := h.spawn.calls(); len(got) != 0 {
		t.Fatalf("codex never spawns background builds, got %v", got)
	}
}

// cursorGate is Cursor's answer to a gating hook.
type cursorGate struct {
	Permission   string `json:"permission"`
	AgentMessage string `json:"agent_message"`
}

func decodeCursor(t *testing.T, out adapters.HookOutput) cursorGate {
	t.Helper()
	var g cursorGate
	if err := json.Unmarshal(out.Stdout, &g); err != nil {
		t.Fatalf("stdout %q: %v", out.Stdout, err)
	}
	return g
}

func sessionDenies(t *testing.T, root, sid string) int {
	t.Helper()
	var n int
	if err := openStore(t, root).DB().QueryRow(`SELECT COALESCE(SUM(denies), 0) FROM sessions WHERE sid = ?`, sid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCursorDoubleInvokeProducesOneDecision(t *testing.T) {
	root := sharedFixture(t)
	h := newHarness(t, root) // no ~/.cursor/hooks.json: both calls are answered
	sid := uniqueSID(t)
	p := cursorGrep(sid, "g1", "tu1", root, "PlaceOrder")

	viaClaude := h.hook("claude", "pre-tool-use", p)
	native := h.hook("cursor", "pre-tool-use", p)
	g := decodeCursor(t, viaClaude)
	if g.Permission != "deny" || !strings.Contains(g.AgentMessage, "PlaceOrder") {
		t.Fatalf("claude-side call must be answered in Cursor's format: %s", viaClaude.Stdout)
	}
	if string(native.Stdout) != string(viaClaude.Stdout) {
		t.Fatalf("duplicate must replay the first decision:\nfirst  %s\nsecond %s", viaClaude.Stdout, native.Stdout)
	}
	if n := sessionDenies(t, root, sid); n != 1 {
		t.Fatalf("the duplicate must not touch the deny budget: denies = %d", n)
	}
	// A deliberate retry (next generation) is steered normally: allowed.
	if g := decodeCursor(t, h.hook("cursor", "pre-tool-use", cursorGrep(sid, "g2", "tu2", root, "PlaceOrder"))); g.Permission == "deny" {
		t.Fatal("retry must be allowed")
	}
}

func TestCursorClaudeDuplicateIsSilentWithNativeHooks(t *testing.T) {
	root := sharedFixture(t)
	h := newHarness(t, root)
	writeCursorHooks(t, h.home, nativeHooks)
	p := cursorGrep(uniqueSID(t), "g1", "tu1", root, "PlaceOrder")
	if out := h.hook("claude", "pre-tool-use", p); len(out.Stdout) != 0 || len(out.Stderr) != 0 {
		t.Fatalf("claude-side duplicate must be silent, got %s", out.Stdout)
	}
	if g := decodeCursor(t, h.hook("cursor", "pre-tool-use", p)); g.Permission != "deny" {
		t.Fatal("the native hook decides")
	}
}

func TestClaudeSessionStartBuildsIndex(t *testing.T) {
	root := miniRepo(t)
	h := newHarness(t, root)
	out := h.hook("claude", "session-start", map[string]any{"session_id": uniqueSID(t), "cwd": root, "hook_event_name": "SessionStart", "source": "startup"})
	ctx := decodeSpecific(t, out).Specific.AdditionalContext
	if !strings.Contains(ctx, "graphmaster") || !strings.Contains(ctx, "index: 1 files") {
		t.Fatalf("context = %q", ctx)
	}
	state, _, err := openStore(t, root).Meta(context.Background(), store.MetaState)
	if err != nil || state != store.StateReady {
		t.Fatalf("state = %q, %v", state, err)
	}
	if countSymbol(t, root, "Foo") != 1 {
		t.Fatal("Foo must be indexed")
	}
	if got := h.spawn.calls(); len(got) != 0 {
		t.Fatalf("claude builds in the foreground, spawned %v", got)
	}
}

func TestCursorSessionStartBuildsInBackground(t *testing.T) {
	root := miniRepo(t)
	h := newHarness(t, root)
	sid := uniqueSID(t)
	p := map[string]any{"conversation_id": sid, "generation_id": "g0", "session_id": sid,
		"hook_event_name": "sessionStart", "workspace_roots": []string{root}}

	out := h.hook("cursor", "session-start", p)
	var s struct {
		AdditionalContext string `json:"additional_context"`
	}
	if err := json.Unmarshal(out.Stdout, &s); err != nil || !strings.Contains(s.AdditionalContext, "background") {
		t.Fatalf("stdout = %s (%v)", out.Stdout, err)
	}
	if got := h.spawn.calls(); len(got) != 1 || got[0] != root {
		t.Fatalf("spawned %v, want [%s]", got, root)
	}
	if fileExists(filepath.Join(root, index.DirName, store.GraphDBName)) {
		t.Fatal("cursor session start must not build in the foreground")
	}

	// Once built, the context carries the index summary; a refresh is
	// still delegated to the background.
	h.hook("claude", "session-start", map[string]any{"session_id": "build", "cwd": root})
	out = h.hook("cursor", "session-start", p)
	if err := json.Unmarshal(out.Stdout, &s); err != nil || !strings.Contains(s.AdditionalContext, "index: 1 files") {
		t.Fatalf("stdout = %s (%v)", out.Stdout, err)
	}
	if got := h.spawn.calls(); len(got) != 2 {
		t.Fatalf("spawned %v, want a second background refresh", got)
	}
}

func TestBashEditThenPreToolUseSeesFreshGraph(t *testing.T) {
	root := miniRepo(t)
	h := newHarness(t, root)
	sid := uniqueSID(t)
	h.hook("claude", "session-start", map[string]any{"session_id": sid, "cwd": root})

	// The agent edits through the shell (e.g. sed -i / a heredoc).
	writeFile(t, root, "zebra.go", "package mini\n\n// ZebraQuux is new.\nfunc ZebraQuux() string { return Foo() }\n")
	post := claudeTool(sid, "b1", root, "PostToolUse", "Bash", map[string]any{"command": "cat > zebra.go <<EOF ... EOF"})
	if out := h.hook("claude", "post-tool-use", post); len(out.Stdout) != 0 {
		t.Fatalf("post-tool-use output = %s", out.Stdout)
	}
	if countSymbol(t, root, "ZebraQuux") != 1 {
		t.Fatal("PostToolUse Bash must reindex the new file")
	}
	requireDeny(t, "grep new symbol", h.hook("claude", "pre-tool-use", claudeGrep(sid, "g1", root, "ZebraQuux")), "ZebraQuux")
}

func TestEditPostToolUseReindexesPath(t *testing.T) {
	root := miniRepo(t)
	h := newHarness(t, root)
	sid := uniqueSID(t)
	h.hook("claude", "session-start", map[string]any{"session_id": sid, "cwd": root})

	writeFile(t, root, "foo.go", "package mini\n\nfunc Foo() string { return Walrus() }\n\nfunc Walrus() string { return \"w\" }\n")
	h.hook("claude", "post-tool-use", claudeTool(sid, "e1", root, "PostToolUse", "Edit", map[string]any{"file_path": "foo.go"}))
	if countSymbol(t, root, "Walrus") != 1 || countSymbol(t, root, "Bar") != 0 {
		t.Fatal("PostToolUse Edit must reindex foo.go")
	}

	// Codex apply_patch paths are hinted too; a deleted file disappears.
	if err := os.Remove(filepath.Join(root, "foo.go")); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Delete File: foo.go\n*** End Patch\n"
	h.hook("codex", "post-tool-use", map[string]any{"session_id": sid, "cwd": root, "hook_event_name": "PostToolUse",
		"tool_name": "apply_patch", "tool_input": map[string]any{"command": patch}})
	if countSymbol(t, root, "Walrus") != 0 {
		t.Fatal("a patched-away file must leave the graph")
	}
}

func TestPostToolUseWithoutIndexDoesNothing(t *testing.T) {
	root := miniRepo(t)
	h := newHarness(t, root)
	h.hook("claude", "post-tool-use", claudeTool(uniqueSID(t), "e1", root, "PostToolUse", "Edit", map[string]any{"file_path": "foo.go"}))
	if fileExists(filepath.Join(root, index.DirName, store.GraphDBName)) {
		t.Fatal("post-tool-use must not create a graph")
	}
}

func TestSubagentStartContext(t *testing.T) {
	root := sharedFixture(t)
	h := newHarness(t, root)
	out := h.hook("claude", "subagent-start", map[string]any{"session_id": uniqueSID(t), "cwd": root,
		"hook_event_name": "SubagentStart", "agent_id": "a1", "agent_type": "Explore"})
	if ctx := decodeSpecific(t, out).Specific.AdditionalContext; !strings.Contains(ctx, "subagent") || !strings.Contains(ctx, "find_symbol") {
		t.Fatalf("context = %q", ctx)
	}
}

func TestPreCompactReminder(t *testing.T) {
	root := sharedFixture(t)
	h := newHarness(t, root)
	out := h.hook("opencode", "pre-compact", map[string]any{"sessionID": uniqueSID(t), "cwd": root})
	var r struct {
		Context string `json:"context"`
	}
	if err := json.Unmarshal(out.Stdout, &r); err != nil || !strings.Contains(r.Context, "find_symbol") {
		t.Fatalf("stdout = %s (%v)", out.Stdout, err)
	}
}

func TestStopAndNonGitAreSilent(t *testing.T) {
	root := sharedFixture(t)
	h := newHarness(t, root)
	if out := h.hook("claude", "stop", map[string]any{"session_id": uniqueSID(t), "cwd": root, "hook_event_name": "Stop"}); len(out.Stdout) != 0 {
		t.Fatalf("stop output = %s", out.Stdout)
	}
	plain := t.TempDir()
	h2 := newHarness(t, plain)
	requireNotDenied(t, "non-git", h2.hook("claude", "pre-tool-use", claudeGrep(uniqueSID(t), "t1", plain, "PlaceOrder")))
	if got := h2.spawn.calls(); len(got) != 0 {
		t.Fatalf("nothing is built outside a repository, spawned %v", got)
	}
}

func TestPreToolUseWithoutIndexStartsBackgroundBuild(t *testing.T) {
	root := miniRepo(t)
	h := newHarness(t, root)
	requireNotDenied(t, "no index", h.hook("claude", "pre-tool-use", claudeGrep(uniqueSID(t), "t1", root, "Foo")))
	if got := h.spawn.calls(); len(got) != 1 || got[0] != root {
		t.Fatalf("spawned %v, want a background build of %s", got, root)
	}
}
