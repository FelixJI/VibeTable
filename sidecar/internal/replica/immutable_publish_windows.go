//go:build windows

package replica

import "syscall"

// publishImmutable moves a completed temporary file into place without
// replacing an existing target. MoveFile works on Windows filesystems that do
// not support hard links, including common removable-drive formats.
func publishImmutable(source string, target string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return syscall.MoveFile(sourcePtr, targetPtr)
}
