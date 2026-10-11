// Package codex adapts the OpenAI Codex CLI to graphmaster.
//
// Codex reads user-level configuration from $CODEX_HOME (default ~/.codex):
// MCP servers from config.toml, lifecycle hooks from hooks.json (the same
// shape as Claude Code's settings "hooks" object) and global instructions
// from AGENTS.md. Codex has no dedicated grep/glob tools, so code search is
// steered by parsing the shell commands it runs.
package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

// agentsMDCap is the size Codex reads from AGENTS.md before truncating
// (project_doc_max_bytes default).
const agentsMDCap = 32 * 1024

// Adapter implements adapters.Agent for the Codex CLI.
type Adapter struct{}

var _ adapters.Agent = Adapter{}

func init() { adapters.Register(Adapter{}) }

// Name implements adapters.Agent.
func (Adapter) Name() hookapi.Agent { return hookapi.AgentCodex }

// lookup reads an environment variable through env, defaulting to the real
// process environment like install.Env does.
func lookup(env install.Env, key string) (string, bool) {
	if env.LookupEnv != nil {
		return env.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

// codexHome returns $CODEX_HOME when absolute, else $HOME/.codex.
func codexHome(env install.Env) (string, error) {
	if d, ok := lookup(env, "CODEX_HOME"); ok && filepath.IsAbs(d) {
		return d, nil
	}
	home, err := env.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// Detect implements adapters.Agent: the Codex config directory exists or a
// codex executable is on PATH.
func (Adapter) Detect(env install.Env) bool {
	if dir, err := codexHome(env); err == nil {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true
		}
	}
	path, _ := lookup(env, "PATH")
	return onPath(path, "codex")
}

// onPath reports whether an executable named name exists in a PATH list.
func onPath(pathList, name string) bool {
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			continue
		}
		for _, cand := range executableNames(name) {
			fi, err := os.Stat(filepath.Join(dir, cand))
			if err == nil && fi.Mode().IsRegular() && isExecutable(fi.Mode()) {
				return true
			}
		}
	}
	return false
}

func executableNames(name string) []string {
	if os.PathSeparator == '\\' {
		return []string{name + ".exe", name + ".cmd", name}
	}
	return []string{name}
}

func isExecutable(m os.FileMode) bool {
	return os.PathSeparator == '\\' || m.Perm()&0o111 != 0
}

// Notes implements adapters.Agent.
func (Adapter) Notes(env install.Env) []string {
	notes := []string{
		"Codex runs user hooks only after you trust them: start `codex`, run `/hooks` and approve the graphmaster entries.",
		"Restart running Codex sessions so they load the graphmaster MCP server and hooks.",
	}
	dir, err := codexHome(env)
	if err != nil {
		return notes
	}
	if fi, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil && fi.Size() > agentsMDCap {
		notes = append(notes, fmt.Sprintf(
			"%s is %d bytes; Codex only reads the first 32 KiB, so the graphmaster block at the end may be cut off. Trim the file or raise project_doc_max_bytes.",
			collapseHome(env, filepath.Join(dir, "AGENTS.md")), fi.Size()))
	}
	return notes
}

// collapseHome shortens paths under $HOME to ~/... for display.
func collapseHome(env install.Env, p string) string {
	home, err := env.Home()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return p
}
