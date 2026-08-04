//go:build windows

package winapi

import (
	"errors"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x1
	lockFileExclusiveLock   = 0x2
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	moveFileExW  = kernel32.NewProc("MoveFileExW")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func MoveFileEx(source *uint16, destination *uint16, flags uint32) error {
	ok, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(source)),
		uintptr(unsafe.Pointer(destination)),
		uintptr(flags),
	)
	if ok == 0 {
		return callErr
	}
	return nil
}

func TryLockFile(handle uintptr) (bool, error) {
	overlapped := new(syscall.Overlapped)
	ok, _, callErr := lockFileEx.Call(
		handle,
		lockFileExclusiveLock|lockFileFailImmediately,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if ok != 0 {
		return true, nil
	}
	if errors.Is(callErr, errorLockViolation) {
		return false, nil
	}
	return false, callErr
}

func UnlockFile(handle uintptr) error {
	overlapped := new(syscall.Overlapped)
	ok, _, callErr := unlockFileEx.Call(
		handle,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if ok == 0 {
		return callErr
	}
	return nil
}
