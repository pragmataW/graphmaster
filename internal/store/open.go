package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// File names inside the .graphmaster directory.
const (
	GraphDBName = "graph.db"
	CacheDBName = "cache.db"
	LockName    = "write.lock"
)

// Sentinel errors.
var (
	// ErrNotFound is returned by accessors when a row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrLocked is returned by Open when graph.db must be rebuilt but the
	// write lock could not be acquired within Options.LockBudget.
	ErrLocked = errors.New("store: write lock busy")
)

// defaultLockBudget is how long Open waits for the write lock when it has to
// rebuild graph.db and the caller did not pass a held lock.
const defaultLockBudget = 5 * time.Second

// Options configure Open.
type Options struct {
	// QuickCheck runs PRAGMA quick_check on open; a failure triggers a
	// rebuild. It reads the whole file, so use it after crashes, not on
	// every open.
	QuickCheck bool
	// Lock, if non-nil and held by the caller, is used for a rebuild
	// instead of acquiring a new one. Open never releases it.
	Lock *Lock
	// LockBudget bounds the wait for the write lock during a rebuild
	// (default 5s). Ignored when Lock is held.
	LockBudget time.Duration
}

// Store is an open graph.db. It is safe for concurrent use; writes must go
// through a Writer.
type Store struct {
	db      *sql.DB
	dir     string
	path    string
	info    os.FileInfo // identity of the file at open, for Replaced
	names   *nameCache
	rebuilt string // non-empty reason when Open recreated the database
}

// Open opens (creating if needed) dir/graph.db, where dir is the
// repository's .graphmaster directory. A database with a different schema
// version, an unreadable file or (with QuickCheck) a failed integrity check
// is moved aside to graph.db.old and recreated empty under the write lock;
// Rebuilt then reports true and the caller must reindex.
func Open(ctx context.Context, dir string, opts Options) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, GraphDBName)
	s, reason, err := tryOpen(ctx, path, opts.QuickCheck)
	if err != nil || reason == "" {
		return s, err
	}

	// Rebuild path: serialize with other writers so two processes never
	// move each other's fresh databases aside.
	lock := opts.Lock
	if lock == nil || !lock.Held() {
		budget := opts.LockBudget
		if budget <= 0 {
			budget = defaultLockBudget
		}
		lock = NewLock(dir)
		ok, err := lock.TryLock(ctx, budget)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: graph.db needs rebuild (%s)", ErrLocked, reason)
		}
		defer func() { _ = lock.Unlock() }()
	}

	// Someone may have rebuilt it while we waited.
	s, reason2, err := tryOpen(ctx, path, opts.QuickCheck)
	if err != nil || reason2 == "" {
		return s, err
	}
	if err := moveAside(path); err != nil {
		return nil, fmt.Errorf("store: move aside %s: %w", path, err)
	}
	s, reason3, err := tryOpen(ctx, path, false)
	if err != nil {
		return nil, err
	}
	if reason3 != "" {
		return nil, fmt.Errorf("store: fresh database unusable: %s", reason3)
	}
	s.rebuilt = reason2
	return s, nil
}

// tryOpen opens path and validates it. A non-empty reason means the file
// must be rebuilt (the returned Store is nil in that case); err is reserved
// for problems a rebuild would not fix (permissions, I/O, cancellation).
func tryOpen(ctx context.Context, path string, quickCheck bool) (s *Store, reason string, err error) {
	db, err := openDB(path)
	if err != nil {
		return nil, "", err
	}
	fail := func(e error) (*Store, string, error) {
		_ = db.Close()
		if isCorrupt(e) {
			return nil, "corrupt: " + e.Error(), nil
		}
		return nil, "", e
	}
	if err := db.PingContext(ctx); err != nil {
		return fail(err)
	}
	var ver int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		return fail(err)
	}
	switch {
	case ver == 0:
		var objects int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&objects); err != nil {
			return fail(err)
		}
		if objects > 0 {
			_ = db.Close()
			return nil, "unversioned database", nil
		}
		err := createSchema(ctx, db)
		if errors.Is(err, errVersionRace) {
			_ = db.Close()
			return nil, "schema version changed concurrently", nil
		}
		if err != nil {
			return fail(err)
		}
	case ver != SchemaVersion:
		_ = db.Close()
		return nil, fmt.Sprintf("schema version %d, want %d", ver, SchemaVersion), nil
	case quickCheck:
		var res string
		if err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&res); err != nil {
			return fail(err)
		}
		if res != "ok" {
			_ = db.Close()
			return nil, "quick_check: " + res, nil
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = db.Close()
		return nil, "", err
	}
	return &Store{
		db:    db,
		dir:   filepath.Dir(path),
		path:  path,
		info:  info,
		names: newNameCache(defaultNameCacheSize),
	}, "", nil
}

// moveAside renames path (and its WAL) to path.old*, replacing any previous
// copy, and removes the shared-memory file. Open handles elsewhere keep
// working on the old inode; SQLite will not checkpoint into a moved file.
func moveAside(path string) error {
	old := path + ".old"
	for _, p := range []string{old, old + "-wal", old + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(path, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(path+"-wal", old+"-wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(path + "-shm"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// DB exposes the underlying pool for read queries. Do not write through it
// while a Writer is active; use the Writer's Exec instead.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the graph.db path.
func (s *Store) Path() string { return s.path }

// Dir returns the .graphmaster directory.
func (s *Store) Dir() string { return s.dir }

// Rebuilt reports whether Open recreated the database (the graph is empty
// and must be fully reindexed).
func (s *Store) Rebuilt() bool { return s.rebuilt != "" }

// RebuildReason explains why Open recreated the database, or "".
func (s *Store) RebuildReason() string { return s.rebuilt }

// Replaced reports whether graph.db on disk is no longer the file this Store
// opened (another process rebuilt it). Long-lived readers should reopen.
func (s *Store) Replaced() bool {
	cur, err := os.Stat(s.path)
	if err != nil {
		return true
	}
	return !os.SameFile(cur, s.info)
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Counts summarizes the graph for status output.
type Counts struct {
	Files, Nodes, Edges, Unresolved int64
	DBBytes                         int64 // graph.db + WAL on disk
}

// Counts returns row counts and on-disk size.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM files),
		(SELECT count(*) FROM nodes),
		(SELECT count(*) FROM edges),
		(SELECT count(*) FROM edges WHERE dst IS NULL)`).Scan(&c.Files, &c.Nodes, &c.Edges, &c.Unresolved)
	if err != nil {
		return c, err
	}
	for _, p := range []string{s.path, s.path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			c.DBBytes += fi.Size()
		}
	}
	return c, nil
}
