package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// lockHelperSentinel marks a re-exec'd test binary that should act as the
// second process in TestLockTwoProcesses.
const lockHelperSentinel = "graphmaster-store-lock-helper"

// TestLockHelperProcess is not a real test: when re-executed with the
// sentinel after "--" it acquires the lock in dir, reports "locked" on
// stdout and holds the lock until stdin is closed.
func TestLockHelperProcess(t *testing.T) {
	args := os.Args
	for i, a := range args {
		if a == "--" && i+2 < len(args) && args[i+1] == lockHelperSentinel {
			runLockHelper(args[i+2])
			return
		}
	}
	t.Skip("helper process only")
}

func runLockHelper(dir string) {
	l := NewLock(dir)
	ok, err := l.TryLock(context.Background(), 5*time.Second)
	if err != nil || !ok {
		fmt.Printf("failed %v %v\n", ok, err)
		os.Exit(2)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin) // hold until parent closes stdin
	_ = l.Unlock()
	os.Exit(0)
}

func startLockHelper(t *testing.T, dir string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$", "--", lockHelperSentinel, dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper said %q (%v)", line, err)
	}
	return cmd, stdin
}

func TestLockTwoProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	dir := t.TempDir()
	ctx := context.Background()
	cmd, stdin := startLockHelper(t, dir)

	l := NewLock(dir)
	start := time.Now()
	ok, err := l.TryLock(ctx, 100*time.Millisecond)
	if err != nil || ok {
		t.Fatalf("TryLock while held elsewhere = %v %v", ok, err)
	}
	if el := time.Since(start); el < 90*time.Millisecond {
		t.Fatalf("budget not honored: %v", el)
	}
	if ok, _ := l.TryLock(ctx, 0); ok {
		t.Fatal("zero-budget TryLock succeeded while held")
	}

	_ = stdin.Close() // helper releases and exits
	ok, err = l.TryLock(ctx, 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("TryLock after release = %v %v", ok, err)
	}
	if !l.Held() {
		t.Fatal("Held() false after acquire")
	}
	_ = cmd.Wait()
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	if l.Held() {
		t.Fatal("Held() true after Unlock")
	}
}

func TestLockReleasedWhenHolderDies(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	dir := t.TempDir()
	cmd, _ := startLockHelper(t, dir)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	l := NewLock(dir)
	ok, err := l.TryLock(context.Background(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("lock not released by dead holder: %v %v", ok, err)
	}
	_ = l.Unlock()
}

func TestLockParentContextCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	dir := t.TempDir()
	cmd, stdin := startLockHelper(t, dir)
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel) // cancel while waiting
	start := time.Now()
	ok, err := NewLock(dir).TryLock(ctx, 5*time.Second)
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation did not interrupt the wait")
	}
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("TryLock with cancelled ctx = %v %v", ok, err)
	}
}
