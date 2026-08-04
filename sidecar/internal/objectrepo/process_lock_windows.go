//go:build windows

package objectrepo

import (
	"os"

	"github.com/vibetable/vibetable/sidecar/internal/winapi"
)

func tryPlatformFileLock(file *os.File) (bool, error) {
	return winapi.TryLockFile(file.Fd())
}

func unlockPlatformFile(file *os.File) error {
	return winapi.UnlockFile(file.Fd())
}
