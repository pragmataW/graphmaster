package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
)

func TestRegistered(t *testing.T) {
	a, ok := adapters.Get(hookapi.AgentCodex)
	if !ok {
		t.Fatal("codex adapter not registered")
	}
	if a.Name() != hookapi.AgentCodex {
		t.Fatalf("Name() = %q", a.Name())
	}
}

func envWith(vars map[string]string) install.Env {
	return install.Env{LookupEnv: func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}}
}

func TestDetect(t *testing.T) {
	a := Adapter{}

	empty := t.TempDir()
	if a.Detect(install.EnvWithHome(empty)) {
		t.Error("detected codex in empty HOME")
	}

	withDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withDir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.Detect(install.EnvWithHome(withDir)) {
		t.Error("~/.codex not detected")
	}

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.Detect(envWith(map[string]string{"HOME": empty, "PATH": "/nonexistent" + string(os.PathListSeparator) + binDir})) {
		t.Error("codex binary on PATH not detected")
	}

	// A non-executable file named codex does not count.
	plainDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(plainDir, "codex"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if a.Detect(envWith(map[string]string{"HOME": empty, "PATH": plainDir})) {
		t.Error("non-executable codex detected")
	}

	alt := filepath.Join(t.TempDir(), "alt")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.Detect(envWith(map[string]string{"HOME": empty, "CODEX_HOME": alt})) {
		t.Error("CODEX_HOME not detected")
	}
}

func TestNotes(t *testing.T) {
	a := Adapter{}
	home := t.TempDir()
	notes := a.Notes(install.EnvWithHome(home))
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "/hooks") {
		t.Errorf("notes must mention /hooks trust: %q", joined)
	}

	// An AGENTS.md over Codex's 32 KiB budget gets a warning.
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("rule\n", agentsMDCap/5+1)
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(a.Notes(install.EnvWithHome(home)), "\n")
	if !strings.Contains(joined, "32 KiB") {
		t.Errorf("missing AGENTS.md size warning: %q", joined)
	}
}
