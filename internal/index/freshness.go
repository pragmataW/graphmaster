package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/repo"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Indexer keeps one repository's graph.db fresh. It is safe for concurrent
// use; Freshen calls are deduplicated within the process.
type Indexer struct {
	loc   *Location
	opts  Options
	cfg   *config.Config
	cache *store.Cache

	stMu sync.RWMutex
	st   *store.Store

	sf    singleflight.Group
	runMu sync.Mutex // serializes runs (Freshen and Rebuild)

	dirMu       sync.Mutex
	dirCache    *repo.DirCache
	pendingDirs map[string]*repo.DirRow // nil value = removed

	lastIndex *repo.Index // last decoded git index (scan-local use only)

	memoMu sync.Mutex
	memo   map[string]keyMemo

	snapMu        sync.Mutex
	snapGen       string
	snapFiles     map[string]*store.File
	snapTracked   *repo.TrackedSet
	snapTrackedIC bool

	crashAfter int
	parseHook  func(path string) // tests: called before every parse
}

// Open locates the repository enclosing dir, prepares .graphmaster/ and
// opens graph.db and cache.db. Outside a git repository it fails with
// ErrNotGitRepo unless opts.AllowNoGit is set or the directory was
// previously indexed with it.
func Open(ctx context.Context, dir string, opts Options) (*Indexer, error) {
	opts.withDefaults()
	loc, err := Locate(dir, opts.AllowNoGit)
	if err != nil {
		return nil, err
	}
	if opts.stateDir != "" {
		loc.Dir = opts.stateDir
	}
	if err := loc.Prepare(); err != nil {
		return nil, fmt.Errorf("index: prepare %s: %w", loc.Dir, err)
	}
	cfg := opts.Config
	if cfg == nil {
		if cfg, err = config.Load(loc.Root.WorkTree); err != nil {
			opts.Logf("index: config: %v (using defaults)", err)
			cfg = config.Default()
		}
	}
	st, err := openStore(ctx, loc.Dir)
	if err != nil {
		return nil, fmt.Errorf("index: open graph.db: %w", err)
	}
	cache, err := store.OpenCache(ctx, filepath.Join(loc.Dir, store.CacheDBName))
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("index: open cache.db: %w", err)
	}
	ix := &Indexer{
		loc: loc, opts: opts, cfg: cfg, st: st, cache: cache,
		pendingDirs: map[string]*repo.DirRow{},
		memo:        map[string]keyMemo{},
		crashAfter:  crashAfterFromEnv(),
	}
	ix.dirCache = ix.loadDirCache(ctx)
	return ix, nil
}

// openStore opens graph.db, re-opening with an integrity check when the
// previous run did not finish (crash safety, PLAN step 9).
func openStore(ctx context.Context, dir string) (*store.Store, error) {
	st, err := openStoreRetry(ctx, dir, store.Options{})
	if err != nil {
		return nil, err
	}
	state, _, err := st.Meta(ctx, store.MetaState)
	if err != nil || state == store.StateReady || state == "" || st.Rebuilt() {
		return st, err
	}
	// "building" with no writer holding the lock means a build crashed:
	// verify the file before trusting it. While a build is running, skip
	// the (full-file) check; readers get the partial graph.
	lock := store.NewLock(dir)
	if ok, lerr := lock.TryLock(ctx, 0); lerr != nil || !ok {
		return st, nil
	}
	defer func() { _ = lock.Unlock() }()
	_ = st.Close()
	return openStoreRetry(ctx, dir, store.Options{QuickCheck: true, Lock: lock})
}

// openBusyBudget bounds retries of store.Open while another process is
// creating or converting the database.
const openBusyBudget = 10 * time.Second

