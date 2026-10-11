// Package hookapi defines the agent-neutral hook contract. Agent adapters
// translate each agent's native hook JSON into a HookEvent and translate the
// resulting Decision back; the steering core only ever sees these types.
package hookapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Agent identifies the AI coding agent that fired a hook.
type Agent string

// Supported agents.
const (
	AgentClaude   Agent = "claude"
	AgentCodex    Agent = "codex"
	AgentCursor   Agent = "cursor"
	AgentOpenCode Agent = "opencode"
)

// Valid reports whether a is a supported agent in canonical spelling.
func (a Agent) Valid() bool {
	for _, k := range AllAgents() {
		if k == a {
			return true
		}
	}
	return false
}

// AllAgents returns every supported agent in stable order.
func AllAgents() []Agent { return []Agent{AgentClaude, AgentCodex, AgentCursor, AgentOpenCode} }

// ParseAgent parses an agent name case-insensitively ("claude-code" is
// accepted as an alias for claude).
func ParseAgent(s string) (Agent, error) {
	n := strings.ToLower(strings.TrimSpace(s))
	if n == "claude-code" || n == "claudecode" {
		return AgentClaude, nil
	}
	for _, a := range AllAgents() {
		if string(a) == n {
			return a, nil
		}
	}
	return "", fmt.Errorf("unknown agent %q", s)
}

// Event is a normalized hook lifecycle event.
type Event string

// Hook events. String values are the kebab-case CLI spelling used by
// `graphmaster hook <event>`.
const (
	EventSessionStart  Event = "session-start"
	EventSubagentStart Event = "subagent-start"
	EventPreToolUse    Event = "pre-tool-use"
	EventPostToolUse   Event = "post-tool-use"
	EventPreCompact    Event = "pre-compact"
	EventStop          Event = "stop"
)

// AllEvents returns every event in stable order.
func AllEvents() []Event {
	return []Event{EventSessionStart, EventSubagentStart, EventPreToolUse, EventPostToolUse, EventPreCompact, EventStop}
}

// squash lowercases s and drops '-', '_' and spaces so "PreToolUse",
// "preToolUse" and "pre-tool-use" compare equal.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r != '-' && r != '_' && r != ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParseEvent accepts the kebab-case, snake_case, PascalCase or camelCase
// spelling of an event. Agent-specific events (e.g. Cursor's afterFileEdit)
// must be mapped by the adapter first.
func ParseEvent(s string) (Event, error) {
	n := squash(s)
	for _, e := range AllEvents() {
		if squash(string(e)) == n {
			return e, nil
		}
	}
	return "", fmt.Errorf("unknown hook event %q", s)
}

// Valid reports whether e is a known event in canonical spelling.
func (e Event) Valid() bool {
	for _, k := range AllEvents() {
		if k == e {
			return true
		}
	}
	return false
}

// IsToolEvent reports whether the event carries a tool invocation.
func (e Event) IsToolEvent() bool { return e == EventPreToolUse || e == EventPostToolUse }

// Tool is the normalized category of the tool an agent is invoking.
type Tool string

// Tool categories.
const (
	ToolGrep  Tool = "grep"  // content search (Claude Grep, Cursor Grep)
	ToolGlob  Tool = "glob"  // file-name search
	ToolRead  Tool = "read"  // file read
	ToolShell Tool = "shell" // shell command (Bash, Shell, beforeShellExecution)
	ToolPatch Tool = "patch" // multi-file patch (Codex apply_patch)
	ToolMCP   Tool = "mcp"   // MCP tool call
	ToolEdit  Tool = "edit"  // in-place file edit
	ToolWrite Tool = "write" // whole-file write
	ToolOther Tool = "other" // anything else
)

var allTools = []Tool{ToolGrep, ToolGlob, ToolRead, ToolShell, ToolPatch, ToolMCP, ToolEdit, ToolWrite, ToolOther}

// Valid reports whether t is a known tool category.
func (t Tool) Valid() bool {
	for _, k := range allTools {
		if k == t {
			return true
		}
	}
	return false
}

// IsSearch reports whether t is a direct code search the steering layer may redirect.
func (t Tool) IsSearch() bool { return t == ToolGrep || t == ToolGlob }

// IsMutation reports whether t modifies files (triggers a dirty hint).
func (t Tool) IsMutation() bool { return t == ToolEdit || t == ToolWrite || t == ToolPatch }

// HookEvent is one agent-neutral hook invocation.
type HookEvent struct {
	Agent     Agent  `json:"agent"`
	Event     Event  `json:"event"`
	SessionID string `json:"session_id,omitempty"`
	// AgentID identifies a subagent within the session; "" for the main agent.
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"` // e.g. "Explore", "Plan"
	Tool      Tool   `json:"tool,omitempty"`
	Pattern   string `json:"pattern,omitempty"` // search pattern (grep)
	// Paths are the target files or directories, as given by the agent
	// (absolute or relative to CWD).
	Paths     []string        `json:"paths,omitempty"`
	Glob      string          `json:"glob,omitempty"`    // glob pattern or grep file filter
	Command   string          `json:"command,omitempty"` // shell command text
	CWD       string          `json:"cwd,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"` // original payload, for logging/dedupe
}

// IsSubagent reports whether the event came from a subagent.
func (e HookEvent) IsSubagent() bool { return e.AgentID != "" }

// Validate checks that the adapter produced a well-formed event.
func (e HookEvent) Validate() error {
	var errs []error
	if !e.Agent.Valid() {
		errs = append(errs, fmt.Errorf("invalid agent %q", e.Agent))
	}
	if !e.Event.Valid() {
		errs = append(errs, fmt.Errorf("invalid event %q", e.Event))
	}
	switch {
	case e.Event.IsToolEvent() && e.Tool == "":
		errs = append(errs, fmt.Errorf("%s event requires a tool", e.Event))
	case e.Tool != "" && !e.Tool.Valid():
		errs = append(errs, fmt.Errorf("invalid tool %q", e.Tool))
	}
	return errors.Join(errs...)
}
