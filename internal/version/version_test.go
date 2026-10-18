package version

import (
	"runtime/debug"
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

func TestFromBuildInfo(t *testing.T) {
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })

	Version, Commit, Date = "dev", "none", "unknown"
	fromBuildInfo(&debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "f0add46e0123456789"},
			{Key: "vcs.time", Value: "2026-10-18T17:36:57Z"},
		},
	})
	if got, want := String(), "0.1.0 (commit f0add46, built 2026-10-18T17:36:57Z)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	// Link-time values win; a local build reports "(devel)".
	Version, Commit, Date = "0.2.0", "abc1234", "2026-11-01"
	fromBuildInfo(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "ffffffffff"}}})
	if got, want := String(), "0.2.0 (commit abc1234, built 2026-11-01)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
