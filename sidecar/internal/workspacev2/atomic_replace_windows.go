//go:build windows

package workspacev2

import (
	"path/filepath"
	"syscall"

	"github.com/vibetable/vibetable/sidecar/internal/winapi"
)

func replaceGrantedFile(source string, destination string) error {
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
