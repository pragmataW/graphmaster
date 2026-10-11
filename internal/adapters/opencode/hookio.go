package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// Wire protocol between the generated plugin and `graphmaster hook`.
//
// stdin: one JSON object (payload). Tool events carry OpenCode's hook input
// ({tool, sessionID, callID}) plus output.args; file.edited is forwarded as
// tool "edit" with args.filePath. Subagent calls are attributed by the
// plugin: sessionID is the root session and agentID the child session.
//
// stdout: empty for "no opinion", otherwise one JSON object (reply). The
// exit code is always 0; the plugin fails open on anything unexpected.

// payload is the plugin's stdin message.
type payload struct {
	Tool      string          `json:"tool"`
	SessionID string          `json:"sessionID"`
	AgentID   string          `json:"agentID"`
	AgentType string          `json:"agentType"`
	CallID    string          `json:"callID"`
	Args      json.RawMessage `json:"args"`
	CWD       string          `json:"cwd"`
}

// reply is the plugin's stdout message.
type reply struct {
	// Action "deny" makes tool.execute.before throw Reason; "allow" with
	// Args replaces the tool arguments.
	Action string          `json:"action,omitempty"`
	Reason string          `json:"reason,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	// Context is appended to the compaction prompt (pre-compact only).
	Context string `json:"context,omitempty"`
}

const defaultDenyReason = "graphmaster blocked this tool call"

// ParseEvent converts a plugin payload into the neutral event.
func (Adapter) ParseEvent(event hookapi.Event, raw []byte, _ []string) (hookapi.HookEvent, error) {
	ev := hookapi.HookEvent{Agent: hookapi.AgentOpenCode, Event: event}
	if !event.Valid() {
		return ev, fmt.Errorf("opencode: unknown event %q", event)
	}
	var p payload
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ev, fmt.Errorf("opencode: decode %s payload: %w", event, err)
		}
		ev.Raw = append(json.RawMessage(nil), raw...)
	}
	ev.SessionID = p.SessionID
	ev.AgentID = p.AgentID
	ev.AgentType = p.AgentType
	ev.CWD = p.CWD
	if !event.IsToolEvent() {
		return ev, nil
	}
	if p.Tool == "" {
		return ev, fmt.Errorf("opencode: %s payload has no tool", event)
	}
	var args toolArgs
	if a := bytes.TrimSpace(p.Args); len(a) > 0 && !bytes.Equal(a, []byte("null")) {
		if err := json.Unmarshal(a, &args); err != nil {
			return ev, fmt.Errorf("opencode: decode %s args: %w", p.Tool, err)
		}
	}
	ev.Tool = normalizeTool(p.Tool)
	ev.ToolUseID = p.CallID
	fillTool(&ev, args)
	return ev, nil
}

// RenderDecision converts a decision into the plugin's reply. Only
// pre-tool-use can block or rewrite input, and only pre-compact can carry
// context; everything else degrades to "no opinion" so a notification can
// never block OpenCode. "ask" has no plugin equivalent and is dropped.
func (Adapter) RenderDecision(ev hookapi.HookEvent, d hookapi.Decision) adapters.HookOutput {
	var r reply
	switch ev.Event {
	case hookapi.EventPreToolUse:
		switch d.Effective() {
		case hookapi.ActionDeny:
			r.Action, r.Reason = "deny", denyReason(d)
		case hookapi.ActionAllow, hookapi.ActionNoop:
			if isJSONObject(d.UpdatedInput) {
				r.Action, r.Args = "allow", d.UpdatedInput
			}
		}
	case hookapi.EventPreCompact:
		r.Context = d.AdditionalContext
	}
	if r.Action == "" && r.Context == "" {
		return adapters.HookOutput{}
	}
	out, err := encodeReply(r)
	if err != nil {
		return adapters.HookOutput{} // fail open
	}
	return adapters.HookOutput{Stdout: out}
}

// denyReason is the error message the model sees. OpenCode has no separate
// context channel, so additional context is appended to the reason.
func denyReason(d hookapi.Decision) string {
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = defaultDenyReason
	}
	if ctx := strings.TrimSpace(d.AdditionalContext); ctx != "" {
		reason += "\n\n" + ctx
	}
	return reason
}

func isJSONObject(b json.RawMessage) bool {
	var m map[string]json.RawMessage
	b = bytes.TrimSpace(b)
	return len(b) > 0 && b[0] == '{' && json.Unmarshal(b, &m) == nil
}

func encodeReply(r reply) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
