package cursor

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// The payloads below are SYNTHETIC: hand-written from Cursor's hook
// documentation (researched 2026-10-11). Real captures belong in
// testdata/hooks/cursor/ and are replayed by the hook integration tests.
const common = `"conversation_id":"conv-1","generation_id":"gen-1","model":"claude-4.5-sonnet",` +
	`"cursor_version":"2.4.21","workspace_roots":["/repo"],"user_email":null,` +
	`"transcript_path":"/home/u/.cursor/projects/repo/agent-transcripts/conv-1.txt"`

func payload(fields string) string { return `{` + common + `,` + fields + `}` }

func TestParseEvent(t *testing.T) {
	cases := []struct {
		name  string
		event hookapi.Event
		body  string
		env   []string
		want  hookapi.HookEvent
	}{
		{
			name:  "sessionStart",
			event: hookapi.EventSessionStart,
			body:  payload(`"hook_event_name":"sessionStart","session_id":"conv-1","is_background_agent":false,"composer_mode":"agent"`),
			want:  hookapi.HookEvent{Event: hookapi.EventSessionStart, SessionID: "conv-1", CWD: "/repo"},
		},
		{
			name:  "preToolUse Grep",
			event: hookapi.EventPreToolUse,
			body: payload(`"hook_event_name":"preToolUse","tool_name":"Grep","tool_use_id":"tool-7","cwd":"/repo",` +
				`"tool_input":{"pattern":"NewServer","path":"internal","glob":"*.go","output_mode":"content"}`),
			want: hookapi.HookEvent{
				Event: hookapi.EventPreToolUse, SessionID: "conv-1", Tool: hookapi.ToolGrep,
				Pattern: "NewServer", Paths: []string{"internal"}, Glob: "*.go", CWD: "/repo", ToolUseID: "tool-7",
			},
		},
		{
			name:  "preToolUse Read",
			event: hookapi.EventPreToolUse,
			body: payload(`"hook_event_name":"preToolUse","tool_name":"Read","tool_use_id":"tool-8",` +
				`"tool_input":{"file_path":"/repo/cmd/main.go","offset":10,"limit":50}`),
			want: hookapi.HookEvent{
				Event: hookapi.EventPreToolUse, SessionID: "conv-1", Tool: hookapi.ToolRead,
				Paths: []string{"/repo/cmd/main.go"}, CWD: "/repo", ToolUseID: "tool-8",
			},
		},
		{
			name:  "preToolUse Shell takes cwd from tool_input",
			event: hookapi.EventPreToolUse,
			body: payload(`"hook_event_name":"preToolUse","tool_name":"Shell","tool_use_id":"tool-9",` +
				`"tool_input":{"command":"rg -n Foo .","cwd":"/repo/sub"}`),
			want: hookapi.HookEvent{
				Event: hookapi.EventPreToolUse, SessionID: "conv-1", Tool: hookapi.ToolShell,
				Command: "rg -n Foo .", CWD: "/repo/sub", ToolUseID: "tool-9",
			},
		},
		{
			name:  "preToolUse Glob",
			event: hookapi.EventPreToolUse,
			body: payload(`"hook_event_name":"preToolUse","tool_name":"Glob",` +
				`"tool_input":{"glob_pattern":"**/*.go","target_directory":"src"}`),
			want: hookapi.HookEvent{
				Event: hookapi.EventPreToolUse, SessionID: "conv-1", Tool: hookapi.ToolGlob,
				Glob: "**/*.go", Paths: []string{"src"}, CWD: "/repo",
			},
		},
		{
			name:  "preToolUse MCP",
			event: hookapi.EventPreToolUse,
			body: payload(`"hook_event_name":"preToolUse","tool_name":"MCP:find_symbol",` +
				`"tool_input":{"query":"Server"}`),
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "conv-1", Tool: hookapi.ToolMCP, CWD: "/repo"},
		},
		{
			name:  "beforeShellExecution",
			event: hookapi.EventPreToolUse,
			body:  payload(`"hook_event_name":"beforeShellExecution","command":"grep -rn Foo .","cwd":"/repo/pkg","sandbox":false`),
			want: hookapi.HookEvent{
				Event: hookapi.EventPreToolUse, SessionID: "conv-1", Tool: hookapi.ToolShell,
				Command: "grep -rn Foo .", CWD: "/repo/pkg",
			},
		},
		{
			name:  "beforeReadFile",
			event: hookapi.EventPreToolUse,
			body:  payload(`"hook_event_name":"beforeReadFile","file_path":"/repo/a.go","content":"package a\n","attachments":[]`),
			want: hookapi.HookEvent{
				Event: hookapi.EventPreToolUse, SessionID: "conv-1", Tool: hookapi.ToolRead,
				Paths: []string{"/repo/a.go"}, CWD: "/repo",
			},
		},
		{
			name:  "afterFileEdit",
			event: hookapi.EventPostToolUse,
			body: payload(`"hook_event_name":"afterFileEdit","file_path":"/repo/internal/server.go",` +
				`"edits":[{"old_string":"a","new_string":"b"}]`),
			want: hookapi.HookEvent{
				Event: hookapi.EventPostToolUse, SessionID: "conv-1", Tool: hookapi.ToolEdit,
				Paths: []string{"/repo/internal/server.go"}, CWD: "/repo",
			},
		},
		{
			name:  "afterShellExecution",
			event: hookapi.EventPostToolUse,
			body:  payload(`"hook_event_name":"afterShellExecution","command":"sed -i s/a/b/ x.go","output":"","duration":12`),
			want: hookapi.HookEvent{
				Event: hookapi.EventPostToolUse, SessionID: "conv-1", Tool: hookapi.ToolShell,
				Command: "sed -i s/a/b/ x.go", CWD: "/repo",
			},
		},
		{
			name:  "subagentStart",
			event: hookapi.EventSubagentStart,
			body:  payload(`"hook_event_name":"subagentStart","subagent_id":"sub-3","subagent_type":"explore","task":"find the router"`),
			want: hookapi.HookEvent{
				Event: hookapi.EventSubagentStart, SessionID: "conv-1", AgentID: "sub-3", AgentType: "explore", CWD: "/repo",
			},
		},
		{
			name:  "preCompact",
			event: hookapi.EventPreCompact,
			body:  payload(`"hook_event_name":"preCompact","trigger":"auto"`),
			want:  hookapi.HookEvent{Event: hookapi.EventPreCompact, SessionID: "conv-1", CWD: "/repo"},
		},
		{
			name:  "missing hook_event_name falls back to the CLI event and payload shape",
			event: hookapi.EventPreToolUse,
			body:  `{"conversation_id":"c2","command":"ls -R","cwd":"/w"}`,
			want: hookapi.HookEvent{
				Event: hookapi.EventPreToolUse, SessionID: "c2", Tool: hookapi.ToolShell, Command: "ls -R", CWD: "/w",
			},
		},
		{
			name:  "cwd falls back to CURSOR_PROJECT_DIR",
			event: hookapi.EventPostToolUse,
			body:  `{"conversation_id":"c3","hook_event_name":"afterFileEdit","file_path":"x.go"}`,
			env:   []string{"PATH=/bin", "CURSOR_PROJECT_DIR=/proj"},
			want: hookapi.HookEvent{
				Event: hookapi.EventPostToolUse, SessionID: "c3", Tool: hookapi.ToolEdit, Paths: []string{"x.go"}, CWD: "/proj",
			},
		},
	}
	a := Adapter{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := a.ParseEvent(c.event, []byte(c.body), c.env)
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			if string(got.Raw) != c.body {
				t.Errorf("Raw not preserved: %s", got.Raw)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("Validate: %v", err)
			}
			got.Raw = nil
			c.want.Agent = hookapi.AgentCursor
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestNormalizeTool(t *testing.T) {
	cases := map[string]hookapi.Tool{
		"Shell": hookapi.ToolShell, "run_terminal_cmd": hookapi.ToolShell, "Terminal": hookapi.ToolShell,
		"Read": hookapi.ToolRead, "read_file": hookapi.ToolRead,
		"Grep": hookapi.ToolGrep, "grep_search": hookapi.ToolGrep,
		"Glob": hookapi.ToolGlob, "glob_file_search": hookapi.ToolGlob, "file_search": hookapi.ToolGlob,
		"Edit": hookapi.ToolEdit, "StrReplace": hookapi.ToolEdit, "MultiEdit": hookapi.ToolEdit,
		"search_replace": hookapi.ToolEdit, "edit_file": hookapi.ToolEdit,
		"Write": hookapi.ToolWrite, "Delete": hookapi.ToolWrite, "delete_file": hookapi.ToolWrite,
		"MCP:find_symbol": hookapi.ToolMCP, "mcp_graphmaster_search": hookapi.ToolMCP, "MCP": hookapi.ToolMCP,
		"Task": hookapi.ToolOther, "codebase_search": hookapi.ToolOther, "": hookapi.ToolOther,
	}
	for in, want := range cases {
		if got := normalizeTool(in); got != want {
			t.Errorf("normalizeTool(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEventErrors(t *testing.T) {
	a := Adapter{}
	for _, body := range []string{"", "   ", "not json", "[1,2]", `"str"`} {
		if _, err := a.ParseEvent(hookapi.EventPreToolUse, []byte(body), nil); err == nil {
			t.Errorf("ParseEvent(%q) should fail", body)
		}
	}
	// An unknown native event with no usable CLI event cannot be mapped.
	if _, err := a.ParseEvent("", []byte(`{"hook_event_name":"beforeSubmitPrompt"}`), nil); err == nil {
		t.Error("unmappable event should fail")
	}
}

func TestIsCursorPayload(t *testing.T) {
	cases := map[string]bool{
		payload(`"hook_event_name":"preToolUse"`):                              true,
		`{"cursor_version":"2.4.0","hook_event_name":"PreToolUse"}`:            true,
		`{"conversation_id":"c","generation_id":"g","tool_name":"Bash"}`:       true,
		`{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Grep"}`: false,
		`{"conversation_id":"c"}`:                                              false,
		`not json`:                                                             false,
		``:                                                                     false,
	}
	for in, want := range cases {
		if got := IsCursorPayload([]byte(in)); got != want {
			t.Errorf("IsCursorPayload(%s) = %v, want %v", in, got, want)
		}
	}
}

func parsed(t *testing.T, ev hookapi.Event, body string) hookapi.HookEvent {
	t.Helper()
	e, err := Adapter{}.ParseEvent(ev, []byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRenderDecision(t *testing.T) {
	preTool := payload(`"hook_event_name":"preToolUse","tool_name":"Grep","tool_input":{"pattern":"Foo"}`)
	beforeShell := payload(`"hook_event_name":"beforeShellExecution","command":"rg Foo","cwd":"/repo"`)
	session := payload(`"hook_event_name":"sessionStart","session_id":"conv-1"`)
	subagent := payload(`"hook_event_name":"subagentStart","subagent_id":"s1","subagent_type":"explore"`)
	afterEdit := payload(`"hook_event_name":"afterFileEdit","file_path":"/repo/a.go"`)
	afterShell := payload(`"hook_event_name":"afterShellExecution","command":"make"`)
	compact := payload(`"hook_event_name":"preCompact"`)

	upd := json.RawMessage(`{"pattern":"Bar"}`)
	cases := []struct {
		name string
		ev   hookapi.Event
		body string
		d    hookapi.Decision
		want string
	}{
		{"pre noop", hookapi.EventPreToolUse, preTool, hookapi.Noop(), `{}`},
		{"pre zero decision", hookapi.EventPreToolUse, preTool, hookapi.Decision{}, `{}`},
		{"pre noop drops context", hookapi.EventPreToolUse, preTool, hookapi.Noop().WithContext("outline"), `{}`},
		{"pre allow", hookapi.EventPreToolUse, preTool, hookapi.Allow(), `{"permission":"allow"}`},
		{"pre allow drops context", hookapi.EventPreToolUse, preTool, hookapi.Allow().WithContext("outline"), `{"permission":"allow"}`},
		{"pre allow user message", hookapi.EventPreToolUse, preTool, hookapi.Allow().WithUserMessage("budget used"),
			`{"permission":"allow","user_message":"budget used"}`},
		{"pre allow updated input", hookapi.EventPreToolUse, preTool, hookapi.Decision{Action: hookapi.ActionAllow, UpdatedInput: upd},
			`{"permission":"allow","updated_input":{"pattern":"Bar"}}`},
		{"pre deny", hookapi.EventPreToolUse, preTool, hookapi.Deny("Use find_symbol.\nresults..."),
			`{"permission":"deny","user_message":"graphmaster: Use find_symbol.","agent_message":"Use find_symbol.\nresults..."}`},
		{"pre deny user message", hookapi.EventPreToolUse, preTool, hookapi.Deny("why").WithUserMessage("redirected"),
			`{"permission":"deny","user_message":"redirected","agent_message":"why"}`},
		{"pre ask", hookapi.EventPreToolUse, preTool, hookapi.Ask("sure?"),
			`{"permission":"ask","user_message":"graphmaster: sure?","agent_message":"sure?"}`},
		{"shell noop", hookapi.EventPreToolUse, beforeShell, hookapi.Noop(), `{}`},
		{"shell deny", hookapi.EventPreToolUse, beforeShell, hookapi.Deny("use search"),
			`{"permission":"deny","user_message":"graphmaster: use search","agent_message":"use search"}`},
		{"shell allow drops updated input", hookapi.EventPreToolUse, beforeShell,
			hookapi.Decision{Action: hookapi.ActionAllow, UpdatedInput: upd}, `{"permission":"allow"}`},
		{"session context", hookapi.EventSessionStart, session, hookapi.Noop().WithContext("graphmaster: 120 files"),
			`{"additional_context":"graphmaster: 120 files"}`},
		{"session no context", hookapi.EventSessionStart, session, hookapi.Allow(), `{}`},
		{"session deny cannot block", hookapi.EventSessionStart, session, hookapi.Deny("x").WithContext("ctx"),
			`{"additional_context":"ctx"}`},
		{"subagent drops context", hookapi.EventSubagentStart, subagent, hookapi.Noop().WithContext("ctx"), `{}`},
		{"subagent allow", hookapi.EventSubagentStart, subagent, hookapi.Allow(), `{"permission":"allow"}`},
		{"after edit deny is ignored", hookapi.EventPostToolUse, afterEdit, hookapi.Deny("x"), `{}`},
		{"after shell", hookapi.EventPostToolUse, afterShell, hookapi.Allow().WithContext("x"), `{}`},
		{"pre compact", hookapi.EventPreCompact, compact, hookapi.Noop().WithContext("x"), `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := Adapter{}.RenderDecision(parsed(t, c.ev, c.body), c.d)
			if out.ExitCode != 0 || len(out.Stderr) != 0 {
				t.Fatalf("exit=%d stderr=%q", out.ExitCode, out.Stderr)
			}
			if !strings.HasSuffix(string(out.Stdout), "\n") {
				t.Errorf("stdout must end with a newline: %q", out.Stdout)
			}
			if got := strings.TrimSuffix(string(out.Stdout), "\n"); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

// TestRenderNeverBlocksUnlessDenied checks every event × non-deny action.
func TestRenderNeverBlocksUnlessDenied(t *testing.T) {
	bodies := map[hookapi.Event]string{
		hookapi.EventSessionStart:  payload(`"hook_event_name":"sessionStart"`),
		hookapi.EventSubagentStart: payload(`"hook_event_name":"subagentStart"`),
		hookapi.EventPreToolUse:    payload(`"hook_event_name":"preToolUse","tool_name":"Read","tool_input":{"file_path":"a.go"}`),
		hookapi.EventPostToolUse:   payload(`"hook_event_name":"afterFileEdit","file_path":"a.go"`),
		hookapi.EventPreCompact:    payload(`"hook_event_name":"preCompact"`),
		hookapi.EventStop:          payload(`"hook_event_name":"stop","status":"completed"`),
	}
	for ev, body := range bodies {
		for _, act := range []hookapi.Action{"", hookapi.ActionNoop, hookapi.ActionAllow, hookapi.ActionAsk} {
			d := hookapi.Decision{Action: act, Reason: "r", AdditionalContext: "c", UserMessage: "u"}
			out := Adapter{}.RenderDecision(parsed(t, ev, body), d)
			var m map[string]any
			if err := json.Unmarshal(out.Stdout, &m); err != nil {
				t.Fatalf("%s/%s: invalid JSON %q", ev, act, out.Stdout)
			}
			if out.ExitCode != 0 || m["permission"] == "deny" {
				t.Errorf("%s/%s blocks: %s exit=%d", ev, act, out.Stdout, out.ExitCode)
			}
		}
	}
}

// TestRenderWithoutRaw covers events built by hand (no native payload).
func TestRenderWithoutRaw(t *testing.T) {
	ev := hookapi.HookEvent{Agent: hookapi.AgentCursor, Event: hookapi.EventPreToolUse, Tool: hookapi.ToolShell}
	out := Adapter{}.RenderDecision(ev, hookapi.Deny("no"))
	if !strings.Contains(string(out.Stdout), `"permission":"deny"`) {
		t.Fatalf("got %s", out.Stdout)
	}
}
