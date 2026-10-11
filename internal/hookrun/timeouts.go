package hookrun

import (
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// maxEventTimeout caps every hook deadline below the longest agent hook
// timeout graphmaster installs (Codex SessionStart: 600s).
const maxEventTimeout = 595 * time.Second

// eventGrace is the time an event may spend beyond its freshness lock
// budget: applying changes once the lock is taken, the graph lookup and
// rendering. Budgets plus grace stay under the agent timeouts graphmaster
// installs (Cursor and OpenCode: 5s for gating hooks; Cursor 10s and
// OpenCode 8s for after-hooks).
var eventGrace = map[hookapi.Event]time.Duration{
	hookapi.EventSessionStart:  5 * time.Second,
	hookapi.EventSubagentStart: 2500 * time.Millisecond,
	hookapi.EventPreToolUse:    2500 * time.Millisecond,
	hookapi.EventPostToolUse:   5 * time.Second,
	hookapi.EventPreCompact:    2500 * time.Millisecond,
}

// minEventTimeout is the deadline of events without a budget (Stop).
const minEventTimeout = 2 * time.Second

// EventTimeout returns how long the dispatcher may work on one event before
// it gives up and answers fail-open. It is the event's freshness lock
// budget from cfg (config.Timeouts) plus a per-event grace, capped at
// maxEventTimeout. A nil cfg uses config.Default.
func EventTimeout(e hookapi.Event, cfg *config.Config) time.Duration {
	if cfg == nil {
		cfg = config.Default()
	}
	grace, ok := eventGrace[e]
	if !ok {
		return minEventTimeout
	}
	d := cfg.Timeouts.ForEvent(e) + grace
	if d < minEventTimeout {
		d = minEventTimeout
	}
	if d > maxEventTimeout {
		d = maxEventTimeout
	}
	return d
}
