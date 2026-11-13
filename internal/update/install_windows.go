//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/Zenyet/banyan/internal/i18n"
)

// installAsset starts a helper that waits for this process to quit, runs
// the installer silently, and opens Banyan again.
func installAsset(file, dir, version string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	script := filepath.Join(dir, "update.cmd")
	const body = `@echo off
setlocal
:wait
tasklist /FI "PID eq %3" /NH | findstr /R /C:"[ ]%3[ ]" >nul
if not errorlevel 1 (
  timeout /t 1 /nobreak >nul
  goto wait
)
start "" /wait "%~1" /SILENT /NORESTART /CLOSEAPPLICATIONS
start "" "%~2"
del "%~1" >nul 2>nul
rmdir /s /q "%~4" >nul 2>nul
`
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("cmd", "/c", script, file, exe, strconv.Itoa(os.Getpid()), dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("The update could not be started"), err)
	}
	return nil
}
