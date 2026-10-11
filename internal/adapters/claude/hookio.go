package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// payload is the subset of Claude Code's hook input graphmaster reads. The
// conversation_id / workspace_roots fields cover Cursor, which also runs the
// hooks in ~/.claude/settings.json and sends its own field names.
type payload struct {
	SessionID      string          `json:"session_id"`
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
	AgentID        string          `json:"agent_id"`
	AgentType      string          `json:"agent_type"`
	ConversationID string          `json:"conversation_id"`
	GenerationID   string          `json:"generation_id"`
	CursorVersion  string          `json:"cursor_version"`
	WorkspaceRoots []string        `json:"workspace_roots"`
}

// ParseEvent converts a Claude Code hook payload into a neutral event. event
// is the event graphmaster was invoked with; when empty, the payload's
// hook_event_name is used instead.
func (Adapter) ParseEvent(event hookapi.Event, data []byte, env []string) (hookapi.HookEvent, error) {
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return hookapi.HookEvent{}, fmt.Errorf("claude: decode hook payload: %w", err)
	}
	if event == "" {
		e, err := hookapi.ParseEvent(p.HookEventName)
		if err != nil {
			return hookapi.HookEvent{}, fmt.Errorf("claude: %w", err)
		}
		event = e
	}
	if !event.Valid() {
		return hookapi.HookEvent{}, fmt.Errorf("claude: invalid event %q", event)
	}

	ev := hookapi.HookEvent{
		Agent:     hookapi.AgentClaude,
		Event:     event,
		SessionID: firstNonEmpty(p.SessionID, p.ConversationID),
		AgentID:   p.AgentID,
		AgentType: p.AgentType,
		CWD:       p.CWD,
		ToolUseID: p.ToolUseID,
		Raw:       json.RawMessage(bytes.Clone(data)),
	}
	if ev.CWD == "" {
		ev.CWD = lookupEnv(env, "CLAUDE_PROJECT_DIR")
	}
	if ev.CWD == "" && len(p.WorkspaceRoots) > 0 {
		ev.CWD = p.WorkspaceRoots[0]
	}

	if event.IsToolEvent() {
		if p.ToolName == "" {
			return hookapi.HookEvent{}, errors.New("claude: tool event without tool_name")
		}
		ev.Tool = normalizeTool(p.ToolName)
		fillToolInput(&ev, decodeInput(p.ToolInput))
	}
	return ev, ev.Validate()
}

// normalizeTool maps a Claude Code tool name to a neutral category.
func normalizeTool(name string) hookapi.Tool {
	switch name {
	case "Grep":
		return hookapi.ToolGrep
	case "Glob":
		return hookapi.ToolGlob
	case "Read":
		return hookapi.ToolRead
	case "Bash", "Shell": // "Shell" when Cursor runs Claude hooks
		return hookapi.ToolShell
	case "Edit", "MultiEdit", "NotebookEdit":
		return hookapi.ToolEdit
	case "Write":
		return hookapi.ToolWrite
	}
	if strings.HasPrefix(name, "mcp__") {
		return hookapi.ToolMCP
	}
	return hookapi.ToolOther
}

// toolInput holds the string-valued members of tool_input. Non-string
// members are skipped so an unexpected type never fails the whole parse.
type toolInput map[string]string

func decodeInput(raw json.RawMessage) toolInput {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	in := toolInput{}
	for k, v := range m {
		var s string
		if json.Unmarshal(v, &s) == nil {
			in[k] = s
		}
	}
	return in
}

// fillToolInput copies the search/target fields relevant to ev.Tool.
func fillToolInput(ev *hookapi.HookEvent, in toolInput) {
	paths := func(keys ...string) []string {
		for _, k := range keys {
			if v := in[k]; v != "" {
				return []string{v}
			}
		}
		return nil
	}
	switch ev.Tool {
	case hookapi.ToolGrep:
		ev.Pattern = in["pattern"]
		ev.Glob = in["glob"]
		ev.Paths = paths("path")
	case hookapi.ToolGlob:
		ev.Glob = in["pattern"]
		ev.Paths = paths("path")
	case hookapi.ToolRead:
		ev.Paths = paths("file_path", "path")
	case hookapi.ToolShell:
		ev.Command = in["command"]
	case hookapi.ToolEdit, hookapi.ToolWrite:
		ev.Paths = paths("file_path", "notebook_path")
	}
}

