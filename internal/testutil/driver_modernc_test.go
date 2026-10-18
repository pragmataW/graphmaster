//go:build !sqlite_fts5

package testutil

import _ "modernc.org/sqlite"

const testDriver = "sqlite"
