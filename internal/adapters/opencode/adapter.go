// Package opencode integrates graphmaster with OpenCode (v1.x).
//
// OpenCode has no command hooks; it loads JavaScript plugins instead. The
// adapter therefore installs a small generated plugin
// (~/.config/opencode/plugins/graphmaster.js) that spawns
// `graphmaster hook <event> --agent opencode` and speaks a private JSON
// protocol with it (see hookio.go). Because graphmaster writes both ends,
// the wire format is ours; OpenCode only defines what the plugin can do:
//
//   - tool.execute.before can block a call (throw) or rewrite its args;
//   - tool.execute.after and bus events (session.created, file.edited) are
//     notifications only;
//   - experimental.session.compacting can append context to the compaction
//     prompt. Nothing else can inject context without denying.
package opencode

import (
	"os"
	"path/filepath"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

func init() { adapters.Register(Adapter{}) }

// Adapter implements adapters.Agent for OpenCode.
type Adapter struct{}

var _ adapters.Agent = Adapter{}

// Name returns the --agent identifier.
func (Adapter) Name() hookapi.Agent { return hookapi.AgentOpenCode }

// Detect reports whether OpenCode appears installed: its global config
// directory exists, the install script's binary (~/.opencode/bin) exists, or
// an opencode executable is on PATH.
func (Adapter) Detect(env install.Env) bool {
	if dir, err := configDir(env); err == nil && isDir(dir) {
		return true
	}
	if home, err := env.Home(); err == nil && isExecutable(filepath.Join(home, ".opencode", "bin", "opencode")) {
		return true
	}
	path, _ := lookupEnv(env, "PATH")
	for _, d := range filepath.SplitList(path) {
		if d != "" && isExecutable(filepath.Join(d, "opencode")) {
			return true
		}
	}
	return false
}

// Notes returns post-install reminders.
func (Adapter) Notes(env install.Env) []string {
	plugin := "~/.config/opencode/plugins/" + pluginFileName
	if dir, err := configDir(env); err == nil {
		plugin = filepath.Join(dir, "plugins", pluginFileName)
	}
	return []string{
		"Restart OpenCode so it loads the graphmaster MCP server and the plugin " + plugin + ".",
		"The plugin embeds the absolute graphmaster path; re-run `graphmaster install` if the binary moves.",
		"OpenCode plugins cannot add context to tool calls: graphmaster's steering hints reach the model only as tool errors when a search is redirected.",
	}
}

// lookupEnv reads an environment variable through env, defaulting to the
// process environment like install.Env does.
func lookupEnv(env install.Env, key string) (string, bool) {
	if env.LookupEnv != nil {
		return env.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
