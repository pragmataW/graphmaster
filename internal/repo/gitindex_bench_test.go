package repo

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/format/index"
)

// BenchmarkTrackedDecode100k measures reading and decoding a 100k-entry
// index file from disk. Target: < 40ms/op.
func BenchmarkTrackedDecode100k(b *testing.B) {
	for _, v := range []uint32{2, 4} {
		var buf bytes.Buffer
		if err := index.NewEncoder(&buf).Encode(synthIndex(b, 100_000, v)); err != nil {
			b.Fatal(err)
		}
		path := filepath.Join(b.TempDir(), "index")
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			b.Fatal(err)
		}
		name := map[uint32]string{2: "v2", 4: "v4"}[v]
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(buf.Len()))
			b.ReportAllocs()
			for b.Loop() {
				data, err := os.ReadFile(path)
				if err != nil {
					b.Fatal(err)
				}
				ix, err := DecodeIndex(data)
				if err != nil {
					b.Fatal(err)
				}
				if len(ix.Entries) != 100_000 {
					b.Fatalf("entries = %d", len(ix.Entries))
				}
			}
		})
	}
}
