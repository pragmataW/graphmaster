package store

import (
	"database/sql"
	"time"
)

// driverName is the database/sql driver registered by this package: a
// SQLite driver with per-connection pragmas and the gm_tokens() function.
//
// Release builds (tag sqlite_fts5) use mattn/go-sqlite3 (cgo, fastest);
// a plain `go install` without tags uses modernc.org/sqlite (pure Go, FTS5
// built in). See driver_mattn.go and driver_modernc.go.
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

// Primary SQLite result codes the store reacts to.
const (
	codeBusy    = 5
	codeLocked  = 6
	codeCorrupt = 11
	codeNotADB  = 26
)

// pragmaBusyBudget bounds retries of a connection pragma that fails with
// SQLITE_BUSY. Switching a brand-new database file to WAL needs an
// exclusive lock and does not go through the busy handler, so concurrent
// first opens (several hook processes at once) would otherwise fail.
const pragmaBusyBudget = 5 * time.Second

// applyPragmas runs connPragmas through exec, the driver's statement runner.
func applyPragmas(exec func(q string) error) error {
	for _, p := range connPragmas {
		if err := execRetryBusy(exec, p); err != nil {
			return err
		}
	}
	return nil
}

// execRetryBusy runs q, retrying with a short backoff while it fails with
// SQLITE_BUSY or SQLITE_LOCKED, for at most pragmaBusyBudget.
func execRetryBusy(exec func(q string) error, q string) error {
	deadline := time.Now().Add(pragmaBusyBudget)
	wait := time.Millisecond
	for {
		err := exec(q)
		if err == nil || !IsBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		wait = min(2*wait, 25*time.Millisecond)
	}
}

// IsBusy reports whether err is SQLITE_BUSY or SQLITE_LOCKED.
func IsBusy(err error) bool {
	code, ok := errCode(err)
	return ok && (code == codeBusy || code == codeLocked)
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
	code, ok := errCode(err)
	return ok && (code == codeCorrupt || code == codeNotADB)
}
