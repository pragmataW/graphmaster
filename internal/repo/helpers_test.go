package repo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestMain isolates git (and this package's config lookup) from the
// developer's global and system configuration.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "repo-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "t",
		"GIT_AUTHOR_EMAIL":    "t@example.com",
		"GIT_COMMITTER_NAME":  "t",
		"GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		_ = os.Setenv(k, v)
	}
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_GLOBAL"} {
		_ = os.Unsetenv(k)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

func requireGit(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// git runs git in dir and returns stdout; it fails the test on error.
func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := gitCmd(dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, stderr.String())
	}
	return string(out)
}

func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	return cmd
}

// newRepo creates an empty repository with a resolved (symlink-free) path.
func newRepo(t testing.TB) string {
	t.Helper()
	requireGit(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	return dir
}

// writeFiles creates files (slash paths relative to dir) with content.
func writeFiles(t testing.TB, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// splitZ splits NUL-terminated git output into sorted records.
func splitZ(s string) []string {
	var out []string
	for _, f := range strings.Split(s, "\x00") {
		if f != "" {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func diffSets(want, got []string) string {
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	g := map[string]bool{}
	for _, s := range got {
		g[s] = true
	}
	var b strings.Builder
	for _, s := range want {
		if !g[s] {
			fmt.Fprintf(&b, "  missing: %s\n", s)
		}
	}
	for _, s := range got {
		if !w[s] {
			fmt.Fprintf(&b, "  extra:   %s\n", s)
		}
	}
	return b.String()
}
