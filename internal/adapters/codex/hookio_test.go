package codex

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// The payloads below are SYNTHETIC: they follow the documented Codex CLI hook
// schema (Claude-compatible field names) but were written by hand. Real
// captures belong in testdata/hooks/codex/ (see its README) and are replayed
// by the hook integration tests.
const (
	synthSessionStart = `{
  "session_id": "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f",
  "transcript_path": "/home/dev/.codex/sessions/2026/10/11/rollout-019a3c1e.jsonl",
  "cwd": "/work/repo",
  "hook_event_name": "SessionStart",
  "model": "gpt-5-codex",
  "source": "startup"
}`
	synthSubagentStart = `{
  "session_id": "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f",
  "cwd": "/work/repo",
  "hook_event_name": "SubagentStart",
  "agent_id": "agent-7f3e",
  "agent_type": "explorer"
}`
	synthPreBash = `{
  "session_id": "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f",
  "turn_id": "turn-4",
  "transcript_path": "/home/dev/.codex/sessions/2026/10/11/rollout-019a3c1e.jsonl",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "model": "gpt-5-codex",
  "tool_name": "Bash",
  "tool_input": {"command": "rg -n \"NewEngine\" internal/"},
  "tool_use_id": "call_Q1w2E3r4"
}`
	synthPreShellArgv = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "shell",
  "tool_input": {"command": ["bash", "-lc", "grep -rn Foo ."], "workdir": "/work/repo/sub"},
  "tool_use_id": "call_argv"
}`
	synthPreExecArgv = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "exec_command",
  "tool_input": {"cmd": "ls -R"},
  "tool_use_id": "call_exec"
}`
	synthPreArgvPlain = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "local_shell",
  "tool_input": {"command": ["rg", "-n", "two words", "src"]},
  "tool_use_id": "call_plain"
}`
	synthPostPatch = `{
  "session_id": "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f",
  "cwd": "/work/repo",
  "hook_event_name": "PostToolUse",
  "tool_name": "apply_patch",
  "tool_input": {"command": "*** Begin Patch\n*** Update File: internal/a.go\n@@\n-x\n+y\n*** Add File: internal/b.go\n+package internal\n*** End Patch\n"},
  "tool_response": {"output": "Success. Updated the following files:\nM internal/a.go\nA internal/b.go\n"},
  "tool_use_id": "call_patch"
}`
	synthPrePatchInput = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "apply_patch",
  "tool_input": {"input": "*** Begin Patch\n*** Delete File: old.go\n*** End Patch"},
  "tool_use_id": "call_patch2"
}`
	synthPrePatchString = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "apply_patch",
  "tool_input": "*** Begin Patch\n*** Update File: x.rs\n*** End Patch",
  "tool_use_id": "call_patch3"
}`
	synthPreMCP = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "mcp__github__search_code",
  "tool_input": {"q": "NewEngine"},
  "tool_use_id": "call_mcp"
}`
	synthPreGrepFiles = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "grep_files",
  "tool_input": {"pattern": "ParseEvent", "path": "internal", "include": "*.go"},
  "tool_use_id": "call_grep"
}`
	synthPreReadFile = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "read_file",
  "tool_input": {"file_path": "/work/repo/internal/a.go", "offset": 1, "limit": 2000},
  "tool_use_id": "call_read"
}`
	synthPreUnknown = `{
  "session_id": "s1",
  "cwd": "/work/repo",
  "hook_event_name": "PreToolUse",
  "tool_name": "update_plan",
  "tool_input": {"plan": []},
  "tool_use_id": "call_plan"
}`
	synthPreNoTool = `{"session_id": "s1", "hook_event_name": "PreToolUse"}`
)

