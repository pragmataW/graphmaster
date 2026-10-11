package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenCreatesSchema(t *testing.T) {
	s := openTestStore(t)
	if s.Rebuilt() {
		t.Fatal("fresh store reported Rebuilt")
	}
	var ver int
	if err := s.DB().QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", ver, SchemaVersion)
	}
	for _, tbl := range []string{
		"meta", "files", "dirs", "names", "nodes", "edges", "imports", "packages",
		"pending_names", "dirty_hints", "sessions", "session_patterns", "hook_dedupe",
		"sym_fts", "sym_tri",
	} {
		if n := countRows(t, s, `SELECT count(*) FROM sqlite_master WHERE name=?`, tbl); n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
	// Partial indexes from the plan.
	for name, frag := range map[string]string{
		"edges_dst":        "WHERE dst IS NOT NULL",
		"edges_unresolved": "WHERE dst IS NULL OR conf < 80",
	} {
		var ddl string
		if err := s.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&ddl); err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
		if !strings.Contains(ddl, frag) {
			t.Errorf("index %s = %q, want %q", name, ddl, frag)
		}
	}
	if v, ok, err := s.Meta(context.Background(), MetaSchemaVer); err != nil || !ok || v != strconv.Itoa(SchemaVersion) {
		t.Errorf("meta schema_ver = %q,%v,%v", v, ok, err)
	}
}

func TestOpenPragmas(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	conn, err := s.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	want := map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"temp_store":   "2", // MEMORY
		"busy_timeout": "5000",
		"foreign_keys": "0",
		"mmap_size":    "1073741824",
	}
	for p, w := range want {
		var got string
		if err := conn.QueryRowContext(ctx, "PRAGMA "+p).Scan(&got); err != nil {
			t.Fatalf("pragma %s: %v", p, err)
		}
		if got != w {
			t.Errorf("PRAGMA %s = %s, want %s", p, got, w)
		}
	}
}

func TestOpenReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = Open(ctx, dir, Options{QuickCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Rebuilt() {
		t.Fatal("healthy reopen rebuilt the db")
	}
	if v, ok, _ := s.Meta(ctx, "k"); !ok || v != "v" {
		t.Fatalf("meta lost: %q %v", v, ok)
	}
}

func setUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(v)); err != nil {
		t.Fatal(err)
	}
}

func TestOpenVersionMismatchRebuilds(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetMeta(ctx, "k", "v")
	_ = s.Close()

	setUserVersion(t, filepath.Join(dir, GraphDBName), SchemaVersion+41)

	s, err = Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Rebuilt() {
		t.Fatal("expected rebuild on version mismatch")
	}
	if _, ok, _ := s.Meta(ctx, "k"); ok {
		t.Fatal("rebuilt db still has old data")
	}
	if _, err := os.Stat(filepath.Join(dir, GraphDBName+".old")); err != nil {
		t.Fatalf("old db not moved aside: %v", err)
	}
}

func TestOpenCorruptFileRebuilds(t *testing.T) {
	dir := t.TempDir()
	garbage := []byte(strings.Repeat("this is not a sqlite database! ", 200))
	if err := os.WriteFile(filepath.Join(dir, GraphDBName), garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), dir, Options{})
	if err != nil {
		t.Fatalf("Open on garbage: %v", err)
	}
	defer s.Close()
	if !s.Rebuilt() {
		t.Fatal("expected rebuild for garbage file")
	}
	if n := countRows(t, s, `SELECT count(*) FROM files`); n != 0 {
		t.Fatalf("files = %d", n)
	}
}

func TestOpenQuickCheckCorruptPagesRebuilds(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.NewWriter(ctx, WriterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		fid := mustFile(t, w, "dir/file_"+strconv.Itoa(i)+".go")
		mustNode(t, w, fid, "SomeFunctionName"+strconv.Itoa(i))
		_ = w.FileDone()
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	path := filepath.Join(dir, GraphDBName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8*4096 {
		t.Fatalf("db unexpectedly small: %d", len(data))
	}
	// Smash pages after the header page; the header stays valid.
	for i := 4096; i < len(data)-4096; i += 3 {
		data[i] = 0xA5
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir, Options{QuickCheck: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if !s.Rebuilt() {
		t.Fatal("expected rebuild after quick_check failure")
	}
}

func TestOpenRebuildBlockedByLock(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	setUserVersion(t, filepath.Join(dir, GraphDBName), 999)

	holder := NewLock(dir)
	if ok, err := holder.TryLock(ctx, 0); !ok || err != nil {
		t.Fatalf("lock: %v %v", ok, err)
	}
	defer holder.Unlock()

	_, err = Open(ctx, dir, Options{LockBudget: 50 * time.Millisecond})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Open err = %v, want ErrLocked", err)
	}

	// Passing the held lock lets the rebuild proceed.
	s, err = Open(ctx, dir, Options{Lock: holder})
	if err != nil {
		t.Fatalf("Open with held lock: %v", err)
	}
	defer s.Close()
	if !s.Rebuilt() {
		t.Fatal("expected rebuild")
	}
	if !holder.Held() {
		t.Fatal("Open released a caller-owned lock")
	}
}

func TestReplaced(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s1, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	if s1.Replaced() {
		t.Fatal("fresh store reports Replaced")
	}
	setUserVersion(t, filepath.Join(dir, GraphDBName), 777)
	s2, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if !s2.Rebuilt() {
		t.Fatal("expected rebuild")
	}
	if !s1.Replaced() {
		t.Fatal("old handle should report Replaced after rebuild")
	}
	if s2.Replaced() {
		t.Fatal("new handle reports Replaced")
	}
}

func TestStoreCounts(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	fid := mustFile(t, w, "a.go")
	mustNode(t, w, fid, "A")
	mustNode(t, w, fid, "B")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	c, err := s.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Files != 1 || c.Nodes != 2 || c.Edges != 0 || c.DBBytes <= 0 {
		t.Fatalf("counts = %+v", c)
	}
}

// TestConcurrentOpenFresh opens a not-yet-existing graph.db from several
// goroutines at once; switching the new file to WAL must not surface
// SQLITE_BUSY to any of them.
func TestConcurrentOpenFresh(t *testing.T) {
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for j := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := Open(context.Background(), dir, Options{})
				errs[j] = err
				if err == nil {
					_ = s.Close()
				}
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
		}
	}
}
