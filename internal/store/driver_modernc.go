//go:build !sqlite_fts5

package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"modernc.org/sqlite"
)

func init() {
	d := &sqlite.Driver{}
	// gm_tokens(name) lets bulk FTS population run entirely inside SQLite.
	if err := d.RegisterDeterministicScalarFunction("gm_tokens", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			switch v := args[0].(type) {
			case string:
				return TokenizeIdent(v), nil
			case []byte:
				return TokenizeIdent(string(v)), nil
			}
			return "", nil
		}); err != nil {
		panic(err)
	}
	d.RegisterConnectionHook(func(c sqlite.ExecQuerierContext, _ string) error {
		return applyPragmas(func(q string) error {
			_, err := c.ExecContext(context.Background(), q, nil)
			return err
		})
	})
	sql.Register(driverName, d)
}

// errCode returns the primary SQLite result code of err (modernc reports
// extended codes).
func errCode(err error) (int, bool) {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() & 0xff, true
	}
	return 0, false
}
