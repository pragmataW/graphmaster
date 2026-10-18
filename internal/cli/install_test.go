package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/install"
)

var agentFixtures = filepath.Join("..", "..", "testdata", "configs")

// seedAgentsHome builds a temp HOME holding the realistic configs of all
// four agents (comments, other MCP servers, user hooks, rules).
func seedAgentsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	copyTree(t, filepath.Join(agentFixtures, "claude", "home"), home)
	copyTree(t, filepath.Join(agentFixtures, "cursor", "home"), home)
	copyTree(t, filepath.Join(agentFixtures, "opencode", "home"), home)
	for _, f := range []string{"config.toml", "hooks.json", "AGENTS.md"} {
		copyFile(t, filepath.Join(agentFixtures, "codex", f), filepath.Join(home, ".codex", f))
	}
	return home
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		copyFile(t, p, filepath.Join(dst, rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeGraphmaster creates an executable named graphmaster outside HOME.
func fakeGraphmaster(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin", "graphmaster")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// testSetup wires the CLI to home and bin with every registered adapter.
func testSetup(home, bin string) setup {
	env := install.EnvWithHome(home)
	var tick int
	env.Now = func() time.Time {
		tick++
		return time.Date(2026, 10, 11, 12, 0, 0, tick, time.UTC)
	}
	return setup{
		env:    env,
		binary: func() (install.BinaryPath, error) { return install.BinaryPath{Path: bin, Reason: "test"}, nil },
		agents: adapters.All(),
		dir:    home, // not a git repository
	}
}

// snapshotTree maps every file and directory under root to its content,
// skipping graphmaster's own state directory when skipState is set.
func snapshotTree(t *testing.T, root string, skipState bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	state := filepath.Join(root, ".config", "graphmaster")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipState && p == state {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSameTree(t *testing.T, want, got map[string]string) {
	t.Helper()
	for k, v := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s: missing after round trip", k)
		} else if g != v {
			t.Errorf("%s: content differs:\n--- want\n%s\n--- got\n%s", k, v, g)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: left behind", k)
		}
	}
}

// runCLI executes install, uninstall or doctor against s.
func runCLI(t *testing.T, s setup, args ...string) (string, error) {
	t.Helper()
	get := func() setup { return s }
	root := &cobra.Command{Use: "graphmaster", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(installCmdWith(get), uninstallCmdWith(get), doctorCmdWith(get))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	home := seedAgentsHome(t)
	before := snapshotTree(t, home, false)
	out, err := runCLI(t, testSetup(home, fakeGraphmaster(t)), "install", "--dry-run")
	if err != nil {
		t.Fatalf("install --dry-run: %v\n%s", err, out)
	}
	for _, want := range []string{"dry run", "+++ b", "claude", "codex", "cursor", "opencode", "would create"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out)
		}
	}
	assertSameTree(t, before, snapshotTree(t, home, false))
}

func TestInstallUninstallRoundTrip(t *testing.T) {
	home := seedAgentsHome(t)
	bin := fakeGraphmaster(t)
	s := testSetup(home, bin)
	before := snapshotTree(t, home, true)

	out, err := runCLI(t, s, "install")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	for _, want := range []string{
		"graphmaster binary: " + bin,
		"claude:", "codex:", "cursor:", "opencode:",
		"/hooks", // Codex trust reminder from the adapter's Notes
		"~/.claude/settings.json",
		"created ~/.config/graphmaster/config.toml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("install output lacks %q:\n%s", want, out)
		}
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(string(settings), bin+" hook pre-tool-use --agent claude") {
		t.Fatalf("claude hooks not installed:\n%s", settings)
	}
	agentsMD, _ := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if !strings.Contains(string(agentsMD), "find_symbol") {
		t.Fatalf("codex AGENTS.md lacks the instruction block:\n%s", agentsMD)
	}

	again, err := runCLI(t, s, "install")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if regexp.MustCompile(`(?m)^\s+(create|update)\s`).MatchString(again) {
		t.Fatalf("second install changed files:\n%s", again)
	}

	out, err = runCLI(t, s, "uninstall")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if !strings.Contains(out, "restore") {
		t.Errorf("uninstall output lacks restore actions:\n%s", out)
	}
	assertSameTree(t, before, snapshotTree(t, home, true))
}

func TestInstallSelectedAgents(t *testing.T) {
	home := seedAgentsHome(t)
	cursorBefore := snapshotTree(t, filepath.Join(home, ".cursor"), false)
	out, err := runCLI(t, testSetup(home, fakeGraphmaster(t)), "install", "--agents", "claude,codex")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if strings.Contains(out, "cursor:") || strings.Contains(out, "opencode:") {
		t.Fatalf("unselected agents reported:\n%s", out)
	}
	assertSameTree(t, cursorBefore, snapshotTree(t, filepath.Join(home, ".cursor"), false))

	// Uninstalling one agent leaves the other in place.
	if out, err := runCLI(t, testSetup(home, fakeGraphmaster(t)), "uninstall", "--agents", "codex"); err != nil {
		t.Fatalf("uninstall codex: %v\n%s", err, out)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if strings.Contains(string(cfg), "mcp_servers.graphmaster") {
		t.Fatalf("codex config still registers graphmaster:\n%s", cfg)
	}
	claudeJSON, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !strings.Contains(string(claudeJSON), "graphmaster") {
		t.Fatal("claude install was removed by uninstall --agents codex")
	}
}

func TestInstallErrors(t *testing.T) {
	bin := fakeGraphmaster(t)
	t.Run("unknown agent", func(t *testing.T) {
		_, err := runCLI(t, testSetup(seedAgentsHome(t), bin), "install", "--agents", "vim")
		if err == nil || !strings.Contains(err.Error(), "vim") {
			t.Fatalf("err = %v, want unknown agent", err)
		}
	})
	t.Run("nothing detected", func(t *testing.T) {
		_, err := runCLI(t, testSetup(t.TempDir(), bin), "install")
		if err == nil || !strings.Contains(err.Error(), "--agents") {
			t.Fatalf("err = %v, want a hint about --agents", err)
		}
	})
	t.Run("temporary binary", func(t *testing.T) {
		home := seedAgentsHome(t)
		before := snapshotTree(t, home, false)
		s := testSetup(home, bin)
		s.binary = func() (install.BinaryPath, error) { return install.BinaryPath{}, errors.New("temporary build") }
		if _, err := runCLI(t, s, "install"); err == nil || !strings.Contains(err.Error(), "temporary build") {
			t.Fatalf("err = %v, want binary resolution error", err)
		}
		assertSameTree(t, before, snapshotTree(t, home, false))
	})
	t.Run("corrupt config aborts every agent", func(t *testing.T) {
		home := seedAgentsHome(t)
		if err := os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte("{ broken"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := snapshotTree(t, home, true) // the engine's lock file may appear
		if _, err := runCLI(t, testSetup(home, bin), "install"); err == nil {
			t.Fatal("install succeeded over corrupt JSON")
		}
		assertSameTree(t, before, snapshotTree(t, home, true))
		if fileExists(filepath.Join(home, ".config", "graphmaster", "config.toml")) {
			t.Fatal("default config written although install failed")
		}
	})
}

func TestUninstallNothingInstalled(t *testing.T) {
	out, err := runCLI(t, testSetup(seedAgentsHome(t), fakeGraphmaster(t)), "uninstall")
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !strings.Contains(out, "nothing to uninstall") {
		t.Fatalf("output = %q", out)
	}
}

func TestDefaultUserConfig(t *testing.T) {
	text := defaultUserConfig()
	path := filepath.Join(t.TempDir(), "config.toml")
	load := func(body string) *config.Config {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadWith(config.Options{UserPath: path, LookupEnv: func(string) (string, bool) { return "", false }})
		if err != nil {
			t.Fatalf("load:\n%s\n%v", body, err)
		}
		if len(cfg.Warnings) > 0 {
			t.Fatalf("warnings: %v", cfg.Warnings)
		}
		return cfg
	}
	def := config.Default()
	if cfg := load(text); cfg.Mode != config.ModeBalanced {
		t.Fatalf("mode = %s, want balanced", cfg.Mode)
	}
	// Every commented option is valid and spells out the default.
	uncommented := regexp.MustCompile(`(?m)^# ((?:[a-z_]+ = |\[timeouts\]).*)$`).ReplaceAllString(text, "$1")
	if uncommented == text {
		t.Fatal("no commented options found")
	}
	cfg := load(uncommented)
	if cfg.MaxFileSize != def.MaxFileSize || cfg.SoftBudget != def.SoftBudget || cfg.Timeouts != def.Timeouts ||
		strings.Join(cfg.Excludes, ",") != strings.Join(def.Excludes, ",") ||
		strings.Join(cfg.Languages, ",") != strings.Join(def.Languages, ",") {
		t.Fatalf("uncommented options differ from defaults: %+v", cfg)
	}
}

func TestEnsureUserConfigKeepsExisting(t *testing.T) {
	home := t.TempDir()
	env := install.EnvWithHome(home)
	path := filepath.Join(home, ".config", "graphmaster", "config.toml")
	copyFile(t, filepath.Join(agentFixtures, "codex", "AGENTS.md"), path) // any content
	want, _ := os.ReadFile(path)
	got, created, err := ensureUserConfig(env, false)
	if err != nil || created || got != path {
		t.Fatalf("ensureUserConfig = %q, %v, %v", got, created, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, want) {
		t.Fatal("existing config was modified")
	}
}
