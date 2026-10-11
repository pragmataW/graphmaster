package steer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// Shared, read-mostly fixture index. Session state lives in graph.db, so
// every test uses its own session id (t.Name()).
var (
	fixtureOnce sync.Once
	fixtureTmp  string
	fixtureRoot string
	fixtureIx   *index.Indexer
	fixtureErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtureIx != nil {
		_ = fixtureIx.Close()
	}
	if fixtureTmp != "" {
		_ = os.RemoveAll(fixtureTmp)
	}
	os.Exit(code)
}

// sharedIndex returns the indexed materialized fixture.
func sharedIndex(t testing.TB) (*index.Indexer, string) {
	t.Helper()
	fixtureOnce.Do(func() {
		if fixtureTmp, fixtureErr = os.MkdirTemp("", "gm-steer-fixture-"); fixtureErr != nil {
			return
		}
		fixtureRoot = filepath.Join(fixtureTmp, "repo")
		if fixtureErr = testutil.MaterializeTo(fixtureRoot); fixtureErr != nil {
			return
		}
		if r, err := filepath.EvalSymlinks(fixtureRoot); err == nil {
			fixtureRoot = r
		}
		fixtureIx, fixtureErr = index.Open(context.Background(), fixtureRoot,
			index.Options{Resolver: &resolve.Resolver{}, Logf: func(string, ...any) {}})
		if fixtureErr != nil {
			return
		}
		_, fixtureErr = fixtureIx.Freshen(context.Background(), time.Minute)
	})
	if fixtureErr != nil {
		t.Fatalf("fixture: %v", fixtureErr)
	}
	return fixtureIx, fixtureRoot
}

// borrowed hides Close so the engine never closes the shared indexer.
type borrowed struct{ Index }

// fixtureEngine returns an engine over the shared fixture index.
func fixtureEngine(t testing.TB) (*Engine, string) {
	t.Helper()
	ix, root := sharedIndex(t)
	e := New(Options{
		Open: func(context.Context, *index.Location, *config.Config) (Index, error) {
			return borrowed{ix}, nil
		},
		LookupBudget: 10 * time.Second, // -race and loaded CI machines
		Logf:         t.Logf,
	})
	t.Cleanup(func() { _ = e.Close() })
	return e, root
}

func cfgMode(m config.Mode) config.Config {
	c := *config.Default()
	c.Mode = m
	return c
}

// evs builds PreToolUse events for one (session, agent).
type evs struct {
	root, sid, agentID string
	agent              hookapi.Agent
}

func newEvs(t *testing.T, root string) evs {
	return evs{root: root, sid: t.Name(), agent: hookapi.AgentClaude}
}

func (b evs) base(tool hookapi.Tool) hookapi.HookEvent {
	return hookapi.HookEvent{
		Agent: b.agent, Event: hookapi.EventPreToolUse, SessionID: b.sid, AgentID: b.agentID,
		Tool: tool, CWD: b.root,
	}
}

func (b evs) grep(pattern string, paths ...string) hookapi.HookEvent {
	ev := b.base(hookapi.ToolGrep)
	ev.Pattern, ev.Paths = pattern, paths
	return ev
}

func (b evs) grepGlob(pattern, glob string) hookapi.HookEvent {
	ev := b.grep(pattern)
	ev.Glob = glob
	return ev
}

func (b evs) glob(glob string, paths ...string) hookapi.HookEvent {
	ev := b.base(hookapi.ToolGlob)
	ev.Glob, ev.Paths = glob, paths
	return ev
}

func (b evs) shell(cmd string) hookapi.HookEvent {
	ev := b.base(hookapi.ToolShell)
	ev.Command = cmd
	return ev
}

func (b evs) read(path string, input map[string]any) hookapi.HookEvent {
	ev := b.base(hookapi.ToolRead)
	ev.Paths = []string{path}
	if input == nil {
		input = map[string]any{}
	}
	input["file_path"] = path
	raw, _ := json.Marshal(map[string]any{"tool_name": "Read", "tool_input": input})
	ev.Raw = raw
	return ev
}

