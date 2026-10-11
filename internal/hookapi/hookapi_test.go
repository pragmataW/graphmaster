package hookapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseEvent(t *testing.T) {
	cases := map[string]Event{
		"SessionStart":  EventSessionStart,
		"sessionStart":  EventSessionStart,
		"session-start": EventSessionStart,
		"session_start": EventSessionStart,
		"SubagentStart": EventSubagentStart,
		"subagentStart": EventSubagentStart,
		"PreToolUse":    EventPreToolUse,
		"pre-tool-use":  EventPreToolUse,
		"preToolUse":    EventPreToolUse,
		"PostToolUse":   EventPostToolUse,
		"post-tool-use": EventPostToolUse,
		"PreCompact":    EventPreCompact,
		"pre-compact":   EventPreCompact,
		"Stop":          EventStop,
		" stop ":        EventStop, //nolint:gocritic // surrounding whitespace is part of the test
	}
	for in, want := range cases {
		got, err := ParseEvent(in)
		if err != nil || got != want {
			t.Errorf("ParseEvent(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "afterFileEdit", "launch"} {
		if _, err := ParseEvent(bad); err == nil {
			t.Errorf("ParseEvent(%q) should fail", bad)
		}
	}
	for _, e := range AllEvents() {
		if got, err := ParseEvent(string(e)); err != nil || got != e {
			t.Errorf("round trip %q failed", e)
		}
	}
}

func TestParseAgent(t *testing.T) {
	cases := map[string]Agent{
		"claude": AgentClaude, "Claude": AgentClaude, "claude-code": AgentClaude,
		"codex": AgentCodex, "cursor": AgentCursor, "opencode": AgentOpenCode, "OpenCode": AgentOpenCode,
	}
	for in, want := range cases {
		got, err := ParseAgent(in)
		if err != nil || got != want {
			t.Errorf("ParseAgent(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseAgent("vim"); err == nil {
		t.Fatal("expected error")
	}
	if len(AllAgents()) != 4 {
		t.Fatal("expected 4 agents")
	}
}

func TestToolPredicates(t *testing.T) {
	cases := []struct {
		tool             Tool
		search, mutation bool
	}{
		{ToolGrep, true, false},
		{ToolGlob, true, false},
		{ToolRead, false, false},
		{ToolShell, false, false},
		{ToolPatch, false, true},
		{ToolEdit, false, true},
		{ToolWrite, false, true},
		{ToolMCP, false, false},
		{ToolOther, false, false},
	}
	for _, c := range cases {
		if c.tool.IsSearch() != c.search || c.tool.IsMutation() != c.mutation {
			t.Errorf("%q: IsSearch=%v IsMutation=%v", c.tool, c.tool.IsSearch(), c.tool.IsMutation())
		}
		if !c.tool.Valid() {
			t.Errorf("%q should be valid", c.tool)
		}
	}
	if Tool("hammer").Valid() {
		t.Fatal("unknown tool must be invalid")
	}
}

func TestHookEventValidate(t *testing.T) {
	ok := HookEvent{Agent: AgentClaude, Event: EventPreToolUse, Tool: ToolGrep, Pattern: "Foo", CWD: "/r"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	if err := (HookEvent{Agent: AgentCodex, Event: EventSessionStart}).Validate(); err != nil {
		t.Fatalf("session start needs no tool: %v", err)
	}
	bad := []HookEvent{
		{Agent: "vim", Event: EventStop},
		{Agent: AgentClaude, Event: "nope"},
		{Agent: AgentClaude, Event: EventPreToolUse},                  // missing tool
		{Agent: AgentClaude, Event: EventPostToolUse, Tool: "hammer"}, // unknown tool
	}
	for i, e := range bad {
		if err := e.Validate(); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestHookEventIsSubagent(t *testing.T) {
	if (HookEvent{}).IsSubagent() {
		t.Fatal("main session is not a subagent")
	}
	if !(HookEvent{AgentID: "a1"}).IsSubagent() {
		t.Fatal("agent id implies subagent")
	}
}

func TestHookEventJSON(t *testing.T) {
	e := HookEvent{
		Agent: AgentCursor, Event: EventPreToolUse, SessionID: "s", Tool: ToolShell,
		Command: "rg Foo", Paths: []string{"a.go"}, Raw: json.RawMessage(`{"x":1}`),
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"agent":"cursor"`, `"event":"pre-tool-use"`, `"tool":"shell"`, `"raw":{"x":1}`} {
		if !strings.Contains(s, want) {
			t.Errorf("json %s missing %s", s, want)
		}
	}
	if strings.Contains(s, `"glob"`) {
		t.Errorf("empty fields must be omitted: %s", s)
	}
	var back HookEvent
	if err := json.Unmarshal(b, &back); err != nil || back.Command != "rg Foo" || back.Tool != ToolShell {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

func TestDecisionConstructors(t *testing.T) {
	if d := Allow(); d.Action != ActionAllow || d.Blocks() {
		t.Fatalf("Allow = %+v", d)
	}
	d := Deny("use find_symbol")
	if d.Action != ActionDeny || d.Reason != "use find_symbol" || !d.Blocks() {
		t.Fatalf("Deny = %+v", d)
	}
	if d := Ask("sure?"); d.Action != ActionAsk || d.Blocks() {
		t.Fatalf("Ask = %+v", d)
	}
	if d := Noop(); d.Action != ActionNoop {
		t.Fatalf("Noop = %+v", d)
	}
	if (Decision{}).Effective() != ActionNoop {
		t.Fatal("zero decision is a noop")
	}
	if Deny("x").Effective() != ActionDeny {
		t.Fatal("Effective must keep explicit action")
	}
	c := Allow().WithContext("outline...").WithUserMessage("hi")
	if c.Action != ActionAllow || c.AdditionalContext != "outline..." || c.UserMessage != "hi" {
		t.Fatalf("builder = %+v", c)
	}
}

func TestParseAction(t *testing.T) {
	for _, a := range []Action{ActionAllow, ActionDeny, ActionAsk, ActionNoop} {
		got, err := ParseAction(string(a))
		if err != nil || got != a {
			t.Errorf("ParseAction(%q) = %q, %v", a, got, err)
		}
	}
	if _, err := ParseAction("block"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecisionJSON(t *testing.T) {
	b, err := json.Marshal(Deny("r"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"action":"deny","reason":"r"}` {
		t.Fatalf("got %s", b)
	}
}
