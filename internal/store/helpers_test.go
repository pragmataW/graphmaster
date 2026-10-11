package store

import (
	"context"
	"sort"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTestWriter(t *testing.T, s *Store, opt WriterOptions) *Writer {
	t.Helper()
	w, err := s.NewWriter(context.Background(), opt)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func mustFile(t *testing.T, w *Writer, path string) int64 {
	t.Helper()
	id, err := w.UpsertFile(&File{Path: path, Lang: model.LangGo, Size: 10, Tracked: true})
	if err != nil {
		t.Fatalf("UpsertFile(%s): %v", path, err)
	}
	return id
}

func mustNode(t *testing.T, w *Writer, fileID int64, name string) int64 {
	t.Helper()
	id, err := w.InsertNode(&Node{FileID: fileID, Kind: model.KindFunction, Name: name, QName: "pkg." + name, StartLine: 1, EndLine: 2})
	if err != nil {
		t.Fatalf("InsertNode(%s): %v", name, err)
	}
	return id
}

func sortedIDs(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func equalIDs(a, b []int64) bool {
	a, b = sortedIDs(a), sortedIDs(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}