func requireAction(t *testing.T, label string, d hookapi.Decision, want hookapi.Action) {
	t.Helper()
	if d.Action != want {
		t.Fatalf("%s: action = %q, want %q (reason=%q, context=%q)", label, d.Action, want, d.Reason, d.AdditionalContext)
	}
	if len(d.Reason) > MaxReasonLen {
		t.Fatalf("%s: reason is %d bytes, cap %d:\n%s", label, len(d.Reason), MaxReasonLen, d.Reason)
	}
}

func contains(t *testing.T, label, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("%s: missing %q in:\n%s", label, w, s)
		}
	}
}

// step is one event in a matrix scenario and its expected verdict.
type step struct {
	ev   func(evs) hookapi.HookEvent
	want hookapi.Action
	has  []string // substrings expected in Reason (deny) or context (allow)
}

func runSteps(t *testing.T, e *Engine, cfg config.Config, b evs, steps []step) {
	t.Helper()
	for i, s := range steps {
		d := e.Decide(context.Background(), s.ev(b), cfg)
		label := fmt.Sprintf("step %d", i)
		requireAction(t, label, d, s.want)
		if len(s.has) > 0 {
			contains(t, label, d.Reason+d.AdditionalContext, s.has...)
		}
	}
}

func grepEv(p string, paths ...string) func(evs) hookapi.HookEvent {
	return func(b evs) hookapi.HookEvent { return b.grep(p, paths...) }
}

func globEv(g string, paths ...string) func(evs) hookapi.HookEvent {
	return func(b evs) hookapi.HookEvent { return b.glob(g, paths...) }
}

func shellEv(cmd string) func(evs) hookapi.HookEvent {
	return func(b evs) hookapi.HookEvent { return b.shell(cmd) }
}

const (
	allow = hookapi.ActionAllow
	deny  = hookapi.ActionDeny
)

// TestDecideMatrix covers mode × pattern type × retry.
func TestDecideMatrix(t *testing.T) {
	cases := []struct {
		name  string
		mode  config.Mode
		steps []step
	}{
		// balanced
		{"balanced/symbol/first-deny-retry-allow", config.ModeBalanced, []step{
			{grepEv("PlaceOrder"), deny, []string{"PlaceOrder", "go/internal/service/order.go", "find_symbol", "again"}},
			{grepEv("PlaceOrder"), allow, nil},
			{grepEv("PlaceOrder"), allow, nil},
		}},
		{"balanced/symbol/def-keyword-and-anchor", config.ModeBalanced, []step{
			{grepEv(`^func\s+NewOrderService\(`), deny, []string{"NewOrderService"}},
			{grepEv(`func NewOrderService`), allow, nil}, // same pnorm
		}},
		{"balanced/symbol/alternation", config.ModeBalanced, []step{
			{grepEv("OrderService|NewOrderService"), deny, []string{"OrderService"}},
			{grepEv("NewOrderService|OrderService"), allow, nil},
		}},
		{"balanced/symbol/path-scope-ignored-for-retry", config.ModeBalanced, []step{
			{grepEv("FormatMoney", "go"), deny, []string{"FormatMoney"}},
			{grepEv("FormatMoney", "."), allow, nil},
		}},
		{"balanced/symbol/empty-graph-allow", config.ModeBalanced, []step{
			{grepEv("ZzqNothingLikeThisQq"), allow, nil},
		}},
		{"balanced/text/allow", config.ModeBalanced, []step{
			{grepEv("out of stock"), allow, nil},
			{grepEv("TODO"), allow, nil},
			{grepEv(`"github.com/`), allow, nil},
			{grepEv("[0-9]+ items"), allow, nil},
		}},
		{"balanced/glob/explore-deny-retry-allow", config.ModeBalanced, []step{
			{globEv("**/*.go"), deny, []string{"repo_map", "go/", "again"}},
			{globEv("**/*.go"), allow, nil},
		}},
		{"balanced/glob/scoped-explore", config.ModeBalanced, []step{
			{globEv("go/**"), deny, []string{"service/"}},
		}},
		{"balanced/glob/literal-allow", config.ModeBalanced, []step{
			{globEv("**/order.go"), allow, nil},
			{globEv("**/user_service.go"), allow, nil},
		}},
		{"balanced/glob/noncode-allow", config.ModeBalanced, []step{
			{globEv("**/*.md"), allow, nil},
			{globEv("**/*.{yaml,json}"), allow, nil},
		}},
		// strict
		{"strict/symbol/always-deny", config.ModeStrict, []step{
			{grepEv("PlaceOrder"), deny, []string{"PlaceOrder", "strict", `mode = "balanced"`}},
			{grepEv("PlaceOrder"), deny, []string{"strict"}},
		}},
		{"strict/text/code-scoped-deny", config.ModeStrict, []step{
			{grepEv("out of stock"), deny, []string{"strict", "balanced"}},
		}},
		{"strict/symbol/empty-graph-still-deny", config.ModeStrict, []step{
			{grepEv("ZzqNothingLikeThisQq"), deny, []string{"balanced"}},
		}},
		{"strict/glob/explore-deny", config.ModeStrict, []step{
			{globEv("**/*.ts"), deny, []string{"ts/", "balanced"}},
			{globEv("**/*.ts"), deny, nil},
		}},
		{"strict/glob/literal-and-noncode-allow", config.ModeStrict, []step{
			{globEv("**/order.go"), allow, nil},
			{globEv("**/*.md"), allow, nil},
		}},
		{"strict/noncode-scope-allow", config.ModeStrict, []step{
			{func(b evs) hookapi.HookEvent { return b.grepGlob("PlaceOrder", "*.md") }, allow, nil},
			{grepEv("PlaceOrder", "README.md"), allow, nil},
		}},
		// off
		{"off/everything-allowed", config.ModeOff, []step{
			{grepEv("PlaceOrder"), allow, nil},
			{globEv("**/*.go"), allow, nil},
			{shellEv("rg PlaceOrder"), allow, nil},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, root := fixtureEngine(t)
			runSteps(t, e, cfgMode(c.mode), newEvs(t, root), c.steps)
		})
	}
}

