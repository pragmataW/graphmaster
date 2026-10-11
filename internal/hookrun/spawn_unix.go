//go:build !windows

package hookrun

import "syscall"

// detachedAttr puts the child in its own session so it outlives the hook
// process and never receives the agent's terminal signals.
func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
