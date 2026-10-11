package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
)

// CacheSchemaVersion is cache.db's PRAGMA user_version. A mismatch discards
// the cache (it is only an accelerator).
const CacheSchemaVersion = 1

// Cache GC defaults.
const (
	DefaultCacheMaxAge = 30 * 24 * time.Hour
	// touchInterval limits last_used refreshes to one write per entry per
	// interval, so cache hits stay read-only in the common case.
	touchInterval = 24 * time.Hour
)

const cacheSchemaSQL = `
CREATE TABLE IF NOT EXISTS parse_cache(
  blob      BLOB NOT NULL,
  lang      INTEGER NOT NULL,
  xver      INTEGER NOT NULL,
  facts     BLOB NOT NULL,
  last_used INTEGER NOT NULL,
  PRIMARY KEY(blob, lang, xver)
);
CREATE INDEX IF NOT EXISTS parse_cache_lru ON parse_cache(last_used);
`

// Cache is cache.db: encoded parse facts keyed by (git blob SHA-1, language,
// extractor version). It survives graph.db rebuilds. Safe for concurrent use.
type Cache struct {
	db   *sql.DB
	path string
}

// CacheEntry is one parse_cache row for PutBatch.
type CacheEntry struct {
	Blob  []byte
	Lang  model.Lang
	XVer  int
	Facts []byte
}

// OpenCache opens or creates cache.db at path. A corrupt file or a schema
// version mismatch deletes and recreates it.
func OpenCache(ctx context.Context, path string) (*Cache, error) {
	c, recreate, err := tryOpenCache(ctx, path)
	if err != nil || !recreate {
		return c, err
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	c, recreate, err = tryOpenCache(ctx, path)
	if err == nil && recreate {
		err = fmt.Errorf("store: cannot recreate %s", path)
	}
	return c, err
}

func tryOpenCache(ctx context.Context, path string) (c *Cache, recreate bool, err error) {
	db, err := openDB(path)
	if err != nil {
		return nil, false, err
	}
	fail := func(e error) (*Cache, bool, error) {
		_ = db.Close()
		if isCorrupt(e) {
			return nil, true, nil
		}
		return nil, false, e
	}
	if err := db.PingContext(ctx); err != nil {
		return fail(err)
	}
	var ver int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		return fail(err)
	}
	switch ver {
	case CacheSchemaVersion:
	case 0:
		// auto_vacuum must be chosen before the first table exists; it
		// lets GC hand space back with incremental_vacuum.
		if _, err := db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			return fail(err)
		}
		if _, err := db.ExecContext(ctx, cacheSchemaSQL+"PRAGMA user_version = "+strconv.Itoa(CacheSchemaVersion)); err != nil {
			return fail(err)
		}
	default:
		_ = db.Close()
		return nil, true, nil
	}
	return &Cache{db: db, path: path}, false, nil
}

// Close closes cache.db.
func (c *Cache) Close() error { return c.db.Close() }

// Get returns cached facts. Hits refresh last_used at most once a day.
func (c *Cache) Get(ctx context.Context, blob []byte, lang model.Lang, xver int) ([]byte, bool, error) {
	var (
		facts []byte
		used  int64
	)
	err := c.db.QueryRowContext(ctx, `SELECT facts, last_used FROM parse_cache WHERE blob = ? AND lang = ? AND xver = ?`,
		blob, int(lang), xver).Scan(&facts, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	now := time.Now().Unix()
	if now-used >= int64(touchInterval/time.Second) {
		// Best effort: a failed touch only makes the entry look older.
		_, _ = c.db.ExecContext(ctx, `UPDATE parse_cache SET last_used = ? WHERE blob = ? AND lang = ? AND xver = ?`,
			now, blob, int(lang), xver)
	}
	return facts, true, nil
}

// Put stores (or replaces) one entry.
func (c *Cache) Put(ctx context.Context, blob []byte, lang model.Lang, xver int, facts []byte) error {
	return c.PutBatch(ctx, []CacheEntry{{Blob: blob, Lang: lang, XVer: xver, Facts: facts}})
}

// PutBatch stores entries in one transaction.
func (c *Cache) PutBatch(ctx context.Context, entries []CacheEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	st, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO parse_cache(blob, lang, xver, facts, last_used) VALUES(?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	now := time.Now().Unix()
	for _, e := range entries {
		if _, err := st.ExecContext(ctx, e.Blob, int(e.Lang), e.XVer, e.Facts, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CacheStats describes cache.db contents.
type CacheStats struct {
	Entries int64
	Bytes   int64 // sum of facts sizes
	Deleted int64 // entries removed by the GC run that produced the stats
}

// Stats returns entry count and payload size.
func (c *Cache) Stats(ctx context.Context) (CacheStats, error) {
	var st CacheStats
	err := c.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(length(facts)), 0) FROM parse_cache`).Scan(&st.Entries, &st.Bytes)
	return st, err
}

// GCOptions configure Cache.GC.
type GCOptions struct {
	Now    time.Time     // default time.Now()
	MaxAge time.Duration // entries unused for longer are dropped (default 30 days)
	// LiveBytes is the facts size referenced by the current graph. When
	// positive, least recently used entries are evicted until the cache
	// holds at most 2x LiveBytes.
	LiveBytes int64
}

// GC evicts entries older than MaxAge, then trims the cache to twice the
// live size, oldest first.
func (c *Cache) GC(ctx context.Context, opt GCOptions) (CacheStats, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	if opt.MaxAge <= 0 {
		opt.MaxAge = DefaultCacheMaxAge
	}
	var deleted int64
	res, err := c.db.ExecContext(ctx, `DELETE FROM parse_cache WHERE last_used < ?`, opt.Now.Add(-opt.MaxAge).Unix())
	if err != nil {
		return CacheStats{}, err
	}
	n, _ := res.RowsAffected()
	deleted += n

	if opt.LiveBytes > 0 {
		n, err := c.trimTo(ctx, 2*opt.LiveBytes)
		if err != nil {
			return CacheStats{}, err
		}
		deleted += n
	}
	if deleted > 0 {
		_, _ = c.db.ExecContext(ctx, "PRAGMA incremental_vacuum")
	}
	st, err := c.Stats(ctx)
	st.Deleted = deleted
	return st, err
}

// trimTo deletes least recently used entries until total facts size <= maxBytes.
func (c *Cache) trimTo(ctx context.Context, maxBytes int64) (int64, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(length(facts)), 0) FROM parse_cache`).Scan(&total); err != nil {
		return 0, err
	}
	if total <= maxBytes {
		return 0, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid, length(facts) FROM parse_cache ORDER BY last_used, rowid`)
	if err != nil {
		return 0, err
	}
	var victims []int64
	for rows.Next() && total > maxBytes {
		var id, size int64
		if err := rows.Scan(&id, &size); err != nil {
			rows.Close()
			return 0, err
		}
		victims = append(victims, id)
		total -= size
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	st, err := tx.PrepareContext(ctx, `DELETE FROM parse_cache WHERE rowid = ?`)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	for _, id := range victims {
		if _, err := st.ExecContext(ctx, id); err != nil {
			return 0, err
		}
	}
	return int64(len(victims)), tx.Commit()
}
