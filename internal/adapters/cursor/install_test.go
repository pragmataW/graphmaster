package cursor

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

const (
	testBin   = "/usr/local/bin/graphmaster"
	testInstr = "## graphmaster\n\nUse the graphmaster MCP tools (find_symbol, search) before grep.\n"
)

var seedDir = filepath.Join("..", "..", "..", "testdata", "configs", "cursor", "home")

// seededHome copies the realistic fixture HOME into a temp dir.
func seededHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	err := filepath.WalkDir(seedDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(seedDir, p)
		dst := filepath.Join(home, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func testEngine(home string) *install.Engine {
	env := install.EnvWithHome(home)
	var tick int64
	env.Now = func() time.Time {
		tick++
		return time.Date(2026, 10, 11, 12, 0, 0, int(tick), time.UTC)
	}
	return install.NewEngine(env)
}

// snapshot captures every file and directory under home except
// graphmaster's own state directory.
func snapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	out := map[string]string{}
	skip := filepath.Join(home, ".config", "graphmaster")
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == skip {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(home, p)
		if rel == ".config" {
			return nil
		}
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshots(t *testing.T, want, got map[string]string) {
	t.Helper()
	for k, v := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("missing %s", k)
		} else if g != v {
			t.Errorf("%s differs:\n--- want\n%s\n--- got\n%s", k, v, g)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected %s", k)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// decodeJSONC parses a JSONC file into a generic map.
func decodeJSONC(t *testing.T, p string) map[string]any {
	t.Helper()
	std, err := install.StandardizeJSON([]byte(readFile(t, p)))
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	var m map[string]any
	if err := json.Unmarshal(std, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func plan(t *testing.T, home string, opts adapters.InstallOptions) install.Plan {
	t.Helper()
	p, err := Adapter{}.InstallPlan(install.EnvWithHome(home), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRegistered(t *testing.T) {
	a, ok := adapters.Get(hookapi.AgentCursor)
	if !ok || a.Name() != hookapi.AgentCursor {
		t.Fatalf("cursor adapter not registered: %v %v", a, ok)
	}
}

func TestInstallPlanShape(t *testing.T) {
	home := seededHome(t)
	p := plan(t, home, adapters.InstallOptions{Binary: testBin, Instructions: testInstr})
	if p.Agent != "cursor" {
		t.Fatalf("agent = %q", p.Agent)
	}
	files := map[string]install.FileChange{}
	for _, f := range p.Files {
		files[f.Path] = f
	}
	mcp, ok := files[filepath.Join(home, ".cursor", "mcp.json")]
	if !ok || mcp.Format != install.FormatJSON || len(mcp.Ops) != 1 {
		t.Fatalf("mcp change = %+v", mcp)
	}
	set := mcp.Ops[0].(install.JSONSet)
	if set.Pointer != "/mcpServers/graphmaster" {
		t.Fatalf("pointer = %s", set.Pointer)
	}
	hooks := files[filepath.Join(home, ".cursor", "hooks.json")]
	got := map[string][]string{}
	for _, op := range hooks.Ops {
		if a, ok := op.(install.JSONAppend); ok {
			b, _ := json.Marshal(a.Value)
			got[a.Pointer] = append(got[a.Pointer], string(b))
		} else {
			t.Errorf("unexpected op %T (seed already has version)", op)
		}
	}
	want := map[string]string{
		"/hooks/sessionStart":         `{"command":"/usr/local/bin/graphmaster hook session-start --agent cursor","timeout":10}`,
		"/hooks/subagentStart":        `{"command":"/usr/local/bin/graphmaster hook subagent-start --agent cursor","timeout":5}`,
		"/hooks/preToolUse":           `{"command":"/usr/local/bin/graphmaster hook pre-tool-use --agent cursor","matcher":"Shell|Read|Grep","timeout":5}`,
		"/hooks/beforeShellExecution": `{"command":"/usr/local/bin/graphmaster hook pre-tool-use --agent cursor","timeout":5}`,
		"/hooks/afterFileEdit":        `{"command":"/usr/local/bin/graphmaster hook post-tool-use --agent cursor","timeout":10}`,
		"/hooks/afterShellExecution":  `{"command":"/usr/local/bin/graphmaster hook post-tool-use --agent cursor","timeout":10}`,
	}
	if len(got) != len(want) {
		t.Errorf("hook events = %v", got)
	}
	for ptr, w := range want {
		if len(got[ptr]) != 1 || got[ptr][0] != w {
			t.Errorf("%s = %v, want %s", ptr, got[ptr], w)
		}
	}
	rules, ok := files[filepath.Join(home, ".cursor", "rules", "graphmaster.mdc")]
	if !ok || rules.Format != install.FormatGenerated {
		t.Fatalf("rules file missing: %+v", rules)
	}
	body := string(rules.Content)
	if !strings.HasPrefix(body, "---\n") || !strings.Contains(body, "alwaysApply: true") || !strings.HasSuffix(body, testInstr) {
		t.Errorf("rules content:\n%s", body)
	}
}

func TestInstallPlanFreshHome(t *testing.T) {
	home := t.TempDir()
	p := plan(t, home, adapters.InstallOptions{Binary: testBin, Instructions: testInstr})
	var hooks install.FileChange
	for _, f := range p.Files {
		if strings.HasSuffix(f.Path, "rules/graphmaster.mdc") {
			t.Error("rules file must only be written when ~/.cursor/rules exists")
		}
		if strings.HasSuffix(f.Path, "hooks.json") {
			hooks = f
		}
	}
	first, ok := hooks.Ops[0].(install.JSONSet)
	if !ok || first.Pointer != "/version" {
		t.Fatalf("fresh hooks.json must set version first: %+v", hooks.Ops)
	}
}

func TestInstallPlanQuotesBinary(t *testing.T) {
	home := t.TempDir()
	p := plan(t, home, adapters.InstallOptions{Binary: "/Users/Jane Doe/bin/graphmaster"})
	for _, f := range p.Files {
		for _, op := range f.Ops {
			if a, ok := op.(install.JSONAppend); ok {
				cmd := a.Value.(hookEntry).Command
				if !strings.HasPrefix(cmd, `'/Users/Jane Doe/bin/graphmaster' hook `) {
					t.Fatalf("unquoted command %q", cmd)
				}
			}
			if s, ok := op.(install.JSONSet); ok && s.Pointer == "/mcpServers/graphmaster" {
				b, _ := json.Marshal(s.Value)
				if !strings.Contains(string(b), `"command":"/Users/Jane Doe/bin/graphmaster"`) {
					t.Fatalf("mcp command must be the raw path: %s", b)
				}
			}
		}
	}
}

func TestInstallPlanErrors(t *testing.T) {
	for _, bin := range []string{"", "graphmaster", "./bin/graphmaster"} {
		if _, err := (Adapter{}).InstallPlan(install.EnvWithHome(t.TempDir()), adapters.InstallOptions{Binary: bin}); err == nil {
			t.Errorf("binary %q should be rejected", bin)
		}
	}
	if _, err := (Adapter{}).InstallPlan(install.EnvWithHome(""), adapters.InstallOptions{Binary: testBin}); err == nil {
		t.Error("missing HOME should fail")
	}
}

func TestInstallRoundTripSeeded(t *testing.T) {
	home := seededHome(t)
	before := snapshot(t, home)
	e := testEngine(home)
	opts := adapters.InstallOptions{Binary: testBin, Instructions: testInstr}
	if _, err := e.Install(install.Options{}, plan(t, home, opts)); err != nil {
		t.Fatal(err)
	}

	// Existing user config survives next to graphmaster's entries.
	mcpText := readFile(t, filepath.Join(home, ".cursor", "mcp.json"))
	if !strings.Contains(mcpText, "// Personal MCP servers") || !strings.Contains(mcpText, "// remote server") {
		t.Errorf("mcp.json comments lost:\n%s", mcpText)
	}
	servers := decodeJSONC(t, filepath.Join(home, ".cursor", "mcp.json"))["mcpServers"].(map[string]any)
	gm, _ := servers["graphmaster"].(map[string]any)
	if servers["github"] == nil || servers["context7"] == nil || gm["command"] != testBin {
		t.Errorf("mcpServers = %v", servers)
	}
	if args, _ := gm["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Errorf("args = %v", gm["args"])
	}
	hooksText := readFile(t, filepath.Join(home, ".cursor", "hooks.json"))
	if !strings.Contains(hooksText, "// audit every shell command") {
		t.Errorf("hooks.json comment lost:\n%s", hooksText)
	}
	hooks := decodeJSONC(t, filepath.Join(home, ".cursor", "hooks.json"))
	if hooks["version"] != float64(1) {
		t.Errorf("version = %v", hooks["version"])
	}
	h := hooks["hooks"].(map[string]any)
	if n := len(h["beforeShellExecution"].([]any)); n != 2 {
		t.Errorf("beforeShellExecution has %d entries, want user's + ours", n)
	}
	if n := len(h["stop"].([]any)); n != 1 {
		t.Errorf("stop hooks touched: %d", n)
	}
	for _, ev := range []string{"sessionStart", "subagentStart", "preToolUse", "afterFileEdit", "afterShellExecution"} {
		if h[ev] == nil {
			t.Errorf("missing %s", ev)
		}
	}
	if got := readFile(t, filepath.Join(home, ".cursor", "rules", "graphmaster.mdc")); !strings.Contains(got, testInstr) {
		t.Errorf("rules file:\n%s", got)
	}

	// Second install is a no-op.
	snap := snapshot(t, home)
	rep, err := e.Install(install.Options{}, plan(t, home, opts))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed() {
		t.Fatalf("second install changed files:\n%s", rep)
	}
	sameSnapshots(t, snap, snapshot(t, home))

	// A moved binary replaces entries instead of accumulating them.
	moved := adapters.InstallOptions{Binary: "/opt/homebrew/bin/graphmaster", Instructions: testInstr}
	if _, err := e.Install(install.Options{}, plan(t, home, moved)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(home, ".cursor", "hooks.json")); strings.Contains(got, testBin) {
		t.Fatalf("stale hook commands left behind:\n%s", got)
	}

	if _, err := e.Uninstall(install.Options{}, "cursor"); err != nil {
		t.Fatal(err)
	}
	sameSnapshots(t, before, snapshot(t, home))
}

func TestInstallRoundTripFresh(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, home)
	e := testEngine(home)
	opts := adapters.InstallOptions{Binary: testBin, Instructions: testInstr}
	if _, err := e.Install(install.Options{}, plan(t, home, opts)); err != nil {
		t.Fatal(err)
	}
	hooks := decodeJSONC(t, filepath.Join(home, ".cursor", "hooks.json"))
	if hooks["version"] != float64(1) {
		t.Fatalf("hooks.json version = %v", hooks["version"])
	}
	if !strings.HasPrefix(readFile(t, filepath.Join(home, ".cursor", "hooks.json")), "{\n  \"version\": 1,") {
		t.Errorf("version should lead the fresh hooks.json")
	}
	rep, err := e.Install(install.Options{}, plan(t, home, opts))
	if err != nil || rep.Changed() {
		t.Fatalf("second install: %v\n%s", err, rep)
	}
	if _, err := e.Uninstall(install.Options{}); err != nil {
		t.Fatal(err)
	}
	sameSnapshots(t, before, snapshot(t, home))
}

// TestUninstallAfterUserEditKeepsUserConfig: once the user edits hooks.json
// after install, uninstall removes only graphmaster's entries and keeps the
// user's own version member and new hook.
func TestUninstallAfterUserEditKeepsUserConfig(t *testing.T) {
	home := seededHome(t)
	e := testEngine(home)
	if _, err := e.Install(install.Options{}, plan(t, home, adapters.InstallOptions{Binary: testBin})); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(home, ".cursor", "hooks.json")
	edited := strings.Replace(readFile(t, hooksPath), `"stop": [`, `"stop": [
      { "command": "notify-send done" },`, 1)
	if err := os.WriteFile(hooksPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Uninstall(install.Options{}, "cursor"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, hooksPath)
	if strings.Contains(got, "graphmaster") || !strings.Contains(got, "notify-send done") {
		t.Fatalf("surgical uninstall:\n%s", got)
	}
	h := decodeJSONC(t, hooksPath)
	if h["version"] != float64(1) {
		t.Fatalf("user's version member removed:\n%s", got)
	}
	if n := len(h["hooks"].(map[string]any)["beforeShellExecution"].([]any)); n != 1 {
		t.Fatalf("beforeShellExecution entries = %d", n)
	}
}

func TestDetect(t *testing.T) {
	home := t.TempDir()
	a := Adapter{}
	if a.Detect(install.EnvWithHome(home)) {
		t.Fatal("empty HOME must not detect Cursor")
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.Detect(install.EnvWithHome(home)) {
		t.Fatal("~/.cursor should detect Cursor")
	}

	// A cursor binary on PATH also counts.
	home2, bin := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "cursor"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := install.Env{LookupEnv: func(k string) (string, bool) {
		switch k {
		case "HOME":
			return home2, true
		case "PATH":
			return bin, true
		}
		return "", false
	}}
	if !a.Detect(env) {
		t.Fatal("cursor on PATH should detect Cursor")
	}
}

func TestNotes(t *testing.T) {
	home := t.TempDir()
	notes := strings.Join(Adapter{}.Notes(install.EnvWithHome(home)), "\n")
	for _, want := range []string{"~/.claude/settings.json", "User Rules", "MCP"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
	seeded := strings.Join(Adapter{}.Notes(install.EnvWithHome(seededHome(t))), "\n")
	if strings.Contains(seeded, "User Rules") {
		t.Errorf("rules hint is only needed without ~/.cursor/rules:\n%s", seeded)
	}
}
