package hookapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Action is the steering verdict for a hook event.
type Action string

// Actions.
const (
	ActionAllow Action = "allow" // explicitly permit the tool call
	ActionDeny  Action = "deny"  // block; Reason is shown to the model
	ActionAsk   Action = "ask"   // defer to the user (agent permitting)
	ActionNoop  Action = "noop"  // express no opinion; agent default applies
)

// ParseAction parses an action name case-insensitively.
func ParseAction(s string) (Action, error) {
	switch a := Action(strings.ToLower(strings.TrimSpace(s))); a {
	case ActionAllow, ActionDeny, ActionAsk, ActionNoop:
		return a, nil
	}
	return "", fmt.Errorf("unknown action %q", s)
}

// Decision is the agent-neutral hook result. Adapters render it into each
// agent's native output (exit code, hookSpecificOutput, permission, ...).
type Decision struct {
	Action Action `json:"action,omitempty"`
	// Reason is shown to the model, primarily on deny.
	Reason string `json:"reason,omitempty"`
	// AdditionalContext is injected into the model context (session start
	// summaries, read outlines).
	AdditionalContext string `json:"additional_context,omitempty"`
	// UserMessage is shown to the human only.
	UserMessage string `json:"user_message,omitempty"`
	// UpdatedInput optionally rewrites the tool input (agent permitting).
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
}

// Allow returns an allow decision.
func Allow() Decision { return Decision{Action: ActionAllow} }

// Deny returns a deny decision with a reason for the model.
func Deny(reason string) Decision { return Decision{Action: ActionDeny, Reason: reason} }

// Ask returns an ask decision with a reason.
func Ask(reason string) Decision { return Decision{Action: ActionAsk, Reason: reason} }

// Noop returns a decision that expresses no opinion.
func Noop() Decision { return Decision{Action: ActionNoop} }

// Effective returns the action, treating the zero value as noop.
func (d Decision) Effective() Action {
	if d.Action == "" {
		return ActionNoop
	}
	return d.Action
}

// Blocks reports whether the decision prevents the tool call.
func (d Decision) Blocks() bool { return d.Action == ActionDeny }

// WithContext returns a copy of d with AdditionalContext set.
func (d Decision) WithContext(ctx string) Decision {
	d.AdditionalContext = ctx
	return d
}

// WithUserMessage returns a copy of d with UserMessage set.
func (d Decision) WithUserMessage(msg string) Decision {
	d.UserMessage = msg
	return d
}
