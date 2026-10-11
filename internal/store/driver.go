package store

import (
	"database/sql"
	"errors"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// driverName is the database/sql driver registered by this package. It wraps
// mattn/go-sqlite3 with per-connection pragmas and the gm_tokens() function.
const driverName = "sqlite3_graphmaster"

// connPragmas are applied to every new connection. busy_timeout goes first so
// that the remaining pragmas wait instead of failing under contention.
var connPragmas = []string{
	"PRAGMA busy_timeout = 5000",
	"PRAGMA journal_mode = WAL",
	"PRAGMA synchronous = NORMAL",
	"PRAGMA temp_store = MEMORY",
	"PRAGMA mmap_size = 1073741824",
	"PRAGMA foreign_keys = OFF",
}

func init() {
	sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: connectHook})
}

// pragmaBusyBudget bounds retries of a connection pragma that fails with
// SQLITE_BUSY. Switching a brand-new database file to WAL needs an
// exclusive lock and does not go through the busy handler, so concurrent
// first opens (several hook processes at once) would otherwise fail.
const pragmaBusyBudget = 5 * time.Second

func connectHook(c *sqlite3.SQLiteConn) error {
	for _, p := range connPragmas {
		if err := execRetryBusy(c, p); err != nil {
			return err
		}
	}
	// gm_tokens(name) lets bulk FTS population run entirely inside SQLite.
	return c.RegisterFunc("gm_tokens", TokenizeIdent, true)
}

// execRetryBusy runs q, retrying with a short backoff while it fails with
// SQLITE_BUSY or SQLITE_LOCKED, for at most pragmaBusyBudget.
func execRetryBusy(c *sqlite3.SQLiteConn, q string) error {
	deadline := time.Now().Add(pragmaBusyBudget)
	wait := time.Millisecond
	for {
		_, err := c.Exec(q, nil)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		wait = min(2*wait, 25*time.Millisecond)
	}
}

// isBusy reports whether err is SQLITE_BUSY or SQLITE_LOCKED.
func isBusy(err error) bool {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked
	}
	return false
}

// openDB opens path with the graphmaster driver. database/sql connects
// lazily, so callers must Ping to surface open errors.
func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open(driverName, path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	return db, nil
}

// isCorrupt reports whether err means the file is not a usable database, in
// which case it is safe to throw it away (all content is derived).
func isCorrupt(err error) bool {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return se.Code == sqlite3.ErrCorrupt || se.Code == sqlite3.ErrNotADB
	}
	return false
}
