package parse

import (
	"context"
	"os"
	"runtime"
	"syscall"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

// maxRSS returns the peak resident set size of the process in bytes.
func maxRSS(t *testing.T) int64 {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Skipf("getrusage: %v", err)
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) //nolint:unconvert // Maxrss is int32 on some platforms; bytes on macOS
	}
	return int64(ru.Maxrss) * 1024 //nolint:unconvert // kilobytes on Linux
}

// TestNoNativeLeak parses 10k files and checks that peak RSS stays bounded.
// Every Parse creates and closes a tree; a missing Close (tree, cursor,
// parser, query) or a leaked handle would grow native memory linearly
// (a ~5 KiB tree per parse => ~50 MiB over 10k parses).
func TestNoNativeLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	if runtime.GOOS == "windows" {
		t.Skip("rusage unavailable")
	}
	src, err := os.ReadFile(goldenRoot + "/go/server.go")
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine()
	defer e.Close()
	ctx := context.Background()
	parse := func(n int) {
		for range n {
			if _, err := e.ParseLang(ctx, model.LangGo, src); err != nil {
				t.Fatal(err)
			}
		}
	}

	parse(1000) // warm up allocator arenas and caches
	runtime.GC()
	base := maxRSS(t)
	parse(10_000)
	runtime.GC()
	grown := maxRSS(t) - base
	t.Logf("peak RSS grew %d KiB over 10k parses", grown/1024)
	if limit := int64(24 << 20); grown > limit {
		t.Fatalf("peak RSS grew %d MiB over 10k parses (limit %d MiB): native leak?", grown>>20, limit>>20)
	}
}

// TestEnginesReleaseState creates and closes many engines (parser + cursor
// per language each time) to catch leaks on the engine lifecycle path.
func TestEnginesReleaseState(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	src := []byte("package p\nfunc F() { G() }\n")
	run := func(n int) {
		for range n {
			e := NewEngine()
			if _, err := e.ParseLang(context.Background(), model.LangGo, src); err != nil {
				t.Fatal(err)
			}
			e.Close()
		}
	}
	run(500)
	runtime.GC()
	base := maxRSS(t)
	run(5000)
	runtime.GC()
	if grown := maxRSS(t) - base; grown > 24<<20 {
		t.Fatalf("peak RSS grew %d MiB over 5k engine lifecycles", grown>>20)
	}
}
