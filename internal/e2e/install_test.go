package e2e

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configFixtures holds realistic per-agent user configs (comments, other
// MCP servers, user hooks, rules).
var configFixtures = filepath.Join(moduleRoot(), "testdata", "configs")

// seedHome builds a temp HOME with the existing configs of all four agents.
func seedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, agent := range []string{"claude", "cursor", "opencode"} {
		copyTree(t, filepath.Join(configFixtures, agent, "home"), home)
	}
	for _, f := range []string{"config.toml", "hooks.json", "AGENTS.md"} {
		copyFile(t, filepath.Join(configFixtures, "codex", f), filepath.Join(home, ".codex", f))
	}
	return home
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
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
	writeFile(t, dst, string(b))
}

// snapshot maps every regular file under home (except graphmaster's own
// state directory) to its content.
func snapshot(t *testing.T, home string) map[string][]byte {
	t.Helper()
	own := filepath.Join(home, ".config", "graphmaster")
	out := map[string][]byte{}
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == own {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, p)
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInstallDoctorUninstallRoundTrip(t *testing.T) {
	binary(t)
	home := seedHome(t)
	env := childEnv(home)
	repo := fixture(t)
	mustGM(t, repo, env, "index", "--quiet")
	before := snapshot(t, home)

	inst := mustGM(t, repo, env, "install")
	for _, agent := range []string{"claude:", "codex:", "cursor:", "opencode:"} {
		if !strings.Contains(inst.stdout, agent) {
			t.Errorf("install output lacks %s\n%s", agent, inst.stdout)
		}
	}
	if !strings.Contains(inst.stdout, filepath.Join(binDir, "graphmaster")) {
		t.Errorf("install must record the PATH binary %s:\n%s", binDir, inst.stdout)
	}
	installed := snapshot(t, home)
	if _, ok := installed[".config/opencode/plugins/graphmaster.js"]; !ok {
		t.Error("install did not write the OpenCode plugin")
	}
	for _, f := range []string{".claude.json", ".claude/settings.json", ".codex/config.toml", ".codex/hooks.json", ".cursor/mcp.json", ".cursor/hooks.json", ".config/opencode/opencode.json"} {
		if bytes.Equal(before[f], installed[f]) {
			t.Errorf("install left %s unchanged", f)
		}
	}

	// A second install changes nothing.
	mustGM(t, repo, env, "install")
	assertSameFiles(t, "second install", installed, snapshot(t, home))

	doc := gm(t, repo, env, nil, "doctor")
	if doc.code != 0 {
		t.Fatalf("doctor exit %d:\n%s%s", doc.code, doc.stdout, doc.stderr)
	}
	for _, agent := range []string{"claude", "codex", "cursor", "opencode"} {
		if !strings.Contains(doc.stdout, "\n"+agent+"\n") {
			t.Errorf("doctor has no %s section:\n%s", agent, doc.stdout)
		}
	}
	if !strings.Contains(doc.stdout, "0 problem(s)") {
		t.Errorf("doctor reports problems:\n%s", doc.stdout)
	}
	t.Logf("doctor:\n%s", doc.stdout)

	// With Cursor's native hooks installed, Cursor running the Claude hook
	// stays silent and the native hook alone decides.
	payload := mustJSON(t, cursorGrep("cur-e2e", "g1", "tu1", repo, "PlaceOrder"))
	if r := gm(t, repo, env, payload, "hook", "pre-tool-use", "--agent", "claude"); r.code != 0 || r.stdout != "" {
		t.Fatalf("claude-side duplicate: exit %d stdout %q", r.code, r.stdout)
	}
	if g := cursorDecision(t, gm(t, repo, env, payload, "hook", "pre-tool-use", "--agent", "cursor")); g.Permission != "deny" {
		t.Fatalf("native cursor hook must deny, got %+v", g)
	}

	mustGM(t, repo, env, "uninstall")
	assertSameFiles(t, "uninstall", before, snapshot(t, home))
	if after := gm(t, repo, env, nil, "doctor"); after.code == 0 {
		t.Errorf("doctor must report the missing installation:\n%s", after.stdout)
	}
}

func assertSameFiles(t *testing.T, label string, want, got map[string][]byte) {
	t.Helper()
	for p, w := range want {
		g, ok := got[p]
		switch {
		case !ok:
			t.Errorf("%s: %s is missing", label, p)
		case !bytes.Equal(w, g):
			t.Errorf("%s: %s differs:\n--- want\n%s\n--- got\n%s", label, p, w, g)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("%s: unexpected file %s", label, p)
		}
	}
}

// cursorGate is Cursor's answer to a gating hook.
type cursorGate struct {
	Permission   string `json:"permission"`
	AgentMessage string `json:"agent_message"`
}

func cursorDecision(t *testing.T, r result) cursorGate {
	t.Helper()
	var g cursorGate
	if r.code != 0 {
		t.Fatalf("cursor hook exit %d: %s", r.code, r.stderr)
	}
	if err := json.Unmarshal([]byte(r.stdout), &g); err != nil {
		t.Fatalf("cursor stdout %q: %v", r.stdout, err)
	}
	return g
}
