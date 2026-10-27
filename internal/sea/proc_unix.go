//go:build unix

package sea

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// setProcGroup puts the child in its own process group so signaling reaches
// wrappers (npx, uvx) and anything they started.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

func terminateGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }

func killProcessGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }

func terminatePID(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// processName returns the command name for pid, or "" when it is gone. It is
// used before a pid file is honored: a recycled pid must not be signaled.
func processName(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() //nolint:gosec // fixed binary, argv-only, no shell
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return ""
	}
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		return name[slash+1:]
	}
	return name
}
