//go:build !windows

package ai

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in a group of its own, so that
// cancelling it ends the agent and everything it started, such as npx's
// node.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
