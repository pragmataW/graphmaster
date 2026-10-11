// Package cursor adapts graphmaster to the Cursor editor's agent.
//
// Cursor specifics this adapter encodes:
//
//   - Hooks live in ~/.cursor/hooks.json ({"version":1,"hooks":{...}}) and
//     use camelCase native event names (sessionStart, preToolUse,
//     beforeShellExecution, afterFileEdit, ...). Every hook invokes
//     `<bin> hook <canonical-event> --agent cursor`; the native event name
//     travels in the payload's hook_event_name.
//   - Gating hooks answer {"permission","user_message","agent_message"}.
//     Cursor cannot attach context to an allowed tool call, so
//     context-only decisions degrade to a plain allow/no-op. sessionStart
//     is fire-and-forget: it may add context but never blocks.
//   - MCP servers are registered in ~/.cursor/mcp.json.
//   - Cursor has no documented global instruction file (User Rules live in
//     its settings database). The instruction block is written to
//     ~/.cursor/rules/graphmaster.mdc only when ~/.cursor/rules already
//     exists; otherwise the MCP server's own instructions carry the
//     guidance and Notes tells the user how to pin it as a User Rule.
//   - Cursor also executes Claude Code hooks from ~/.claude/settings.json.
//     IsCursorPayload lets the dispatcher recognize those duplicate calls.
package cursor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

func init() { adapters.Register(Adapter{}) }

// Adapter implements adapters.Agent for Cursor.
type Adapter struct{}

var _ adapters.Agent = Adapter{}

// Name returns the --agent identifier.
func (Adapter) Name() hookapi.Agent { return hookapi.AgentCursor }

// Detect reports whether ~/.cursor exists or a cursor binary is on PATH.
func (Adapter) Detect(env install.Env) bool {
	if dir, err := cursorDir(env); err == nil && isDir(dir) {
		return true
	}
	return onPath(env, "cursor")
}

// Notes returns post-install reminders.
func (Adapter) Notes(env install.Env) []string {
	notes := []string{
		"Cursor also runs Claude Code hooks from ~/.claude/settings.json; graphmaster recognizes " +
			"Cursor's payload on those calls and answers once, so both installs can coexist.",
		"Restart Cursor (or reopen the window) so it reloads ~/.cursor/hooks.json, and make sure the " +
			"graphmaster server is enabled under Cursor Settings → MCP.",
	}
	if dir, err := cursorDir(env); err != nil || !isDir(filepath.Join(dir, "rules")) {
		notes = append(notes, "Cursor has no global rules file, so graphmaster's guidance reaches Cursor through "+
			"its MCP server instructions. To pin it, paste the graphmaster instruction block into "+
			"Cursor Settings → Rules → User Rules.")
	}
	return notes
}

func cursorDir(env install.Env) (string, error) {
	home, err := env.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor"), nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// onPath reports whether an executable named name is on env's PATH.
func onPath(env install.Env, name string) bool {
	lookup := env.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	path, _ := lookup("PATH")
	names := []string{name}
	if runtime.GOOS == "windows" {
		names = []string{name + ".exe", name + ".cmd"}
	}
	for _, dir := range filepath.SplitList(path) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, n := range names {
			fi, err := os.Stat(filepath.Join(dir, n))
			if err == nil && !fi.IsDir() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0) {
				return true
			}
		}
	}
	return false
}
