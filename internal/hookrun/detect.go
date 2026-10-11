package hookrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters/cursor"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

// resolveAgent works out which agent really fired a hook. Cursor also runs
// the Claude Code hooks in ~/.claude/settings.json, so a Cursor-shaped
// payload (cursor_version, or conversation_id + generation_id) delivered to
// a Claude hook is treated as Cursor. When graphmaster's Cursor-native hooks
// are installed too, that Claude-side call is a duplicate and silent is
// true: the dispatcher exits 0 without output and lets the native hook
// answer. Detection is payload-based only; environment variables would
// misfire for Claude Code running in Cursor's terminal.
func resolveAgent(invoked hookapi.Agent, payload []byte, home string) (agent hookapi.Agent, silent bool) {
	if invoked != hookapi.AgentClaude || !cursor.IsCursorPayload(payload) {
		return invoked, false
	}
	return hookapi.AgentCursor, cursorNativeInstalled(home)
}

// cursorNativeInstalled reports whether ~/.cursor/hooks.json (JSONC
// tolerated) holds a graphmaster hook command.
func cursorNativeInstalled(home string) bool {
	if home == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(home, ".cursor", "hooks.json"))
	if err != nil {
		return false
	}
	std, err := install.StandardizeJSON(b)
	if err != nil {
		return false
	}
	var file struct {
		Hooks map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(std, &file) != nil {
		return false
	}
	for _, entries := range file.Hooks {
		for _, e := range entries {
			if isGraphmasterHook(e.Command) {
				return true
			}
		}
	}
	return false
}

// isGraphmasterHook matches the command graphmaster's Cursor adapter
// installs: `<bin> hook <event> --agent cursor`.
func isGraphmasterHook(cmd string) bool {
	if !strings.Contains(cmd, " hook ") {
		return false
	}
	return strings.Contains(cmd, "graphmaster") || strings.Contains(cmd, "--agent cursor")
}
