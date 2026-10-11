package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

// hookEntry is one element of a hooks.json event array. Field order is the
// order written to disk.
type hookEntry struct {
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
	// Timeout is in seconds.
	Timeout int `json:"timeout,omitempty"`
}

// hookSpec binds a Cursor native hook to the graphmaster event it invokes.
type hookSpec struct {
	native  string
	event   hookapi.Event
	matcher string
	timeout int
}

// hookSpecs lists every Cursor hook graphmaster installs, in file order.
//
// preToolUse (Shell|Read|Grep) and beforeShellExecution both see shell
// commands; that is deliberate. When preToolUse denies, the command never
// reaches beforeShellExecution; when it allows (e.g. a retried search), the
// steering core allows the identical command again. Cursor has no
// documented preCompact hook that can add context, so none is installed.
var hookSpecs = []hookSpec{
	{native: "sessionStart", event: hookapi.EventSessionStart, timeout: 10},
	{native: "subagentStart", event: hookapi.EventSubagentStart, timeout: 5},
	{native: "preToolUse", event: hookapi.EventPreToolUse, matcher: "Shell|Read|Grep", timeout: 5},
	{native: "beforeShellExecution", event: hookapi.EventPreToolUse, timeout: 5},
	{native: "afterFileEdit", event: hookapi.EventPostToolUse, timeout: 10},
	{native: "afterShellExecution", event: hookapi.EventPostToolUse, timeout: 10},
}

// hooksFileVersion is the hooks.json schema version Cursor expects.
const (
	hooksFileVersion = 1
	versionPointer   = "/version"
)

// rulesFileName is graphmaster's generated rule inside ~/.cursor/rules.
const rulesFileName = "graphmaster.mdc"

// InstallPlan registers the MCP server, the hooks and (when Cursor's rules
// directory exists) the instruction block.
func (Adapter) InstallPlan(env install.Env, opts adapters.InstallOptions) (install.Plan, error) {
	if opts.Binary == "" || !filepath.IsAbs(opts.Binary) {
		return install.Plan{}, fmt.Errorf("cursor: graphmaster binary path must be absolute, got %q", opts.Binary)
	}
	dir, err := cursorDir(env)
	if err != nil {
		return install.Plan{}, err
	}
	plan := install.Plan{Agent: string(hookapi.AgentCursor)}

	plan.Files = append(plan.Files, install.FileChange{
		Path:   filepath.Join(dir, "mcp.json"),
		Format: install.FormatJSON,
		Ops: []install.Op{install.JSONSet{
			Pointer: install.Pointer("mcpServers", "graphmaster"),
			Value:   mcpServer{Command: opts.Binary, Args: []string{"mcp"}},
		}},
	})

	hooksPath := filepath.Join(dir, "hooks.json")
	var ops []install.Op
	if !hasVersion(hooksPath) || ownsVersion(env, hooksPath) {
		ops = append(ops, install.JSONSet{Pointer: versionPointer, Value: hooksFileVersion})
	}
	bin := shellQuote(opts.Binary)
	for _, s := range hookSpecs {
		ops = append(ops, install.JSONAppend{
			Pointer: install.Pointer("hooks", s.native),
			Value: hookEntry{
				Command: fmt.Sprintf("%s hook %s --agent %s", bin, s.event, hookapi.AgentCursor),
				Matcher: s.matcher,
				Timeout: s.timeout,
			},
		})
	}
	plan.Files = append(plan.Files, install.FileChange{Path: hooksPath, Format: install.FormatJSON, Ops: ops})

	if rules := filepath.Join(dir, "rules"); opts.Instructions != "" && isDir(rules) {
		plan.Files = append(plan.Files, install.FileChange{
			Path:    filepath.Join(rules, rulesFileName),
			Format:  install.FormatGenerated,
			Content: []byte(rulesContent(opts.Instructions)),
		})
	}
	return plan, nil
}

// mcpServer is a stdio entry in ~/.cursor/mcp.json.
type mcpServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// hasVersion reports whether hooks.json already declares a schema version.
// A missing or unreadable file reports false; the install engine refuses
// corrupt JSON on its own.
func hasVersion(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	std, err := install.StandardizeJSON(b)
	if err != nil {
		return false
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(std, &doc); err != nil {
		return false
	}
	_, ok := doc["version"]
	return ok
}

// ownsVersion reports whether an earlier install recorded graphmaster as
// the author of hooks.json's version member. Re-declaring it keeps it on
// reinstall; a user's own version is never claimed, so a surgical uninstall
// cannot remove it.
func ownsVersion(env install.Env, path string) bool {
	statePath, err := env.StatePath()
	if err != nil {
		return false
	}
	st, err := install.LoadState(statePath)
	if err != nil || st.Files[path] == nil {
		return false
	}
	for _, in := range st.Files[path].Insertions {
		if in.Agent == string(hookapi.AgentCursor) && in.Kind == install.KindJSONMember && in.Pointer == versionPointer {
			return true
		}
	}
	return false
}

// rulesContent wraps instructions in an always-applied Cursor rule.
func rulesContent(instructions string) string {
	if !strings.HasSuffix(instructions, "\n") {
		instructions += "\n"
	}
	return "---\n" +
		"description: graphmaster code graph navigation (managed by graphmaster install)\n" +
		"alwaysApply: true\n" +
		"---\n\n" + instructions
}

// shellQuote quotes p for the POSIX shell Cursor runs hook commands with,
// leaving plain paths untouched.
func shellQuote(p string) string {
	safe := true
	for _, r := range p {
		if !shellSafe(r) {
			safe = false
			break
		}
	}
	if safe && p != "" {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// shellSafe reports whether r needs no quoting in a POSIX shell word.
func shellSafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	default:
		return strings.ContainsRune("/._-+:@%,=", r)
	}
}
