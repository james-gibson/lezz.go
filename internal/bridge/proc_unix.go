//go:build unix

package bridge

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the child in its own process group so that signaling the
// group reaches wrapper processes (uvx, npx) and anything they started.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup delivers sig to the child's whole process group.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Negative pid signals the group created by Setpgid, so wrapper commands
	// cannot leave the real server orphaned.
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// terminateGroup asks the child's process group to stop.
func terminateGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }

// killProcessGroup force-kills the child's process group.
func killProcessGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }
