package query

import (
	"context"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/index"
)

// configBackend is a Backend that carries a configuration, like
// *index.Indexer.
type configBackend struct {
	Backend
	cfg *config.Config
}

func (c configBackend) Config() *config.Config { return c.cfg }

func (configBackend) Freshen(context.Context, time.Duration) (*index.Result, error) {
	return &index.Result{}, nil
}

func TestNewFreshenBudget(t *testing.T) {
	custom := config.Default()
	custom.Timeouts.MCP = config.Duration(3 * time.Second)
	zero := config.Default()
	zero.Timeouts.MCP = 0

	cases := []struct {
		name string
		b    Backend
		opts Options
		want time.Duration
	}{
		{"no config", configBackend{}, Options{}, DefaultFreshenBudget},
		{"config timeouts.mcp", configBackend{cfg: custom}, Options{}, 3 * time.Second},
		{"zero config value", configBackend{cfg: zero}, Options{}, DefaultFreshenBudget},
		{"explicit option wins", configBackend{cfg: custom}, Options{FreshenBudget: time.Second}, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.b, tc.opts).budget; got != tc.want {
				t.Fatalf("budget = %v, want %v", got, tc.want)
			}
		})
	}
}
