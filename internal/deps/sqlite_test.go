//go:build sqlite_fts5

package deps_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestSQLiteFTS5 verifies the binary was built with the sqlite_fts5 tag.
func TestSQLiteFTS5(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE VIRTUAL TABLE t USING fts5(body)`); err != nil {
		t.Fatalf("fts5 unavailable (build with -tags sqlite_fts5): %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t(body) VALUES ('hello graph world')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t WHERE t MATCH 'graph'`).Scan(&n); err != nil {
		t.Fatalf("match: %v", err)
	}
	if n != 1 {
		t.Fatalf("match count = %d, want 1", n)
	}
}
