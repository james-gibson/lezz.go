//go:build windows

package bridge

import (
	"os/exec"
	"syscall"
)

// setProcGroup starts the child in its own process group so a later stop does
// not signal the console process group lezz itself belongs to.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminateGroup stops the child. Windows has no group-wide SIGTERM, so this is
// a direct kill of the child process.
func terminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

// killProcessGroup force-kills the child. As with terminateGroup, this cannot reach
// grandchildren on Windows.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
