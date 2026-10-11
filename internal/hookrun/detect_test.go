package hookrun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

const cursorClaudePayload = `{"conversation_id":"c1","generation_id":"g1","hook_event_name":"PreToolUse",` +
	`"tool_name":"Grep","tool_input":{"pattern":"Foo"},"workspace_roots":["/r"]}`

func writeCursorHooks(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const nativeHooks = `{
  // installed by graphmaster
  "version": 1,
  "hooks": {
    "preToolUse": [{"command": "'/opt/bin/graphmaster' hook pre-tool-use --agent cursor", "matcher": "Shell|Read|Grep"}],
  }
}`

func TestCursorNativeInstalled(t *testing.T) {
	home := t.TempDir()
	if cursorNativeInstalled(home) {
		t.Fatal("no hooks.json: not installed")
	}
	writeCursorHooks(t, home, `{"version":1,"hooks":{"stop":[{"command":"other-tool notify"}]}}`)
	if cursorNativeInstalled(home) {
		t.Fatal("foreign hooks only: not installed")
	}
	writeCursorHooks(t, home, nativeHooks)
	if !cursorNativeInstalled(home) {
		t.Fatal("JSONC hooks.json with a graphmaster hook: installed")
	}
	// A renamed binary is still recognized by its hook arguments.
	writeCursorHooks(t, home, `{"hooks":{"afterFileEdit":[{"command":"/usr/local/bin/gm hook post-tool-use --agent cursor"}]}}`)
	if !cursorNativeInstalled(home) {
		t.Fatal("hook ... --agent cursor command: installed")
	}
	writeCursorHooks(t, home, `{not json`)
	if cursorNativeInstalled(home) {
		t.Fatal("corrupt hooks.json: treated as not installed")
	}
	if cursorNativeInstalled("") {
		t.Fatal("no home: not installed")
	}
}

func TestResolveAgent(t *testing.T) {
	home := t.TempDir()
	claudePayload := []byte(`{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Grep"}`)

	cases := []struct {
		name    string
		invoked hookapi.Agent
		payload string
		native  bool
		want    hookapi.Agent
		silent  bool
	}{
		{"claude stays claude", hookapi.AgentClaude, string(claudePayload), false, hookapi.AgentClaude, false},
		{"cursor payload under claude becomes cursor", hookapi.AgentClaude, cursorClaudePayload, false, hookapi.AgentCursor, false},
		{"cursor_version alone is cursor", hookapi.AgentClaude, `{"cursor_version":"1.7.2","session_id":"s"}`, false, hookapi.AgentCursor, false},
		{"duplicate of a native hook is silent", hookapi.AgentClaude, cursorClaudePayload, true, hookapi.AgentCursor, true},
		{"native cursor hook is never silent", hookapi.AgentCursor, cursorClaudePayload, true, hookapi.AgentCursor, false},
		{"codex is untouched", hookapi.AgentCodex, cursorClaudePayload, true, hookapi.AgentCodex, false},
		{"garbage payload keeps the agent", hookapi.AgentClaude, `not json`, true, hookapi.AgentClaude, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := filepath.Join(home, c.name)
			if c.native {
				writeCursorHooks(t, h, nativeHooks)
			}
			got, silent := resolveAgent(c.invoked, []byte(c.payload), h)
			if got != c.want || silent != c.silent {
				t.Fatalf("resolveAgent = %s, %v; want %s, %v", got, silent, c.want, c.silent)
			}
		})
	}
}
