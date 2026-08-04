//go:build windows

package objectrepo

import (
	"syscall"

	"github.com/vibetable/vibetable/sidecar/internal/winapi"
)

func replaceFileDurable(source, destination string) error {
	sourceUTF16, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationUTF16, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return winapi.MoveFileEx(
		sourceUTF16,
		destinationUTF16,
		winapi.MoveFileReplaceExisting|winapi.MoveFileWriteThrough,
	)
}
