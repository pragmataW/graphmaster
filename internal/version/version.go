// Package version exposes build metadata for the graphmaster binary.
//
// Release builds set the variables at link time, for example:
//
//	go build -ldflags "-X github.com/pragmataW/graphmaster/internal/version.Version=v1.0.0"
//
// Other builds (go install, go build) fall back to the module version and
// VCS stamp the Go toolchain embeds in the binary.
package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Build metadata; replaced via -ldflags -X at release time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		fromBuildInfo(info)
	}
}

// fromBuildInfo fills the variables still at their defaults: the module
// version of `go install ...@v0.1.0`, and the revision and commit time of a
// build inside a git checkout.
func fromBuildInfo(info *debug.BuildInfo) {
	if v := info.Main.Version; Version == "dev" && v != "" && v != "(devel)" {
		Version = strings.TrimPrefix(v, "v")
	}
	for _, s := range info.Settings {
		switch {
		case s.Key == "vcs.revision" && Commit == "none" && s.Value != "":
			Commit = s.Value[:min(len(s.Value), 7)]
		case s.Key == "vcs.time" && Date == "unknown" && s.Value != "":
			Date = s.Value
		}
	}
}

// String renders the full version line printed by `graphmaster --version`.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
