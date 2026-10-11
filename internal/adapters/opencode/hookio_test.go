package opencode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// The payloads below are SYNTHETIC: they follow the stdin protocol of the
// generated plugin (plugin.js.tmpl), which forwards OpenCode v1.1 hook inputs
// ({tool, sessionID, callID} + output.args). Real captures belong in
// testdata/hooks/opencode/ (see testdata/hooks/README.md).

func TestParseEventTools(t *testing.T) {
	tests := []struct {
		name    string
		event   hookapi.Event
		payload string
		want    hookapi.HookEvent
	}{
		{
			name:    "grep with path and include",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"grep","sessionID":"ses_1","callID":"call_1","args":{"pattern":"func ParseEvent","path":"internal","include":"*.go"},"cwd":"/repo"}`,
			want: hookapi.HookEvent{Tool: hookapi.ToolGrep, SessionID: "ses_1", ToolUseID: "call_1",
				Pattern: "func ParseEvent", Paths: []string{"internal"}, Glob: "*.go", CWD: "/repo"},
		},
		{
			name:    "grep without path",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"grep","sessionID":"ses_1","callID":"c","args":{"pattern":"Foo"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolGrep, SessionID: "ses_1", ToolUseID: "c", Pattern: "Foo", CWD: "/repo"},
		},
		{
			name:    "glob",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"glob","sessionID":"s","callID":"c","args":{"pattern":"**/*.ts","path":"/repo/src"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolGlob, SessionID: "s", ToolUseID: "c", Glob: "**/*.ts", Paths: []string{"/repo/src"}, CWD: "/repo"},
		},
		{
			name:    "list is a tree-wide listing",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"list","sessionID":"s","callID":"c","args":{"path":"internal","ignore":["vendor/"]},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolGlob, SessionID: "s", ToolUseID: "c", Glob: "**", Paths: []string{"internal"}, CWD: "/repo"},
		},
		{
			name:    "read",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"read","sessionID":"s","callID":"c","args":{"filePath":"/repo/main.go","offset":10,"limit":50},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolRead, SessionID: "s", ToolUseID: "c", Paths: []string{"/repo/main.go"}, CWD: "/repo"},
		},
		{
			name:    "bash uses workdir as cwd",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"bash","sessionID":"s","callID":"c","args":{"command":"rg -n Foo .","workdir":"/repo/pkg","description":"search"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolShell, SessionID: "s", ToolUseID: "c", Command: "rg -n Foo .", CWD: "/repo/pkg"},
		},
		{
			name:    "bash relative workdir resolves against cwd",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"bash","sessionID":"s","callID":"c","args":{"command":"ls","workdir":"pkg"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolShell, SessionID: "s", ToolUseID: "c", Command: "ls", CWD: "/repo/pkg"},
		},
		{
			name:    "bash after (post-tool-use)",
			event:   hookapi.EventPostToolUse,
			payload: `{"tool":"bash","sessionID":"s","callID":"c","args":{"command":"sed -i s/a/b/ x.go"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolShell, SessionID: "s", ToolUseID: "c", Command: "sed -i s/a/b/ x.go", CWD: "/repo"},
		},
		{
			name:    "file.edited forwarded as edit",
			event:   hookapi.EventPostToolUse,
			payload: `{"tool":"edit","args":{"filePath":"/repo/a.go"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolEdit, Paths: []string{"/repo/a.go"}, CWD: "/repo"},
		},
		{
			name:    "write",
			event:   hookapi.EventPostToolUse,
			payload: `{"tool":"write","sessionID":"s","callID":"c","args":{"filePath":"/repo/new.go","content":"package x"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolWrite, SessionID: "s", ToolUseID: "c", Paths: []string{"/repo/new.go"}, CWD: "/repo"},
		},
		{
			name:    "legacy multiedit",
			event:   hookapi.EventPostToolUse,
			payload: `{"tool":"multiedit","sessionID":"s","callID":"c","args":{"filePath":"/repo/a.go","edits":[]},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolEdit, SessionID: "s", ToolUseID: "c", Paths: []string{"/repo/a.go"}, CWD: "/repo"},
		},
		{
			name:  "apply_patch paths",
			event: hookapi.EventPostToolUse,
			payload: `{"tool":"apply_patch","sessionID":"s","callID":"c","cwd":"/repo","args":{"patchText":` +
				jsonString("*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** Move to: b.go\n*** Add File: c.go\n+package c\n*** Delete File: d.go\n*** Update File: a.go\n*** End Patch") + `}}`,
			want: hookapi.HookEvent{Tool: hookapi.ToolPatch, SessionID: "s", ToolUseID: "c", Paths: []string{"a.go", "b.go", "c.go", "d.go"}, CWD: "/repo"},
		},
		{
			name:    "mcp tool",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"graphmaster_find_symbol","sessionID":"s","callID":"c","args":{"query":"Foo"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolMCP, SessionID: "s", ToolUseID: "c", CWD: "/repo"},
		},
		{
			name:    "built-in with underscore is not mcp",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"plan_enter","sessionID":"s","callID":"c","args":{},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolOther, SessionID: "s", ToolUseID: "c", CWD: "/repo"},
		},
		{
			name:    "other tool",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"webfetch","sessionID":"s","callID":"c","args":{"url":"https://example.com"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolOther, SessionID: "s", ToolUseID: "c", CWD: "/repo"},
		},
		{
			name:    "subagent tool call",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"grep","sessionID":"ses_parent","agentID":"ses_child","agentType":"explore","callID":"c","args":{"pattern":"Foo"},"cwd":"/repo"}`,
			want: hookapi.HookEvent{Tool: hookapi.ToolGrep, SessionID: "ses_parent", AgentID: "ses_child", AgentType: "explore",
				ToolUseID: "c", Pattern: "Foo", CWD: "/repo"},
		},
		{
			name:    "missing args object",
			event:   hookapi.EventPreToolUse,
			payload: `{"tool":"grep","sessionID":"s","callID":"c","cwd":"/repo"}`,
			want:    hookapi.HookEvent{Tool: hookapi.ToolGrep, SessionID: "s", ToolUseID: "c", CWD: "/repo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Adapter{}.ParseEvent(tt.event, []byte(tt.payload), nil)
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			want := tt.want
			want.Agent = hookapi.AgentOpenCode
			want.Event = tt.event
			want.Raw = json.RawMessage(tt.payload)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ParseEvent mismatch\n got: %+v\nwant: %+v", got, want)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("Validate: %v", err)
			}
		})
	}
}