func TestParseEvent(t *testing.T) {
	tests := []struct {
		name    string
		event   hookapi.Event
		payload string
		env     []string
		want    hookapi.HookEvent
	}{
		{
			name: "session start", event: hookapi.EventSessionStart, payload: synthSessionStart,
			want: hookapi.HookEvent{Event: hookapi.EventSessionStart, SessionID: "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f", CWD: "/work/repo"},
		},
		{
			name: "subagent start carries agent id", event: hookapi.EventSubagentStart, payload: synthSubagentStart,
			want: hookapi.HookEvent{Event: hookapi.EventSubagentStart, SessionID: "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f", CWD: "/work/repo", AgentID: "agent-7f3e", AgentType: "explorer"},
		},
		{
			name: "bash command string", event: hookapi.EventPreToolUse, payload: synthPreBash,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f", CWD: "/work/repo", Tool: hookapi.ToolShell, Command: `rg -n "NewEngine" internal/`, ToolUseID: "call_Q1w2E3r4"},
		},
		{
			name: "shell argv via bash -lc, workdir overrides cwd", event: hookapi.EventPreToolUse, payload: synthPreShellArgv,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo/sub", Tool: hookapi.ToolShell, Command: "grep -rn Foo .", ToolUseID: "call_argv"},
		},
		{
			name: "exec_command cmd field", event: hookapi.EventPreToolUse, payload: synthPreExecArgv,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo", Tool: hookapi.ToolShell, Command: "ls -R", ToolUseID: "call_exec"},
		},
		{
			name: "plain argv is shell-quoted", event: hookapi.EventPreToolUse, payload: synthPreArgvPlain,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo", Tool: hookapi.ToolShell, Command: "rg -n 'two words' src", ToolUseID: "call_plain"},
		},
		{
			name: "apply_patch post", event: hookapi.EventPostToolUse, payload: synthPostPatch,
			want: hookapi.HookEvent{Event: hookapi.EventPostToolUse, SessionID: "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f", CWD: "/work/repo", Tool: hookapi.ToolPatch, Paths: []string{"internal/a.go", "internal/b.go"}, ToolUseID: "call_patch"},
		},
		{
			name: "apply_patch input field", event: hookapi.EventPreToolUse, payload: synthPrePatchInput,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo", Tool: hookapi.ToolPatch, Paths: []string{"old.go"}, ToolUseID: "call_patch2"},
		},
		{
			name: "apply_patch raw string input", event: hookapi.EventPreToolUse, payload: synthPrePatchString,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo", Tool: hookapi.ToolPatch, Paths: []string{"x.rs"}, ToolUseID: "call_patch3"},
		},
		{
			name: "mcp tool", event: hookapi.EventPreToolUse, payload: synthPreMCP,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo", Tool: hookapi.ToolMCP, ToolUseID: "call_mcp"},
		},
		{
			name: "grep_files", event: hookapi.EventPreToolUse, payload: synthPreGrepFiles,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo", Tool: hookapi.ToolGrep, Pattern: "ParseEvent", Paths: []string{"internal"}, Glob: "*.go", ToolUseID: "call_grep"},
		},
		{
			name: "read_file", event: hookapi.EventPreToolUse, payload: synthPreReadFile,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo", Tool: hookapi.ToolRead, Paths: []string{"/work/repo/internal/a.go"}, ToolUseID: "call_read"},
		},
		{
			name: "unknown tool is other", event: hookapi.EventPreToolUse, payload: synthPreUnknown,
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/work/repo", Tool: hookapi.ToolOther, ToolUseID: "call_plan"},
		},
		{
			name: "missing tool and cwd fall back", event: hookapi.EventPreToolUse, payload: synthPreNoTool,
			env:  []string{"HOME=/home/dev", "PWD=/from/env"},
			want: hookapi.HookEvent{Event: hookapi.EventPreToolUse, SessionID: "s1", CWD: "/from/env", Tool: hookapi.ToolOther},
		},
		{
			name: "event derived from payload when argument empty", event: "", payload: synthSessionStart,
			want: hookapi.HookEvent{Event: hookapi.EventSessionStart, SessionID: "019a3c1e-7d2b-7c40-9f1e-3b2a1c0d9e8f", CWD: "/work/repo"},
		},
	}
	a := Adapter{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.ParseEvent(tt.event, []byte(tt.payload), tt.env)
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if string(got.Raw) != tt.payload {
				t.Errorf("Raw not preserved")
			}
			tt.want.Agent = hookapi.AgentCodex
			got.Raw = nil
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseEvent()\n got  %+v\n want %+v", got, tt.want)
			}
		})
	}
}

