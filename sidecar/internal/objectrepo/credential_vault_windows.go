//go:build windows

package objectrepo

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credentialTypeGeneric      = 1
	credentialPersistLocalHost = 2
)

type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	credWriteW     = advapi32.NewProc("CredWriteW")
	credReadW      = advapi32.NewProc("CredReadW")
	credDeleteW    = advapi32.NewProc("CredDeleteW")
	credFree       = advapi32.NewProc("CredFree")
	errCredMissing = windows.Errno(1168)
)

// WindowsCredentialVault stores protected repository keys in the current
// user's Windows Credential Manager. The recovery key returned at workspace
// creation remains the portable fallback when this device credential is lost.
type WindowsCredentialVault struct{}

func (WindowsCredentialVault) Read(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var credential *windowsCredential
	ok, _, callErr := credReadW.Call(
		uintptr(unsafe.Pointer(target)),
		credentialTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if ok == 0 {
		if errors.Is(callErr, errCredMissing) {
			return nil, ErrKeyMissing
		}
		return nil, fmt.Errorf("read Windows credential: %w", callErr)
	}
	defer credFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential == nil || credential.CredentialBlobSize == 0 {
		return nil, ErrKeyMissing
	}
	value := unsafe.Slice(credential.CredentialBlob, credential.CredentialBlobSize)
	return append([]byte(nil), value...), nil
}

func (WindowsCredentialVault) Write(ctx context.Context, name string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(value) == 0 {
		return ErrKeyMissing
	}
	target, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	username, err := windows.UTF16PtrFromString("VibeTable")
	if err != nil {
		return err
	}
	credential := windowsCredential{
		Type:               credentialTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(value)),
		CredentialBlob:     &value[0],
		Persist:            credentialPersistLocalHost,
		UserName:           username,
	}
	ok, _, callErr := credWriteW.Call(uintptr(unsafe.Pointer(&credential)), 0)
	if ok == 0 {
		return fmt.Errorf("write Windows credential: %w", callErr)
	}
	return nil
}

func (WindowsCredentialVault) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	ok, _, callErr := credDeleteW.Call(
		uintptr(unsafe.Pointer(target)),
		credentialTypeGeneric,
		0,
	)
	if ok == 0 && !errors.Is(callErr, errCredMissing) {
		return fmt.Errorf("delete Windows credential: %w", callErr)
	}
	return nil
}
