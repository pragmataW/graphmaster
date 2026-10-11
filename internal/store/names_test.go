package store

import (
	"context"
	"errors"
	"testing"
)

func TestInternStableIDs(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	a1, err := w.Intern("Alpha")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := w.Intern("Alpha")
	b, _ := w.Intern("Beta")
	if a1 != a2 || a1 == b || a1 == 0 || b == 0 {
		t.Fatalf("ids: a1=%d a2=%d b=%d", a1, a2, b)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if id, ok, err := s.NameID(ctx, "Alpha"); err != nil || !ok || id != a1 {
		t.Fatalf("NameID = %d %v %v", id, ok, err)
	}
	if _, ok, err := s.NameID(ctx, "Gamma"); err != nil || ok {
		t.Fatalf("NameID(missing) = %v %v", ok, err)
	}
	if n, err := s.Name(ctx, b); err != nil || n != "Beta" {
		t.Fatalf("Name = %q %v", n, err)
	}
	if _, err := s.Name(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Name(missing) err = %v", err)
	}
	// Committed names land in the shared cache.
	if id, ok := s.names.get("Alpha"); !ok || id != a1 {
		t.Fatalf("cache miss after commit: %d %v", id, ok)
	}

	// A second writer reuses the same ids (via cache and via SQL).
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	s.names.reset()
	w2 := newTestWriter(t, s, WriterOptions{})
	if id, _ := w2.Intern("Alpha"); id != a1 {
		t.Fatalf("re-intern after cache reset = %d, want %d", id, a1)
	}
}

func TestInternRollbackDoesNotPoisonCache(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	id, err := w.Intern("Ghost")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.names.get("Ghost"); ok {
		t.Fatal("rolled-back name leaked into cache")
	}
	if _, ok, _ := s.NameID(context.Background(), "Ghost"); ok {
		t.Fatal("rolled-back name persisted")
	}
	id2, err := w.Intern("Ghost")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.NameID(context.Background(), "Ghost"); !ok || got != id2 {
		t.Fatalf("NameID = %d %v want %d (first id was %d)", got, ok, id2, id)
	}
}

func TestNameCacheBounded(t *testing.T) {
	c := newNameCache(4)
	for i, n := range []string{"a", "b", "c", "d", "e"} {
		c.put(n, int64(i+1))
	}
	if c.size() > 4 {
		t.Fatalf("cache grew to %d", c.size())
	}
	if id, ok := c.get("e"); !ok || id != 5 {
		t.Fatalf("latest entry missing")
	}
}
