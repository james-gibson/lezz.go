//go:build !windows

package demo

import (
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
)

// processAlive reports whether pid exists. EPERM means it exists but belongs to
// another user, which is still "alive".
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// demoProcessPattern matches the word "demo" in a process command line.
var demoProcessPattern = regexp.MustCompile(`(^|\s|[./])demo(\s|$)`)

// looksLikeDemo checks the command line before signaling a pid from the state
// file, so a reused pid does not receive a stray SIGTERM.
func looksLikeDemo(pid int) bool {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // pid is an int from our own state file; no shell
	if err != nil {
		return false
	}
	return demoProcessPattern.Match(out)
}

// detachSysProcAttr puts the detached demo in its own session so it survives
// the terminal that started it.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// interruptSignal is what `lezz demo stop` sends.
func interruptSignal() os.Signal { return syscall.SIGTERM }
