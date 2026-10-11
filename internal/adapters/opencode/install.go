package opencode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

// mcpEntry is OpenCode's local MCP server definition (field order kept).
type mcpEntry struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
	Enabled bool     `json:"enabled"`
}

// InstallPlan registers the MCP server in the global OpenCode config,
// generates the hook plugin and adds the instruction block to the global
// AGENTS.md.
func (Adapter) InstallPlan(env install.Env, opts adapters.InstallOptions) (install.Plan, error) {
	plan := install.Plan{Agent: string(hookapi.AgentOpenCode)}
	if !filepath.IsAbs(opts.Binary) {
		return plan, fmt.Errorf("opencode: graphmaster binary path must be absolute, got %q", opts.Binary)
	}
	dir, err := configDir(env)
	if err != nil {
		return plan, fmt.Errorf("opencode: %w", err)
	}
	plugin, err := renderPlugin(defaultPluginData(opts.Binary))
	if err != nil {
		return plan, fmt.Errorf("opencode: render plugin: %w", err)
	}

	// The plugin comes first: the engine records the directories a file
	// creates and removes them with it, and uninstall walks files in path
	// order, so the deepest new directory must belong to the last file
	// ("plugins/..." sorts after "AGENTS.md" and "opencode.json").
	plan.Files = append(plan.Files,
		install.FileChange{
			Path:    filepath.Join(dir, "plugins", pluginFileName),
			Format:  install.FormatGenerated,
			Content: plugin,
			Mode:    0o644,
		},
		install.FileChange{
			Path:   configFile(dir),
			Format: install.FormatJSON,
			Ops: []install.Op{install.JSONSet{
				Pointer: install.Pointer("mcp", "graphmaster"),
				Value:   mcpEntry{Type: "local", Command: []string{opts.Binary, "mcp"}, Enabled: true},
			}},
		},
	)
	if strings.TrimSpace(opts.Instructions) != "" {
		plan.Files = append(plan.Files, install.FileChange{
			Path:   filepath.Join(dir, "AGENTS.md"),
			Format: install.FormatMarkdown,
			Ops:    []install.Op{install.Block{Body: opts.Instructions}},
		})
	}
	return plan, nil
}

// configDir is OpenCode's global config directory: $XDG_CONFIG_HOME/opencode
// or ~/.config/opencode.
func configDir(env install.Env) (string, error) {
	base, err := env.ConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "opencode"), nil
}

// configFile picks the global config file to edit. OpenCode merges
// config.json, opencode.json and opencode.jsonc (later wins, maps merge
// deeply), so any of them works; graphmaster edits opencode.json unless
// only opencode.jsonc exists.
func configFile(dir string) string {
	jsonc := filepath.Join(dir, "opencode.jsonc")
	plain := filepath.Join(dir, "opencode.json")
	if _, err := os.Stat(plain); err != nil {
		if _, err := os.Stat(jsonc); err == nil {
			return jsonc
		}
	}
	return plain
}
