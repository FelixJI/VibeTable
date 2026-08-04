//go:build windows

package workspacev2

import (
	"fmt"
	"os"
	"syscall"
)

func snapshotSourceFileIdentity(
	file *os.File,
	_ os.FileInfo,
) (string, error) {
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(
		syscall.Handle(file.Fd()),
		&information,
	); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"windows:%08x:%08x%08x",
		information.VolumeSerialNumber,
		information.FileIndexHigh,
		information.FileIndexLow,
	), nil
}
