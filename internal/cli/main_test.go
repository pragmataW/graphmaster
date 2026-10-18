package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/version"
)

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestVersionFlag(t *testing.T) {
	out, err := execute(t, "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(out, version.String()) {
		t.Fatalf("output %q does not contain %q", out, version.String())
	}
}

func TestRunExitCodes(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--version"}, &stderr); code != 0 {
		t.Fatalf("--version exit = %d, want 0", code)
	}
	stderr.Reset()
	if code := run([]string{"no-such-command"}, &stderr); code != 1 {
		t.Fatalf("unknown command exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q, want 'unknown command'", stderr.String())
	}
}
