//go:build windows

package git

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps git from opening a console window next to the app.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
