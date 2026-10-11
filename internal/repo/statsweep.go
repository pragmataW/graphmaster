package repo

import (
	"context"
	"errors"
	"io/fs"
	"runtime"
	"sync"
	"syscall"
)

// PathStat is the result of lstat-ing one repo-relative path.
type PathStat struct {
	Stat FileStat
	// Exists is false when the path is gone (or a parent is no longer a
	// directory); Stat is then zero.
	Exists bool
	// Err is any other lstat error (permissions, I/O).
	Err error
}

// StatPaths lstats repo-relative paths in parallel (Freshness step 4). The
// result is index-aligned with rels. workers <= 0 picks a default suited to
// metadata-bound I/O.
func StatPaths(ctx context.Context, r *Root, rels []string, workers int) ([]PathStat, error) {
	out := make([]PathStat, len(rels))
	if workers <= 0 {
		workers = min(4*runtime.GOMAXPROCS(0), 64)
	}
	workers = max(1, min(workers, len(rels)))
	const chunk = 256
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		next int
	)
	claim := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		lo := next
		next = min(next+chunk, len(rels))
		return lo, next
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				lo, hi := claim()
				if lo >= hi {
					return
				}
				for i := lo; i < hi; i++ {
					out[i] = statOne(r.Abs(rels[i]))
				}
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func statOne(abs string) PathStat {
	st, err := Lstat(abs)
	switch {
	case err == nil:
		return PathStat{Stat: st, Exists: true}
	case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
		return PathStat{}
	default:
		return PathStat{Err: err}
	}
}

// isNotDir reports ENOTDIR, which lstat returns when a parent path
// component has become a file.
func isNotDir(err error) bool { return errors.Is(err, syscall.ENOTDIR) }
