package cursor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// nativeEvents maps Cursor hook names to graphmaster events.
var nativeEvents = map[string]hookapi.Event{
	"sessionStart":         hookapi.EventSessionStart,
	"subagentStart":        hookapi.EventSubagentStart,
	"preToolUse":           hookapi.EventPreToolUse,
	"beforeShellExecution": hookapi.EventPreToolUse,
	"beforeReadFile":       hookapi.EventPreToolUse,
	"beforeMCPExecution":   hookapi.EventPreToolUse,
	"postToolUse":          hookapi.EventPostToolUse,
	"afterFileEdit":        hookapi.EventPostToolUse,
	"afterShellExecution":  hookapi.EventPostToolUse,
	"afterMCPExecution":    hookapi.EventPostToolUse,
	"preCompact":           hookapi.EventPreCompact,
	"stop":                 hookapi.EventStop,
}

// fields is a leniently decoded JSON object: values of an unexpected type
// read as empty instead of failing the whole payload.
type fields map[string]json.RawMessage

// str returns the first non-empty string value among keys.
func (f fields) str(keys ...string) string {
	for _, k := range keys {
		var s string
		if json.Unmarshal(f[k], &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

// strs returns the first non-empty string list among keys; a single string
// value counts as a one-element list.
func (f fields) strs(keys ...string) []string {
	for _, k := range keys {
		var list []string
		if json.Unmarshal(f[k], &list) == nil && len(list) > 0 {
			return list
		}
		if s := f.str(k); s != "" {
			return []string{s}
		}
	}
	return nil
}

func (f fields) has(k string) bool { _, ok := f[k]; return ok }

func decodeFields(b []byte) (fields, error) {
	var f fields
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("payload is not a JSON object")
	}
	return f, nil
}

// ParseEvent converts a Cursor hook payload into a neutral event. The
// payload's hook_event_name decides the shape; event (the CLI argument) is
// the fallback when the name is missing or unknown.
func (Adapter) ParseEvent(event hookapi.Event, payload []byte, env []string) (hookapi.HookEvent, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return hookapi.HookEvent{}, errors.New("cursor: empty hook payload")
	}
	p, err := decodeFields(payload)
	if err != nil {
		return hookapi.HookEvent{}, fmt.Errorf("cursor: invalid hook payload: %w", err)
	}
	native := p.str("hook_event_name")
	ev := hookapi.HookEvent{
		Agent:     hookapi.AgentCursor,
		SessionID: p.str("session_id", "conversation_id"),
		AgentID:   p.str("subagent_id", "agent_id"),
		AgentType: p.str("subagent_type", "agent_type"),
		ToolUseID: p.str("tool_use_id"),
		Raw:       append(json.RawMessage(nil), payload...),
	}
	ev.Event, err = resolveEvent(native, event)
	if err != nil {
		return hookapi.HookEvent{}, err
	}
	var input fields
	if ev.Event.IsToolEvent() {
		input = fillTool(&ev, native, p)
	}
	ev.CWD = firstNonEmpty(input.str("cwd", "working_directory"), p.str("cwd"), first(p.strs("workspace_roots")),
		envValue(env, "CURSOR_PROJECT_DIR"))
	if err := ev.Validate(); err != nil {
		return hookapi.HookEvent{}, fmt.Errorf("cursor: %w", err)
	}
	return ev, nil
}

func resolveEvent(native string, cli hookapi.Event) (hookapi.Event, error) {
	if e, ok := nativeEvents[native]; ok {
		return e, nil
	}
	if e, err := hookapi.ParseEvent(native); err == nil {
		return e, nil // Claude-style spelling, e.g. "PreToolUse"
	}
	if cli.Valid() {
		return cli, nil
	}
	return "", fmt.Errorf("cursor: cannot map hook event %q (cli event %q)", native, cli)
}

// fillTool sets the tool fields of a pre/post tool event and returns the
// decoded tool_input (nil when the payload has none).
func fillTool(ev *hookapi.HookEvent, native string, p fields) fields {
	switch {
	case native == "beforeShellExecution" || native == "afterShellExecution":
		ev.Tool, ev.Command = hookapi.ToolShell, p.str("command")
		return nil
	case native == "beforeReadFile":
		ev.Tool, ev.Paths = hookapi.ToolRead, p.strs("file_path")
		return nil
	case native == "afterFileEdit":
		ev.Tool, ev.Paths = hookapi.ToolEdit, p.strs("file_path")
		return nil
	case native == "beforeMCPExecution" || native == "afterMCPExecution":
		ev.Tool = hookapi.ToolMCP
		return nil
	case p.has("tool_name"):
		in, _ := decodeFields(p["tool_input"])
		ev.Tool = normalizeTool(p.str("tool_name"))
		fillToolInput(ev, in)
		return in
	// No usable hook_event_name: infer from the payload shape.
	case p.has("command"):
		ev.Tool, ev.Command = hookapi.ToolShell, p.str("command")
	case p.has("file_path") && ev.Event == hookapi.EventPostToolUse:
		ev.Tool, ev.Paths = hookapi.ToolEdit, p.strs("file_path")
	case p.has("file_path"):
		ev.Tool, ev.Paths = hookapi.ToolRead, p.strs("file_path")
	default:
		ev.Tool = hookapi.ToolOther
	}
	return nil
}

// fillToolInput copies the category-specific tool_input fields.
func fillToolInput(ev *hookapi.HookEvent, in fields) {
	switch ev.Tool {
	case hookapi.ToolGrep:
		ev.Pattern = in.str("pattern", "query", "regex")
		ev.Paths = in.strs("path", "paths", "target_directory", "directory")
		ev.Glob = in.str("glob", "include", "include_pattern", "glob_pattern")
	case hookapi.ToolGlob:
		ev.Glob = in.str("glob_pattern", "glob", "pattern")
		ev.Paths = in.strs("target_directory", "path", "directory")
	case hookapi.ToolRead, hookapi.ToolEdit, hookapi.ToolWrite:
		ev.Paths = in.strs("file_path", "target_file", "path", "filePath", "file")
	case hookapi.ToolShell:
		ev.Command = in.str("command", "cmd")
	}
}

// IsCursorPayload reports whether a hook payload was produced by Cursor,
// including when Cursor runs Claude Code hooks from ~/.claude/settings.json.
// The dispatcher uses it to answer such duplicate invocations only once.
func IsCursorPayload(payload []byte) bool {
	p, err := decodeFields(payload)
	if err != nil {
		return false
	}
	return p.str("cursor_version") != "" || (p.str("conversation_id") != "" && p.str("generation_id") != "")
}

// gateOutput is Cursor's answer to a gating hook. Field order is wire order.
type gateOutput struct {
	Permission   string          `json:"permission,omitempty"`
	UserMessage  string          `json:"user_message,omitempty"`
	AgentMessage string          `json:"agent_message,omitempty"`
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
}

// sessionOutput is Cursor's sessionStart answer (fire-and-forget).
type sessionOutput struct {
	AdditionalContext string `json:"additional_context,omitempty"`
}

// RenderDecision writes d in Cursor's wire format. The exit code is always
// 0: blocking is expressed only through "permission":"deny", so a no-op or
// allow can never block, and hooks that cannot block (sessionStart,
// after-hooks) ignore deny entirely.
func (Adapter) RenderDecision(ev hookapi.HookEvent, d hookapi.Decision) adapters.HookOutput {
	var out any = struct{}{}
	switch ev.Event {
	case hookapi.EventSessionStart:
		out = sessionOutput{AdditionalContext: d.AdditionalContext}
	case hookapi.EventPreToolUse:
		g := gate(d)
		if g.Permission == string(hookapi.ActionAllow) && nativeName(ev.Raw) == "preToolUse" {
			g.UpdatedInput = d.UpdatedInput
		}
		out = g
	case hookapi.EventSubagentStart:
		out = gate(d)
	}
	return adapters.HookOutput{Stdout: encode(out)}
}

// gate maps a decision to permission fields. Cursor cannot add context to a
// tool call without denying it, so AdditionalContext is dropped.
func gate(d hookapi.Decision) gateOutput {
	switch d.Effective() {
	case hookapi.ActionAllow:
		return gateOutput{Permission: "allow", UserMessage: d.UserMessage}
	case hookapi.ActionDeny, hookapi.ActionAsk:
		user := d.UserMessage
		if user == "" {
			user = "graphmaster: " + summary(d.Reason)
		}
		return gateOutput{Permission: string(d.Effective()), UserMessage: user, AgentMessage: d.Reason}
	}
	return gateOutput{}
}

// summary returns the first line of reason, capped for the user-facing UI.
func summary(reason string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(reason), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "tool call redirected to the code graph"
	}
	const maxRunes = 200
	if utf8.RuneCountInString(line) > maxRunes {
		line = string([]rune(line)[:maxRunes]) + "…"
	}
	return line
}

func nativeName(raw json.RawMessage) string {
	p, err := decodeFields(raw)
	if err != nil {
		return ""
	}
	return p.str("hook_event_name")
}

// encode marshals v as one JSON line; it falls back to "{}" (no opinion)
// rather than emitting something Cursor might treat as an error.
func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("{}\n")
	}
	return buf.Bytes()
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
