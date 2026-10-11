// Package claude adapts Claude Code to graphmaster: it parses Claude's hook
// payloads, renders decisions as hookSpecificOutput JSON and builds the
// user-level install plan (~/.claude.json, ~/.claude/settings.json and
// ~/.claude/CLAUDE.md).
package claude

import (
	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// Adapter implements adapters.Agent for Claude Code. The zero value is ready
// to use.
type Adapter struct{}

var _ adapters.Agent = Adapter{}

func init() { adapters.Register(Adapter{}) }

// Name returns hookapi.AgentClaude.
func (Adapter) Name() hookapi.Agent { return hookapi.AgentClaude }
