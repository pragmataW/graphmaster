package hookrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/install"
	"github.com/pragmataW/graphmaster/internal/steer"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// fakeAdapter decodes the payload as a hookapi.HookEvent and renders the
// decision as JSON, so dispatcher tests see exactly what was decided.
type fakeAdapter struct{ name hookapi.Agent }

func (f fakeAdapter) Name() hookapi.Agent      { return f.name }
func (fakeAdapter) Detect(install.Env) bool    { return true }
func (fakeAdapter) Notes(install.Env) []string { return nil }
func (fakeAdapter) InstallPlan(install.Env, adapters.InstallOptions) (install.Plan, error) {
	return install.Plan{}, nil
}

func (f fakeAdapter) ParseEvent(event hookapi.Event, payload []byte, _ []string) (hookapi.HookEvent, error) {
	if strings.Contains(string(payload), "PANIC_PARSE") {
		panic("parse exploded")
	}
	var ev hookapi.HookEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return hookapi.HookEvent{}, err
	}
	ev.Agent = f.name
	if event != "" {
		ev.Event = event
	}
	ev.Raw = payload
	return ev, ev.Validate()
}

// rendered is fakeAdapter's output.
type rendered struct {
	Event    hookapi.Event    `json:"event"`
	Decision hookapi.Decision `json:"decision"`
}

func (fakeAdapter) RenderDecision(ev hookapi.HookEvent, d hookapi.Decision) adapters.HookOutput {
	if ev.Pattern == "PANIC_RENDER" {
		panic("render exploded")
	}
	b, _ := json.Marshal(rendered{Event: ev.Event, Decision: d})
	return adapters.HookOutput{Stdout: b}
}

func fakeLookup(hookapi.Agent) (adapters.Agent, bool) {
	return fakeAdapter{name: hookapi.AgentOpenCode}, true
}

// gitDir returns a fresh (unindexed) git repository.
func gitDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, "init", "-q")
	return dir
}

func payload(t *testing.T, ev hookapi.HookEvent) []byte {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeRendered(t *testing.T, out adapters.HookOutput) rendered {
	t.Helper()
	if out.ExitCode != 0 {
		t.Fatalf("exit code %d", out.ExitCode)
	}
	var r rendered
	if err := json.Unmarshal(out.Stdout, &r); err != nil {
		t.Fatalf("stdout %q: %v", out.Stdout, err)
	}
	return r
}

func logOf(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, index.DirName, LogFileName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(b)
}

func testEnv(home string, kv ...string) []string {
	return append([]string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}, kv...)
}

func fakeDispatcher(opts Options) *Dispatcher {
	if opts.Adapter == nil {
		opts.Adapter = fakeLookup
	}
	if opts.Spawn == nil {
		opts.Spawn = func(string) error { return nil }
	}
	return New(opts)
}

