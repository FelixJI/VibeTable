//go:build windows

package workspacev2

import (
	"syscall"

	"github.com/vibetable/vibetable/sidecar/internal/winapi"
)

func replaceConflictFile(source string, target string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return winapi.MoveFileEx(
		sourcePtr,
		targetPtr,
		winapi.MoveFileReplaceExisting|
			winapi.MoveFileWriteThrough,
	)
}
