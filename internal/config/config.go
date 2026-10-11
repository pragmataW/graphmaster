// Package config loads graphmaster settings. Layers are applied in order
// defaults < user (~/.config/graphmaster/config.toml) < project
// (<root>/.graphmaster/config.toml) < environment (GRAPHMASTER_*); a later
// layer overrides only the keys it sets.
package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/model"
)

// Timeouts are freshness lock-wait budgets per caller: how long a caller
// waits for the index writer before answering from the current (stale)
// graph with a banner.
type Timeouts struct {
	MCP           Duration `toml:"mcp"`
	PreToolUse    Duration `toml:"pre_tool_use"`
	PostToolUse   Duration `toml:"post_tool_use"`
	SessionStart  Duration `toml:"session_start"`
	SubagentStart Duration `toml:"subagent_start"`
	PreCompact    Duration `toml:"pre_compact"`
}

// ForEvent returns the budget for a hook event. Events without a budget
// (e.g. Stop) return 0: they never wait for the writer.
func (t Timeouts) ForEvent(e hookapi.Event) time.Duration {
	switch e {
	case hookapi.EventSessionStart:
		return t.SessionStart.D()
	case hookapi.EventSubagentStart:
		return t.SubagentStart.D()
	case hookapi.EventPreToolUse:
		return t.PreToolUse.D()
	case hookapi.EventPostToolUse:
		return t.PostToolUse.D()
	case hookapi.EventPreCompact:
		return t.PreCompact.D()
	}
	return 0
}

// Config is the effective configuration.
type Config struct {
	Mode        Mode     `toml:"mode"`
	MaxFileSize ByteSize `toml:"max_file_size"`
	// Excludes replaces the default exclude list (gitignore syntax).
	Excludes []string `toml:"excludes"`
	// ExtraExcludes accumulates across layers on top of Excludes.
	ExtraExcludes []string `toml:"extra_excludes"`
	// Languages lists enabled language names; empty means all.
	Languages  []string `toml:"languages"`
	SoftBudget int      `toml:"soft_budget"` // balanced-mode denies per (session, agent)
	Timeouts   Timeouts `toml:"timeouts"`

	// Sources lists the layers that contributed (file paths and/or "env").
	Sources []string `toml:"-"`
	// Warnings collects non-fatal problems such as unknown keys.
	Warnings []string `toml:"-"`
}

// defaultExcludes are always-skipped paths unless Excludes is overridden.
var defaultExcludes = []string{"vendor/", "node_modules/", "dist/", "build/", "target/", "*.min.*"}

// Default returns the built-in configuration.
func Default() *Config {
	langs := make([]string, 0, len(model.AllLangs()))
	for _, l := range model.AllLangs() {
		langs = append(langs, l.String())
	}
	return &Config{
		Mode:        ModeBalanced,
		MaxFileSize: 1 << 20,
		Excludes:    append([]string(nil), defaultExcludes...),
		Languages:   langs,
		SoftBudget:  8,
		Timeouts: Timeouts{
			MCP:           Duration(20 * time.Second),
			PreToolUse:    Duration(1500 * time.Millisecond),
			PostToolUse:   Duration(1500 * time.Millisecond),
			SessionStart:  Duration(590 * time.Second),
			SubagentStart: Duration(1500 * time.Millisecond),
			PreCompact:    Duration(1500 * time.Millisecond),
		},
	}
}

// Validate checks semantic constraints.
func (c *Config) Validate() error {
	var errs []error
	if _, err := ParseMode(string(c.Mode)); err != nil {
		errs = append(errs, err)
	}
	if c.MaxFileSize <= 0 {
		errs = append(errs, fmt.Errorf("max_file_size must be > 0, got %d", c.MaxFileSize))
	}
	if c.SoftBudget < 0 {
		errs = append(errs, fmt.Errorf("soft_budget must be >= 0, got %d", c.SoftBudget))
	}
	for _, name := range c.Languages {
		if _, err := model.ParseLang(name); err != nil {
			errs = append(errs, fmt.Errorf("languages: %w", err))
		}
	}
	for _, t := range c.Timeouts.fields() {
		if *t.d < 0 {
			errs = append(errs, fmt.Errorf("timeouts.%s must be >= 0, got %s", t.key, t.d.D()))
		}
	}
	return errors.Join(errs...)
}

// AllExcludes returns Excludes followed by ExtraExcludes, deduplicated.
func (c *Config) AllExcludes() []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{c.Excludes, c.ExtraExcludes} {
		for _, p := range list {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// EnabledLangs returns the enabled languages in model order. Unknown names
// are skipped (Validate reports them).
func (c *Config) EnabledLangs() []model.Lang {
	if len(c.Languages) == 0 {
		return model.AllLangs()
	}
	on := map[model.Lang]bool{}
	for _, name := range c.Languages {
		if l, err := model.ParseLang(name); err == nil {
			on[l] = true
		}
	}
	var out []model.Lang
	for _, l := range model.AllLangs() {
		if on[l] {
			out = append(out, l)
		}
	}
	return out
}

// LangEnabled reports whether l is enabled.
func (c *Config) LangEnabled(l model.Lang) bool {
	for _, e := range c.EnabledLangs() {
		if e == l {
			return true
		}
	}
	return false
}

type timeoutField struct {
	key string // TOML key, also used for GRAPHMASTER_TIMEOUT_<KEY>
	d   *Duration
}

func (t *Timeouts) fields() []timeoutField {
	return []timeoutField{
		{"mcp", &t.MCP},
		{"pre_tool_use", &t.PreToolUse},
		{"post_tool_use", &t.PostToolUse},
		{"session_start", &t.SessionStart},
		{"subagent_start", &t.SubagentStart},
		{"pre_compact", &t.PreCompact},
	}
}
