// Package adapters defines the contract every AI-agent integration implements.
// Adapters only translate: agent JSON ↔ hookapi types, and desired config
// edits → install.Plan. All decision logic lives in the core packages.
package adapters

import (
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

// HookOutput is what the hook dispatcher writes back to the agent process.
type HookOutput struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// InstallOptions carries what an adapter needs to build its install plan.
type InstallOptions struct {
	// Binary is the stable absolute path of the graphmaster executable.
	Binary string
	// Instructions is the agent-neutral steering text placed in the agent's
	// instruction file (CLAUDE.md, AGENTS.md, ...).
	Instructions string
}

// Agent is implemented by each package under internal/adapters/<agent>.
type Agent interface {
	// Name is the agent identifier used on the command line (--agent).
	Name() hookapi.Agent

	// Detect reports whether the agent appears installed for env's HOME.
	Detect(env install.Env) bool

	// ParseEvent converts a raw hook payload into the neutral event.
	// event is the event name graphmaster was invoked with; env is os.Environ().
	ParseEvent(event hookapi.Event, payload []byte, env []string) (hookapi.HookEvent, error)

	// RenderDecision converts a neutral decision into the agent's wire format.
	RenderDecision(ev hookapi.HookEvent, d hookapi.Decision) HookOutput

	// InstallPlan describes every config edit needed for MCP registration,
	// hooks and instructions at user level.
	InstallPlan(env install.Env, opts InstallOptions) (install.Plan, error)

	// Notes returns human-facing post-install reminders (e.g. Codex /hooks trust).
	Notes(env install.Env) []string
}

var registry = map[hookapi.Agent]Agent{}

// Register adds an adapter; called from each adapter package's init().
func Register(a Agent) {
	if _, dup := registry[a.Name()]; dup {
		panic("adapters: duplicate registration for " + string(a.Name()))
	}
	registry[a.Name()] = a
}

// Get returns the adapter for name.
func Get(name hookapi.Agent) (Agent, bool) {
	a, ok := registry[name]
	return a, ok
}

// All returns registered adapters in hookapi.AllAgents order.
func All() []Agent {
	out := make([]Agent, 0, len(registry))
	for _, n := range hookapi.AllAgents() {
		if a, ok := registry[n]; ok {
			out = append(out, a)
		}
	}
	return out
}
