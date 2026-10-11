package testutil

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Fixed identity and timestamps make commits (and therefore HEAD) identical
// across runs and machines.
const (
	gitUser  = "graphmaster-fixture"
	gitEmail = "fixture@graphmaster.invalid"
	gitDate  = "2026-01-01T00:00:00Z"
)

// gitEnv returns an environment that isolates git from the developer's
// global/system configuration and from any GIT_* variables of a parent
// process (e.g. when tests run inside a git hook).
func gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME="+gitUser,
		"GIT_AUTHOR_EMAIL="+gitEmail,
		"GIT_AUTHOR_DATE="+gitDate,
		"GIT_COMMITTER_NAME="+gitUser,
		"GIT_COMMITTER_EMAIL="+gitEmail,
		"GIT_COMMITTER_DATE="+gitDate,
		"LC_ALL=C",
	)
}

// runGit runs git in dir with the isolated environment.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		ge := &gitError{args: args, err: err}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			ge.stderr = string(ee.Stderr)
		}
		return string(out), ge
	}
	return string(out), nil
}

type gitError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	return "git " + strings.Join(e.args, " ") + ": " + e.err.Error() + ": " + strings.TrimSpace(e.stderr)
}

func (e *gitError) Unwrap() error { return e.err }

// Git runs git in dir with an isolated environment (no global or system
// config) and returns stdout with trailing newlines trimmed. It fails the
// test on error.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(out, "\n")
}

// initRepo creates a repository in dir with hermetic local settings.
// core.excludesFile points at the null device so the developer's global
// ignore rules never leak into the fixture, even for callers that invoke git
// without gitEnv.
func initRepo(dir string) error {
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", gitUser},
		{"config", "user.email", gitEmail},
		{"config", "commit.gpgsign", "false"},
		{"config", "core.autocrlf", "false"},
		{"config", "core.excludesFile", os.DevNull},
	}
	for _, s := range steps {
		if _, err := runGit(dir, s...); err != nil {
			return err
		}
	}
	return nil
}

// commitAll stages everything not ignored and commits it.
func commitAll(dir, msg string) error {
	if _, err := runGit(dir, "add", "-A"); err != nil {
		return err
	}
	_, err := runGit(dir, "commit", "-q", "--no-verify", "-m", msg)
	return err
}
