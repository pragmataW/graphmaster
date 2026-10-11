package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
)

func openTestCache(t *testing.T, path string) *Cache {
	t.Helper()
	c, err := OpenCache(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func blobKey(b byte) []byte { return bytes.Repeat([]byte{b}, 20) }

func TestCachePutGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), CacheDBName)
	c := openTestCache(t, path)
	ctx := context.Background()
	if err := c.Put(ctx, blobKey(1), model.LangGo, 3, []byte("facts-v3")); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(ctx, blobKey(1), model.LangGo, 3)
	if err != nil || !ok || string(got) != "facts-v3" {
		t.Fatalf("Get = %q %v %v", got, ok, err)
	}
	if _, ok, _ := c.Get(ctx, blobKey(1), model.LangGo, 4); ok {
		t.Fatal("hit on different xver")
	}
	if _, ok, _ := c.Get(ctx, blobKey(1), model.LangPython, 3); ok {
		t.Fatal("hit on different lang")
	}
	// Overwrite.
	if err := c.Put(ctx, blobKey(1), model.LangGo, 3, []byte("facts-new")); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := c.Get(ctx, blobKey(1), model.LangGo, 3); string(got) != "facts-new" {
		t.Fatalf("overwrite = %q", got)
	}
	if err := c.PutBatch(ctx, []CacheEntry{
		{Blob: blobKey(2), Lang: model.LangGo, XVer: 3, Facts: []byte("b2")},
		{Blob: blobKey(3), Lang: model.LangGo, XVer: 3, Facts: []byte("b3")},
	}); err != nil {
		t.Fatal(err)
	}
	st, err := c.Stats(ctx)
	if err != nil || st.Entries != 3 {
		t.Fatalf("Stats = %+v %v", st, err)
	}

	// Survives reopen.
	_ = c.Close()
	c2 := openTestCache(t, path)
	if got, ok, _ := c2.Get(ctx, blobKey(2), model.LangGo, 3); !ok || string(got) != "b2" {
		t.Fatal("cache lost on reopen")
	}
}

func setLastUsed(t *testing.T, c *Cache, blob []byte, ts int64) {
	t.Helper()
	if _, err := c.db.Exec(`UPDATE parse_cache SET last_used=? WHERE blob=?`, ts, blob); err != nil {
		t.Fatal(err)
	}
}

func TestCacheGCByAge(t *testing.T) {
	c := openTestCache(t, filepath.Join(t.TempDir(), CacheDBName))
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	for i := byte(1); i <= 3; i++ {
		_ = c.Put(ctx, blobKey(i), model.LangGo, 1, []byte("x"))
	}
	setLastUsed(t, c, blobKey(1), now.Add(-31*24*time.Hour).Unix())
	setLastUsed(t, c, blobKey(2), now.Add(-29*24*time.Hour).Unix())
	setLastUsed(t, c, blobKey(3), now.Unix())
	st, err := c.GC(ctx, GCOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if st.Deleted != 1 {
		t.Fatalf("deleted = %d", st.Deleted)
	}
	if _, ok, _ := c.Get(ctx, blobKey(1), model.LangGo, 1); ok {
		t.Fatal("old entry survived GC")
	}
	if _, ok, _ := c.Get(ctx, blobKey(2), model.LangGo, 1); !ok {
		t.Fatal("recent entry removed")
	}
}

func TestCacheGCBySize(t *testing.T) {
	c := openTestCache(t, filepath.Join(t.TempDir(), CacheDBName))
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	payload := bytes.Repeat([]byte("f"), 100)
	for i := byte(1); i <= 10; i++ {
		_ = c.Put(ctx, blobKey(i), model.LangGo, 1, payload)
		setLastUsed(t, c, blobKey(i), now.Add(-time.Duration(20-i)*time.Hour).Unix()) // 1 oldest, 10 newest
	}
	// Live set is 200 bytes => keep at most 400 bytes => 4 newest entries.
	st, err := c.GC(ctx, GCOptions{Now: now, LiveBytes: 200})
	if err != nil {
		t.Fatal(err)
	}
	if st.Deleted != 6 || st.Bytes != 400 || st.Entries != 4 {
		t.Fatalf("GC stats = %+v", st)
	}
	for i := byte(1); i <= 10; i++ {
		_, ok, _ := c.Get(ctx, blobKey(i), model.LangGo, 1)
		if want := i >= 7; ok != want {
			t.Errorf("entry %d present=%v want %v", i, ok, want)
		}
	}
}

func TestCacheGetTouchesStaleEntries(t *testing.T) {
	c := openTestCache(t, filepath.Join(t.TempDir(), CacheDBName))
	ctx := context.Background()
	_ = c.Put(ctx, blobKey(1), model.LangGo, 1, []byte("x"))
	old := time.Now().Add(-10 * 24 * time.Hour).Unix()
	setLastUsed(t, c, blobKey(1), old)
	if _, ok, _ := c.Get(ctx, blobKey(1), model.LangGo, 1); !ok {
		t.Fatal("miss")
	}
	var lu int64
	_ = c.db.QueryRow(`SELECT last_used FROM parse_cache`).Scan(&lu)
	if lu <= old {
		t.Fatalf("last_used not refreshed: %d", lu)
	}
}

func TestCacheVersionMismatchRecreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), CacheDBName)
	c, err := OpenCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Put(context.Background(), blobKey(1), model.LangGo, 1, []byte("x"))
	_ = c.Close()
	db, _ := sql.Open("sqlite3", path)
	_, _ = db.Exec(`PRAGMA user_version = 99`)
	_ = db.Close()

	c2 := openTestCache(t, path)
	if _, ok, _ := c2.Get(context.Background(), blobKey(1), model.LangGo, 1); ok {
		t.Fatal("stale-version cache kept data")
	}
	if err := c2.Put(context.Background(), blobKey(1), model.LangGo, 1, []byte("y")); err != nil {
		t.Fatal(err)
	}
}
