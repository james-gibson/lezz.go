//go:build windows

package demo

import (
	"os"
	"syscall"
)

// processAlive reports whether pid exists. On Windows os.FindProcess opens the
// process and fails when it is gone.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = proc.Release()
	return true
}

// looksLikeDemo has no cheap command-line probe on Windows; the state file's pid
// is the only check available.
func looksLikeDemo(int) bool { return true }

// detachSysProcAttr detaches the child from the console that started it.
func detachSysProcAttr() *syscall.SysProcAttr {
	const (
		createNewProcessGroup = 0x00000200
		detachedProcess       = 0x00000008
	)
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

// interruptSignal is what `lezz demo stop` sends. Windows has no cross-process
// SIGTERM equivalent.
func interruptSignal() os.Signal { return os.Kill }
