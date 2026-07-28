//go:build windows

package filehistory

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func replaceMaterializedFile(source string, destination string) error {
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

func syncDirectoryDurable(string) error {
	return nil
}

func pathHasReparsePoint(path string) bool {
	value, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(value)
	return err != nil ||
		attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