func TestDecideScope(t *testing.T) {
	e, root := fixtureEngine(t)
	b := newEvs(t, root)
	cfg := cfgMode(config.ModeStrict) // strict: anything left in scope would be denied
	cases := []struct {
		name string
		ev   hookapi.HookEvent
	}{
		{"non-code file", b.grep("PlaceOrder", "README.md")},
		{"non-code glob filter", b.grepGlob("PlaceOrder", "*.{md,txt}")},
		{"vendored (not indexed)", b.grep("Reverse", "go/vendor")},
		{"ignored dir", b.grep("built", "build")},
		{"missing dir", b.grep("PlaceOrder", "docs")},
		{"outside root", b.grep("PlaceOrder", "/etc")},
		{"outside root relative", b.grep("PlaceOrder", "..")},
		{"single code file", b.grep("PlaceOrder", "go/internal/service/order.go")},
		{"absolute single code file", b.grep("PlaceOrder", filepath.Join(root, "go/internal/service/order.go"))},
	}
	for _, c := range cases {
		requireAction(t, c.name, e.Decide(context.Background(), c.ev, cfg), allow)
	}
	// In-scope controls.
	requireAction(t, "code dir", e.Decide(context.Background(), b.grep("PlaceOrder", "go/internal"), cfg), deny)
	requireAction(t, "absolute root", e.Decide(context.Background(), b.grep("PlaceOrder", root), cfg), deny)
	sub := b
	sub.root = filepath.Join(root, "go")
	requireAction(t, "cwd subdir", e.Decide(context.Background(), sub.grep("PlaceOrder"), cfg), deny)
	requireAction(t, "cwd subdir parent", e.Decide(context.Background(), sub.grep("PlaceOrder", "../go/internal/service/order.go"), cfg), allow)
}

