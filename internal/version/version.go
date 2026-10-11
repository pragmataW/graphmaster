// Package version exposes build metadata for the graphmaster binary.
//
// The variables are overridden at link time, for example:
//
//	go build -ldflags "-X github.com/pragmataW/graphmaster/internal/version.Version=v1.0.0"
package version

import "fmt"

// Build metadata; replaced via -ldflags -X at release time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders the full version line printed by `graphmaster --version`.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
