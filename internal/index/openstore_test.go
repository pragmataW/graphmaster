package index

import (
	"context"
	"sync"
	"testing"
)

// TestConcurrentOpenStore guards against SQLITE_BUSY when several
// processes open a fresh graph.db at once (see also store's
// TestConcurrentOpenFresh).
func TestConcurrentOpenStore(t *testing.T) {
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for j := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := openStore(context.Background(), dir)
				errs[j] = err
				if err == nil {
					s.Close()
				}
			}()
		}
		wg.Wait()
		for _, e := range errs {
			if e != nil {
				t.Fatalf("iter %d: %v", i, e)
			}
		}
	}
}
