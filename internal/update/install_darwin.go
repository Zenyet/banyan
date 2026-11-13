//go:build darwin

package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Zenyet/banyan/internal/i18n"
)

// installAsset expands the zip and starts a helper that replaces the app
// bundle once this process has quit, then opens the new one.
func installAsset(file, dir, version string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(app, ".app") {
		return errors.New(i18n.T("Banyan is not running from an application bundle"))
	}
	if strings.Contains(app, "AppTranslocation") {
		return errors.New(i18n.T("Move Banyan to a folder you can write to, such as Applications, then try again"))
	}
	if err := exec.Command("ditto", "-x", "-k", file, dir).Run(); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("The update could not be unpacked"), err)
	}
	fresh := filepath.Join(dir, "Banyan.app")
	if _, err := os.Stat(fresh); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("The update has no Banyan.app"), err)
	}
	// Replacing the bundle needs its folder writable. Say so before
	// quitting rather than failing silently once the app is gone.
	probe, err := os.CreateTemp(filepath.Dir(app), ".banyan-update-")
	if err != nil {
		return errors.New(i18n.T("Move Banyan to a folder you can write to, such as Applications, then try again"))
	}
	probe.Close()
	os.Remove(probe.Name())

	script := filepath.Join(dir, "update.sh")
	const body = `#!/bin/sh
APP="$1"; NEW="$2"; PID="$3"; DIR="$4"; OLD="$APP.old"
while kill -0 "$PID" 2>/dev/null; do sleep 0.2; done
rm -rf "$OLD"
mv "$APP" "$OLD" || exit 1
if ! ditto "$NEW" "$APP"; then
  rm -rf "$APP"
  mv "$OLD" "$APP"
  open "$APP"
  exit 1
fi
rm -rf "$OLD" "$DIR"
xattr -dr com.apple.quarantine "$APP" 2>/dev/null || true
open "$APP"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", script, app, fresh, strconv.Itoa(os.Getpid()), dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}
