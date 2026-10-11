package steer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSessionStore(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	s := sessionStore{db: st.DB(), now: func() time.Time { return now }}
	main := sessionKey{SID: "s1"}
	sub := sessionKey{SID: "s1", AgentID: "a1"}

	seen, denies, err := s.lookup(ctx, main, "Foo")
	if err != nil || seen || denies != 0 {
		t.Fatalf("empty lookup = %v %d %v", seen, denies, err)
	}
	if err := s.recordDeny(ctx, main, hookapi.AgentClaude, "Foo", hookapi.ToolGrep); err != nil {
		t.Fatal(err)
	}
	if err := s.recordDeny(ctx, main, hookapi.AgentClaude, "Bar Foo", hookapi.ToolShell); err != nil {
		t.Fatal(err)
	}
	seen, denies, err = s.lookup(ctx, main, "Foo")
	if err != nil || !seen || denies != 2 {
		t.Fatalf("lookup after denies = %v %d %v", seen, denies, err)
	}
	if seen, _, _ := s.lookup(ctx, main, "Baz"); seen {
		t.Fatalf("unrelated pattern reported as seen")
	}

	// Subagents are isolated from the main agent and each other.
	seen, denies, err = s.lookup(ctx, sub, "Foo")
	if err != nil || seen || denies != 0 {
		t.Fatalf("subagent lookup = %v %d %v", seen, denies, err)
	}

	if err := s.recordAllow(ctx, main, hookapi.AgentClaude); err != nil {
		t.Fatal(err)
	}
	if err := s.touch(ctx, sub, hookapi.AgentClaude); err != nil {
		t.Fatal(err)
	}
	var d, a int
	var agent string
	if err := st.DB().QueryRow(`SELECT denies, allows, agent FROM sessions WHERE sid='s1' AND agent_id=''`).Scan(&d, &a, &agent); err != nil {
		t.Fatal(err)
	}
	if d != 2 || a != 1 || agent != "claude" {
		t.Errorf("main session row = denies %d allows %d agent %q", d, a, agent)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM sessions WHERE sid='s1'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("session rows = %d (%v), want 2", n, err)
	}
	var tool string
	var pd int
	if err := st.DB().QueryRow(`SELECT tool, denies FROM session_patterns WHERE pnorm='Bar Foo'`).Scan(&tool, &pd); err != nil {
		t.Fatal(err)
	}
	if tool != "shell" || pd != 1 {
		t.Errorf("pattern row = %q %d", tool, pd)
	}
}

func TestSessionStartContext(t *testing.T) {
	e, root := fixtureEngine(t)
	ctx := context.Background()
	ev := hookapi.HookEvent{Agent: hookapi.AgentClaude, Event: hookapi.EventSessionStart, SessionID: t.Name(), CWD: root}

	for _, mode := range []config.Mode{config.ModeBalanced, config.ModeStrict, config.ModeOff} {
		got := e.SessionStartContext(ctx, ev, cfgMode(mode))
		if len(got) > MaxStartContextBytes {
			t.Errorf("%s: context is %d bytes, cap %d", mode, len(got), MaxStartContextBytes)
		}
		contains(t, string(mode), got, "graphmaster", "find_symbol", "callers", "repo_map",
			"mode "+string(mode), "files", "symbols", "go/", "python/")
	}
	contains(t, "balanced rules", e.SessionStartContext(ctx, ev, cfgMode(config.ModeBalanced)), "again")
	contains(t, "strict rules", e.SessionStartContext(ctx, ev, cfgMode(config.ModeStrict)), "balanced")
	contains(t, "off rules", e.SessionStartContext(ctx, ev, cfgMode(config.ModeOff)), "not steered")

	sub := ev
	sub.Event, sub.AgentID, sub.AgentType = hookapi.EventSubagentStart, "agent-1", "Explore"
	got := e.SubagentStartContext(ctx, sub, cfgMode(config.ModeBalanced))
	if len(got) > MaxStartContextBytes {
		t.Errorf("subagent context is %d bytes", len(got))
	}
	contains(t, "subagent", got, "subagent", "find_symbol", "go/")

	// Starting a session registers it.
	var n int
	if err := fixtureIx.Store().DB().QueryRow(`SELECT count(*) FROM sessions WHERE sid = ?`, t.Name()).Scan(&n); err != nil || n != 2 {
		t.Errorf("sessions rows = %d (%v), want main + subagent", n, err)
	}
}

func TestStartContextOutsideRepoAndUnindexed(t *testing.T) {
	ctx := context.Background()
	e := New(Options{Logf: t.Logf})
	defer e.Close()
	ev := hookapi.HookEvent{Agent: hookapi.AgentClaude, Event: hookapi.EventSessionStart, CWD: t.TempDir()}
	if got := e.SessionStartContext(ctx, ev, cfgMode(config.ModeBalanced)); got != "" {
		t.Errorf("non-repo context = %q, want empty", got)
	}

	// SubagentStart in a repo without an index: start a background build and
	// still hand the subagent the rules.
	repoDir := t.TempDir()
	testutil.Git(t, repoDir, "init", "-q")
	var started int
	e2 := New(Options{Logf: t.Logf, StartBuild: func(string) { started++ }})
	defer e2.Close()
	sub := hookapi.HookEvent{Agent: hookapi.AgentClaude, Event: hookapi.EventSubagentStart, AgentID: "x", CWD: repoDir}
	got := e2.SubagentStartContext(ctx, sub, cfgMode(config.ModeBalanced))
	contains(t, "unindexed subagent", got, "find_symbol", "building")
	if started != 1 {
		t.Errorf("StartBuild calls = %d, want 1", started)
	}
}

// SessionStart builds a missing index (the agent waits for it), so the
// first context already has statistics.
func TestSessionStartBuildsIndex(t *testing.T) {
	root := testutil.Materialize(t)
	e := New(Options{Logf: t.Logf})
	defer e.Close()
	ev := hookapi.HookEvent{Agent: hookapi.AgentClaude, Event: hookapi.EventSessionStart, SessionID: "s", CWD: root}
	got := e.SessionStartContext(context.Background(), ev, cfgMode(config.ModeBalanced))
	contains(t, "built", got, "symbols", "go/")
	if _, err := os.Stat(filepath.Join(root, ".graphmaster", "graph.db")); err != nil {
		t.Errorf("graph.db missing: %v", err)
	}
	if strings.Contains(got, "building") {
		t.Errorf("context reports a build in progress:\n%s", got)
	}
}
