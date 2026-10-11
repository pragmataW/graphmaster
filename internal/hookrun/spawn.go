package hookrun

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/store"
)

// SpawnIndex starts `<this executable> index --quiet` in root as a detached
// process (its own session, no stdio) and does not wait for it. It is the
// one-shot background build agents that cannot block use at session start
// (PLAN step 10); it is not a daemon.
func SpawnIndex(root string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("spawn index: %w", err)
	}
	cmd := exec.Command(exe, "index", "--quiet")
	cmd.Dir = root
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn index: %w", err)
	}
	return cmd.Process.Release()
}

// writerBusy reports whether another process holds the index write lock of
// loc, i.e. a build or refresh is already running there.
func writerBusy(loc *index.Location) bool {
	if err := loc.Prepare(); err != nil {
		return false
	}
	lock := store.NewLock(loc.Dir)
	ok, err := lock.TryLock(context.Background(), 0)
	if err != nil || !ok {
		return err == nil
	}
	_ = lock.Unlock()
	return false
}
