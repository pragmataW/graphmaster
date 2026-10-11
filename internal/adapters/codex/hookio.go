package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// payload is the common Codex hook stdin object. Codex uses Claude Code's
// field names; agent_id/agent_type only appear on subagent events.
type payload struct {
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id"`
	AgentID       string          `json:"agent_id"`
	AgentType     string          `json:"agent_type"`
}

// toolInput is the union of tool_input fields graphmaster reads.
type toolInput struct {
	// Command is a string (Bash, apply_patch) or an argv array (shell).
	Command  json.RawMessage `json:"command"`
	Cmd      json.RawMessage `json:"cmd"`
	Workdir  string          `json:"workdir"`
	Input    string          `json:"input"`
	Patch    string          `json:"patch"`
	Pattern  string          `json:"pattern"`
	Path     string          `json:"path"`
	FilePath string          `json:"file_path"`
	Glob     string          `json:"glob"`
	Include  string          `json:"include"`
}

// toolCategories maps lower-cased Codex tool names to graphmaster tools.
// Names starting with "mcp__" are MCP tools; anything else is ToolOther.
var toolCategories = map[string]hookapi.Tool{
	"bash":           hookapi.ToolShell,
	"shell":          hookapi.ToolShell,
	"local_shell":    hookapi.ToolShell,
	"exec_command":   hookapi.ToolShell,
	"unified_exec":   hookapi.ToolShell,
	"container.exec": hookapi.ToolShell,
	"apply_patch":    hookapi.ToolPatch,
	"grep":           hookapi.ToolGrep,
	"grep_files":     hookapi.ToolGrep,
	"glob":           hookapi.ToolGlob,
	"read":           hookapi.ToolRead,
	"read_file":      hookapi.ToolRead,
	"edit":           hookapi.ToolEdit,
	"multiedit":      hookapi.ToolEdit,
	"write":          hookapi.ToolWrite,
}

func normalizeTool(name string) hookapi.Tool {
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(n, "mcp__") {
		return hookapi.ToolMCP
	}
	if t, ok := toolCategories[n]; ok {
		return t
	}
	return hookapi.ToolOther
}

// ParseEvent implements adapters.Agent.
func (Adapter) ParseEvent(event hookapi.Event, raw []byte, env []string) (hookapi.HookEvent, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return hookapi.HookEvent{}, fmt.Errorf("codex: decode hook payload: %w", err)
	}
	ev, err := resolveEvent(event, p.HookEventName)
	if err != nil {
		return hookapi.HookEvent{}, err
	}
	out := hookapi.HookEvent{
		Agent:     hookapi.AgentCodex,
		Event:     ev,
		SessionID: p.SessionID,
		AgentID:   p.AgentID,
		AgentType: p.AgentType,
		CWD:       p.CWD,
		ToolUseID: p.ToolUseID,
		Raw:       json.RawMessage(raw),
	}
	if out.CWD == "" {
		out.CWD = envValue(env, "PWD")
	}
	if ev.IsToolEvent() || p.ToolName != "" {
		out.Tool = normalizeTool(p.ToolName)
		fillToolFields(&out, p.ToolInput)
	}
	return out, nil
}

// resolveEvent prefers the event graphmaster was invoked with and falls back
// to the payload's hook_event_name.
func resolveEvent(arg hookapi.Event, name string) (hookapi.Event, error) {
	if arg.Valid() {
		return arg, nil
	}
	if name == "" {
		if arg == "" {
			return "", errors.New("codex: hook event not given and payload has no hook_event_name")
		}
		return "", fmt.Errorf("codex: unknown hook event %q", arg)
	}
	ev, err := hookapi.ParseEvent(name)
	if err != nil {
		return "", fmt.Errorf("codex: %w", err)
	}
	return ev, nil
}

// fillToolFields copies the tool-specific inputs into ev.
func fillToolFields(ev *hookapi.HookEvent, raw json.RawMessage) {
	var in toolInput
	var rawText string
	if len(raw) > 0 && raw[0] == '"' {
		// apply_patch may deliver the envelope as a bare string.
		_ = json.Unmarshal(raw, &rawText)
	} else if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in) // tolerate odd shapes; fields stay empty
	}

	switch ev.Tool {
	case hookapi.ToolShell:
		ev.Command = commandText(in.Command)
		if ev.Command == "" {
			ev.Command = commandText(in.Cmd)
		}
		if ev.Command == "" {
			ev.Command = rawText
		}
		if in.Workdir != "" {
			ev.CWD = joinDir(ev.CWD, in.Workdir)
		}
	case hookapi.ToolPatch:
		text := firstNonEmpty(commandText(in.Command), in.Input, in.Patch, rawText)
		ev.Paths = patchPaths(text)
	case hookapi.ToolGrep:
		ev.Pattern = in.Pattern
		ev.Paths = nonEmpty(in.Path)
		ev.Glob = firstNonEmpty(in.Glob, in.Include)
	case hookapi.ToolGlob:
		ev.Glob = firstNonEmpty(in.Pattern, in.Glob)
		ev.Paths = nonEmpty(in.Path)
	case hookapi.ToolRead, hookapi.ToolEdit, hookapi.ToolWrite:
		ev.Paths = nonEmpty(firstNonEmpty(in.FilePath, in.Path))
	}
}

