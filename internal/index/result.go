package index

import (
	"fmt"
	"time"
)

// Result describes one Freshen run. MCP tools and hooks render Banner as
// the first line of their output.
type Result struct {
	// File changes applied (or, when Stale, detected but not applied).
	Added, Modified, Deleted, StatOnly int
	// Parsed files went through tree-sitter; CacheHits came from cache.db.
	Parsed, CacheHits, ParseErrors int
	// FullBuild is set when the graph was built from an empty database.
	FullBuild bool
	// Rebuilt explains why the graph was reset (schema mismatch, config or
	// manifest change, --force), "" otherwise.
	Rebuilt string
	// Stale is set when changes were found but the write lock could not be
	// acquired within the budget; Pending counts the files waiting.
	Stale   bool
	Pending int
	// Building is set while the first build is still in progress
	// (another process holds the lock); Progress is its percentage.
	Building bool
	Progress int
	// Waited is the time spent waiting for the write lock.
	Waited time.Duration
	// Duration is the wall time of the whole run.
	Duration time.Duration
}

// Changed is the number of files whose graph changed.
func (r *Result) Changed() int { return r.Added + r.Modified + r.Deleted }

// Clean reports whether the graph was already up to date.
func (r *Result) Clean() bool {
	return !r.Stale && !r.Building && r.Changed() == 0 && !r.FullBuild && r.Rebuilt == ""
}

// Banner is the one-line freshness summary, e.g. "fresh (+2 files, 41ms)",
// "⚠ stale: 3 files pending" or "⚠ building 37% (partial graph)".
func (r *Result) Banner() string {
	switch {
	case r.Building:
		return fmt.Sprintf("⚠ building %d%% (partial graph)", r.Progress)
	case r.Stale:
		return fmt.Sprintf("⚠ stale: %d files pending", r.Pending)
	case r.Changed() == 0:
		return "fresh"
	}
	return fmt.Sprintf("fresh (+%d files, %s)", r.Changed(), roundDuration(r.Duration))
}

func roundDuration(d time.Duration) time.Duration {
	switch {
	case d >= 10*time.Second:
		return d.Round(time.Second)
	case d >= time.Second:
		return d.Round(100 * time.Millisecond)
	}
	return d.Round(time.Millisecond)
}
