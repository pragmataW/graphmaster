package claude

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// The payloads below are SYNTHETIC: hand-written from Claude Code's documented
// hook schema. Real captures belong in testdata/hooks/claude/ and are replayed
// by the hook integration tests.

const synthPreGrep = `{
  "session_id": "4f1c2a9e-0b7d-4c1e-9a55-2d3f6b8e1c00",
  "transcript_path": "/Users/me/.claude/projects/-Users-me-code-shop/4f1c2a9e.jsonl",
  "cwd": "/Users/me/code/shop",
  "permission_mode": "default",
  "hook_event_name": "PreToolUse",
  "tool_name": "Grep",
  "tool_input": {"pattern": "func NewCart", "path": "internal/cart", "glob": "*.go", "output_mode": "content", "-n": true},
  "tool_use_id": "toolu_01ABCgrep"
}`

const synthPreGlobSubagent = `{
  "session_id": "s1",
  "transcript_path": "/tmp/t.jsonl",
  "cwd": "/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "Glob",
  "tool_input": {"pattern": "**/*.ts", "path": "/repo/web"},
  "tool_use_id": "toolu_glob",
  "agent_id": "agent-7f3e",
  "agent_type": "Explore"
}`

const synthPreRead = `{
  "session_id": "s1", "cwd": "/repo", "hook_event_name": "PreToolUse",
  "tool_name": "Read",
  "tool_input": {"file_path": "/repo/internal/cart/cart.go", "offset": 10, "limit": 50},
  "tool_use_id": "toolu_read"
}`

const synthPreBash = `{
  "session_id": "s1", "cwd": "/repo", "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": {"command": "grep -rn NewCart .", "description": "Search for NewCart", "timeout": 120000},
  "tool_use_id": "toolu_bash"
}`

const synthPostEdit = `{
  "session_id": "s1", "cwd": "/repo", "hook_event_name": "PostToolUse",
  "tool_name": "Edit",
  "tool_input": {"file_path": "/repo/a.go", "old_string": "x", "new_string": "y"},
  "tool_response": {"filePath": "/repo/a.go", "success": true},
  "tool_use_id": "toolu_edit"
}`

const synthPostMultiEdit = `{
  "session_id": "s1", "cwd": "/repo", "hook_event_name": "PostToolUse",
  "tool_name": "MultiEdit",
  "tool_input": {"file_path": "b.go", "edits": [{"old_string": "a", "new_string": "b"}]},
  "tool_use_id": "toolu_me"
}`

const synthPostWrite = `{
  "session_id": "s1", "cwd": "/repo", "hook_event_name": "PostToolUse",
  "tool_name": "Write",
  "tool_input": {"file_path": "/repo/new.go", "content": "package x\n"},
  "tool_use_id": "toolu_write"
}`

const synthPostNotebook = `{
  "session_id": "s1", "cwd": "/repo", "hook_event_name": "PostToolUse",
  "tool_name": "NotebookEdit",
  "tool_input": {"notebook_path": "/repo/nb.ipynb", "new_source": "print(1)"},
  "tool_use_id": "toolu_nb"
}`

const synthPreMCP = `{
  "session_id": "s1", "cwd": "/repo", "hook_event_name": "PreToolUse",
  "tool_name": "mcp__graphmaster__find_symbol",
  "tool_input": {"query": "NewCart"},
  "tool_use_id": "toolu_mcp"
}`

const synthPreTask = `{
  "session_id": "s1", "cwd": "/repo", "hook_event_name": "PreToolUse",
  "tool_name": "Task",
  "tool_input": {"subagent_type": "Explore", "prompt": "find cart code"},
  "tool_use_id": "toolu_task"
}`

const synthSessionStart = `{
  "session_id": "s1",
  "transcript_path": "/tmp/t.jsonl",
  "cwd": "/repo",
  "hook_event_name": "SessionStart",
  "source": "startup",
  "model": "claude-opus-5-5"
}`

const synthSubagentStart = `{
  "session_id": "s1", "transcript_path": "/tmp/t.jsonl", "cwd": "/repo",
  "hook_event_name": "SubagentStart",
  "agent_id": "agent-plan-1", "agent_type": "Plan"
}`

