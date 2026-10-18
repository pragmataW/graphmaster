package cli

import (
	"bytes"
	"strings"
	"testing"
)

// runHook executes `graphmaster hook args...` with stdin, in a temporary
// working directory outside any repository.
func runHook(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	prev := hookWorkDir
	hookWorkDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { hookWorkDir = prev })

	root := newRootCmd()
	var out, errw bytes.Buffer
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errw)
	root.SetArgs(append([]string{"hook"}, args...))
	err := root.Execute()
	if errw.Len() != 0 {
		t.Fatalf("hook wrote to stderr: %q", errw.String())
	}
	return out.String(), err
}

func TestHookStopIsSilent(t *testing.T) {
	out, err := runHook(t, `{"session_id":"s","hook_event_name":"Stop"}`, "stop", "--agent", "claude")
	if err != nil || out != "" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestHookRendersAgentFormat(t *testing.T) {
	out, err := runHook(t, `{"conversation_id":"c","hook_event_name":"stop"}`, "stop", "--agent", "cursor")
	if err != nil || strings.TrimSpace(out) != "{}" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestHookNeverFails(t *testing.T) {
	cases := [][]string{
		{"pre-tool-use", "--agent", "claude"}, // garbage payload
		{"pre-tool-use", "--agent", "vim"},    // unknown agent
		{"bogus-event", "--agent", "codex"},   // unknown event
		{},                                    // no event, no agent
		{"stop", "--agent", "claude", "--future-flag=1"}, // flags from newer installs
		{"a", "b", "c", "--agent", "opencode"},           // extra args
	}
	for _, args := range cases {
		out, err := runHook(t, "not json", args...)
		if err != nil {
			t.Errorf("hook %v: %v", args, err)
		}
		if strings.Contains(out, "deny") {
			t.Errorf("hook %v must fail open, got %q", args, out)
		}
	}
}