// openStoreRetry serializes the creation of a fresh graph.db under the
// write lock and retries opens that still hit a busy database (store.Open
// itself retries the WAL switch; this also covers schema creation races).
func openStoreRetry(ctx context.Context, dir string, opts store.Options) (*store.Store, error) {
	if _, err := os.Stat(filepath.Join(dir, store.GraphDBName)); errors.Is(err, os.ErrNotExist) && (opts.Lock == nil || !opts.Lock.Held()) {
		lock := store.NewLock(dir)
		if ok, err := lock.TryLock(ctx, openBusyBudget); err == nil && ok {
			defer func() { _ = lock.Unlock() }()
			opts.Lock = lock
		}
	}
	deadline := time.Now().Add(openBusyBudget)
	for {
		st, err := store.Open(ctx, dir, opts)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return st, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func isBusy(err error) bool {
	return store.IsBusy(err) || strings.Contains(err.Error(), "database is locked")
}

func (ix *Indexer) loadDirCache(ctx context.Context) *repo.DirCache {
	rows, err := ix.Store().LoadDirs(ctx)
	if err != nil {
		ix.opts.Logf("index: load dir cache: %v", err)
		return repo.NewDirCache()
	}
	conv := make([]repo.DirRow, len(rows))
	for i, r := range rows {
		conv[i] = repo.DirRow{Path: r.Path, MtimeNS: r.MtimeNS, IgnoreStat: r.IgnoreStat, Names: r.Names}
	}
	c, err := repo.DirCacheFromRows(conv)
	if err != nil {
		ix.opts.Logf("index: dir cache: %v", err)
		return repo.NewDirCache()
	}
	return c
}

// Root returns the located repository.
func (ix *Indexer) Root() *repo.Root { return ix.loc.Root }

// Location returns where the repository and its state directory are.
func (ix *Indexer) Location() *Location { return ix.loc }

// Config returns the effective configuration.
func (ix *Indexer) Config() *config.Config { return ix.cfg }

// Store returns the open graph.db. It may be replaced by a later Freshen
// when another process rebuilt the database file.
func (ix *Indexer) Store() *store.Store {
	ix.stMu.RLock()
	defer ix.stMu.RUnlock()
	return ix.st
}

// Close releases the databases.
func (ix *Indexer) Close() error {
	ix.runMu.Lock()
	defer ix.runMu.Unlock()
	return errors.Join(ix.Store().Close(), ix.cache.Close())
}

// Freshen brings the graph up to date (PLAN "Freshness algoritması").
// budget bounds the wait for the write lock: MCP 20s, PreToolUse 1.5s,
// SessionStart 590s, async 0. When the lock is not acquired in time the
// graph is left as is and the Result reports Stale (and Building during a
// first build in another process). Concurrent calls share one run.
func (ix *Indexer) Freshen(ctx context.Context, budget time.Duration) (*Result, error) {
	return ix.shared(ctx, "freshen", budget, "")
}

// Rebuild discards the graph (keeping cache.db) and builds it again.
func (ix *Indexer) Rebuild(ctx context.Context, budget time.Duration) (*Result, error) {
	return ix.shared(ctx, "rebuild", budget, "forced rebuild")
}

func (ix *Indexer) shared(ctx context.Context, key string, budget time.Duration, reset string) (*Result, error) {
	ch := ix.sf.DoChan(key, func() (any, error) {
		// Detached: one caller giving up must not abort a run others share.
		return ix.run(context.WithoutCancel(ctx), budget, reset)
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		res := *r.Val.(*Result)
		return &res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (ix *Indexer) run(ctx context.Context, budget time.Duration, reset string) (*Result, error) {
	ix.runMu.Lock()
	defer ix.runMu.Unlock()
	t0 := time.Now()
	if err := ix.reopenIfReplaced(ctx); err != nil {
		return nil, err
	}
	p, err := ix.scan(ctx, reset)
	if err != nil {
		return nil, err
	}
	haveResolver := ix.opts.Resolver != nil
	if !p.needsWrite(haveResolver) {
		ix.persistCaches(ctx, p, 0)
		return &Result{Duration: time.Since(t0)}, nil
	}

	lock := store.NewLock(ix.loc.Dir)
	w0 := time.Now()
	ok, err := lock.TryLock(ctx, budget)
	if err != nil {
		return nil, err
	}
	waited := time.Since(w0)
	if !ok {
		res := &Result{Stale: true, Pending: p.pendingFiles(), Waited: waited}
		ix.fillBuilding(ctx, res)
		res.Duration = time.Since(t0)
		return res, nil
	}
	defer func() { _ = lock.Unlock() }()

	// Step 7: another writer may have done the work while we waited.
	if p, err = ix.scan(ctx, reset); err != nil {
		return nil, err
	}
	if !p.needsWrite(haveResolver) {
		ix.persistCaches(ctx, p, -1)
		return &Result{Waited: waited, Duration: time.Since(t0)}, nil
	}
	res, err := ix.apply(ctx, p)
	if err != nil {
		return nil, err
	}
	res.Waited = waited
	res.Duration = time.Since(t0)
	return res, nil
}

// fillBuilding reports an in-progress first build by another process.
func (ix *Indexer) fillBuilding(ctx context.Context, res *Result) {
	st := ix.Store()
	state, _, err := st.Meta(ctx, store.MetaState)
	if err != nil || state != store.StateBuilding {
		return
	}
	res.Building = true
	if v, ok, _ := st.Meta(ctx, store.MetaProgress); ok {
		res.Progress, _ = strconv.Atoi(v)
	}
}

// reopenIfReplaced reopens graph.db when another process moved it aside.
func (ix *Indexer) reopenIfReplaced(ctx context.Context) error {
	if !ix.Store().Replaced() {
		return nil
	}
	st, err := openStore(ctx, ix.loc.Dir)
	if err != nil {
		return err
	}
	ix.stMu.Lock()
	old := ix.st
	ix.st = st
	ix.stMu.Unlock()
	_ = old.Close()
	ix.dropSnapshot()
	ix.dirMu.Lock()
	ix.dirCache = ix.loadDirCache(ctx)
	clear(ix.pendingDirs)
	ix.dirMu.Unlock()
	return nil
}

// notePendingDirs merges a walk's directory-cache delta into the set of
// rows not yet persisted. Callers hold dirMu.
func (ix *Indexer) notePendingDirs(changed []repo.DirRow, removed []string) {
	for i := range changed {
		r := changed[i]
		ix.pendingDirs[r.Path] = &r
	}
	for _, p := range removed {
		ix.pendingDirs[p] = nil
	}
}

// pendingDirRows lists unpersisted directory rows. Callers hold dirMu.
func (ix *Indexer) pendingDirRows() (changed []repo.DirRow, removed []string) {
	for p, r := range ix.pendingDirs {
		if r == nil {
			removed = append(removed, p)
		} else {
			changed = append(changed, *r)
		}
	}
	return changed, removed
}

// persisted drops directory rows that were written by a committed batch.
func (ix *Indexer) persisted(p *plan) {
	ix.dirMu.Lock()
	defer ix.dirMu.Unlock()
	for _, r := range p.dirChanged {
		if cur, ok := ix.pendingDirs[r.Path]; ok && cur != nil && cur.MtimeNS == r.MtimeNS && cur.IgnoreStat == r.IgnoreStat {
			delete(ix.pendingDirs, r.Path)
		}
	}
	for _, path := range p.dirRemoved {
		if cur, ok := ix.pendingDirs[path]; ok && cur == nil {
			delete(ix.pendingDirs, path)
		}
	}
}

func writeDirRows(w *store.Writer, p *plan) error {
	for i := range p.dirChanged {
		r := &p.dirChanged[i]
		if err := w.UpsertDir(&store.Dir{Path: r.Path, MtimeNS: r.MtimeNS, IgnoreStat: r.IgnoreStat, Names: r.Names}); err != nil {
			return err
		}
	}
	for _, path := range p.dirRemoved {
		if err := w.DeleteDir(path); err != nil {
			return err
		}
	}
	return nil
}

// persistCaches stores cache-only changes of a clean scan (directory
// listings, .git/index signature). It never waits: lockBudget 0 tries the
// lock once, -1 means the caller already holds it.
func (ix *Indexer) persistCaches(ctx context.Context, p *plan, lockBudget time.Duration) {
	if !p.needsCachePersist() {
		return
	}
	if lockBudget >= 0 {
		lock := store.NewLock(ix.loc.Dir)
		ok, err := lock.TryLock(ctx, lockBudget)
		if err != nil || !ok {
			return
		}
		defer func() { _ = lock.Unlock() }()
	}
	w, err := ix.Store().NewWriter(ctx, store.WriterOptions{})
	if err != nil {
		return
	}
	err = writeDirRows(w, p)
	if err == nil && p.idxChanged {
		err = w.SetMeta(store.MetaGitIndexStat, p.idxSig.String())
	}
	if err != nil {
		_ = w.Abort()
	}
	if cerr := w.Close(); err == nil && cerr == nil {
		ix.persisted(p)
	}
}

// codeSet returns the candidate paths of p.
func (p *plan) codeSet() map[string]struct{} {
	out := make(map[string]struct{}, len(p.cands))
	for path := range p.cands {
		out[path] = struct{}{}
	}
	return out
}

// errCrash is never returned; os.Exit happens first. It documents intent.
var errCrash = errors.New("index: simulated crash")

func (ix *Indexer) crashCheck(commits int) {
	if ix.crashAfter > 0 && commits >= ix.crashAfter {
		fmt.Fprintf(os.Stderr, "%v after %d commits\n", errCrash, commits)
		os.Exit(crashExitCode)
	}
}
