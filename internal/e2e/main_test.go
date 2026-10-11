package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/testutil"
)

// buildTags are the tags every graphmaster build uses.
const buildTags = "sqlite_fts5 sqlite_omit_load_extension"

// binDir holds the graphmaster binary built by TestMain; empty when the
// suite runs with -short.
var binDir string

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "gm-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	if err := build(dir); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: build graphmaster:", err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// moduleRoot is the repository root (two levels above this file).
func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func build(dir string) error {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return err
	}
	cmd := exec.Command(goBin, "build", "-tags", buildTags, "-o", filepath.Join(dir, "graphmaster"), "./cmd/graphmaster")
	cmd.Dir = moduleRoot()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// binary returns the built graphmaster path, skipping under -short.
func binary(t *testing.T) string {
	t.Helper()
	if binDir == "" {
		t.Skip("end-to-end tests need a built binary; skipped with -short")
	}
	return filepath.Join(binDir, "graphmaster")
}

// strippedPrefixes are inherited variables that would make the child
// behave differently from a clean agent environment (the developer may run
// the suite from inside Claude Code, Cursor or a git hook).
var strippedPrefixes = []string{"HOME=", "XDG_", "GRAPHMASTER_", "CLAUDE", "CODEX", "CURSOR", "OPENCODE", "GIT_", "PATH="}

// childEnv is a hermetic environment for the binary: HOME is home, the
// binary's directory leads PATH (so install records a stable PATH entry)
// and extra is appended last.
func childEnv(home string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		keep := true
		for _, p := range strippedPrefixes {
			if strings.HasPrefix(kv, p) {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	env = append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	return append(env, extra...)
}

// result is one finished process.
type result struct {
	stdout, stderr string
	code           int
}

// gm runs the binary in dir with env and stdin and waits for it.
func gm(t *testing.T, dir string, env []string, stdin []byte, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary(t), args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	case err != nil:
		t.Fatalf("graphmaster %s: %v", strings.Join(args, " "), err)
	}
	return r
}

// mustGM is gm that fails the test on a non-zero exit.
func mustGM(t *testing.T, dir string, env []string, args ...string) result {
	t.Helper()
	r := gm(t, dir, env, nil, args...)
	if r.code != 0 {
		t.Fatalf("graphmaster %s: exit %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

// fixture materializes the fixture repository and returns its root with
// symlinks resolved (macOS /var -> /private/var), as agents report cwd.
func fixture(t *testing.T) string {
	t.Helper()
	root := testutil.Materialize(t)
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
