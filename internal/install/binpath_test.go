package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkExe(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func symlink(t *testing.T, target, link string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func locator(exe string, path string) BinaryLocator {
	return BinaryLocator{
		Executable: func() (string, error) { return exe, nil },
		LookPath: func(name string) (string, error) {
			for _, dir := range filepath.SplitList(path) {
				p := filepath.Join(dir, name)
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
					return p, nil
				}
			}
			return "", errors.New("not found")
		},
		TempDirs: []string{"/nonexistent-temp-root"},
	}
}

func TestResolveBinaryHomebrewCellar(t *testing.T) {
	prefix := t.TempDir()
	cellar := mkExe(t, filepath.Join(prefix, "Cellar", "graphmaster", "1.2.0", "bin", "graphmaster"))
	link := symlink(t, "../Cellar/graphmaster/1.2.0/bin/graphmaster", filepath.Join(prefix, "bin", "graphmaster"))
	got, err := locator(cellar, "").Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != link || !strings.Contains(got.Reason, "Homebrew") {
		t.Fatalf("got %+v, want %s", got, link)
	}
}

func TestResolveBinaryHomebrewCask(t *testing.T) {
	prefix := t.TempDir()
	cask := mkExe(t, filepath.Join(prefix, "Caskroom", "graphmaster", "1.2.0", "graphmaster"))
	link := symlink(t, cask, filepath.Join(prefix, "bin", "graphmaster"))
	// Executable reported as the resolved Caskroom path.
	got, err := locator(cask, "").Resolve()
	if err != nil || got.Path != link {
		t.Fatalf("got %+v %v, want %s", got, err, link)
	}
}

func TestResolveBinaryPrefersPATHSymlink(t *testing.T) {
	root := t.TempDir()
	resolved := mkExe(t, filepath.Join(root, "opt", "gm", "graphmaster"))
	bin := filepath.Join(root, "bin")
	link := symlink(t, resolved, filepath.Join(bin, "graphmaster"))
	got, err := locator(resolved, bin).Resolve()
	if err != nil || got.Path != link {
		t.Fatalf("got %+v %v, want %s", got, err, link)
	}
}

func TestResolveBinaryPlainExecutable(t *testing.T) {
	root := t.TempDir()
	exe := mkExe(t, filepath.Join(root, "go", "bin", "graphmaster"))
	other := mkExe(t, filepath.Join(root, "elsewhere", "graphmaster"))
	got, err := locator(exe, filepath.Dir(other)).Resolve()
	if err != nil || got.Path != exe {
		t.Fatalf("got %+v %v, want %s", got, err, exe)
	}
}

func TestResolveBinaryTemporaryBuild(t *testing.T) {
	root := t.TempDir()
	tmp := mkExe(t, filepath.Join(root, "go-build123456", "b001", "exe", "graphmaster"))
	if _, err := locator(tmp, "").Resolve(); err == nil {
		t.Fatal("expected error for a go-build temp binary with nothing on PATH")
	}
	installed := mkExe(t, filepath.Join(root, "home", "go", "bin", "graphmaster"))
	got, err := locator(tmp, filepath.Dir(installed)).Resolve()
	if err != nil || got.Path != installed {
		t.Fatalf("got %+v %v, want %s", got, err, installed)
	}

	// Anything under a temp dir counts as ephemeral too.
	l := locator(mkExe(t, filepath.Join(root, "tmpdir", "x", "graphmaster")), "")
	l.TempDirs = []string{filepath.Join(root, "tmpdir")}
	if _, err := l.Resolve(); err == nil {
		t.Fatal("expected error for a binary under TempDir")
	}
}
