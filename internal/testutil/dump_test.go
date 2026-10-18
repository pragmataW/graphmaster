package testutil

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

// planSchema mirrors the graph.db schema (docs/architecture.md) closely enough to
// exercise CanonicalDump without depending on internal/store.
const planSchema = `
CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE files(id INTEGER PRIMARY KEY, path TEXT UNIQUE, lang INTEGER, size INTEGER,
  mtime_ns INTEGER, ino INTEGER, mode INTEGER, blob BLOB, tracked INTEGER, status INTEGER,
  loc INTEGER, pkg_id INTEGER);
CREATE TABLE dirs(path TEXT PRIMARY KEY, mtime_ns INTEGER, ignore_stat TEXT, names BLOB);
CREATE TABLE names(id INTEGER PRIMARY KEY, name TEXT UNIQUE);
CREATE TABLE nodes(id INTEGER PRIMARY KEY, file_id INTEGER, parent_id INTEGER, kind INTEGER,
  name_id INTEGER, qname TEXT, start_line INTEGER, start_col INTEGER, end_line INTEGER,
  start_byte INTEGER, end_byte INTEGER, sig TEXT, flags INTEGER, skey BLOB);
CREATE TABLE edges(id INTEGER PRIMARY KEY, file_id INTEGER, src INTEGER, dst INTEGER,
  kind INTEGER, name_id INTEGER, qual TEXT, line INTEGER, col INTEGER, conf INTEGER);
CREATE TABLE imports(file_id INTEGER, spec TEXT, alias TEXT, names TEXT, target_file INTEGER,
  target_pkg INTEGER, line INTEGER);
CREATE TABLE packages(id INTEGER PRIMARY KEY, lang INTEGER, name TEXT, dir TEXT, kind INTEGER,
  UNIQUE(lang, name));
CREATE TABLE pending_names(name_id INTEGER PRIMARY KEY);
`

// graphRows is a tiny logical graph inserted with configurable row ids so
// tests can prove the dump is independent of them.
type graphRows struct {
	fileA, fileB, pkg   int
	nStore, nGet, nMain int
	nameGet, nameMain   int
	nameStore, nameX    int
	mtime               int64
	getEndLine          int
}