func TestDecideSoftBudget(t *testing.T) {
	e, root := fixtureEngine(t)
	cfg := cfgMode(config.ModeBalanced)
	cfg.SoftBudget = 2
	b := newEvs(t, root)
	runSteps(t, e, cfg, b, []step{
		{grepEv("PlaceOrder"), deny, nil},
		{grepEv("FormatMoney"), deny, nil},
		{grepEv("NewOrderService"), allow, []string{"budget", "find_symbol"}},
		{grepEv("FormatID"), allow, []string{"budget"}},
		{grepEv("PlaceOrder"), allow, nil}, // retry still allowed
	})

	// A subagent of the same session has its own budget and patterns.
	sub := b
	sub.agentID = "agent-explore-1"
	runSteps(t, e, cfg, sub, []step{
		{grepEv("PlaceOrder"), deny, nil},
		{grepEv("PlaceOrder"), allow, nil},
		{grepEv("FormatMoney"), deny, nil},
		{grepEv("NewOrderService"), allow, []string{"budget"}},
	})

	// A second subagent starts fresh as well.
	sub2 := b
	sub2.agentID = "agent-plan-2"
	runSteps(t, e, cfg, sub2, []step{{grepEv("NewOrderService"), deny, nil}})

	// Another session is independent of all of them.
	other := b
	other.sid = t.Name() + "-other"
	runSteps(t, e, cfg, other, []step{{grepEv("NewOrderService"), deny, nil}})
}

func TestDecideDefaultSoftBudget(t *testing.T) {
	e, root := fixtureEngine(t)
	cfg := cfgMode(config.ModeBalanced)
	cfg.SoftBudget = 0 // unset: DefaultSoftBudget applies
	b := newEvs(t, root)
	syms := []string{"PlaceOrder", "FormatMoney", "NewOrderService", "FormatID", "OrderService",
		"UserService", "Animal", "Invoice", "Vec2"}
	for i, s := range syms {
		want := deny
		if i >= DefaultSoftBudget {
			want = allow
		}
		requireAction(t, s, e.Decide(context.Background(), b.grep(s), cfg), want)
	}
}

func TestDecideRetryAcrossTools(t *testing.T) {
	e, root := fixtureEngine(t)
	runSteps(t, e, cfgMode(config.ModeBalanced), newEvs(t, root), []step{
		{grepEv("PlaceOrder"), deny, nil},
		{shellEv("rg -n PlaceOrder"), allow, nil},
		{shellEv("grep -rn 'func FormatMoney' ."), deny, []string{"FormatMoney"}},
		{grepEv("FormatMoney"), allow, nil},
	})
}

func TestDecideShell(t *testing.T) {
	cases := []struct {
		cmd  string
		want hookapi.Action
		has  []string
	}{
		{"rg PlaceOrder", deny, []string{"PlaceOrder", "order.go"}},
		{"grep -rn FormatMoney .", deny, []string{"FormatMoney"}},
		{"git grep -n NewOrderService", deny, nil},
		{"rg -F OrderService", deny, nil},
		{"bash -c 'rg UserService'", deny, []string{"UserService"}},
		{"cd go && rg FormatID", deny, nil},
		{"grep -r Invoice . | head -20", deny, nil},
		{"find . -name '*.java' | xargs grep -n Animal", deny, []string{"Animal"}},
		{"ls -R", deny, []string{"repo_map"}},
		{"find . -name '*.py'", deny, []string{"python/"}},
		// allowed
		{"ps aux | grep PlaceOrder", allow, nil},
		{"cat go/internal/service/order.go | grep PlaceOrder", allow, nil},
		{"grep PlaceOrder go/internal/service/order.go", allow, nil},
		{"grep -r PlaceOrder . > out.txt", allow, nil},
		{"grep -r PlaceOrder . && rm -rf x", allow, nil},
		{"rg 'out of stock'", allow, nil},
		{"rg TODO", allow, nil},
		{"rg PlaceOrder docs/", allow, nil},
		{"rg PlaceOrder /etc", allow, nil},
		{"rg Reverse go/vendor", allow, nil},
		{"rg -g '*.md' PlaceOrder", allow, nil},
		{"grep -r --include='*.yaml' PlaceOrder .", allow, nil},
		{"find . -name order.go", allow, nil},
		{"find . -name '*.md'", allow, nil},
		{"fd order", allow, nil},
		{"rg ZzqNothingLikeThisQq", allow, nil},
		{`rg "$PATTERN"`, allow, nil},
		{"cd ~ && rg PlaceOrder", allow, nil},
		{"echo 'unterminated", allow, nil},
		{"go test ./...", allow, nil},
		{"", allow, nil},
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			e, root := fixtureEngine(t)
			d := e.Decide(context.Background(), newEvs(t, root).shell(c.cmd), cfgMode(config.ModeBalanced))
			requireAction(t, c.cmd, d, c.want)
			contains(t, c.cmd, d.Reason, c.has...)
		})
	}
}

