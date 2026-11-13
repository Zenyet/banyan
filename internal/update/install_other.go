//go:build !darwin && !windows && !linux

package update

import (
	"errors"
	"runtime"

	"github.com/Zenyet/banyan/internal/i18n"
)

func installAsset(file, dir, version string) error {
	return errors.New(i18n.T("Updates are not supported on ") + runtime.GOOS)
}