func TestParseEventLifecycle(t *testing.T) {
	tests := []struct {
		name    string
		event   hookapi.Event
		payload string
		want    hookapi.HookEvent
	}{
		{
			name:    "session start",
			event:   hookapi.EventSessionStart,
			payload: `{"sessionID":"ses_1","cwd":"/repo"}`,
			want:    hookapi.HookEvent{SessionID: "ses_1", CWD: "/repo"},
		},
		{
			name:    "subagent start",
			event:   hookapi.EventSubagentStart,
			payload: `{"sessionID":"ses_1","agentID":"ses_2","agentType":"general","cwd":"/repo"}`,
			want:    hookapi.HookEvent{SessionID: "ses_1", AgentID: "ses_2", AgentType: "general", CWD: "/repo"},
		},
		{
			name:    "pre compact",
			event:   hookapi.EventPreCompact,
			payload: `{"sessionID":"ses_1","cwd":"/repo"}`,
			want:    hookapi.HookEvent{SessionID: "ses_1", CWD: "/repo"},
		},
		{
			name:    "tool ignored on lifecycle events",
			event:   hookapi.EventSessionStart,
			payload: `{"sessionID":"ses_1","tool":"grep","args":{"pattern":"x"},"cwd":"/repo"}`,
			want:    hookapi.HookEvent{SessionID: "ses_1", CWD: "/repo"},
		},
		{
			name:    "empty payload",
			event:   hookapi.EventSessionStart,
			payload: ``,
			want:    hookapi.HookEvent{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Adapter{}.ParseEvent(tt.event, []byte(tt.payload), nil)
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			want := tt.want
			want.Agent = hookapi.AgentOpenCode
			want.Event = tt.event
			if tt.payload != "" {
				want.Raw = json.RawMessage(tt.payload)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ParseEvent mismatch\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
}

func TestParseEventErrors(t *testing.T) {
	tests := []struct {
		name    string
		event   hookapi.Event
		payload string
	}{
		{"invalid json", hookapi.EventPreToolUse, `{"tool":`},
		{"not an object", hookapi.EventPreToolUse, `["grep"]`},
		{"tool event without tool", hookapi.EventPreToolUse, `{"sessionID":"s","args":{}}`},
		{"empty tool event", hookapi.EventPostToolUse, ``},
		{"unknown event", hookapi.Event("bogus"), `{"tool":"grep"}`},
		{"args not an object", hookapi.EventPreToolUse, `{"tool":"grep","args":"Foo"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := (Adapter{}).ParseEvent(tt.event, []byte(tt.payload), nil); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestPatchPaths(t *testing.T) {
	tests := []struct {
		patch string
		want  []string
	}{
		{"", nil},
		{"*** Begin Patch\n*** End Patch", nil},
		{"*** Begin Patch\r\n*** Update File: src/a b.go\r\n*** End Patch", []string{"src/a b.go"}},
		{"*** Add File:   spaced.go  \n", []string{"spaced.go"}},
		{"*** Update File:\n", nil},
		{"+*** Update File: inside-hunk.go\n", nil},
	}
	for _, tt := range tests {
		if got := patchPaths(tt.patch); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("patchPaths(%q) = %q, want %q", tt.patch, got, tt.want)
		}
	}
}

func TestRenderDecision(t *testing.T) {
	pre := hookapi.HookEvent{Agent: hookapi.AgentOpenCode, Event: hookapi.EventPreToolUse, Tool: hookapi.ToolGrep}
	tests := []struct {
		name string
		ev   hookapi.HookEvent
		d    hookapi.Decision
		want string // stdout
	}{
		{"noop", pre, hookapi.Noop(), ""},
		{"zero decision", pre, hookapi.Decision{}, ""},
		{"allow", pre, hookapi.Allow(), ""},
		{"deny", pre, hookapi.Deny("use find_symbol <Foo>"), `{"action":"deny","reason":"use find_symbol <Foo>"}` + "\n"},
		{"deny without reason", pre, hookapi.Deny(""), `{"action":"deny","reason":"graphmaster blocked this tool call"}` + "\n"},
		{"deny with context folds it into the reason", pre, hookapi.Deny("results:").WithContext("func Foo  a.go:1-3"),
			`{"action":"deny","reason":"results:\n\nfunc Foo  a.go:1-3"}` + "\n"},
		{"ask degrades to no opinion", pre, hookapi.Ask("sure?"), ""},
		{"allow with context only degrades to no opinion", pre, hookapi.Allow().WithContext("outline"), ""},
		{"user message is dropped", pre, hookapi.Allow().WithUserMessage("hi"), ""},
		{"updated input", pre, hookapi.Decision{Action: hookapi.ActionAllow, UpdatedInput: json.RawMessage(`{"pattern":"Bar"}`)},
			`{"action":"allow","args":{"pattern":"Bar"}}` + "\n"},
		{"updated input on noop", pre, hookapi.Decision{UpdatedInput: json.RawMessage(`{"pattern":"Bar"}`)},
			`{"action":"allow","args":{"pattern":"Bar"}}` + "\n"},
		{"updated input that is not an object is ignored", pre, hookapi.Decision{Action: hookapi.ActionAllow, UpdatedInput: json.RawMessage(`"x"`)}, ""},
		{"session start context cannot be injected", ev(hookapi.EventSessionStart), hookapi.Noop().WithContext("graphmaster ready"), ""},
		{"subagent start context cannot be injected", ev(hookapi.EventSubagentStart), hookapi.Noop().WithContext("ctx"), ""},
		{"deny on post-tool-use never blocks", ev(hookapi.EventPostToolUse), hookapi.Deny("no"), ""},
		{"deny on session start never blocks", ev(hookapi.EventSessionStart), hookapi.Deny("no"), ""},
		{"pre compact context", ev(hookapi.EventPreCompact), hookapi.Noop().WithContext("graphmaster: use find_symbol"),
			`{"context":"graphmaster: use find_symbol"}` + "\n"},
		{"pre compact deny is ignored but context kept", ev(hookapi.EventPreCompact), hookapi.Deny("no").WithContext("c"),
			`{"context":"c"}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := Adapter{}.RenderDecision(tt.ev, tt.d)
			if out.ExitCode != 0 {
				t.Errorf("exit code = %d, want 0", out.ExitCode)
			}
			if string(out.Stdout) != tt.want {
				t.Errorf("stdout = %q, want %q", out.Stdout, tt.want)
			}
			if len(out.Stderr) != 0 {
				t.Errorf("stderr = %q, want empty", out.Stderr)
			}
			if tt.d.Effective() != hookapi.ActionDeny || tt.ev.Event != hookapi.EventPreToolUse {
				if strings.Contains(string(out.Stdout), `"deny"`) {
					t.Errorf("non-blocking decision rendered as deny: %s", out.Stdout)
				}
			}
		})
	}
}

func ev(e hookapi.Event) hookapi.HookEvent {
	return hookapi.HookEvent{Agent: hookapi.AgentOpenCode, Event: e}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
