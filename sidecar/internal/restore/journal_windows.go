//go:build windows

package restore

import (
	"path/filepath"
	"syscall"

	"github.com/vibetable/vibetable/sidecar/internal/winapi"
)

func replaceJournalFile(source, destination string) error {
	from, err := syscall.UTF16PtrFromString(filepath.Clean(source))
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(filepath.Clean(destination))
	if err != nil {
		return err
	}
	return winapi.MoveFileEx(
		from,
		to,
		winapi.MoveFileReplaceExisting|winapi.MoveFileWriteThrough,
	)
}

func syncDirectory(string) error {
	// MOVEFILE_WRITE_THROUGH provides the durable replacement boundary on
	// Windows. Directory handles cannot be fsynced through os.File.
	return nil
}
