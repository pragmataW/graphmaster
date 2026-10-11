package index

import (
	"context"
	"runtime"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Resolver resolves the names queued in pending_names after files were
// written. It must replace (not append) the edges of every name it
// processes and clear them from the queue. It runs while the caller holds
// the write lock and no Writer is open. internal/resolve provides the real
// implementation; index only depends on this interface.
type Resolver interface {
	ResolvePending(ctx context.Context, st *store.Store, rc model.RepoContext) error
}

// FullResolver may be implemented by a Resolver to handle a full build
// (every name queued) more cheaply than ResolvePending, e.g. by skipping
// the dependent re-checks an incremental run needs. resolve.Resolver
// implements it.
type FullResolver interface {
	ResolveAll(ctx context.Context, st *store.Store, rc model.RepoContext) error
}

// IncrementalHints may be implemented by a Resolver to let the writer keep
// a rewritten file's definitions in place when only their bodies or
// positions changed (see rewrite.go). Without it every rewrite replaces the
// file's graph and queues all of its names. resolve.Resolver implements it.
type IncrementalHints interface {
	// CommonNameLimit is the reference count above which a name is too
	// common for global guessing; crossing it changes how every
	// reference of the name resolves.
	CommonNameLimit() int
	// ReadsForeignSource reports whether resolving references may read the
	// source text of definitions of lang in other files, so a body-only
	// edit can change how references elsewhere resolve.
	ReadsForeignSource(lang model.Lang) bool
}

// Options configure an Indexer.
type Options struct {
	// Config is the effective configuration; nil loads it for the
	// repository (falling back to config.Default on error).
	Config *config.Config
	// Resolver runs after every write; nil leaves pending_names queued.
	Resolver Resolver
	// Workers is the parse parallelism (default GOMAXPROCS).
	Workers int
	// BatchFiles and BatchDelay tune writer commits (store defaults).
	BatchFiles int
	BatchDelay time.Duration
	// AllowNoGit lets Open index a directory outside any git repository
	// (`graphmaster index --force`).
	AllowNoGit bool
	// Progress, if set, is called from the writer goroutine as files are
	// committed during a run that writes.
	Progress func(done, total int)
	// Now is the clock used for scan timestamps (racy-git rule); tests
	// only. Default time.Now.
	Now func() time.Time
	// Logf receives diagnostics; default discards.
	Logf func(format string, args ...any)

	// stateDir overrides the .graphmaster location (tests: a second,
	// from-scratch index of the same working tree).
	stateDir string
}

func (o *Options) withDefaults() {
	if o.Workers <= 0 {
		o.Workers = runtime.GOMAXPROCS(0)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}
