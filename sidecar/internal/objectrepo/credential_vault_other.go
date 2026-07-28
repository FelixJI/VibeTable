//go:build !windows

package objectrepo

import (
	"context"
	"errors"
)

var ErrCredentialVaultUnsupported = errors.New("repository.credential_vault_unsupported")

type WindowsCredentialVault struct{}

func (WindowsCredentialVault) Read(context.Context, string) ([]byte, error) {
	return nil, ErrCredentialVaultUnsupported
}

func (WindowsCredentialVault) Write(context.Context, string, []byte) error {
	return ErrCredentialVaultUnsupported
}

func (WindowsCredentialVault) Delete(context.Context, string) error {
	return ErrCredentialVaultUnsupported
}
