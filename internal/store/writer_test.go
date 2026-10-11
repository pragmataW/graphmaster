package store

import (
	"context"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
)

func visibleFiles(t *testing.T, s *Store) int {
	t.Helper()
	return countRows(t, s, `SELECT count(*) FROM files`)
}

func TestWriterCommitsEveryNFiles(t *testing.T) {
	s := openTestStore(t)
	var commits []int
	w := newTestWriter(t, s, WriterOptions{MaxFiles: 2, MaxDelay: time.Hour, OnCommit: func(n int) { commits = append(commits, n) }})

	mustFile(t, w, "a.go")
	if err := w.FileDone(); err != nil {
		t.Fatal(err)
	}
	if n := visibleFiles(t, s); n != 0 {
		t.Fatalf("uncommitted file visible to readers: %d", n)
	}
	mustFile(t, w, "b.go")
	if err := w.FileDone(); err != nil {
		t.Fatal(err)
	}
	if n := visibleFiles(t, s); n != 2 {
		t.Fatalf("after 2 files visible = %d, want 2", n)
	}
	mustFile(t, w, "c.go")
	_ = w.FileDone()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n := visibleFiles(t, s); n != 3 {
		t.Fatalf("Close did not flush: %d", n)
	}
	if len(commits) != 2 || commits[0] != 2 || commits[1] != 3 {
		t.Fatalf("OnCommit calls = %v, want [2 3]", commits)
	}
}

func TestWriterCommitsAfterDelay(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{MaxFiles: 1000, MaxDelay: time.Millisecond})
	mustFile(t, w, "a.go")
	time.Sleep(5 * time.Millisecond)
	if err := w.FileDone(); err != nil {
		t.Fatal(err)
	}
	if n := visibleFiles(t, s); n != 1 {
		t.Fatalf("delay commit missing: %d", n)
	}
}

func TestWriterAbortDiscardsOpenBatch(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{MaxFiles: 100, MaxDelay: time.Hour})
	mustFile(t, w, "keep.go")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	mustFile(t, w, "drop.go")
	_ = w.FileDone()
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n := visibleFiles(t, s); n != 1 {
		t.Fatalf("files = %d, want 1", n)
	}
}

func TestUpsertFileUpdatesInPlace(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	f := &File{Path: "x/y.go", Lang: model.LangGo, Size: 1, MtimeNS: 5, Ino: 7, Mode: 0o644, Blob: []byte{1, 2, 3}, Tracked: true, Status: model.StatusOK, LOC: 3}
	id1, err := w.UpsertFile(f)
	if err != nil {
		t.Fatal(err)
	}
	f.Size, f.Status, f.Tracked = 99, model.StatusParseErr, false
	id2, err := w.UpsertFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("upsert changed id %d -> %d", id1, id2)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := s.FileByPath(context.Background(), "x/y.go")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id1 || got.Size != 99 || got.Status != model.StatusParseErr || got.Tracked || got.Ino != 7 || got.Mode != 0o644 || string(got.Blob) != "\x01\x02\x03" || got.LOC != 3 || got.Lang != model.LangGo {
		t.Fatalf("FileByPath = %+v", got)
	}
	if _, err := s.FileByPath(context.Background(), "nope"); err == nil {
		t.Fatal("expected ErrNotFound")
	}
	var seen []string
	if err := s.ForEachFile(context.Background(), func(f *File) error { seen = append(seen, f.Path); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "x/y.go" {
		t.Fatalf("ForEachFile = %v", seen)
	}
}

func TestWriterExecInBatch(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	if _, err := w.Exec(`INSERT INTO meta(key,value) VALUES('custom','1')`); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := w.QueryRow(`SELECT value FROM meta WHERE key='custom'`).Scan(&v); err != nil || v != "1" {
		t.Fatalf("QueryRow in batch: %q %v", v, err)
	}
	if err := w.SetMeta("state", "ready"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Meta(context.Background(), "state"); !ok || v != "ready" {
		t.Fatalf("meta = %q %v", v, ok)
	}
}
