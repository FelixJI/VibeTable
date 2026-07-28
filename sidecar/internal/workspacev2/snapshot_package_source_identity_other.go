//go:build !windows

package workspacev2

import (
	"errors"
	"fmt"
	"os"
	"reflect"
)

func snapshotSourceFileIdentity(
	_ *os.File,
	info os.FileInfo,
) (string, error) {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return "", errors.New("snapshot.package_source_identity_unavailable")
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", errors.New(
				"snapshot.package_source_identity_unavailable",
			)
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return "", errors.New("snapshot.package_source_identity_unavailable")
	}
	device, ok := snapshotSourceIdentityNumber(value.FieldByName("Dev"))
	if !ok {
		return "", errors.New("snapshot.package_source_identity_unavailable")
	}
	inode, ok := snapshotSourceIdentityNumber(value.FieldByName("Ino"))
	if !ok {
		return "", errors.New("snapshot.package_source_identity_unavailable")
	}
	return fmt.Sprintf("unix:%x:%x", device, inode), nil
}

func snapshotSourceIdentityNumber(value reflect.Value) (uint64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16,
		reflect.Int32, reflect.Int64:
		number := value.Int()
		if number < 0 {
			return 0, false
		}
		return uint64(number), true
	default:
		return 0, false
	}
}
