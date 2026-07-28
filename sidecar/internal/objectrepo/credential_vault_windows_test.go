//go:build windows

package objectrepo

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

func TestWindowsCredentialVaultContract(t *testing.T) {
	if os.Getenv("VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER") != "1" {
		t.Skip("set VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER=1 on the Windows release runner")
	}
	vault := WindowsCredentialVault{}
	name := "VibeTable/tests/" + time.Now().UTC().Format("20060102T150405.000000000")
	value := []byte("credential-manager-contract-value")
	t.Cleanup(func() {
		_ = vault.Delete(context.Background(), name)
	})
	if err := vault.Write(context.Background(), name, value); err != nil {
		t.Fatal(err)
	}
	actual, err := vault.Read(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, value) {
		t.Fatalf("credential changed: %q", actual)
	}
	if err := vault.Delete(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Read(context.Background(), name); err != ErrKeyMissing {
		t.Fatalf("deleted credential remained readable: %v", err)
	}
}
