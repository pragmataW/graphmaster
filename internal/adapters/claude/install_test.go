package claude

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/hujson"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

const (
	testBin      = "/opt/homebrew/bin/graphmaster"
	testInstruct = "## graphmaster\n\nUse the graphmaster MCP tools (find_symbol, search, callers) before grep.\n"
)

var fixtureHome = filepath.Join("..", "..", "..", "testdata", "configs", "claude", "home")

// seedHome copies the realistic fixture HOME into a temp dir.
func seedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	err := filepath.WalkDir(fixtureHome, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixtureHome, p)
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
	var tick int
	env.Now = func() time.Time {
		tick++
		return time.Date(2026, 10, 11, 12, 0, 0, tick, time.UTC)
	}
	return install.NewEngine(env)
}

// snapshot maps every path under home (except graphmaster's state dir) to its content.
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

func sameSnapshot(t *testing.T, want, got map[string]string) {
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

func plan(t *testing.T, home string) install.Plan {
	t.Helper()
	p, err := Adapter{}.InstallPlan(install.EnvWithHome(home), adapters.InstallOptions{Binary: testBin, Instructions: testInstruct})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// standardize parses JSONC into a generic value.
func standardize(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	std, err := hujson.Standardize(b)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(std, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

type hookEntry struct {
	Matcher string
	Command string
	Timeout float64
}

// graphmasterHooks returns graphmaster's hook entries per Claude event name.
func graphmasterHooks(t *testing.T, settings map[string]any) map[string][]hookEntry {
	t.Helper()
	out := map[string][]hookEntry{}
	hooks, _ := settings["hooks"].(map[string]any)
	for name, groups := range hooks {
		for _, g := range groups.([]any) {
			gm := g.(map[string]any)
			matcher, _ := gm["matcher"].(string)
			for _, h := range gm["hooks"].([]any) {
				hm := h.(map[string]any)
				cmd, _ := hm["command"].(string)
				if !strings.Contains(cmd, "graphmaster") {
					continue
				}
				if hm["type"] != "command" {
					t.Errorf("%s: hook type %v", name, hm["type"])
				}
				to, _ := hm["timeout"].(float64)
				out[name] = append(out[name], hookEntry{matcher, cmd, to})
			}
		}
	}
	return out
}

func TestInstallPlanShape(t *testing.T) {
	home := "/home/u"
	p := plan(t, home)
	if p.Agent != "claude" {
		t.Fatalf("agent %q", p.Agent)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	paths := map[string]install.Format{}
	for _, f := range p.Files {
		paths[f.Path] = f.Format
	}
	want := map[string]install.Format{
		"/home/u/.claude.json":          install.FormatJSON,
		"/home/u/.claude/settings.json": install.FormatJSON,
		"/home/u/.claude/CLAUDE.md":     install.FormatMarkdown,
	}
	if len(paths) != len(want) {
		t.Fatalf("files %v", paths)
	}
	for k, v := range want {
		if paths[k] != v {
			t.Errorf("%s: format %q, want %q", k, paths[k], v)
		}
	}
}

func TestInstallPlanErrors(t *testing.T) {
	env := install.EnvWithHome("/home/u")
	for _, opts := range []adapters.InstallOptions{
		{Binary: ""},
		{Binary: "graphmaster"},
		{Binary: "./bin/graphmaster"},
	} {
		if _, err := (Adapter{}).InstallPlan(env, opts); err == nil {
			t.Errorf("binary %q: expected error", opts.Binary)
		}
	}
	noHome := install.Env{LookupEnv: func(string) (string, bool) { return "", false }}
	if _, err := (Adapter{}).InstallPlan(noHome, adapters.InstallOptions{Binary: testBin}); err == nil {
		t.Error("expected error without HOME")
	}
}

func TestInstallPlanSkipsEmptyInstructions(t *testing.T) {
	p, err := Adapter{}.InstallPlan(install.EnvWithHome("/h"), adapters.InstallOptions{Binary: testBin, Instructions: "  \n"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Files {
		if f.Format == install.FormatMarkdown {
			t.Fatalf("unexpected instruction file %s", f.Path)
		}
	}
}

func TestInstallPlanHonoursClaudeConfigDir(t *testing.T) {
	env := install.Env{LookupEnv: func(k string) (string, bool) {
		switch k {
		case "HOME":
			return "/home/u", true
		case "CLAUDE_CONFIG_DIR":
			return "/cfg/claude", true
		}
		return "", false
	}}
	p, err := Adapter{}.InstallPlan(env, adapters.InstallOptions{Binary: testBin, Instructions: testInstruct})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range p.Files {
		got = append(got, f.Path)
	}
	want := "/cfg/claude/.claude.json,/cfg/claude/settings.json,/cfg/claude/CLAUDE.md"
	if strings.Join(got, ",") != want {
		t.Fatalf("paths %v, want %s", got, want)
	}
}

func TestInstallAppliesToRealisticHome(t *testing.T) {
	home := seedHome(t)
	e := testEngine(home)
	if _, err := e.Install(install.Options{}, plan(t, home)); err != nil {
		t.Fatal(err)
	}

	// MCP registration next to the user's servers, comments intact.
	cjPath := filepath.Join(home, ".claude.json")
	raw, _ := os.ReadFile(cjPath)
	if !strings.Contains(string(raw), "// Personal MCP servers; graphmaster must not disturb these.") {
		t.Error(".claude.json lost its comment")
	}
	cj := standardize(t, cjPath)
	servers := cj["mcpServers"].(map[string]any)
	for _, other := range []string{"github", "context7"} {
		if _, ok := servers[other]; !ok {
			t.Errorf("mcp server %s removed", other)
		}
	}
	gm, _ := json.Marshal(servers["graphmaster"])
	if string(gm) != `{"args":["mcp"],"command":"`+testBin+`","type":"stdio"}` {
		t.Errorf("graphmaster server = %s", gm)
	}

	// Hooks for every event graphmaster uses, user hooks kept.
	settings := standardize(t, filepath.Join(home, ".claude", "settings.json"))
	got := graphmasterHooks(t, settings)
	want := map[string]hookEntry{
		"SessionStart":  {"", testBin + " hook session-start --agent claude", 600},
		"SubagentStart": {"", testBin + " hook subagent-start --agent claude", 30},
		"PreToolUse":    {"Grep|Glob|Read|Bash", testBin + " hook pre-tool-use --agent claude", 10},
		"PostToolUse":   {"Edit|MultiEdit|Write|NotebookEdit|Bash", testBin + " hook post-tool-use --agent claude", 15},
		"PreCompact":    {"", testBin + " hook pre-compact --agent claude", 30},
	}
	if len(got) != len(want) {
		t.Errorf("hook events %v", got)
	}
	for name, w := range want {
		if len(got[name]) != 1 || got[name][0] != w {
			t.Errorf("%s hooks = %+v, want [%+v]", name, got[name], w)
		}
	}
	userHooks := settings["hooks"].(map[string]any)
	if len(userHooks["PreToolUse"].([]any)) != 2 || len(userHooks["SessionStart"].([]any)) != 2 {
		t.Errorf("user hooks not preserved: %v", userHooks)
	}
	if _, ok := userHooks["Notification"]; !ok {
		t.Error("Notification hooks removed")
	}
	if settings["statusLine"] == nil || settings["model"] != "opus" {
		t.Error("unrelated settings disturbed")
	}

	// Instruction block appended to the user's CLAUDE.md.
	md, _ := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	seed, _ := os.ReadFile(filepath.Join(fixtureHome, ".claude", "CLAUDE.md"))
	if !strings.HasPrefix(string(md), string(seed)) || !strings.Contains(string(md), "<!-- graphmaster:begin v1 -->\n"+testInstruct) {
		t.Errorf("CLAUDE.md:\n%s", md)
	}
}

func TestInstallTwiceIdempotentAndUninstallByteExact(t *testing.T) {
	home := seedHome(t)
	before := snapshot(t, home)
	e := testEngine(home)

	if _, err := e.Install(install.Options{}, plan(t, home)); err != nil {
		t.Fatal(err)
	}
	afterFirst := snapshot(t, home)
	rep, err := e.Install(install.Options{}, plan(t, home))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed() {
		t.Fatalf("second install changed files:\n%s", rep)
	}
	sameSnapshot(t, afterFirst, snapshot(t, home))

	if _, err := e.Uninstall(install.Options{}, "claude"); err != nil {
		t.Fatal(err)
	}
	sameSnapshot(t, before, snapshot(t, home))
}

func TestInstallIntoEmptyHomeAndUninstall(t *testing.T) {
	home := t.TempDir()
	before := snapshot(t, home)
	e := testEngine(home)
	if _, err := e.Install(install.Options{}, plan(t, home)); err != nil {
		t.Fatal(err)
	}
	settings := standardize(t, filepath.Join(home, ".claude", "settings.json"))
	if n := len(graphmasterHooks(t, settings)); n != 5 {
		t.Fatalf("got %d hook events", n)
	}
	if _, err := e.Uninstall(install.Options{}, "claude"); err != nil {
		t.Fatal(err)
	}
	sameSnapshot(t, before, snapshot(t, home))
}

func TestHookCommandQuotesBinaryWithSpaces(t *testing.T) {
	got := hookCommand("/Users/me/My Tools/graphmaster", hookapi.EventPreToolUse)
	if got != `'/Users/me/My Tools/graphmaster' hook pre-tool-use --agent claude` {
		t.Fatalf("got %s", got)
	}
	got = hookCommand("/opt/it's/graphmaster", hookapi.EventStop)
	if got != `'/opt/it'\''s/graphmaster' hook stop --agent claude` {
		t.Fatalf("got %s", got)
	}
}

func TestDetect(t *testing.T) {
	a := Adapter{}
	if a.Detect(install.EnvWithHome(t.TempDir())) {
		t.Error("detected in empty home")
	}
	if !a.Detect(install.EnvWithHome(seedHome(t))) {
		t.Error("not detected with ~/.claude")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.Detect(install.EnvWithHome(home)) {
		t.Error("not detected with ~/.claude.json")
	}

	// Binary on PATH.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := install.Env{LookupEnv: func(k string) (string, bool) {
		switch k {
		case "HOME":
			return t.TempDir(), true
		case "PATH":
			return "/nonexistent" + string(os.PathListSeparator) + bin, true
		}
		return "", false
	}}
	if !a.Detect(env) {
		t.Error("not detected via PATH")
	}
}

func TestNotes(t *testing.T) {
	home := t.TempDir()
	notes := Adapter{}.Notes(install.EnvWithHome(home))
	if len(notes) == 0 {
		t.Fatal("no notes")
	}
	joined := strings.Join(notes, "\n")
	if strings.Contains(joined, "Cursor") {
		t.Error("Cursor note without Cursor installed")
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(Adapter{}.Notes(install.EnvWithHome(home)), "\n")
	if !strings.Contains(joined, "Cursor") {
		t.Error("missing Cursor double-hook note")
	}
}

func TestRegistered(t *testing.T) {
	a, ok := adapters.Get(hookapi.AgentClaude)
	if !ok {
		t.Fatal("claude adapter not registered")
	}
	if a.Name() != hookapi.AgentClaude {
		t.Fatalf("name %q", a.Name())
	}
}

func TestReinstallWithMovedBinaryReplacesHooks(t *testing.T) {
	home := seedHome(t)
	before := snapshot(t, home)
	e := testEngine(home)
	opts := adapters.InstallOptions{Binary: "/usr/local/bin/graphmaster", Instructions: testInstruct}
	for _, bin := range []string{"/usr/local/bin/graphmaster", testBin} {
		opts.Binary = bin
		p, err := Adapter{}.InstallPlan(install.EnvWithHome(home), opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Install(install.Options{}, p); err != nil {
			t.Fatal(err)
		}
	}
	for name, hs := range graphmasterHooks(t, standardize(t, filepath.Join(home, ".claude", "settings.json"))) {
		if len(hs) != 1 || !strings.HasPrefix(hs[0].Command, testBin+" ") {
			t.Errorf("%s hooks = %+v", name, hs)
		}
	}
	if _, err := e.Uninstall(install.Options{}, "claude"); err != nil {
		t.Fatal(err)
	}
	sameSnapshot(t, before, snapshot(t, home))
}
