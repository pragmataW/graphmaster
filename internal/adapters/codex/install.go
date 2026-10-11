package codex

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

// maxInstructionBytes keeps the AGENTS.md block well inside Codex's 32 KiB
// AGENTS.md budget, which is shared with the user's own rules.
const maxInstructionBytes = 8 * 1024

// hookSpec is one graphmaster hook registration in hooks.json.
type hookSpec struct {
	// name is the Codex event key in hooks.json.
	name  string
	event hookapi.Event
	// matcher is the tool-name regex; "" matches every invocation.
	matcher string
	// timeout is in seconds.
	timeout int
}

// codexHooks lists every event graphmaster uses on Codex. Codex has no
// PreCompact event, and SessionStart has no meaningful matcher. PreToolUse
// only needs the shell: Codex searches and reads files through it. The
// post-tool hook covers apply_patch edits (reindex the patched paths) and
// shell commands (full freshen).
var codexHooks = []hookSpec{
	// SessionStart may build the first index (budget 590s).
	{name: "SessionStart", event: hookapi.EventSessionStart, timeout: 600},
	{name: "SubagentStart", event: hookapi.EventSubagentStart, timeout: 30},
	{name: "PreToolUse", event: hookapi.EventPreToolUse, matcher: "Bash", timeout: 10},
	{name: "PostToolUse", event: hookapi.EventPostToolUse, matcher: "Bash|apply_patch", timeout: 15},
}

// hookGroup and hookCommandEntry mirror hooks.json; field order is the
// order written to disk.
type hookGroup struct {
	Matcher string             `json:"matcher,omitempty"`
	Hooks   []hookCommandEntry `json:"hooks"`
}

type hookCommandEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// InstallPlan implements adapters.Agent.
func (Adapter) InstallPlan(env install.Env, opts adapters.InstallOptions) (install.Plan, error) {
	if err := checkBinary(opts.Binary); err != nil {
		return install.Plan{}, err
	}
	if n := len(opts.Instructions); n > maxInstructionBytes {
		return install.Plan{}, fmt.Errorf("codex: instructions are %d bytes, limit is %d (AGENTS.md is capped at 32 KiB)", n, maxInstructionBytes)
	}
	dir, err := codexHome(env)
	if err != nil {
		return install.Plan{}, fmt.Errorf("codex: %w", err)
	}

	files := []install.FileChange{
		{
			Path:   filepath.Join(dir, "config.toml"),
			Format: install.FormatTOML,
			Ops:    []install.Op{install.Block{Body: mcpBlock(opts.Binary)}},
		},
		{
			Path:   filepath.Join(dir, "hooks.json"),
			Format: install.FormatJSON,
			Ops:    hookOps(opts.Binary),
		},
	}
	if strings.TrimSpace(opts.Instructions) != "" {
		files = append(files, install.FileChange{
			Path:   filepath.Join(dir, "AGENTS.md"),
			Format: install.FormatMarkdown,
			Ops:    []install.Op{install.Block{Body: opts.Instructions}},
		})
	}
	return install.Plan{Agent: string(hookapi.AgentCodex), Files: files}, nil
}

func checkBinary(bin string) error {
	switch {
	case bin == "":
		return errors.New("codex: graphmaster binary path is empty")
	case !filepath.IsAbs(bin):
		return fmt.Errorf("codex: graphmaster binary path %q is not absolute", bin)
	case strings.ContainsAny(bin, "\x00\r\n"):
		return fmt.Errorf("codex: graphmaster binary path %q contains control characters", bin)
	}
	return nil
}

// hookOps appends one graphmaster group per event to hooks.json.
func hookOps(bin string) []install.Op {
	ops := make([]install.Op, 0, len(codexHooks))
	for _, h := range codexHooks {
		ops = append(ops, install.JSONAppend{
			Pointer: install.Pointer("hooks", h.name),
			Value: hookGroup{
				Matcher: h.matcher,
				Hooks: []hookCommandEntry{{
					Type:    "command",
					Command: hookCommand(bin, h.event),
					Timeout: h.timeout,
				}},
			},
		})
	}
	return ops
}

// hookCommand renders the shell command Codex runs for event.
func hookCommand(bin string, event hookapi.Event) string {
	return shellQuote(bin) + " hook " + string(event) + " --agent " + string(hookapi.AgentCodex)
}

// shellQuote single-quotes s for POSIX shells unless it is made only of
// characters that never need quoting.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, needsQuote) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func needsQuote(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("_-./:+=@%,", r)
}

// mcpBlock is the config.toml marker block registering the MCP server.
func mcpBlock(bin string) string {
	return "[mcp_servers.graphmaster]\n" +
		"command = " + tomlString(bin) + "\n" +
		`args = ["mcp"]` + "\n"
}

// tomlString renders s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
