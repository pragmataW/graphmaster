package testutil

import "testing"

// Large fixture commits must not leave a detached auto gc running: it
// writes into .git while TempDir cleanup removes the repository.
func TestGitDisablesAutoGC(t *testing.T) {
	dir := t.TempDir()
	Git(t, dir, "init", "-q")
	if got := Git(t, dir, "config", "gc.auto"); got != "0" {
		t.Errorf("gc.auto = %q, want 0", got)
	}
	if got := Git(t, dir, "config", "maintenance.auto"); got != "false" {
		t.Errorf("maintenance.auto = %q, want false", got)
	}
}
