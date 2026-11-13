//go:build linux

package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/Zenyet/banyan/internal/i18n"
)

// installAsset expands the tarball, replaces this binary, and starts the
// new one. A Banyan installed by a package manager lives where only root
// can write; it says to download that update instead.
func installAsset(file, dir, version string) error {
	if err := exec.Command("tar", "-xzf", file, "-C", dir).Run(); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("The update could not be unpacked"), err)
	}
	var fresh string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || fresh != "" || d.IsDir() {
			return nil
		}
		if d.Name() == "banyan" {
			fresh = path
		}
		return nil
	})
	if fresh == "" {
		return errors.New(i18n.T("The update has no Banyan program"))
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Replacing the binary needs its folder writable. Say so before
	// quitting rather than failing silently once the app is gone.
	probe, err := os.CreateTemp(filepath.Dir(exe), ".banyan-update-")
	if err != nil {
		return errors.New(i18n.T("Banyan cannot replace itself here; download the update from the release page"))
	}
	probe.Close()
	os.Remove(probe.Name())

	script := filepath.Join(dir, "update.sh")
	const body = `#!/bin/sh
EXE="$1"; NEW="$2"; PID="$3"; DIR="$4"; OLD="$EXE.old"
while kill -0 "$PID" 2>/dev/null; do sleep 0.2; done
rm -f "$OLD"
mv "$EXE" "$OLD" || exit 1
if ! cp "$NEW" "$EXE"; then
  mv "$OLD" "$EXE"
  exit 1
fi
chmod 755 "$EXE"
rm -f "$OLD"
rm -rf "$DIR"
if command -v setsid >/dev/null 2>&1; then
  setsid "$EXE" >/dev/null 2>&1 < /dev/null &
else
  nohup "$EXE" >/dev/null 2>&1 < /dev/null &
fi
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", script, exe, fresh, strconv.Itoa(os.Getpid()), dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}