// FromCursor reports whether a payload delivered to a Claude hook was sent
// by Cursor, which executes ~/.claude/settings.json hooks by default. The
// hook dispatcher uses this to stay silent when Cursor-native hooks are also
// installed. Detection is payload-based only: environment variables such as
// TERM_PROGRAM would misfire for Claude Code running in Cursor's terminal.
func FromCursor(data []byte) bool {
	var p payload
	if json.Unmarshal(data, &p) != nil {
		return false
	}
	return p.CursorVersion != "" || (p.ConversationID != "" && p.GenerationID != "")
}

// hookOutput is Claude Code's hook stdout JSON.
type hookOutput struct {
	// SystemMessage is shown to the user, not the model.
	SystemMessage string          `json:"systemMessage,omitempty"`
	Specific      *specificOutput `json:"hookSpecificOutput,omitempty"`
}

type specificOutput struct {
	HookEventName            string          `json:"hookEventName"`
	PermissionDecision       string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string          `json:"additionalContext,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
}

// eventNames maps neutral events to Claude's hookEventName spelling.
var eventNames = map[hookapi.Event]string{
	hookapi.EventSessionStart:  "SessionStart",
	hookapi.EventSubagentStart: "SubagentStart",
	hookapi.EventPreToolUse:    "PreToolUse",
	hookapi.EventPostToolUse:   "PostToolUse",
	hookapi.EventPreCompact:    "PreCompact",
	hookapi.EventStop:          "Stop",
}

// RenderDecision renders d as Claude Code hook output. The exit code is
// always 0 with JSON on stdout, so a malformed decision can never block.
//
//   - PreToolUse: deny/ask become permissionDecision. Allow and noop never
//     set permissionDecision: graphmaster does not grant permissions the
//     user's own rules would otherwise ask for. Their reason and context are
//     still delivered as additionalContext.
//   - PostToolUse, SessionStart, SubagentStart: the tool already ran or the
//     event cannot be blocked, so reason and context become additionalContext.
//   - PreCompact, Stop: Claude accepts no model context there and blocking
//     would stall the session, so only the user message is kept.
func (Adapter) RenderDecision(ev hookapi.HookEvent, d hookapi.Decision) adapters.HookOutput {
	out := hookOutput{SystemMessage: d.UserMessage}
	spec := &specificOutput{HookEventName: eventNames[ev.Event]}
	action := d.Effective()

	switch ev.Event {
	case hookapi.EventPreToolUse:
		if action == hookapi.ActionDeny || action == hookapi.ActionAsk {
			spec.PermissionDecision = string(action)
			spec.PermissionDecisionReason = d.Reason
			spec.AdditionalContext = d.AdditionalContext
		} else {
			spec.AdditionalContext = joinContext(d.Reason, d.AdditionalContext)
		}
		if action != hookapi.ActionDeny && json.Valid(d.UpdatedInput) {
			spec.UpdatedInput = d.UpdatedInput
		}
	case hookapi.EventPostToolUse, hookapi.EventSessionStart, hookapi.EventSubagentStart:
		spec.AdditionalContext = joinContext(d.Reason, d.AdditionalContext)
	}
	if spec.PermissionDecision != "" || spec.AdditionalContext != "" || len(spec.UpdatedInput) > 0 {
		out.Specific = spec
	}
	if out.SystemMessage == "" && out.Specific == nil {
		return adapters.HookOutput{}
	}
	b, err := marshal(out)
	if err != nil {
		return adapters.HookOutput{} // fail open
	}
	return adapters.HookOutput{Stdout: b}
}

// marshal encodes v without HTML escaping, newline-terminated.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func joinContext(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "\n\n")
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// lookupEnv finds key in an os.Environ-style slice (last entry wins).
func lookupEnv(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}
