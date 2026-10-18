//go:build sqlite_fts5

package store

import (
	"database/sql"
	"errors"

	sqlite3 "github.com/mattn/go-sqlite3"
)

func init() {
	sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		if err := applyPragmas(func(q string) error { _, err := c.Exec(q, nil); return err }); err != nil {
			return err
		}
		// gm_tokens(name) lets bulk FTS population run entirely inside SQLite.
		return c.RegisterFunc("gm_tokens", TokenizeIdent, true)
	}})
}

// errCode returns the primary SQLite result code of err.
func errCode(err error) (int, bool) {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return int(se.Code), true
	}
	return 0, false
}
