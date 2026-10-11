package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

// hookSpec is one Claude Code hook graphmaster installs.
type hookSpec struct {
	claudeEvent string        // settings.json key
	event       hookapi.Event // `graphmaster hook <event>`
	matcher     string        // tool matcher; "" omits the field
	timeout     int           // seconds
}

// hookSpecs lists every hook, in install order. Timeouts leave headroom over
// the dispatcher's own budgets: SessionStart may build the index (590s lock
// budget); PreToolUse waits at most 1.5s for the write lock; PostToolUse
// reindexes edited files or runs a full Freshen after Bash.
var hookSpecs = []hookSpec{
	{"SessionStart", hookapi.EventSessionStart, "", 600},
	{"SubagentStart", hookapi.EventSubagentStart, "", 30},
	{"PreToolUse", hookapi.EventPreToolUse, "Grep|Glob|Read|Bash", 10},
	{"PostToolUse", hookapi.EventPostToolUse, "Edit|MultiEdit|Write|NotebookEdit|Bash", 15},
	{"PreCompact", hookapi.EventPreCompact, "", 30},
}

// paths are Claude Code's user-level config locations.
type paths struct {
	configDir    string // ~/.claude or $CLAUDE_CONFIG_DIR
	globalConfig string // ~/.claude.json (MCP servers)
	settings     string // ~/.claude/settings.json (hooks)
	instructions string // ~/.claude/CLAUDE.md
}

func resolvePaths(env install.Env) (paths, error) {
	if dir, ok := lookup(env, "CLAUDE_CONFIG_DIR"); ok && filepath.IsAbs(dir) {
		return paths{
			configDir:    dir,
			globalConfig: filepath.Join(dir, ".claude.json"),
			settings:     filepath.Join(dir, "settings.json"),
			instructions: filepath.Join(dir, "CLAUDE.md"),
		}, nil
	}
	home, err := env.Home()
	if err != nil {
		return paths{}, err
	}
	dir := filepath.Join(home, ".claude")
	return paths{
		configDir:    dir,
		globalConfig: filepath.Join(home, ".claude.json"),
		settings:     filepath.Join(dir, "settings.json"),
		instructions: filepath.Join(dir, "CLAUDE.md"),
	}, nil
}

// InstallPlan registers the MCP server in ~/.claude.json, adds graphmaster's
// hooks to ~/.claude/settings.json and the instruction block to
// ~/.claude/CLAUDE.md. CLAUDE_CONFIG_DIR relocates all three.
func (Adapter) InstallPlan(env install.Env, opts adapters.InstallOptions) (install.Plan, error) {
	if opts.Binary == "" || !filepath.IsAbs(opts.Binary) {
		return install.Plan{}, fmt.Errorf("claude: binary path must be absolute, got %q", opts.Binary)
	}
	p, err := resolvePaths(env)
	if err != nil {
		return install.Plan{}, fmt.Errorf("claude: %w", err)
	}

	mcp := install.FileChange{Path: p.globalConfig, Format: install.FormatJSON, Ops: []install.Op{
		install.JSONSet{
			Pointer: install.Pointer("mcpServers", "graphmaster"),
			Value:   mcpServer{Type: "stdio", Command: opts.Binary, Args: []string{"mcp"}},
		},
	}}

	hooks := install.FileChange{Path: p.settings, Format: install.FormatJSON}
	for _, h := range hookSpecs {
		hooks.Ops = append(hooks.Ops, install.JSONAppend{
			Pointer: install.Pointer("hooks", h.claudeEvent),
			Value: hookGroup{Matcher: h.matcher, Hooks: []hookCmd{{
				Type: "command", Command: hookCommand(opts.Binary, h.event), Timeout: h.timeout,
			}}},
		})
	}

	plan := install.Plan{Agent: string(hookapi.AgentClaude), Files: []install.FileChange{mcp, hooks}}
	if strings.TrimSpace(opts.Instructions) != "" {
		plan.Files = append(plan.Files, install.FileChange{
			Path: p.instructions, Format: install.FormatMarkdown,
			Ops: []install.Op{install.Block{Body: opts.Instructions}},
		})
	}
	return plan, plan.Validate()
}

type mcpServer struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type hookGroup struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []hookCmd `json:"hooks"`
}

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// hookCommand is the shell command Claude Code runs for event.
func hookCommand(bin string, event hookapi.Event) string {
	return shellQuote(bin) + " hook " + string(event) + " --agent " + string(hookapi.AgentClaude)
}

// shellQuote single-quotes s when it contains characters a POSIX shell
// would interpret.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Detect reports whether Claude Code appears installed: its config
// directory or global config exists, or `claude` is on PATH.
func (Adapter) Detect(env install.Env) bool {
	if p, err := resolvePaths(env); err == nil {
		if isDir(p.configDir) || isFile(p.globalConfig) {
			return true
		}
	}
	return onPath(env, "claude")
}

// Notes returns post-install reminders.
func (Adapter) Notes(env install.Env) []string {
	notes := []string{
		"Restart Claude Code (or open /hooks) so it loads the new hooks and the graphmaster MCP server; `claude mcp list` should show graphmaster.",
		"Claude Code asks before the first use of each graphmaster MCP tool; allow \"mcp__graphmaster\" in /permissions to skip the prompts.",
	}
	if home, err := env.Home(); err == nil && isDir(filepath.Join(home, ".cursor")) {
		notes = append(notes, "Cursor also runs the hooks in ~/.claude/settings.json; graphmaster detects Cursor payloads and stays silent when Cursor's own graphmaster hooks are installed.")
	}
	return notes
}

func lookup(env install.Env, key string) (string, bool) {
	if env.LookupEnv != nil {
		return env.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

func onPath(env install.Env, name string) bool {
	pathVar, _ := lookup(env, "PATH")
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, name))
		if err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return true
		}
	}
	return false
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
