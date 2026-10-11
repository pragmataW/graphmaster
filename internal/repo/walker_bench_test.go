package repo

import (
	"context"
	"fmt"
	"testing"
)

// BenchmarkWalk20k walks 20k untracked files in 1000 directories, with and
// without a warm directory cache.
func BenchmarkWalk20k(b *testing.B) {
	dir := newRepo(b)
	files := map[string]string{".gitignore": "*.log\n"}
	for i := range 20_000 {
		files[fmt.Sprintf("p%02d/d%02d/f%05d.go", i%40, (i/40)%25, i)] = "package p\n"
	}
	writeFiles(b, dir, files)
	r, err := FindRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	ig, err := NewIgnore(r, LoadGitConfig(r), []string{"vendor/"})
	if err != nil {
		b.Fatal(err)
	}
	backdateTB(b, dir)
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("cache=%v", cached), func(b *testing.B) {
			var cache *DirCache
			if cached {
				cache = NewDirCache()
				if _, err := Walk(context.Background(), WalkOptions{Root: r, Ignore: ig, Cache: cache}); err != nil {
					b.Fatal(err)
				}
			}
			for b.Loop() {
				res, err := Walk(context.Background(), WalkOptions{Root: r, Ignore: ig, Cache: cache})
				if err != nil {
					b.Fatal(err)
				}
				if len(res.Untracked) != 20_001 {
					b.Fatalf("untracked = %d", len(res.Untracked))
				}
			}
		})
	}
}
