package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// helperEnv names the repository a re-executed test binary should index.
const helperEnv = "GRAPHMASTER_INDEX_TEST_HELPER"

// TestIndexHelperProcess is not a real test: re-executed with helperEnv set
// it runs one Freshen like `graphmaster index` would and prints the result.
func TestIndexHelperProcess(t *testing.T) {
	dir := os.Getenv(helperEnv)
	if dir == "" {
		t.Skip("helper process only")
	}
	ctx := context.Background()
	ix, err := Open(ctx, dir, Options{Resolver: &fakeResolver{}, BatchFiles: 5, Workers: 2})
	if err != nil {
		fmt.Println("ERROR", err)
		os.Exit(2)
	}
	res, err := ix.Freshen(ctx, time.Minute)
	if err != nil {
		fmt.Println("ERROR", err)
		os.Exit(2)
	}
	fmt.Printf("RESULT full=%v added=%d modified=%d\n", res.FullBuild, res.Added, res.Modified)
	_ = ix.Close()
	os.Exit(0)
}

func helperCmd(dir string, env ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestIndexHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), append([]string{helperEnv + "=" + dir}, env...)...)
	return cmd
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// TestCrashRecovery kills the indexer after a few committed batches, first
// during the initial bulk build and then during an incremental update; the
// next Freshen must converge to exactly the from-scratch graph.
func TestCrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns subprocesses")
	}
	dir := testutil.Materialize(t)
	out, err := helperCmd(dir, EnvCrashAfterBatch+"=3").CombinedOutput()
	if code := exitCode(err); code != crashExitCode {
		t.Fatalf("helper exit %d, want simulated crash %d:\n%s", code, crashExitCode, out)
	}

	ctx := context.Background()
	ix := openIx(t, dir, Options{})
	st := ix.Store()
	if v, _, _ := st.Meta(ctx, store.MetaBulk); v != "1" {
		t.Fatalf("expected an interrupted bulk build, bulk=%q", v)
	}
	if v, _, _ := st.Meta(ctx, store.MetaState); v != store.StateBuilding {
		t.Fatalf("state after crash = %q", v)
	}
	var files int
	_ = st.DB().QueryRow(`SELECT count(*) FROM files`).Scan(&files)
	if files != 15 {
		t.Fatalf("files committed before crash = %d, want 3 batches of 5", files)
	}
	res := freshen(t, ix)
	if res.FullBuild || res.Added == 0 {
		t.Fatalf("recovery run: %+v", res)
	}
	want := scratchDump(t, dir, Options{})
	requireSameDump(t, "bulk crash recovery", dump(t, ix), want)

	// Crash in the middle of an incremental update.
	o := &oracle{t: t, dir: dir}
	for i, p := range o.goFiles() {
		if i%2 == 0 {
			b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
			writeFile(t, dir, p, string(b)+fmt.Sprintf("\nfunc CrashGen%d() { Helper() }\n", i))
		}
	}
	ix.Close()
	out, err = helperCmd(dir, EnvCrashAfterBatch+"=1").CombinedOutput()
	if code := exitCode(err); code != crashExitCode {
		t.Fatalf("incremental helper exit %d:\n%s", code, out)
	}
	ix2 := openIx(t, dir, Options{})
	freshen(t, ix2)
	requireSameDump(t, "incremental crash recovery", dump(t, ix2), scratchDump(t, dir, Options{}))
}

// TestConcurrentIndexProcesses runs two `index` processes at once: one
// builds, the other waits for the lock, re-scans and finds nothing to do.
func TestConcurrentIndexProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns subprocesses")
	}
	dir := testutil.Materialize(t)
	var (
		wg   sync.WaitGroup
		outs [2]bytes.Buffer
		errs [2]error
	)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := helperCmd(dir)
			cmd.Stdout, cmd.Stderr = &outs[i], &outs[i]
			errs[i] = cmd.Run()
		}()
	}
	wg.Wait()
	full := 0
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("process %d: %v\n%s", i, errs[i], outs[i].String())
		}
		if strings.Contains(outs[i].String(), "RESULT full=true") {
			full++
		}
	}
	if full != 1 {
		t.Fatalf("want exactly one full build, got %d:\n%s\n%s", full, outs[0].String(), outs[1].String())
	}
	ix := openIx(t, dir, Options{})
	if res := freshen(t, ix); res.Changed() != 0 {
		t.Fatalf("graph not complete after both processes: %+v", res)
	}
	requireSameDump(t, "two processes", dump(t, ix), scratchDump(t, dir, Options{}))
}
