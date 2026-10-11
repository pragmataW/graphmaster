package replay

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildTags are the tags every graphmaster build uses.
const buildTags = "sqlite_fts5 sqlite_omit_load_extension"

// binPath is the graphmaster binary built by TestMain; empty with -short
// or when the build is disabled.
var binPath string

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "gm-replay-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
	if err := buildBinary(filepath.Join(dir, "graphmaster")); err != nil {
		fmt.Fprintln(os.Stderr, "replay: build graphmaster:", err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "graphmaster")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func buildBinary(out string) error {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return err
	}
	cmd := exec.Command(goBin, "build", "-tags", buildTags, "-o", out, "./cmd/graphmaster")
	cmd.Dir = filepath.Join(HooksDir(), "..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOTOOLCHAIN=local")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, b)
	}
	return nil
}

// strippedPrefixes are inherited variables that would make the child
// behave unlike a clean agent environment (the suite may run inside an
// agent session or a git hook).
var strippedPrefixes = []string{"HOME=", "XDG_", "GRAPHMASTER_", "CLAUDE", "CODEX", "CURSOR", "OPENCODE", "GIT_"}

// binary execs `graphmaster hook <event> --agent <agent>` the way agents
// do: the payload on stdin, the fixture's cwd as working directory.
func binary(t *testing.T, w *world, agent, event, dir string, stdin []byte, env []string) result {
	t.Helper()
	var full []string
	for _, kv := range os.Environ() {
		keep := true
		for _, p := range strippedPrefixes {
			if strings.HasPrefix(kv, p) {
				keep = false
				break
			}
		}
		if keep {
			full = append(full, kv)
		}
	}
	full = append(full, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	full = append(full, env...)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "hook", event, "--agent", agent)
	cmd.Dir = dir
	cmd.Env = full
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{stdout: out.Bytes(), stderr: errb.Bytes()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	case err != nil:
		t.Fatalf("graphmaster hook %s --agent %s: %v", event, agent, err)
	}
	return r
}

// TestReplayBinary pipes the same scenarios into a built graphmaster and
// expects byte-identical transcripts: the CLI entry (stdin handling, exit
// code, adapter registration) adds nothing to the dispatcher's answer.
// Scenarios that start background builds are left out, since a real
// binary would spawn `graphmaster index` processes. Runs are sequential:
// the binary keeps the production 25ms graph lookup budget.
func TestReplayBinary(t *testing.T) {
	if binPath == "" {
		t.Skip("needs a built binary; skipped with -short")
	}
	for _, sc := range scenarios() {
		if sc.spawns {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			got := play(t, sc, binary)
			compareGolden(t, sc.name, got, false)
		})
	}
}
