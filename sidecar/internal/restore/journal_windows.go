//go:build windows

package restore

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func replaceJournalFile(source, destination string) error {
	from, err := windows.UTF16PtrFromString(filepath.Clean(source))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Clean(destination))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		from,
		to,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

func syncDirectory(string) error {
	// MOVEFILE_WRITE_THROUGH provides the durable replacement boundary on
	// Windows. Directory handles cannot be fsynced through os.File.
	return nil
}
