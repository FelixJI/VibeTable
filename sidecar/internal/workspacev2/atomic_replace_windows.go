//go:build windows

package workspacev2

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func replaceGrantedFile(source string, destination string) error {
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
