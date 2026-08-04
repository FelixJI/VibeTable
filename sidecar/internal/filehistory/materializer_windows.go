//go:build windows

package filehistory

import (
	"path/filepath"
	"syscall"

	"github.com/vibetable/vibetable/sidecar/internal/winapi"
)

func replaceMaterializedFile(source string, destination string) error {
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

func syncDirectoryDurable(string) error {
	return nil
}

func pathHasReparsePoint(path string) bool {
	value, err := syscall.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return true
	}
	attributes, err := syscall.GetFileAttributes(value)
	return err != nil ||
		attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