// commandText decodes a command given as a string or as an argv array.
// "bash -lc SCRIPT"-style argv yields SCRIPT; other argv are shell-quoted
// and joined so the steering parser sees an equivalent command line.
func commandText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return ""
	}
	if len(argv) == 3 && isShell(argv[0]) && isScriptFlag(argv[1]) {
		return argv[2]
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func isShell(arg string) bool {
	switch filepath.Base(arg) {
	case "bash", "sh", "zsh", "dash", "ksh", "fish":
		return true
	}
	return false
}

func isScriptFlag(f string) bool {
	switch f {
	case "-c", "-lc", "-ic", "-cl", "-ci", "-lic", "-ilc":
		return true
	}
	return false
}

func joinDir(cwd, dir string) string {
	if filepath.IsAbs(dir) || cwd == "" {
		return dir
	}
	return filepath.Join(cwd, dir)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// envValue returns the last KEY=value entry for key in an os.Environ list.
func envValue(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}

// hookOutput is Codex's hook stdout JSON (Claude Code compatible).
type hookOutput struct {
	Decision           string        `json:"decision,omitempty"`
	Reason             string        `json:"reason,omitempty"`
	SystemMessage      string        `json:"systemMessage,omitempty"`
	HookSpecificOutput *hookSpecific `json:"hookSpecificOutput,omitempty"`
}

type hookSpecific struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string `json:"additionalContext,omitempty"`
}

// eventNames are the Codex spellings used in hookEventName.
var eventNames = map[hookapi.Event]string{
	hookapi.EventSessionStart:  "SessionStart",
	hookapi.EventSubagentStart: "SubagentStart",
	hookapi.EventPreToolUse:    "PreToolUse",
	hookapi.EventPostToolUse:   "PostToolUse",
}

const defaultDenyReason = "blocked by graphmaster"

// RenderDecision implements adapters.Agent. Every rendering exits 0: a deny
// is expressed in JSON, and noop/allow print nothing unless there is context
// to add. Codex does not support allow/ask overrides or input rewriting, so
// allow and ask render as "no opinion" (ask never escalates to deny).
func (Adapter) RenderDecision(ev hookapi.HookEvent, d hookapi.Decision) adapters.HookOutput {
	name, ok := eventNames[ev.Event]
	if !ok {
		// PreCompact/Stop: nothing graphmaster says there is actionable.
		return adapters.HookOutput{}
	}
	out := hookOutput{SystemMessage: d.UserMessage}
	spec := &hookSpecific{HookEventName: name, AdditionalContext: d.AdditionalContext}

	switch ev.Event {
	case hookapi.EventPreToolUse:
		if d.Blocks() {
			spec.PermissionDecision = "deny"
			spec.PermissionDecisionReason = firstNonEmpty(d.Reason, defaultDenyReason)
		}
	case hookapi.EventPostToolUse:
		// The tool already ran; "block" feeds the reason back to the model.
		if d.Blocks() {
			out.Decision = "block"
			out.Reason = firstNonEmpty(d.Reason, defaultDenyReason)
		}
	default:
		// Session/subagent start cannot block; keep a deny reason as context.
		if d.Blocks() && d.Reason != "" {
			spec.AdditionalContext = joinNonEmpty(spec.AdditionalContext, d.Reason)
		}
	}
	if spec.PermissionDecision != "" || spec.AdditionalContext != "" {
		out.HookSpecificOutput = spec
	}
	if out == (hookOutput{}) {
		return adapters.HookOutput{}
	}
	return adapters.HookOutput{Stdout: encodeJSON(out)}
}

func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n\n" + b
}

// encodeJSON marshals v on one line without HTML escaping.
func encodeJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Only plain strings are encoded; this cannot happen.
		return nil
	}
	return buf.Bytes()
}
