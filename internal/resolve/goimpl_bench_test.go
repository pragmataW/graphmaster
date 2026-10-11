package resolve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// BenchmarkGoImplPass runs the Go implements pass on an existing index:
// GRAPHMASTER_BENCH_DIR names a checkout whose .graphmaster/graph.db is
// copied to a temporary directory first.
func BenchmarkGoImplPass(b *testing.B) {
	src := os.Getenv("GRAPHMASTER_BENCH_DIR")
	if src == "" {
		b.Skip("GRAPHMASTER_BENCH_DIR not set")
	}
	dir := b.TempDir()
	data, err := os.ReadFile(filepath.Join(src, ".graphmaster", store.GraphDBName))
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, store.GraphDBName), data, 0o644); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	rc := model.NewMemRepo(nil)
	b.ResetTimer()
	for range b.N {
		w, err := st.NewWriter(ctx, store.WriterOptions{})
		if err != nil {
			b.Fatal(err)
		}
		s := newSession(ctx, w, rc, Options{})
		var stats Stats
		if err := goImplPass(s, w, &stats); err != nil {
			b.Fatal(err)
		}
		s.close()
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