func TestDecideShellStrict(t *testing.T) {
	e, root := fixtureEngine(t)
	b := newEvs(t, root)
	cfg := cfgMode(config.ModeStrict)
	requireAction(t, "rg text", e.Decide(context.Background(), b.shell("rg 'out of stock'"), cfg), deny)
	requireAction(t, "ps|grep", e.Decide(context.Background(), b.shell("ps aux | grep foo"), cfg), allow)
	requireAction(t, "single file", e.Decide(context.Background(), b.shell("grep Foo go/cmd/server/main.go"), cfg), allow)
	requireAction(t, "md glob", e.Decide(context.Background(), b.shell("rg -g '*.md' Foo"), cfg), allow)
}

func TestDecideNonSteeredEvents(t *testing.T) {
	e, root := fixtureEngine(t)
	b := newEvs(t, root)
	cfg := cfgMode(config.ModeStrict)
	edit := b.base(hookapi.ToolEdit)
	edit.Paths = []string{"go/cmd/server/main.go"}
	requireAction(t, "edit", e.Decide(context.Background(), edit, cfg), allow)
	requireAction(t, "mcp", e.Decide(context.Background(), b.base(hookapi.ToolMCP), cfg), allow)
	post := b.grep("PlaceOrder")
	post.Event = hookapi.EventPostToolUse
	requireAction(t, "post-tool-use", e.Decide(context.Background(), post, cfg), allow)
}

func TestDecideNotGitAndNoIndex(t *testing.T) {
	cfg := cfgMode(config.ModeStrict)

	// Outside any repository.
	plain := t.TempDir()
	e := New(Options{Logf: t.Logf})
	defer e.Close()
	b := newEvs(t, plain)
	requireAction(t, "not git", e.Decide(context.Background(), b.grep("PlaceOrder"), cfg), allow)

	// A git repository that was never indexed: allow and start a build.
	repoDir := t.TempDir()
	testutil.Git(t, repoDir, "init", "-q")
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n\nfunc PlaceOrder() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var started []string
	e2 := New(Options{Logf: t.Logf, StartBuild: func(root string) { started = append(started, root) }})
	defer e2.Close()
	b2 := newEvs(t, repoDir)
	requireAction(t, "no index", e2.Decide(context.Background(), b2.grep("PlaceOrder"), cfg), allow)
	if len(started) != 1 {
		t.Fatalf("StartBuild calls = %v, want one", started)
	}
	want, _ := filepath.EvalSymlinks(repoDir)
	if started[0] != want {
		t.Errorf("StartBuild root = %q, want %q", started[0], want)
	}
	if _, err := os.Stat(filepath.Join(repoDir, index.DirName, "graph.db")); err == nil {
		t.Errorf("Decide created graph.db; the build belongs to the background process")
	}
}

