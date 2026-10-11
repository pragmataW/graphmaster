package cursor

import (
	"strings"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// toolNames maps squashed Cursor tool names (lowercase, no '_' or '-') to
// tool categories. Both the hook-facing names (Shell, Read, Grep, ...) and
// the agent's internal tool ids (run_terminal_cmd, read_file, ...) appear in
// payloads depending on the Cursor version.
var toolNames = map[string]hookapi.Tool{
	"shell": hookapi.ToolShell, "terminal": hookapi.ToolShell, "runterminalcmd": hookapi.ToolShell,
	"runterminalcommand": hookapi.ToolShell, "bash": hookapi.ToolShell,

	"read": hookapi.ToolRead, "readfile": hookapi.ToolRead,

	"grep": hookapi.ToolGrep, "grepsearch": hookapi.ToolGrep, "ripgrep": hookapi.ToolGrep,

	"glob": hookapi.ToolGlob, "globfilesearch": hookapi.ToolGlob, "filesearch": hookapi.ToolGlob,

	"edit": hookapi.ToolEdit, "multiedit": hookapi.ToolEdit, "strreplace": hookapi.ToolEdit,
	"searchreplace": hookapi.ToolEdit, "editfile": hookapi.ToolEdit, "applypatch": hookapi.ToolEdit,

	// Deleting a file is a mutation too; "write" makes the core mark it dirty.
	"write": hookapi.ToolWrite, "delete": hookapi.ToolWrite, "deletefile": hookapi.ToolWrite,

	"mcp": hookapi.ToolMCP,
}

// normalizeTool maps a Cursor tool_name to a neutral tool category.
// MCP tools arrive as "MCP:<tool>" (hooks) or "mcp_<server>_<tool>".
func normalizeTool(name string) hookapi.Tool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(lower, "mcp:") || strings.HasPrefix(lower, "mcp_") {
		return hookapi.ToolMCP
	}
	squashed := strings.NewReplacer("_", "", "-", "", " ", "").Replace(lower)
	if t, ok := toolNames[squashed]; ok {
		return t
	}
	return hookapi.ToolOther
}
