package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// lockRetry is the polling interval while waiting for the write lock.
const lockRetry = 10 * time.Millisecond

// Lock is the cross-process single-writer lock (.graphmaster/write.lock).
// It is an advisory flock released automatically when the process dies.
// A Lock is not safe for concurrent use by multiple goroutines.
type Lock struct {
	fl *flock.Flock
}

// NewLock returns the write lock for the .graphmaster directory dir.
func NewLock(dir string) *Lock {
	return &Lock{fl: flock.New(filepath.Join(dir, LockName))}
}

// Path returns the lock file path.
func (l *Lock) Path() string { return l.fl.Path() }

// TryLock acquires the lock, waiting at most budget. A budget <= 0 tries
// exactly once. It returns (false, nil) when the budget runs out and an
// error only for I/O failures or when ctx itself is done.
func (l *Lock) TryLock(ctx context.Context, budget time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(l.fl.Path()), 0o755); err != nil {
		return false, err
	}
	if budget <= 0 {
		return l.fl.TryLock()
	}
	wctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	ok, err := l.fl.TryLockContext(wctx, lockRetry)
	if err != nil && !ok {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return false, nil
		}
	}
	return ok, err
}

// Held reports whether this Lock currently holds the lock.
func (l *Lock) Held() bool { return l.fl.Locked() }

// Unlock releases the lock. It is a no-op when not held.
func (l *Lock) Unlock() error { return l.fl.Unlock() }