func TestParseEventErrors(t *testing.T) {
	a := Adapter{}
	for name, tc := range map[string]struct {
		event   hookapi.Event
		payload string
	}{
		"invalid json":         {hookapi.EventPreToolUse, `{"tool_name":`},
		"empty payload":        {hookapi.EventPreToolUse, ``},
		"json array":           {hookapi.EventPreToolUse, `[]`},
		"unknown event":        {"", `{"hook_event_name":"UserPromptSubmit"}`},
		"no event at all":      {"", `{}`},
		"invalid event string": {"bogus", `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := a.ParseEvent(tc.event, []byte(tc.payload), nil); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestRenderDecision(t *testing.T) {
	pre := hookapi.HookEvent{Agent: hookapi.AgentCodex, Event: hookapi.EventPreToolUse, Tool: hookapi.ToolShell}
	post := hookapi.HookEvent{Agent: hookapi.AgentCodex, Event: hookapi.EventPostToolUse, Tool: hookapi.ToolPatch}
	start := hookapi.HookEvent{Agent: hookapi.AgentCodex, Event: hookapi.EventSessionStart}
	sub := hookapi.HookEvent{Agent: hookapi.AgentCodex, Event: hookapi.EventSubagentStart, AgentID: "a1"}
	compact := hookapi.HookEvent{Agent: hookapi.AgentCodex, Event: hookapi.EventPreCompact}
	stop := hookapi.HookEvent{Agent: hookapi.AgentCodex, Event: hookapi.EventStop}

	tests := []struct {
		name   string
		ev     hookapi.HookEvent
		d      hookapi.Decision
		stdout string
	}{
		{"pre noop is silent", pre, hookapi.Noop(), ""},
		{"pre zero decision is silent", pre, hookapi.Decision{}, ""},
		{"pre allow is silent", pre, hookapi.Allow(), ""},
		{
			"pre deny", pre, hookapi.Deny("use graphmaster find_symbol"),
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"use graphmaster find_symbol"}}` + "\n",
		},
		{
			"pre deny keeps html characters", pre, hookapi.Deny("a <b> & c"),
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"a <b> & c"}}` + "\n",
		},
		{
			"pre deny with context and user message", pre, hookapi.Deny("r").WithContext("ctx").WithUserMessage("hi"),
			`{"systemMessage":"hi","hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"r","additionalContext":"ctx"}}` + "\n",
		},
		{
			"pre allow with context adds context without deciding", pre, hookapi.Allow().WithContext("outline"),
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"outline"}}` + "\n",
		},
		{
			"pre noop with user message only", pre, hookapi.Noop().WithUserMessage("stale index"),
			`{"systemMessage":"stale index"}` + "\n",
		},
		{"pre ask degrades to silent", pre, hookapi.Ask("sure?"), ""},
		{
			"pre ask with context keeps context", pre, hookapi.Ask("sure?").WithContext("c"),
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"c"}}` + "\n",
		},
		{
			"pre allow ignores updated input", pre, hookapi.Decision{Action: hookapi.ActionAllow, UpdatedInput: json.RawMessage(`{"command":"ls"}`)},
			"",
		},
		{"post noop", post, hookapi.Noop(), ""},
		{
			"post context", post, hookapi.Noop().WithContext("reindexed 2 files"),
			`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"reindexed 2 files"}}` + "\n",
		},
		{
			"post deny becomes block feedback", post, hookapi.Deny("bad"),
			`{"decision":"block","reason":"bad"}` + "\n",
		},
		{
			"session start context", start, hookapi.Noop().WithContext("graphmaster: 1200 files"),
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"graphmaster: 1200 files"}}` + "\n",
		},
		{"session start empty", start, hookapi.Allow(), ""},
		{
			"session start deny never blocks, reason becomes context", start, hookapi.Deny("x"),
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"x"}}` + "\n",
		},
		{
			"subagent start context", sub, hookapi.Noop().WithContext("cheat sheet"),
			`{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"cheat sheet"}}` + "\n",
		},
		{"pre compact is silent", compact, hookapi.Noop().WithContext("ignored"), ""},
		{"stop is silent", stop, hookapi.Deny("ignored"), ""},
	}
	a := Adapter{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := a.RenderDecision(tt.ev, tt.d)
			if out.ExitCode != 0 {
				t.Errorf("exit code = %d, want 0", out.ExitCode)
			}
			if string(out.Stdout) != tt.stdout {
				t.Errorf("stdout\n got  %q\n want %q", out.Stdout, tt.stdout)
			}
			if len(out.Stderr) != 0 {
				t.Errorf("stderr = %q, want empty", out.Stderr)
			}
			if len(out.Stdout) > 0 && !json.Valid(out.Stdout) {
				t.Errorf("stdout is not valid JSON")
			}
		})
	}
}

// Noop and allow must never produce output that could block, whatever the
// event or extra fields.
func TestRenderNeverBlocksOnNonDeny(t *testing.T) {
	a := Adapter{}
	for _, ev := range hookapi.AllEvents() {
		for _, d := range []hookapi.Decision{
			hookapi.Noop(), hookapi.Allow(), hookapi.Ask("q"),
			hookapi.Allow().WithContext("c").WithUserMessage("u"),
		} {
			out := a.RenderDecision(hookapi.HookEvent{Agent: hookapi.AgentCodex, Event: ev, Tool: hookapi.ToolShell}, d)
			s := string(out.Stdout)
			if out.ExitCode != 0 || strings.Contains(s, "deny") || strings.Contains(s, "block") {
				t.Errorf("%s/%s: blocking output %q exit %d", ev, d.Action, s, out.ExitCode)
			}
		}
	}
}

func TestParseRenderRoundTrip(t *testing.T) {
	a := Adapter{}
	ev, err := a.ParseEvent(hookapi.EventPreToolUse, []byte(synthPreBash), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := a.RenderDecision(ev, hookapi.Deny("why"))
	var got struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Stdout, &got); err != nil {
		t.Fatal(err)
	}
	if got.HookSpecificOutput.HookEventName != "PreToolUse" || got.HookSpecificOutput.PermissionDecision != "deny" || got.HookSpecificOutput.PermissionDecisionReason != "why" {
		t.Fatalf("unexpected %+v", got)
	}
}