// TestDecideRealOpen drives the default opener (index.Open) end to end on a
// private copy of the fixture, including the Read outline for a large file.
func TestDecideRealOpen(t *testing.T) {
	root := testutil.Materialize(t)
	var big strings.Builder
	big.WriteString("package service\n\n")
	for i := 0; i < 160; i++ {
		fmt.Fprintf(&big, "// Helper%03d is generated filler.\nfunc Helper%03d() int {\n\treturn %d\n}\n\n", i, i, i)
	}
	if err := os.WriteFile(filepath.Join(root, "go/internal/service/big.go"), []byte(big.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(context.Background(), root, index.Options{Resolver: &resolve.Resolver{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Freshen(context.Background(), time.Minute); err != nil {
		t.Fatal(err)
	}
	_ = ix.Close()

	e := New(Options{Logf: t.Logf, LookupBudget: 10 * time.Second})
	defer e.Close()
	b := newEvs(t, root)
	cfg := cfgMode(config.ModeBalanced)
	ctx := context.Background()

	requireAction(t, "grep", e.Decide(ctx, b.grep("Helper042"), cfg), deny)

	// Read is never denied; a large code file read whole gets an outline.
	d := e.Decide(ctx, b.read("go/internal/service/big.go", nil), cfg)
	requireAction(t, "read big", d, allow)
	contains(t, "read big", d.AdditionalContext, "big.go", "Helper000", "read_symbol")
	if n := strings.Count(strings.TrimRight(d.AdditionalContext, "\n"), "\n") + 1; n > ReadOutlineMaxLines+2 {
		t.Errorf("outline has %d lines, cap %d (+header/footer):\n%s", n, ReadOutlineMaxLines, d.AdditionalContext)
	}

	// Ranged reads, small files, non-code files and agents without context
	// injection get a plain allow.
	plain := []struct {
		name string
		ev   hookapi.HookEvent
	}{
		{"offset", b.read("go/internal/service/big.go", map[string]any{"offset": 100, "limit": 50})},
		{"small", b.read("go/internal/service/order.go", nil)},
		{"non-code", b.read("README.md", nil)},
		{"missing", b.read("nope/missing.go", nil)},
	}
	for _, c := range plain {
		d := e.Decide(ctx, c.ev, cfg)
		requireAction(t, c.name, d, allow)
		if d.AdditionalContext != "" {
			t.Errorf("%s: unexpected context %q", c.name, d.AdditionalContext)
		}
	}
	cursor := b.read("go/internal/service/big.go", nil)
	cursor.Agent = hookapi.AgentCursor
	if d := e.Decide(ctx, cursor, cfg); d.Action != allow || d.AdditionalContext != "" {
		t.Errorf("cursor read: %+v", d)
	}
	codex := b.read("go/internal/service/big.go", nil)
	codex.Agent = hookapi.AgentCodex
	if d := e.Decide(ctx, codex, cfg); !strings.Contains(d.AdditionalContext, "Helper000") {
		t.Errorf("codex read lacks outline: %+v", d)
	}
	// Strict mode never denies a read either.
	requireAction(t, "strict read", e.Decide(ctx, b.read("go/internal/service/big.go", nil), cfgMode(config.ModeStrict)), allow)
}

func TestReadHasRange(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{"tool_input":{"file_path":"a.go"}}`, false},
		{`{"tool_input":{"file_path":"a.go","offset":10}}`, true},
		{`{"tool_input":{"file_path":"a.go","limit":10}}`, true},
		{`{"tool_input":{"file_path":"a.go","offset":null}}`, false},
		{`{"args":{"filePath":"a.go","offset":3}}`, true},
		{`{"file_path":"a.go","start_line":3}`, true},
		{`not json`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := readHasRange(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("readHasRange(%s) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestPackageDecideFailsOpen(t *testing.T) {
	// Package-level Decide on a non-repository directory must allow.
	ev := hookapi.HookEvent{Agent: hookapi.AgentClaude, Event: hookapi.EventPreToolUse,
		Tool: hookapi.ToolGrep, Pattern: "PlaceOrder", CWD: t.TempDir()}
	if d := Decide(context.Background(), ev, cfgMode(config.ModeStrict)); d.Action != allow {
		t.Fatalf("Decide = %+v", d)
	}
	// A broken opener fails open as well.
	_, root := sharedIndex(t)
	e := New(Options{Open: func(context.Context, *index.Location, *config.Config) (Index, error) {
		return nil, fmt.Errorf("boom")
	}})
	defer e.Close()
	ev.CWD = root
	if d := e.Decide(context.Background(), ev, cfgMode(config.ModeStrict)); d.Action != allow {
		t.Fatalf("broken opener: %+v", d)
	}
}

func TestReasonCap(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = strings.Repeat("x", 90) + fmt.Sprint(i)
	}
	r := buildReason("head line", lines, "footer line\nsecond footer")
	if len(r) > MaxReasonLen {
		t.Fatalf("reason %d bytes > %d", len(r), MaxReasonLen)
	}
	contains(t, "reason", r, "head line", "footer line", "second footer", "more")
}
