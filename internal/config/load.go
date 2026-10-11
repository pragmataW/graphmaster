package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Environment variables. Lists are comma-separated.
const (
	EnvMode          = "GRAPHMASTER_MODE"
	EnvMaxFileSize   = "GRAPHMASTER_MAX_FILE_SIZE"
	EnvExcludes      = "GRAPHMASTER_EXCLUDES"       // replaces excludes
	EnvExtraExcludes = "GRAPHMASTER_EXTRA_EXCLUDES" // appended to extra_excludes
	EnvLanguages     = "GRAPHMASTER_LANGUAGES"
	EnvSoftBudget    = "GRAPHMASTER_SOFT_BUDGET"
	// EnvTimeoutPrefix + upper-cased timeout key, e.g. GRAPHMASTER_TIMEOUT_PRE_TOOL_USE.
	EnvTimeoutPrefix = "GRAPHMASTER_TIMEOUT_"
)

// Options controls Load. Zero values use the real environment.
type Options struct {
	// Root is the repository root; "" skips the project layer.
	Root string
	// UserPath overrides the user config file location.
	UserPath string
	// LookupEnv defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
}

// Load loads the configuration for the repository at root using the real
// environment. On error callers should fall back to Default (fail-open).
func Load(root string) (*Config, error) { return LoadWith(Options{Root: root}) }

// LoadWith loads defaults < user < project < env per opts.
func LoadWith(opts Options) (*Config, error) {
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	cfg := Default()

	userPath := opts.UserPath
	if userPath == "" {
		// A missing HOME only disables the user layer.
		userPath, _ = UserConfigPath(lookup)
	}
	paths := []string{userPath}
	if opts.Root != "" {
		paths = append(paths, ProjectConfigPath(opts.Root))
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := applyFile(cfg, p); err != nil {
			return nil, err
		}
	}
	if err := applyEnv(cfg, lookup); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config (%s): %w", strings.Join(cfg.Sources, " < "), err)
	}
	return cfg, nil
}

// UserConfigPath returns $XDG_CONFIG_HOME/graphmaster/config.toml, or
// $HOME/.config/graphmaster/config.toml. A relative XDG_CONFIG_HOME is
// ignored, as the XDG spec requires.
func UserConfigPath(lookup func(string) (string, bool)) (string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if x, ok := lookup("XDG_CONFIG_HOME"); ok && filepath.IsAbs(x) {
		return filepath.Join(x, "graphmaster", "config.toml"), nil
	}
	home, ok := lookup("HOME")
	if !ok || home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("locate user config: %w", err)
		}
	}
	return filepath.Join(home, ".config", "graphmaster", "config.toml"), nil
}

// ProjectConfigPath returns <root>/.graphmaster/config.toml.
func ProjectConfigPath(root string) string {
	return filepath.Join(root, ".graphmaster", "config.toml")
}

// fileLayer mirrors Config with pointers so absent keys are distinguishable
// from zero values.
type fileLayer struct {
	Mode          *Mode     `toml:"mode"`
	MaxFileSize   *ByteSize `toml:"max_file_size"`
	Excludes      *[]string `toml:"excludes"`
	ExtraExcludes []string  `toml:"extra_excludes"`
	Languages     *[]string `toml:"languages"`
	SoftBudget    *int      `toml:"soft_budget"`
	Timeouts      struct {
		MCP           *Duration `toml:"mcp"`
		PreToolUse    *Duration `toml:"pre_tool_use"`
		PostToolUse   *Duration `toml:"post_tool_use"`
		SessionStart  *Duration `toml:"session_start"`
		SubagentStart *Duration `toml:"subagent_start"`
		PreCompact    *Duration `toml:"pre_compact"`
	} `toml:"timeouts"`
}

func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}

	var l fileLayer
	// Strict pass only collects unknown keys as warnings, so a config
	// written for a newer graphmaster still loads.
	strictErr := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&fileLayer{})
	var missing *toml.StrictMissingError
	if errors.As(strictErr, &missing) {
		for i := range missing.Errors {
			e := &missing.Errors[i]
			row, _ := e.Position()
			cfg.Warnings = append(cfg.Warnings,
				fmt.Sprintf("%s:%d: unknown key %q ignored", path, row, strings.Join(e.Key(), ".")))
		}
	}
	if err := toml.Unmarshal(data, &l); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return fmt.Errorf("parse config %s:%d:%d: %w", path, row, col, err)
		}
		return fmt.Errorf("parse config %s: %w", path, err)
	}

	if l.Mode != nil {
		cfg.Mode = *l.Mode
	}
	if l.MaxFileSize != nil {
		cfg.MaxFileSize = *l.MaxFileSize
	}
	if l.Excludes != nil {
		cfg.Excludes = *l.Excludes
	}
	cfg.ExtraExcludes = append(cfg.ExtraExcludes, l.ExtraExcludes...)
	if l.Languages != nil {
		cfg.Languages = *l.Languages
	}
	if l.SoftBudget != nil {
		cfg.SoftBudget = *l.SoftBudget
	}
	t := l.Timeouts
	for _, f := range []struct {
		src *Duration
		dst *Duration
	}{
		{t.MCP, &cfg.Timeouts.MCP},
		{t.PreToolUse, &cfg.Timeouts.PreToolUse},
		{t.PostToolUse, &cfg.Timeouts.PostToolUse},
		{t.SessionStart, &cfg.Timeouts.SessionStart},
		{t.SubagentStart, &cfg.Timeouts.SubagentStart},
		{t.PreCompact, &cfg.Timeouts.PreCompact},
	} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	cfg.Sources = append(cfg.Sources, path)
	return nil
}

func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	used := false
	get := func(key string) (string, bool) {
		v, ok := lookup(key)
		if ok && strings.TrimSpace(v) != "" {
			used = true
			return v, true
		}
		return "", false
	}
	wrap := func(key string, err error) error { return fmt.Errorf("env %s: %w", key, err) }

	if v, ok := get(EnvMode); ok {
		m, err := ParseMode(v)
		if err != nil {
			return wrap(EnvMode, err)
		}
		cfg.Mode = m
	}
	if v, ok := get(EnvMaxFileSize); ok {
		if err := cfg.MaxFileSize.UnmarshalText([]byte(v)); err != nil {
			return wrap(EnvMaxFileSize, err)
		}
	}
	if v, ok := get(EnvExcludes); ok {
		cfg.Excludes = splitList(v)
	}
	if v, ok := get(EnvExtraExcludes); ok {
		cfg.ExtraExcludes = append(cfg.ExtraExcludes, splitList(v)...)
	}
	if v, ok := get(EnvLanguages); ok {
		cfg.Languages = splitList(v)
	}
	if v, ok := get(EnvSoftBudget); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return wrap(EnvSoftBudget, fmt.Errorf("invalid integer %q", v))
		}
		cfg.SoftBudget = n
	}
	for _, f := range cfg.Timeouts.fields() {
		key := EnvTimeoutPrefix + strings.ToUpper(f.key)
		if v, ok := get(key); ok {
			if err := f.d.UnmarshalText([]byte(v)); err != nil {
				return wrap(key, err)
			}
		}
	}
	if used {
		cfg.Sources = append(cfg.Sources, "env")
	}
	return nil
}

// splitList splits a comma-separated list, trimming blanks.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