const synthPreCompact = `{
  "session_id": "s1", "transcript_path": "/tmp/t.jsonl", "cwd": "/repo",
  "hook_event_name": "PreCompact", "trigger": "auto", "custom_instructions": ""
}`

// Cursor executing a Claude Code hook: Cursor-flavoured fields, no cwd.
const synthCursorAsClaude = `{
  "conversation_id": "conv-1", "generation_id": "gen-9", "cursor_version": "2.1.4",
  "hook_event_name": "PreToolUse", "workspace_roots": ["/repo"],
  "tool_name": "Grep", "tool_input": {"pattern": "NewCart"}
}`

func TestParseEvent(t *testing.T) {
	a := Adapter{}
	tests := []struct {
		name    string
		event   hookapi.Event
		payload string
		env     []string
		want    hookapi.HookEvent
	}{
		{"grep", hookapi.EventPreToolUse, synthPreGrep, nil, hookapi.HookEvent{
			SessionID: "4f1c2a9e-0b7d-4c1e-9a55-2d3f6b8e1c00", Event: hookapi.EventPreToolUse,
			Tool: hookapi.ToolGrep, Pattern: "func NewCart", Paths: []string{"internal/cart"}, Glob: "*.go",
			CWD: "/Users/me/code/shop", ToolUseID: "toolu_01ABCgrep",
		}},
		{"glob in subagent", hookapi.EventPreToolUse, synthPreGlobSubagent, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPreToolUse, Tool: hookapi.ToolGlob, Glob: "**/*.ts",
			Paths: []string{"/repo/web"}, CWD: "/repo", ToolUseID: "toolu_glob", AgentID: "agent-7f3e", AgentType: "Explore",
		}},
		{"read", hookapi.EventPreToolUse, synthPreRead, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPreToolUse, Tool: hookapi.ToolRead,
			Paths: []string{"/repo/internal/cart/cart.go"}, CWD: "/repo", ToolUseID: "toolu_read",
		}},
		{"bash", hookapi.EventPreToolUse, synthPreBash, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPreToolUse, Tool: hookapi.ToolShell,
			Command: "grep -rn NewCart .", CWD: "/repo", ToolUseID: "toolu_bash",
		}},
		{"edit", hookapi.EventPostToolUse, synthPostEdit, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPostToolUse, Tool: hookapi.ToolEdit,
			Paths: []string{"/repo/a.go"}, CWD: "/repo", ToolUseID: "toolu_edit",
		}},
		{"multiedit", hookapi.EventPostToolUse, synthPostMultiEdit, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPostToolUse, Tool: hookapi.ToolEdit,
			Paths: []string{"b.go"}, CWD: "/repo", ToolUseID: "toolu_me",
		}},
		{"write", hookapi.EventPostToolUse, synthPostWrite, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPostToolUse, Tool: hookapi.ToolWrite,
			Paths: []string{"/repo/new.go"}, CWD: "/repo", ToolUseID: "toolu_write",
		}},
		{"notebook", hookapi.EventPostToolUse, synthPostNotebook, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPostToolUse, Tool: hookapi.ToolEdit,
			Paths: []string{"/repo/nb.ipynb"}, CWD: "/repo", ToolUseID: "toolu_nb",
		}},
		{"mcp", hookapi.EventPreToolUse, synthPreMCP, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPreToolUse, Tool: hookapi.ToolMCP, CWD: "/repo", ToolUseID: "toolu_mcp",
		}},
		{"other tool", hookapi.EventPreToolUse, synthPreTask, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPreToolUse, Tool: hookapi.ToolOther, CWD: "/repo", ToolUseID: "toolu_task",
		}},
		{"session start", hookapi.EventSessionStart, synthSessionStart, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventSessionStart, CWD: "/repo",
		}},
		{"subagent start", hookapi.EventSubagentStart, synthSubagentStart, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventSubagentStart, CWD: "/repo", AgentID: "agent-plan-1", AgentType: "Plan",
		}},
		{"pre compact", hookapi.EventPreCompact, synthPreCompact, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventPreCompact, CWD: "/repo",
		}},
		{"event from payload when arg empty", "", synthSessionStart, nil, hookapi.HookEvent{
			SessionID: "s1", Event: hookapi.EventSessionStart, CWD: "/repo",
		}},
		{"cwd falls back to CLAUDE_PROJECT_DIR", hookapi.EventSessionStart,
			`{"session_id":"s2","hook_event_name":"SessionStart"}`,
			[]string{"PATH=/bin", "CLAUDE_PROJECT_DIR=/proj"}, hookapi.HookEvent{
				SessionID: "s2", Event: hookapi.EventSessionStart, CWD: "/proj",
			}},
		{"cursor running claude hook", hookapi.EventPreToolUse, synthCursorAsClaude, nil, hookapi.HookEvent{
			SessionID: "conv-1", Event: hookapi.EventPreToolUse, Tool: hookapi.ToolGrep, Pattern: "NewCart", CWD: "/repo",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.ParseEvent(tt.event, []byte(tt.payload), tt.env)
			if err != nil {
				t.Fatal(err)
			}
			if string(got.Raw) != tt.payload {
				t.Errorf("Raw not preserved verbatim")
			}
			if err := got.Validate(); err != nil {
				t.Errorf("invalid event: %v", err)
			}
			got.Raw = nil
			tt.want.Agent = hookapi.AgentClaude
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseEventErrors(t *testing.T) {
	a := Adapter{}
	tests := []struct {
		name    string
		event   hookapi.Event
		payload string
	}{
		{"empty", hookapi.EventPreToolUse, ""},
		{"not json", hookapi.EventPreToolUse, "grep -r foo"},
		{"array", hookapi.EventPreToolUse, "[]"},
		{"tool event without tool", hookapi.EventPreToolUse, `{"session_id":"s","hook_event_name":"PreToolUse"}`},
		{"unknown event in payload", "", `{"hook_event_name":"UserPromptSubmit"}`},
		{"invalid event arg", "bogus", `{"session_id":"s"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := a.ParseEvent(tt.event, []byte(tt.payload), nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNormalizeTool(t *testing.T) {
	for name, want := range map[string]hookapi.Tool{
		"Grep": hookapi.ToolGrep, "Glob": hookapi.ToolGlob, "Read": hookapi.ToolRead,
		"Bash": hookapi.ToolShell, "Edit": hookapi.ToolEdit, "MultiEdit": hookapi.ToolEdit,
		"NotebookEdit": hookapi.ToolEdit, "Write": hookapi.ToolWrite,
		"mcp__github__search_code": hookapi.ToolMCP, "WebFetch": hookapi.ToolOther,
		"Task": hookapi.ToolOther, "TodoWrite": hookapi.ToolOther, "": hookapi.ToolOther,
		// Cursor running Claude hooks reports its own tool names.
		"Shell": hookapi.ToolShell,
	} {
		if got := normalizeTool(name); got != want {
			t.Errorf("normalizeTool(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFromCursor(t *testing.T) {
	if !FromCursor([]byte(synthCursorAsClaude)) {
		t.Error("cursor payload not detected")
	}
	if !FromCursor([]byte(`{"conversation_id":"c","generation_id":"g"}`)) {
		t.Error("conversation_id+generation_id not detected")
	}
	for _, p := range []string{synthPreGrep, synthSessionStart, "", "nope", `{"conversation_id":"c"}`} {
		if FromCursor([]byte(p)) {
			t.Errorf("false positive for %q", p)
		}
	}
}

func ev(e hookapi.Event, tool hookapi.Tool) hookapi.HookEvent {
	return hookapi.HookEvent{Agent: hookapi.AgentClaude, Event: e, Tool: tool}
}

func TestRenderDecision(t *testing.T) {
	a := Adapter{}
	pre := ev(hookapi.EventPreToolUse, hookapi.ToolGrep)
	post := ev(hookapi.EventPostToolUse, hookapi.ToolEdit)
	tests := []struct {
		name string
		ev   hookapi.HookEvent
		d    hookapi.Decision
		want string // exact stdout; "" means silent
	}{
		{"noop is silent", pre, hookapi.Noop(), ""},
		{"zero decision is silent", pre, hookapi.Decision{}, ""},
		{"allow does not grant permission", pre, hookapi.Allow(), ""},
		{"deny", pre, hookapi.Deny("Use find_symbol: <cart.go:12>"),
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Use find_symbol: <cart.go:12>"}}`},
		{"deny with user message", pre, hookapi.Deny("r").WithUserMessage("graphmaster redirected a grep"),
			`{"systemMessage":"graphmaster redirected a grep","hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"r"}}`},
		{"ask", pre, hookapi.Ask("large scan"),
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"large scan"}}`},
		{"read outline context without deny", ev(hookapi.EventPreToolUse, hookapi.ToolRead), hookapi.Allow().WithContext("outline:\nfunc A 1-9"),
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"outline:\nfunc A 1-9"}}`},
		{"allow with reminder reason only", pre, hookapi.Decision{Action: hookapi.ActionAllow, Reason: "budget exhausted"},
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"budget exhausted"}}`},
		{"updated input", pre, hookapi.Decision{Action: hookapi.ActionAllow, UpdatedInput: json.RawMessage(`{"pattern":"x"}`)},
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"pattern":"x"}}}`},
		{"post context", post, hookapi.Noop().WithContext("reindexed a.go"),
			`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"reindexed a.go"}}`},
		{"post deny never blocks", post, hookapi.Deny("stale"),
			`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"stale"}}`},
		{"session start", ev(hookapi.EventSessionStart, ""), hookapi.Noop().WithContext("graphmaster: 1203 files indexed"),
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"graphmaster: 1203 files indexed"}}`},
		{"subagent start", ev(hookapi.EventSubagentStart, ""), hookapi.Allow().WithContext("use find_symbol"),
			`{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"use find_symbol"}}`},
		{"session start deny degrades to context", ev(hookapi.EventSessionStart, ""), hookapi.Deny("why"),
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"why"}}`},
		{"pre compact drops context", ev(hookapi.EventPreCompact, ""), hookapi.Deny("no").WithContext("ctx"), ""},
		{"pre compact user message", ev(hookapi.EventPreCompact, ""), hookapi.Noop().WithUserMessage("index flushed"),
			`{"systemMessage":"index flushed"}`},
		{"stop never blocks", ev(hookapi.EventStop, ""), hookapi.Deny("keep going"), ""},
		{"no html escaping", ev(hookapi.EventSessionStart, ""), hookapi.Noop().WithContext("a <b> & c"),
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"a <b> & c"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := a.RenderDecision(tt.ev, tt.d)
			if out.ExitCode != 0 {
				t.Errorf("exit code %d, want 0", out.ExitCode)
			}
			if len(out.Stderr) != 0 {
				t.Errorf("stderr %q", out.Stderr)
			}
			got := strings.TrimSuffix(string(out.Stdout), "\n")
			if got != tt.want {
				t.Errorf("stdout\n got  %s\n want %s", got, tt.want)
			}
			if got != "" && !json.Valid(out.Stdout) {
				t.Errorf("invalid JSON %s", got)
			}
			if !tt.d.Blocks() && strings.Contains(got, `"deny"`) {
				t.Errorf("non-deny decision rendered a deny: %s", got)
			}
		})
	}
}

func TestRenderNeverBlocksWithoutDeny(t *testing.T) {
	a := Adapter{}
	for _, e := range hookapi.AllEvents() {
		for _, d := range []hookapi.Decision{
			hookapi.Noop(), hookapi.Allow(), hookapi.Noop().WithContext("c"), hookapi.Allow().WithUserMessage("u"),
		} {
			out := a.RenderDecision(ev(e, hookapi.ToolGrep), d)
			s := string(out.Stdout)
			if out.ExitCode != 0 || strings.Contains(s, "deny") || strings.Contains(s, `"block"`) ||
				strings.Contains(s, `"continue"`) || strings.Contains(s, "permissionDecision") {
				t.Errorf("%s %+v rendered blocking output %q (exit %d)", e, d, s, out.ExitCode)
			}
		}
	}
}
