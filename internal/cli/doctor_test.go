package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/install"
)

// failLines returns doctor's FAIL lines.
func failLines(out string) []string {
	return regexp.MustCompile(`(?m)^\s*FAIL\s.*$`).FindAllString(out, -1)
}

func installAll(t *testing.T, s setup) {
	t.Helper()
	if out, err := runCLI(t, s, "install"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
}

func TestDoctorAfterInstallIsGreen(t *testing.T) {
	home := seedAgentsHome(t)
	bin := fakeGraphmaster(t)
	s := testSetup(home, bin)
	before := snapshotTree(t, home, true)
	installAll(t, s)

	out, err := runCLI(t, s, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if f := failLines(out); len(f) > 0 {
		t.Fatalf("doctor reported failures %q:\n%s", f, out)
	}
	for _, want := range []string{
		"claude", "codex", "cursor", "opencode",
		"MCP server registered in ~/.claude.json",
		"hooks installed in ~/.claude/settings.json -> " + bin,
		"instructions in ~/.codex/AGENTS.md",
		"hooks installed in ~/.config/opencode/plugins/graphmaster.js",
		"/hooks",         // Codex trust is not recorded in the fixture
		"dedupe",         // Cursor also loads Claude hooks
		"config.toml",    // user config
		"git repository", // dir is not a repository
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}

	if out, err := runCLI(t, s, "uninstall"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	assertSameTree(t, before, snapshotTree(t, home, true))
}

func TestDoctorNothingInstalled(t *testing.T) {
	out, err := runCLI(t, testSetup(seedAgentsHome(t), fakeGraphmaster(t)), "doctor")
	if !errors.Is(err, errDoctorProblems) {
		t.Fatalf("err = %v, want errDoctorProblems\n%s", err, out)
	}
	if !strings.Contains(out, "graphmaster install") || !strings.Contains(out, "not installed") {
		t.Fatalf("doctor output lacks install hint:\n%s", out)
	}
}

func TestDoctorMissingBinary(t *testing.T) {
	home := seedAgentsHome(t)
	oldBin := fakeGraphmaster(t)
	installAll(t, testSetup(home, oldBin))
	if err := os.Remove(oldBin); err != nil {
		t.Fatal(err)
	}
	// The new graphmaster lives elsewhere; configs still point at the old one.
	out, err := runCLI(t, testSetup(home, fakeGraphmaster(t)), "doctor")
	if !errors.Is(err, errDoctorProblems) {
		t.Fatalf("err = %v, want errDoctorProblems\n%s", err, out)
	}
	if !strings.Contains(out, "missing binary "+oldBin) {
		t.Fatalf("doctor does not name the missing binary:\n%s", out)
	}
}

func TestDoctorMovedBinaryWarns(t *testing.T) {
	home := seedAgentsHome(t)
	oldBin := fakeGraphmaster(t)
	installAll(t, testSetup(home, oldBin))
	newBin := fakeGraphmaster(t)
	out, err := runCLI(t, testSetup(home, newBin), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN") || !strings.Contains(out, oldBin) || !strings.Contains(out, "re-run graphmaster install") {
		t.Fatalf("doctor does not warn about the old binary:\n%s", out)
	}
}

func TestDoctorRemovedHooksFail(t *testing.T) {
	home := seedAgentsHome(t)
	s := testSetup(home, fakeGraphmaster(t))
	installAll(t, s)
	if err := os.Remove(filepath.Join(home, ".cursor", "hooks.json")); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, s, "doctor")
	if !errors.Is(err, errDoctorProblems) {
		t.Fatalf("err = %v, want errDoctorProblems\n%s", err, out)
	}
	if f := failLines(out); len(f) != 1 || !strings.Contains(f[0], "hooks") || !strings.Contains(f[0], "~/.cursor/hooks.json") {
		t.Fatalf("FAIL lines = %q, want one about cursor hooks\n%s", f, out)
	}
}

func TestDoctorCorruptConfigFails(t *testing.T) {
	home := seedAgentsHome(t)
	s := testSetup(home, fakeGraphmaster(t))
	installAll(t, s)
	cfg := filepath.Join(home, ".config", "graphmaster", "config.toml")
	if err := os.WriteFile(cfg, []byte("mode = \"loud\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, s, "doctor")
	if !errors.Is(err, errDoctorProblems) || !strings.Contains(out, "loud") {
		t.Fatalf("err = %v, want config failure\n%s", err, out)
	}
}

// trustCodexHooks records a Codex trust entry for every graphmaster group
// in hooks.json, the way Codex's /hooks approval does, and returns the
// number of entries.
func trustCodexHooks(t *testing.T, home string, limit int) int {
	t.Helper()
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	std, err := install.StandardizeJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(std, &doc); err != nil {
		t.Fatal(err)
	}
	var tables strings.Builder
	n := 0
	for event, groups := range doc.Hooks {
		for i, g := range groups {
			if !strings.Contains(fmt.Sprint(g), "--agent codex") || n >= limit {
				continue
			}
			fmt.Fprintf(&tables, "\n[hooks.state.%q]\ntrusted_hash = \"sha256:%064d\"\n",
				fmt.Sprintf("%s:%s:%d:0", hooksPath, snakeCase(event), i), n)
			n++
		}
	}
	f, err := os.OpenFile(filepath.Join(home, ".codex", "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(tables.String()); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDoctorCodexTrust(t *testing.T) {
	home := seedAgentsHome(t)
	s := testSetup(home, fakeGraphmaster(t))
	installAll(t, s)
	if n := trustCodexHooks(t, home, 1<<30); n != 4 {
		t.Fatalf("trusted %d graphmaster hooks, want 4", n)
	}
	out, err := runCLI(t, s, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !regexp.MustCompile(`(?m)^\s*ok\s+hooks trusted in Codex`).MatchString(out) {
		t.Fatalf("doctor does not report trusted Codex hooks:\n%s", out)
	}
}

func TestDoctorCodexPartlyTrusted(t *testing.T) {
	home := seedAgentsHome(t)
	s := testSetup(home, fakeGraphmaster(t))
	installAll(t, s)
	trustCodexHooks(t, home, 2)
	out, err := runCLI(t, s, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "2 of 4 graphmaster hooks not trusted") {
		t.Fatalf("doctor does not report untrusted hooks:\n%s", out)
	}
}

func TestDoctorRepositoryState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := seedAgentsHome(t)
	s := testSetup(home, fakeGraphmaster(t))
	installAll(t, s)
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	s.dir = repo
	out, err := runCLI(t, s, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not indexed yet") {
		t.Fatalf("doctor does not report the unindexed repository:\n%s", out)
	}
}

func TestSnakeCase(t *testing.T) {
	for in, want := range map[string]string{
		"PreToolUse": "pre_tool_use", "SessionStart": "session_start", "Stop": "stop", "pre_compact": "pre_compact",
	} {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
