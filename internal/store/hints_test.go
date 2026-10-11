package store

import (
	"context"
	"sort"
	"testing"
)

func TestDirtyHints(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.AddDirtyHints(ctx, []string{"a.go", "b/c.go"}, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDirtyHints(ctx, []string{"a.go"}, 200); err != nil {
		t.Fatal(err)
	}
	got, err := s.TakeDirtyHints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "a.go" || got[1] != "b/c.go" {
		t.Fatalf("hints = %v", got)
	}
	if got, _ := s.TakeDirtyHints(ctx); len(got) != 0 {
		t.Fatalf("hints not cleared: %v", got)
	}
}

func TestPendingNamesLimit(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	if err := w.EnqueuePending(5, 3, 9, 3); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	got, err := s.PendingNames(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 3 || got[1] != 5 {
		t.Fatalf("PendingNames(2) = %v", got)
	}
}
