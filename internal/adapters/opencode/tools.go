package opencode

import (
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// preTools are forwarded on tool.execute.before: the search, read and
// shell tools graphmaster may steer. Every other tool runs without a spawn.
var preTools = []string{"grep", "glob", "list", "read", "bash"}

// postTools are forwarded on tool.execute.after. File edits are reported
// through the file.edited bus event instead, which carries the absolute
// path and covers edit, write and apply_patch alike.
var postTools = []string{"bash"}

// builtinTools maps OpenCode's built-in tool ids (v1.1, plus older ids that
// may still appear) to graphmaster's categories.
var builtinTools = map[string]hookapi.Tool{
	"grep":        hookapi.ToolGrep,
	"glob":        hookapi.ToolGlob,
	"list":        hookapi.ToolGlob, // recursive directory listing, like `ls -R`
	"read":        hookapi.ToolRead,
	"bash":        hookapi.ToolShell,
	"edit":        hookapi.ToolEdit,
	"multiedit":   hookapi.ToolEdit,
	"write":       hookapi.ToolWrite,
	"apply_patch": hookapi.ToolPatch,
	"patch":       hookapi.ToolPatch,
	"task":        hookapi.ToolOther,
	"batch":       hookapi.ToolOther,
	"lsp":         hookapi.ToolOther,
	"skill":       hookapi.ToolOther,
	"question":    hookapi.ToolOther,
	"plan_enter":  hookapi.ToolOther,
	"plan_exit":   hookapi.ToolOther,
	"todoread":    hookapi.ToolOther,
	"todowrite":   hookapi.ToolOther,
	"webfetch":    hookapi.ToolOther,
	"websearch":   hookapi.ToolOther,
	"codesearch":  hookapi.ToolOther,
	"invalid":     hookapi.ToolOther,
}

// normalizeTool categorizes an OpenCode tool id. OpenCode names MCP tools
// "<server>_<tool>" with both parts sanitized to [A-Za-z0-9_-], so an
// unknown id containing '_' is treated as MCP.
func normalizeTool(name string) hookapi.Tool {
	if t, ok := builtinTools[name]; ok {
		return t
	}
	if strings.Contains(name, "_") {
		return hookapi.ToolMCP
	}
	return hookapi.ToolOther
}

// toolArgs holds the argument fields graphmaster reads from OpenCode tools.
type toolArgs struct {
	FilePath  string `json:"filePath"`  // read, edit, write, multiedit
	Path      string `json:"path"`      // grep, glob, list
	Pattern   string `json:"pattern"`   // grep (regex), glob (glob)
	Include   string `json:"include"`   // grep file filter
	Command   string `json:"command"`   // bash
	Workdir   string `json:"workdir"`   // bash
	PatchText string `json:"patchText"` // apply_patch, patch
}

// fillTool sets the tool-specific fields of ev from args.
func fillTool(ev *hookapi.HookEvent, a toolArgs) {
	switch ev.Tool {
	case hookapi.ToolGrep:
		ev.Pattern = a.Pattern
		ev.Paths = nonEmpty(a.Path)
		ev.Glob = a.Include
	case hookapi.ToolGlob:
		ev.Paths = nonEmpty(a.Path)
		ev.Glob = a.Pattern
		if ev.Glob == "" {
			ev.Glob = "**" // list: the whole tree below Path
		}
	case hookapi.ToolRead, hookapi.ToolEdit, hookapi.ToolWrite:
		ev.Paths = nonEmpty(a.FilePath)
	case hookapi.ToolShell:
		ev.Command = a.Command
		if a.Workdir != "" {
			ev.CWD = resolveDir(ev.CWD, a.Workdir)
		}
	case hookapi.ToolPatch:
		ev.Paths = patchPaths(a.PatchText)
	}
}

func nonEmpty(p string) []string {
	if p == "" {
		return nil
	}
	return []string{p}
}

func resolveDir(cwd, dir string) string {
	if filepath.IsAbs(dir) || cwd == "" {
		return dir
	}
	return filepath.Join(cwd, dir)
}

// patchHeaders introduce a file path in OpenCode's apply_patch format (the
// same envelope Codex uses).
var patchHeaders = []string{"*** Update File:", "*** Add File:", "*** Delete File:", "*** Move to:"}

// patchPaths returns the distinct file paths named by a patch, in order.
func patchPaths(patch string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, h := range patchHeaders {
			if rest, ok := strings.CutPrefix(line, h); ok {
				if p := strings.TrimSpace(rest); p != "" && !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
				break
			}
		}
	}
	return out
}
