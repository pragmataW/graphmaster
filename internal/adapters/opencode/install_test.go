package opencode

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

const (
	testBin   = "/opt/homebrew/bin/graphmaster"
	testInstr = "## graphmaster\n\nUse the graphmaster MCP tools (find_symbol, callers, impact) before grep."
)

var seedRoot = filepath.Join("..", "..", "..", "testdata", "configs", "opencode", "home")

// seededHome copies testdata/configs/opencode/home into a temp HOME.
func seededHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	err := filepath.WalkDir(seedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(seedRoot, p)
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

// snapshot returns every regular file under dir (relative path -> bytes).
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return out
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func installOnce(t *testing.T, env install.Env, opts adapters.InstallOptions) *install.Report {
	t.Helper()
	plan, err := Adapter{}.InstallPlan(env, opts)
	if err != nil {
		t.Fatalf("InstallPlan: %v", err)
	}
	rep, err := install.NewEngine(env).Install(install.Options{}, plan)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return rep
}

func TestRegistered(t *testing.T) {
	a, ok := adapters.Get(hookapi.AgentOpenCode)
	if !ok {
		t.Fatal("opencode adapter not registered")
	}
	if a.Name() != hookapi.AgentOpenCode {
		t.Fatalf("Name() = %q", a.Name())
	}
}

func TestInstallPlanShape(t *testing.T) {
	home := t.TempDir()
	env := install.EnvWithHome(home)
	plan, err := Adapter{}.InstallPlan(env, adapters.InstallOptions{Binary: testBin, Instructions: testInstr})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan invalid: %v", err)
	}
	if plan.Agent != "opencode" {
		t.Errorf("plan.Agent = %q", plan.Agent)
	}
	dir := filepath.Join(home, ".config", "opencode")
	byPath := map[string]install.FileChange{}
	for _, f := range plan.Files {
		byPath[f.Path] = f
	}
	cfg, ok := byPath[filepath.Join(dir, "opencode.json")]
	if !ok || cfg.Format != install.FormatJSON || len(cfg.Ops) != 1 {
		t.Fatalf("opencode.json change = %+v", cfg)
	}
	set, ok := cfg.Ops[0].(install.JSONSet)
	if !ok || set.Pointer != "/mcp/graphmaster" {
		t.Fatalf("op = %#v", cfg.Ops[0])
	}
	b, _ := json.Marshal(set.Value)
	if string(b) != `{"type":"local","command":["/opt/homebrew/bin/graphmaster","mcp"],"enabled":true}` {
		t.Errorf("mcp entry = %s", b)
	}
	plug, ok := byPath[filepath.Join(dir, "plugins", "graphmaster.js")]
	if !ok || plug.Format != install.FormatGenerated {
		t.Fatalf("plugin change = %+v", plug)
	}
	if !strings.Contains(string(plug.Content), `const BIN = "/opt/homebrew/bin/graphmaster";`) {
		t.Errorf("plugin does not embed the absolute binary path:\n%s", plug.Content)
	}
	md, ok := byPath[filepath.Join(dir, "AGENTS.md")]
	if !ok || md.Format != install.FormatMarkdown || len(md.Ops) != 1 || md.Ops[0].(install.Block).Body != testInstr {
		t.Fatalf("AGENTS.md change = %+v", md)
	}
}

func TestInstallPlanErrors(t *testing.T) {
	env := install.EnvWithHome(t.TempDir())
	for _, bin := range []string{"", "graphmaster", "./bin/graphmaster"} {
		if _, err := (Adapter{}).InstallPlan(env, adapters.InstallOptions{Binary: bin}); err == nil {
			t.Errorf("Binary %q: expected error", bin)
		}
	}
	noHome := install.Env{LookupEnv: func(string) (string, bool) { return "", false }}
	if _, err := (Adapter{}).InstallPlan(noHome, adapters.InstallOptions{Binary: testBin}); err == nil {
		t.Error("expected error without HOME")
	}
}

