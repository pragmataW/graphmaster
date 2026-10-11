package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

const (
	testBin   = "/opt/homebrew/bin/graphmaster"
	testInstr = "## graphmaster\n\nUse the graphmaster MCP tools (find_symbol, search, callers) before grep/rg.\n"
)

var seedFiles = []string{"config.toml", "hooks.json", "AGENTS.md"}

// seededHome copies testdata/configs/codex into a temp HOME's ~/.codex.
func seededHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dst := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range seedFiles {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "configs", "codex", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func snapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range seedFiles {
		b, err := os.ReadFile(filepath.Join(home, ".codex", f))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		out[f] = string(b)
	}
	return out
}

func plan(t *testing.T, env install.Env, opts adapters.InstallOptions) install.Plan {
	t.Helper()
	p, err := Adapter{}.InstallPlan(env, opts)
	if err != nil {
		t.Fatalf("InstallPlan: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("plan invalid: %v", err)
	}
	return p
}

func defaultOpts() adapters.InstallOptions {
	return adapters.InstallOptions{Binary: testBin, Instructions: testInstr}
}

// hooksFile mirrors the hooks.json shape for assertions.
type hooksFile struct {
	Hooks map[string][]struct {
		Matcher *string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func readHooks(t *testing.T, home string) hooksFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	std, err := install.StandardizeJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	var h hooksFile
	if err := json.Unmarshal(std, &h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestInstallPlanShape(t *testing.T) {
	home := t.TempDir()
	p := plan(t, install.EnvWithHome(home), defaultOpts())
	if p.Agent != "codex" {
		t.Fatalf("agent = %q", p.Agent)
	}
	var paths []string
	for _, f := range p.Files {
		paths = append(paths, f.Path)
	}
	want := []string{
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(home, ".codex", "hooks.json"),
		filepath.Join(home, ".codex", "AGENTS.md"),
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
}

func TestInstallPlanErrors(t *testing.T) {
	env := install.EnvWithHome(t.TempDir())
	for name, opts := range map[string]adapters.InstallOptions{
		"empty binary":      {Binary: "", Instructions: testInstr},
		"relative binary":   {Binary: "bin/graphmaster", Instructions: testInstr},
		"newline binary":    {Binary: "/opt/x\ny", Instructions: testInstr},
		"huge instructions": {Binary: testBin, Instructions: strings.Repeat("x", maxInstructionBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (Adapter{}).InstallPlan(env, opts); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := (Adapter{}).InstallPlan(install.Env{LookupEnv: func(string) (string, bool) { return "", false }}, defaultOpts()); err == nil {
		t.Fatal("want error without HOME")
	}
}

func TestInstallPlanSkipsEmptyInstructions(t *testing.T) {
	home := t.TempDir()
	p := plan(t, install.EnvWithHome(home), adapters.InstallOptions{Binary: testBin})
	for _, f := range p.Files {
		if filepath.Base(f.Path) == "AGENTS.md" {
			t.Fatal("AGENTS.md planned without instructions")
		}
	}
}

func TestInstallRoundTrip(t *testing.T) {
	home := seededHome(t)
	env := install.EnvWithHome(home)
	before := snapshot(t, home)
	eng := install.NewEngine(env)
	p := plan(t, env, defaultOpts())

	rep, err := eng.Install(install.Options{}, p)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !rep.Changed() {
		t.Fatal("first install changed nothing")
	}
	after := snapshot(t, home)

	// config.toml: valid TOML, graphmaster registered, other servers intact.
	var cfg struct {
		Model      string `toml:"model"`
		MCPServers map[string]struct {
			Command string   `toml:"command"`
			Args    []string `toml:"args"`
		} `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal([]byte(after["config.toml"]), &cfg); err != nil {
		t.Fatalf("config.toml invalid after install: %v\n%s", err, after["config.toml"])
	}
	gm := cfg.MCPServers["graphmaster"]
	if gm.Command != testBin || !slices.Equal(gm.Args, []string{"mcp"}) {
		t.Fatalf("graphmaster server = %+v", gm)
	}
	if cfg.MCPServers["context7"].Command != "npx" || cfg.MCPServers["github"].Command != "docker" || cfg.Model != "gpt-5-codex" {
		t.Fatalf("existing config damaged: %+v", cfg)
	}
	if !strings.HasPrefix(after["config.toml"], before["config.toml"]) {
		t.Fatal("existing config.toml text was not preserved as a prefix")
	}
	if !strings.Contains(after["config.toml"], "# graphmaster:begin v1") {
		t.Fatal("missing TOML marker")
	}

	// hooks.json: user hooks intact, graphmaster hooks added per event.
	h := readHooks(t, home)
	if got := h.Hooks["Stop"]; len(got) != 1 || got[0].Hooks[0].Command != "~/bin/ding.sh" {
		t.Fatalf("Stop hooks damaged: %+v", got)
	}
	wantCmd := map[string]string{
		"SessionStart":  testBin + " hook session-start --agent codex",
		"SubagentStart": testBin + " hook subagent-start --agent codex",
		"PreToolUse":    testBin + " hook pre-tool-use --agent codex",
		"PostToolUse":   testBin + " hook post-tool-use --agent codex",
	}
	for event, cmd := range wantCmd {
		var found bool
		for _, g := range h.Hooks[event] {
			for _, hk := range g.Hooks {
				if hk.Command == cmd {
					found = true
					if hk.Type != "command" || hk.Timeout <= 0 {
						t.Errorf("%s: bad hook %+v", event, hk)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s: graphmaster hook %q missing", event, cmd)
		}
	}
	if len(h.Hooks["PreToolUse"]) != 2 || h.Hooks["PreToolUse"][0].Hooks[0].Command != "python3 ~/.codex/hooks/guard_rm.py" {
		t.Fatalf("PreToolUse user hook damaged: %+v", h.Hooks["PreToolUse"])
	}
	if _, ok := h.Hooks["PreCompact"]; ok {
		t.Error("PreCompact is not a Codex event and must not be installed")
	}
	if !strings.Contains(after["hooks.json"], `"command": "~/bin/notify-start.sh"`) {
		t.Error("user hook formatting lost")
	}

	// AGENTS.md: block appended after user rules.
	if !strings.HasPrefix(after["AGENTS.md"], before["AGENTS.md"]) || !strings.Contains(after["AGENTS.md"], testInstr) {
		t.Fatalf("AGENTS.md = %q", after["AGENTS.md"])
	}
	if !strings.Contains(after["AGENTS.md"], "<!-- graphmaster:begin v1 -->") {
		t.Fatal("missing markdown marker")
	}

	// Second install: idempotent, byte-identical.
	rep2, err := eng.Install(install.Options{}, plan(t, env, defaultOpts()))
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if rep2.Changed() {
		t.Fatalf("second install changed files:\n%s", rep2)
	}
	if got := snapshot(t, home); !mapsEqual(got, after) {
		t.Fatal("second install modified bytes")
	}

	// Uninstall restores every file byte-for-byte.
	if _, err := eng.Uninstall(install.Options{}, "codex"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := snapshot(t, home); !mapsEqual(got, before) {
		for k := range before {
			if got[k] != before[k] {
				t.Errorf("%s not restored:\n got  %q\n want %q", k, got[k], before[k])
			}
		}
	}
}

func TestInstallFreshHomeAndUninstallRemovesCreatedFiles(t *testing.T) {
	home := t.TempDir()
	env := install.EnvWithHome(home)
	eng := install.NewEngine(env)
	if _, err := eng.Install(install.Options{}, plan(t, env, defaultOpts())); err != nil {
		t.Fatalf("install: %v", err)
	}
	h := readHooks(t, home)
	if len(h.Hooks["PreToolUse"]) != 1 || *h.Hooks["PreToolUse"][0].Matcher != "Bash" {
		t.Fatalf("PreToolUse = %+v", h.Hooks["PreToolUse"])
	}
	if m := h.Hooks["SessionStart"][0].Matcher; m != nil {
		t.Fatalf("SessionStart matcher should be omitted, got %q", *m)
	}
	cfg, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := toml.Unmarshal(cfg, &v); err != nil {
		t.Fatalf("invalid TOML: %v\n%s", err, cfg)
	}
	if _, err := eng.Uninstall(install.Options{}, "codex"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := snapshot(t, home); len(got) != 0 {
		t.Fatalf("files left after uninstall: %v", got)
	}
}

func TestInstallBinaryChangeReplacesHooks(t *testing.T) {
	home := seededHome(t)
	env := install.EnvWithHome(home)
	eng := install.NewEngine(env)
	if _, err := eng.Install(install.Options{}, plan(t, env, defaultOpts())); err != nil {
		t.Fatal(err)
	}
	newBin := "/usr/local/bin/graph master" // space forces quoting
	if _, err := eng.Install(install.Options{}, plan(t, env, adapters.InstallOptions{Binary: newBin, Instructions: testInstr})); err != nil {
		t.Fatal(err)
	}
	h := readHooks(t, home)
	for _, g := range h.Hooks["PreToolUse"] {
		for _, hk := range g.Hooks {
			if strings.Contains(hk.Command, testBin) {
				t.Fatalf("stale hook kept: %q", hk.Command)
			}
		}
	}
	want := "'" + newBin + "' hook pre-tool-use --agent codex"
	if len(h.Hooks["PreToolUse"]) != 2 || h.Hooks["PreToolUse"][1].Hooks[0].Command != want {
		t.Fatalf("PreToolUse = %+v", h.Hooks["PreToolUse"])
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string `toml:"command"`
		} `toml:"mcp_servers"`
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err := toml.Unmarshal(b, &cfg); err != nil || cfg.MCPServers["graphmaster"].Command != newBin {
		t.Fatalf("mcp command = %+v err %v", cfg, err)
	}
}

func TestInstallUserEditThenSurgicalUninstall(t *testing.T) {
	home := seededHome(t)
	env := install.EnvWithHome(home)
	eng := install.NewEngine(env)
	if _, err := eng.Install(install.Options{}, plan(t, env, defaultOpts())); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	b, _ := os.ReadFile(cfgPath)
	edited := strings.Replace(string(b), `model = "gpt-5-codex"`, `model = "gpt-5.1-codex"`, 1)
	if err := os.WriteFile(cfgPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Uninstall(install.Options{}, "codex"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfgPath)
	want := strings.Replace(snapshotSeed(t, "config.toml"), `model = "gpt-5-codex"`, `model = "gpt-5.1-codex"`, 1)
	if string(got) != want {
		t.Fatalf("surgical uninstall:\n got  %q\n want %q", got, want)
	}
}

func TestInstallHonorsCodexHome(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-alt")
	env := install.Env{LookupEnv: func(k string) (string, bool) {
		switch k {
		case "HOME":
			return home, true
		case "CODEX_HOME":
			return codexHome, true
		}
		return "", false
	}}
	p := plan(t, env, defaultOpts())
	for _, f := range p.Files {
		if filepath.Dir(f.Path) != codexHome {
			t.Fatalf("%s not under CODEX_HOME", f.Path)
		}
	}
}

func TestHookCommandQuoting(t *testing.T) {
	tests := map[string]string{
		"/usr/bin/graphmaster":     "/usr/bin/graphmaster hook pre-tool-use --agent codex",
		"/a b/graphmaster":         "'/a b/graphmaster' hook pre-tool-use --agent codex",
		"/it's/graphmaster":        `'/it'\''s/graphmaster' hook pre-tool-use --agent codex`,
		"/x/$HOME/graphmaster":     "'/x/$HOME/graphmaster' hook pre-tool-use --agent codex",
		`C:\Tools\graphmaster.exe`: `'C:\Tools\graphmaster.exe' hook pre-tool-use --agent codex`,
	}
	for bin, want := range tests {
		if got := hookCommand(bin, hookapi.EventPreToolUse); got != want {
			t.Errorf("hookCommand(%q) = %q, want %q", bin, got, want)
		}
	}
}

func TestMCPBlockEscapesBinary(t *testing.T) {
	for _, bin := range []string{`/opt/a "q"/gm`, `C:\Program Files\gm.exe`, "/opt/\u00e9/gm"} {
		body := mcpBlock(bin)
		var v struct {
			MCPServers map[string]struct {
				Command string   `toml:"command"`
				Args    []string `toml:"args"`
			} `toml:"mcp_servers"`
		}
		if err := toml.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("%q: %v\n%s", bin, err, body)
		}
		if v.MCPServers["graphmaster"].Command != bin {
			t.Fatalf("round trip %q -> %q", bin, v.MCPServers["graphmaster"].Command)
		}
	}
}

func snapshotSeed(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "configs", "codex", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
