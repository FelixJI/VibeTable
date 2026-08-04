//go:build !windows

package winapi

import "errors"

var errUnsupported = errors.New("winapi.unsupported_platform")

func MoveFileEx(*uint16, *uint16, uint32) error {
	return errUnsupported
}

func TryLockFile(uintptr) (bool, error) {
	return false, errUnsupported
}

func UnlockFile(uintptr) error {
	return errUnsupported
}
