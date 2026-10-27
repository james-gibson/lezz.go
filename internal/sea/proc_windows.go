//go:build !unix

package sea

import (
	"os"
	"os/exec"
)

func setProcGroup(*exec.Cmd) {}

func terminateGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killProcessGroup(cmd *exec.Cmd) { terminateGroup(cmd) }

func terminatePID(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func processName(int) string { return "" }