func TestRunUnknownAgentIsSilent(t *testing.T) {
	root := gitDir(t)
	out := fakeDispatcher(Options{}).Run(context.Background(), Request{
		Event: "pre-tool-use", Agent: "vim", Stdin: []byte(`{}`), Env: testEnv(t.TempDir()), WorkDir: root,
	})
	if len(out.Stdout) != 0 || out.ExitCode != 0 {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(logOf(t, root), `unknown agent "vim"`) {
		t.Fatal("unknown agent must be logged")
	}
}

func TestRunMissingAdapterIsSilent(t *testing.T) {
	d := New(Options{Adapter: func(hookapi.Agent) (adapters.Agent, bool) { return nil, false }})
	out := d.Run(context.Background(), Request{Event: "stop", Agent: "codex", Stdin: []byte(`{}`), Env: testEnv(t.TempDir()), WorkDir: t.TempDir()})
	if len(out.Stdout) != 0 || out.ExitCode != 0 {
		t.Fatalf("out = %+v", out)
	}
}

func TestRunParseErrorFailsOpen(t *testing.T) {
	root := gitDir(t)
	out := fakeDispatcher(Options{}).Run(context.Background(), Request{
		Event: "pre-tool-use", Agent: "opencode", Stdin: []byte(`not json`), Env: testEnv(t.TempDir()), WorkDir: root,
	})
	r := decodeRendered(t, out)
	if r.Decision.Effective() != hookapi.ActionNoop || r.Event != hookapi.EventPreToolUse {
		t.Fatalf("parse error must fail open: %+v", r)
	}
	if !strings.Contains(logOf(t, root), "parse payload") {
		t.Fatal("parse error must be logged")
	}
}

func TestRunParsePanicFailsOpen(t *testing.T) {
	root := gitDir(t)
	out := fakeDispatcher(Options{}).Run(context.Background(), Request{
		Event: "pre-tool-use", Agent: "opencode", Stdin: []byte(`{"pattern":"PANIC_PARSE"}`), Env: testEnv(t.TempDir()), WorkDir: root,
	})
	if r := decodeRendered(t, out); r.Decision.Effective() != hookapi.ActionNoop {
		t.Fatalf("panic must fail open: %+v", r)
	}
	if !strings.Contains(logOf(t, root), "parse exploded") {
		t.Fatal("panic must be logged")
	}
}

func TestRunRoutePanicFailsOpen(t *testing.T) {
	root := gitDir(t)
	d := fakeDispatcher(Options{Spawn: func(string) error { panic("spawn exploded") }})
	ev := hookapi.HookEvent{Event: hookapi.EventSessionStart, SessionID: "s", CWD: root}
	out := d.Run(context.Background(), Request{Event: "session-start", Agent: "opencode", Stdin: payload(t, ev), Env: testEnv(t.TempDir()), WorkDir: root})
	r := decodeRendered(t, out)
	if r.Decision.Effective() != hookapi.ActionNoop || r.Decision.AdditionalContext != "" {
		t.Fatalf("panic must fail open: %+v", r)
	}
	if log := logOf(t, root); !strings.Contains(log, "opencode session-start: panic: spawn exploded") {
		t.Fatalf("log = %s", log)
	}
}

func TestRunRenderPanicIsSilent(t *testing.T) {
	root := gitDir(t)
	ev := hookapi.HookEvent{Event: hookapi.EventPreToolUse, Tool: hookapi.ToolGrep, Pattern: "PANIC_RENDER", CWD: root}
	out := fakeDispatcher(Options{}).Run(context.Background(), Request{Event: "pre-tool-use", Agent: "opencode", Stdin: payload(t, ev), Env: testEnv(t.TempDir()), WorkDir: root})
	if len(out.Stdout) != 0 || out.ExitCode != 0 {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(logOf(t, root), "render exploded") {
		t.Fatal("render panic must be logged")
	}
}

func TestRunTimeoutFailsOpen(t *testing.T) {
	root := gitDir(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	d := fakeDispatcher(Options{
		Spawn:   func(string) error { <-release; return nil },
		Timeout: func(hookapi.Event, *config.Config) time.Duration { return 50 * time.Millisecond },
	})
	ev := hookapi.HookEvent{Event: hookapi.EventSessionStart, SessionID: "s", CWD: root}
	start := time.Now()
	out := d.Run(context.Background(), Request{Event: "session-start", Agent: "opencode", Stdin: payload(t, ev), Env: testEnv(t.TempDir()), WorkDir: root})
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("Run took %s despite a 50ms deadline", el)
	}
	if r := decodeRendered(t, out); r.Decision.Effective() != hookapi.ActionNoop || r.Decision.AdditionalContext != "" {
		t.Fatalf("timeout must fail open: %+v", r)
	}
	if !strings.Contains(logOf(t, root), "gave up: context deadline exceeded") {
		t.Fatal("timeout must be logged")
	}
}

func TestRunOpenErrorFailsOpen(t *testing.T) {
	root := gitDir(t)
	// A graph.db that exists makes post-tool-use open the index.
	if err := os.MkdirAll(filepath.Join(root, index.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, index.DirName, "graph.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	d := fakeDispatcher(Options{Open: func(context.Context, *index.Location, *config.Config) (steer.Index, error) {
		return nil, errors.New("disk on fire")
	}})
	ev := hookapi.HookEvent{Event: hookapi.EventPostToolUse, Tool: hookapi.ToolShell, Command: "make", CWD: root}
	out := d.Run(context.Background(), Request{Event: "post-tool-use", Agent: "opencode", Stdin: payload(t, ev), Env: testEnv(t.TempDir()), WorkDir: root})
	if r := decodeRendered(t, out); r.Decision.Effective() != hookapi.ActionNoop {
		t.Fatalf("open error must fail open: %+v", r)
	}
	if !strings.Contains(logOf(t, root), "disk on fire") {
		t.Fatal("open error must be logged")
	}
}

func TestRunStopIsNoopWithoutLog(t *testing.T) {
	root := gitDir(t)
	ev := hookapi.HookEvent{Event: hookapi.EventStop, SessionID: "s", CWD: root}
	out := fakeDispatcher(Options{}).Run(context.Background(), Request{Event: "stop", Agent: "opencode", Stdin: payload(t, ev), Env: testEnv(t.TempDir()), WorkDir: root})
	if r := decodeRendered(t, out); r.Decision.Effective() != hookapi.ActionNoop || r.Event != hookapi.EventStop {
		t.Fatalf("stop = %+v", r)
	}
	if fileExists(filepath.Join(root, index.DirName, LogFileName)) {
		t.Fatal("a clean run must not write the log")
	}
}

func TestRunOutsideRepoAllows(t *testing.T) {
	dir := t.TempDir()
	ev := hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s", ToolUseID: "t", Tool: hookapi.ToolGrep, Pattern: "Foo", CWD: dir}
	out := fakeDispatcher(Options{}).Run(context.Background(), Request{Event: "pre-tool-use", Agent: "opencode", Stdin: payload(t, ev), Env: testEnv(t.TempDir()), WorkDir: dir})
	if r := decodeRendered(t, out); r.Decision.Blocks() {
		t.Fatalf("outside a repository nothing is blocked: %+v", r)
	}
}

func TestRunRecordsPayload(t *testing.T) {
	root := gitDir(t)
	recDir := t.TempDir()
	ev := hookapi.HookEvent{Event: hookapi.EventStop, SessionID: "s", CWD: root}
	fakeDispatcher(Options{}).Run(context.Background(), Request{
		Event: "stop", Agent: "opencode", Stdin: payload(t, ev),
		Env: testEnv(t.TempDir(), RecordEnv+"="+recDir), WorkDir: root,
	})
	b, err := os.ReadFile(filepath.Join(recDir, "opencode", "stop-1.json"))
	if err != nil {
		t.Fatalf("recording: %v", err)
	}
	var rec Recording
	if err := json.Unmarshal(b, &rec); err != nil || rec.Agent != "opencode" || rec.Event != "stop" || len(rec.Stdin) == 0 {
		t.Fatalf("recording = %s (%v)", b, err)
	}
	if strings.Contains(string(b), root) {
		t.Fatal("repository path must be scrubbed")
	}
}
