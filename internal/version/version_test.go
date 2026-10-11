package version

import (
	"strings"
	"testing"
)

func TestStringDefaults(t *testing.T) {
	got := String()
	if !strings.HasPrefix(got, Version) {
		t.Fatalf("String() = %q, want prefix %q", got, Version)
	}
	if !strings.Contains(got, "commit "+Commit) {
		t.Fatalf("String() = %q, want commit %q", got, Commit)
	}
}

func TestStringOverridden(t *testing.T) {
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })

	Version, Commit, Date = "v1.2.3", "abc1234", "2026-10-11"
	want := "v1.2.3 (commit abc1234, built 2026-10-11)"
	if got := String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