func openPlanDB(t *testing.T, r graphRows) *sql.DB {
	t.Helper()
	db, err := sql.Open(testDriver, filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stmts := []string{planSchema}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, s := range stmts {
		exec(s)
	}
	exec(`INSERT INTO meta VALUES ('schema_ver','1'),('state','ready'),('last_scan_ns',?),('extractor_ver:go','3')`, r.mtime)
	exec(`INSERT INTO packages VALUES (?, ?, 'example.com/fixture/internal/store', 'go/internal/store', 1)`, r.pkg, model.LangGo)
	exec(`INSERT INTO files VALUES (?, 'go/internal/store/store.go', ?, 120, ?, 77, 420, x'00ff', 1, 0, 30, ?)`,
		r.fileA, model.LangGo, r.mtime, r.pkg)
	exec(`INSERT INTO files VALUES (?, 'go/cmd/server/main.go', ?, 80, ?, 78, 420, x'0102', 0, 0, 12, NULL)`,
		r.fileB, model.LangGo, r.mtime)
	exec(`INSERT INTO names VALUES (?, 'Store'), (?, 'Get'), (?, 'main'), (?, 'Orphan')`,
		r.nameStore, r.nameGet, r.nameMain, r.nameX)
	exec(`INSERT INTO nodes VALUES (?, ?, NULL, ?, ?, 'store.Memory', 10, 1, 40, 100, 900, 'type Memory struct', ?, x'0a0b')`,
		r.nStore, r.fileA, model.KindStruct, r.nameStore, model.FlagExported)
	exec(`INSERT INTO nodes VALUES (?, ?, ?, ?, ?, 'store.Memory.Get', 20, 1, ?, 300, 500, 'func (m *Memory) Get(id string) (Item, bool)', ?, x'0c0d')`,
		r.nGet, r.fileA, r.nStore, model.KindMethod, r.nameGet, r.getEndLine, model.FlagExported)
	exec(`INSERT INTO nodes VALUES (?, ?, NULL, ?, ?, 'main.main', 5, 1, 9, 10, 90, 'func main()', 0, x'0e0f')`,
		r.nMain, r.fileB, model.KindFunction, r.nameMain)
	exec(`INSERT INTO edges VALUES (NULL, ?, ?, ?, ?, ?, 'st', 7, 5, 80)`, r.fileB, r.nMain, r.nGet, model.EdgeCalls, r.nameGet)
	exec(`INSERT INTO edges VALUES (NULL, ?, ?, NULL, ?, ?, NULL, 8, 2, 0)`, r.fileB, r.nMain, model.EdgeCalls, r.nameStore)
	exec(`INSERT INTO edges VALUES (NULL, ?, ?, ?, ?, NULL, NULL, 20, 1, 100)`, r.fileA, r.nStore, r.nGet, model.EdgeContains)
	exec(`INSERT INTO imports VALUES (?, 'example.com/fixture/internal/store', NULL, NULL, NULL, ?, 3)`, r.fileB, r.pkg)
	exec(`INSERT INTO pending_names VALUES (?)`, r.nameStore)
	exec(`INSERT INTO dirs VALUES ('go', ?, NULL, NULL)`, r.mtime)
	return db
}

var baseRows = graphRows{
	fileA: 1, fileB: 2, pkg: 1,
	nStore: 1, nGet: 2, nMain: 3,
	nameStore: 1, nameGet: 2, nameMain: 3, nameX: 4,
	mtime: 1000, getEndLine: 30,
}

func TestCanonicalDumpIgnoresRowIDsAndVolatileColumns(t *testing.T) {
	a := CanonicalDump(openPlanDB(t, baseRows))

	shuffled := graphRows{
		fileA: 50, fileB: 7, pkg: 9,
		nStore: 300, nGet: 12, nMain: 99,
		nameStore: 40, nameGet: 41, nameMain: 2, nameX: 1,
		mtime: 99999, getEndLine: 30,
	}
	b := CanonicalDump(openPlanDB(t, shuffled))
	if a != b {
		t.Fatalf("dump depends on row ids or volatile columns:\n--- a\n%s\n--- b\n%s", a, b)
	}
}

func TestCanonicalDumpDetectsSemanticChange(t *testing.T) {
	a := CanonicalDump(openPlanDB(t, baseRows))
	changed := baseRows
	changed.getEndLine = 31
	if b := CanonicalDump(openPlanDB(t, changed)); a == b {
		t.Fatal("dump did not change when a node span changed")
	}
}

func TestCanonicalDumpContent(t *testing.T) {
	d := CanonicalDump(openPlanDB(t, baseRows))
	for _, want := range []string{
		"go/internal/store/store.go",
		"lang=go",
		"pkg=go:example.com/fixture/internal/store",
		"store.Memory.Get",
		"kind=method",
		"flags=exported",
		"calls go/cmd/server/main.go::main.main#function:5 -> go/internal/store/store.go::store.Memory.Get#method:20",
		"calls go/cmd/server/main.go::main.main#function:5 -> NULL",
		"name=Store",
		"parent=go/internal/store/store.go::store.Memory#struct:10",
		"schema_ver=1",
		"extractor_ver:go=3",
		"target_pkg=go:example.com/fixture/internal/store",
		"== pending_names",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("dump lacks %q\n%s", want, d)
		}
	}
	for _, banned := range []string{"mtime_ns", "ino=", "last_scan_ns", "Orphan", "== dirs"} {
		if strings.Contains(d, banned) {
			t.Errorf("dump contains volatile %q\n%s", banned, d)
		}
	}
}

func TestCanonicalDumpToleratesMissingTables(t *testing.T) {
	db, err := sql.Open(testDriver, filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE files(id INTEGER PRIMARY KEY, path TEXT, lang INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO files VALUES (1, 'a.go', 1)`); err != nil {
		t.Fatal(err)
	}
	d, err := CanonicalDumpE(db)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "a.go") || !strings.Contains(d, "== nodes (absent)") {
		t.Errorf("unexpected dump:\n%s", d)
	}
}