func TestInstallPlanSkipsEmptyInstructions(t *testing.T) {
	home := t.TempDir()
	plan, err := Adapter{}.InstallPlan(install.EnvWithHome(home), adapters.InstallOptions{Binary: testBin, Instructions: " \n"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if filepath.Base(f.Path) == "AGENTS.md" {
			t.Fatal("AGENTS.md planned without instructions")
		}
	}
}

func TestInstallRoundTripSeeded(t *testing.T) {
	home := seededHome(t)
	env := install.EnvWithHome(home)
	dir := filepath.Join(home, ".config", "opencode")
	before := snapshot(t, dir)
	opts := adapters.InstallOptions{Binary: testBin, Instructions: testInstr}

	installOnce(t, env, opts)
	cfg := mustRead(t, filepath.Join(dir, "opencode.json"))
	for _, want := range []string{
		"// Synthetic but realistic global OpenCode config",
		"// never push without asking",
		`"context7"`, `"sqlite"`, `"opencode-notifier@0.4.1"`,
		`"graphmaster"`, `"/opt/homebrew/bin/graphmaster"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("opencode.json lost or lacks %q:\n%s", want, cfg)
		}
	}
	md := mustRead(t, filepath.Join(dir, "AGENTS.md"))
	if !strings.HasPrefix(md, before["AGENTS.md"]) || !strings.Contains(md, testInstr) {
		t.Errorf("AGENTS.md = %q", md)
	}
	plugin := mustRead(t, filepath.Join(dir, "plugins", "graphmaster.js"))
	if !strings.Contains(plugin, "export const GraphmasterPlugin") {
		t.Errorf("plugin missing export:\n%s", plugin)
	}
	if got := mustRead(t, filepath.Join(dir, "plugins", "notify.js")); got != before[filepath.Join("plugins", "notify.js")] {
		t.Error("user plugin modified")
	}
	afterFirst := snapshot(t, dir)

	// Second install is a no-op.
	rep := installOnce(t, env, opts)
	if rep.Changed() {
		t.Errorf("second install changed files:\n%s", rep)
	}
	if got := snapshot(t, dir); !equalMaps(got, afterFirst) {
		t.Error("second install altered files")
	}

	if _, err := install.NewEngine(env).Uninstall(install.Options{}, "opencode"); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, dir); !equalMaps(got, before) {
		t.Errorf("uninstall is not byte-exact\n got: %v\nwant: %v", keys(got), keys(before))
		for k, v := range before {
			if got[k] != v {
				t.Errorf("%s differs:\n got: %q\nwant: %q", k, got[k], v)
			}
		}
	}
}

func TestInstallFreshHomeUninstallCleansUp(t *testing.T) {
	home := t.TempDir()
	env := install.EnvWithHome(home)
	installOnce(t, env, adapters.InstallOptions{Binary: testBin, Instructions: testInstr})
	dir := filepath.Join(home, ".config", "opencode")
	files := snapshot(t, dir)
	if len(files) != 3 {
		t.Fatalf("files after install: %v", keys(files))
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(files["opencode.json"]), &cfg); err != nil {
		t.Fatalf("opencode.json is not plain JSON: %v", err)
	}
	if _, err := install.NewEngine(env).Uninstall(install.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s still exists after uninstall: %v", dir, err)
	}
}

func TestReinstallWithMovedBinary(t *testing.T) {
	home := seededHome(t)
	env := install.EnvWithHome(home)
	dir := filepath.Join(home, ".config", "opencode")
	before := snapshot(t, dir)
	installOnce(t, env, adapters.InstallOptions{Binary: testBin, Instructions: testInstr})
	const moved = "/usr/local/bin/graphmaster"
	installOnce(t, env, adapters.InstallOptions{Binary: moved, Instructions: testInstr + "\nv2"})

	cfg := mustRead(t, filepath.Join(dir, "opencode.json"))
	if strings.Contains(cfg, testBin) || strings.Count(cfg, `"graphmaster"`) != 1 || !strings.Contains(cfg, moved) {
		t.Errorf("opencode.json after reinstall:\n%s", cfg)
	}
	if p := mustRead(t, filepath.Join(dir, "plugins", "graphmaster.js")); !strings.Contains(p, moved) || strings.Contains(p, testBin) {
		t.Error("plugin not regenerated with the new binary")
	}
	if md := mustRead(t, filepath.Join(dir, "AGENTS.md")); strings.Count(md, "graphmaster:begin") != 1 || !strings.Contains(md, "v2") {
		t.Errorf("AGENTS.md after reinstall: %q", md)
	}
	if _, err := install.NewEngine(env).Uninstall(install.Options{}, "opencode"); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, dir); !equalMaps(got, before) {
		t.Error("uninstall after reinstall is not byte-exact")
	}
}

func TestInstallPrefersExistingJSONC(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "opencode")
	writeFile(t, filepath.Join(dir, "opencode.jsonc"), "{\n  // mine\n  \"theme\": \"system\"\n}\n")
	plan, err := Adapter{}.InstallPlan(install.EnvWithHome(home), adapters.InstallOptions{Binary: testBin})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range plan.Files {
		switch filepath.Base(f.Path) {
		case "opencode.jsonc":
			found = true
		case "opencode.json":
			t.Error("planned opencode.json although opencode.jsonc exists")
		}
	}
	if !found {
		t.Error("opencode.jsonc not planned")
	}
}

func TestInstallHonorsXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(t.TempDir(), "xdg")
	env := install.Env{LookupEnv: func(k string) (string, bool) {
		switch k {
		case "HOME":
			return home, true
		case "XDG_CONFIG_HOME":
			return xdg, true
		}
		return "", false
	}}
	plan, err := Adapter{}.InstallPlan(env, adapters.InstallOptions{Binary: testBin})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if !strings.HasPrefix(f.Path, filepath.Join(xdg, "opencode")+string(filepath.Separator)) {
			t.Errorf("%s not under $XDG_CONFIG_HOME/opencode", f.Path)
		}
	}
}

func TestDetect(t *testing.T) {
	t.Run("nothing", func(t *testing.T) {
		if (Adapter{}).Detect(install.EnvWithHome(t.TempDir())) {
			t.Error("detected on empty HOME")
		}
	})
	t.Run("config dir", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !(Adapter{}).Detect(install.EnvWithHome(home)) {
			t.Error("not detected")
		}
	})
	t.Run("install script binary", func(t *testing.T) {
		home := t.TempDir()
		writeExec(t, filepath.Join(home, ".opencode", "bin", "opencode"))
		if !(Adapter{}).Detect(install.EnvWithHome(home)) {
			t.Error("not detected")
		}
	})
	t.Run("binary on PATH", func(t *testing.T) {
		home, bin := t.TempDir(), t.TempDir()
		writeExec(t, filepath.Join(bin, "opencode"))
		env := install.Env{LookupEnv: func(k string) (string, bool) {
			switch k {
			case "HOME":
				return home, true
			case "PATH":
				return "/nonexistent" + string(os.PathListSeparator) + bin, true
			}
			return "", false
		}}
		if !(Adapter{}).Detect(env) {
			t.Error("not detected")
		}
	})
}

func TestNotes(t *testing.T) {
	notes := Adapter{}.Notes(install.EnvWithHome(t.TempDir()))
	if len(notes) == 0 {
		t.Fatal("no notes")
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"Restart OpenCode", "graphmaster.js"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q: %q", want, joined)
		}
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExec(t *testing.T, p string) {
	t.Helper()
	writeFile(t, p, "#!/bin/sh\n")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
