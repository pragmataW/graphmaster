package hookrun

import (
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
)

func TestEventTimeoutDefaults(t *testing.T) {
	cfg := config.Default()
	cases := map[hookapi.Event]time.Duration{
		hookapi.EventSessionStart:  595 * time.Second,
		hookapi.EventSubagentStart: 4 * time.Second,
		hookapi.EventPreToolUse:    4 * time.Second,
		hookapi.EventPostToolUse:   6500 * time.Millisecond,
		hookapi.EventPreCompact:    4 * time.Second,
		hookapi.EventStop:          2 * time.Second,
	}
	for ev, want := range cases {
		if got := EventTimeout(ev, cfg); got != want {
			t.Errorf("EventTimeout(%s) = %s, want %s", ev, got, want)
		}
	}
}

func TestEventTimeoutFollowsConfigBudget(t *testing.T) {
	cfg := config.Default()
	cfg.Timeouts.PreToolUse = config.Duration(3 * time.Second)
	if got := EventTimeout(hookapi.EventPreToolUse, cfg); got != 5500*time.Millisecond {
		t.Fatalf("pre-tool-use = %s", got)
	}
	// A huge budget is capped below the agents' own kill timeouts.
	cfg.Timeouts.SessionStart = config.Duration(time.Hour)
	if got := EventTimeout(hookapi.EventSessionStart, cfg); got != maxEventTimeout {
		t.Fatalf("session-start = %s, want cap %s", got, maxEventTimeout)
	}
	// A nil config uses the defaults; an unknown event still gets a floor.
	if got := EventTimeout(hookapi.EventPreToolUse, nil); got != 4*time.Second {
		t.Fatalf("nil cfg = %s", got)
	}
	if got := EventTimeout("bogus", nil); got != 2*time.Second {
		t.Fatalf("unknown = %s", got)
	}
}
