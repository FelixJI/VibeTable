package objectrepo

import (
	"context"
	"errors"
	"testing"
)

type memoryVault map[string][]byte

func (vault memoryVault) Read(_ context.Context, name string) ([]byte, error) {
	value, ok := vault[name]
	if !ok {
		return nil, ErrKeyMissing
	}
	return append([]byte(nil), value...), nil
}
func (vault memoryVault) Write(_ context.Context, name string, value []byte) error {
	vault[name] = append([]byte(nil), value...)
	return nil
}
func (vault memoryVault) Delete(_ context.Context, name string) error {
	delete(vault, name)
	return nil
}

func TestKeyModesNeverMisrepresentConvenientAsConfidential(t *testing.T) {
	provider := NewKeyProvider(memoryVault{})
	convenient, err := provider.Open(context.Background(), "w", EncryptionConvenient)
	if err != nil || convenient.Confidential || string(convenient.Password) != ConvenientPassword ||
		convenient.Warning == "" {
		t.Fatalf("unsafe convenient mode: %#v %v", convenient, err)
	}
	none, err := provider.Open(context.Background(), "w", EncryptionNone)
	if err != nil || none.Confidential || string(none.Password) != NoneFormatPassword ||
		none.Warning == "" {
		t.Fatalf("unsafe none mode: %#v %v", none, err)
	}
}

func TestProtectedKeyRequiresVaultAndRotates(t *testing.T) {
	ctx := context.Background()
	if _, err := NewKeyProvider(nil).Open(ctx, "w", EncryptionProtected); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("missing vault accepted: %v", err)
	}
	provider := NewKeyProvider(memoryVault{})
	first, err := provider.CreateProtected(ctx, "w")
	if err != nil || !first.Confidential || len(first.RecoveryKey) != 32 ||
		string(first.RecoveryKey) != string(first.Password) {
		t.Fatalf("create failed: %#v %v", first, err)
	}
	opened, err := provider.Open(ctx, "w", EncryptionProtected)
	if err != nil || string(opened.Password) != string(first.Password) {
		t.Fatalf("vault reopen failed: %#v %v", opened, err)
	}
	if _, err := provider.RotateProtected(ctx, "w"); !errors.Is(err, ErrRotationPlanRequired) {
		t.Fatalf("unsafe direct rotation remained available: %v", err)
	}
	plan, err := provider.PreviewProtectedRotation(ctx, "w")
	if err != nil || string(plan.NewPassword) == string(first.Password) {
		t.Fatalf("rotation preview failed: %#v %v", plan, err)
	}
	rekeyCalls := 0
	second, err := provider.ApplyProtectedRotation(
		ctx,
		plan,
		func(_ context.Context, oldPassword, newPassword []byte) error {
			rekeyCalls++
			if !equalSecret(oldPassword, first.Password) || !equalSecret(newPassword, plan.NewPassword) {
				t.Fatal("rotation received unexpected key material")
			}
			return nil
		},
		func(_ context.Context, password []byte) error {
			if !equalSecret(password, plan.NewPassword) {
				t.Fatal("verification did not use the candidate key")
			}
			return nil
		},
	)
	if err != nil || rekeyCalls != 1 || !equalSecret(second.Password, plan.NewPassword) {
		t.Fatalf("rotation apply failed: %#v %v", second, err)
	}
}

func TestProtectedRotationRollsRepositoryBackBeforeKeepingOldVaultKey(t *testing.T) {
	ctx := context.Background()
	provider := NewKeyProvider(memoryVault{})
	first, err := provider.CreateProtected(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := provider.PreviewProtectedRotation(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	rekeyCalls := 0
	_, err = provider.ApplyProtectedRotation(
		ctx,
		plan,
		func(context.Context, []byte, []byte) error {
			rekeyCalls++
			return nil
		},
		func(context.Context, []byte) error {
			return errors.New("independent reopen failed")
		},
	)
	if err == nil || rekeyCalls != 2 {
		t.Fatalf("failed rotation was not rolled back: calls=%d err=%v", rekeyCalls, err)
	}
	opened, err := provider.Open(ctx, "w", EncryptionProtected)
	if err != nil || !equalSecret(opened.Password, first.Password) {
		t.Fatalf("vault key changed after failed rotation: %#v %v", opened, err)
	}
}

func TestRestoreProtectedVerifiesBeforeWritingVault(t *testing.T) {
	ctx := context.Background()
	vault := memoryVault{}
	provider := NewKeyProvider(vault)
	key := []byte("01234567890123456789012345678901")
	_, err := provider.RestoreProtected(
		ctx,
		"w",
		key,
		func(context.Context, []byte) error {
			return errors.New("wrong recovery key")
		},
	)
	if err == nil {
		t.Fatal("wrong recovery key accepted")
	}
	if _, exists := vault[vaultName("w")]; exists {
		t.Fatal("failed verification modified the vault")
	}
	material, err := provider.RestoreProtected(
		ctx,
		"w",
		key,
		func(_ context.Context, candidate []byte) error {
			if !equalSecret(candidate, key) {
				t.Fatal("verification did not receive the recovery key")
			}
			return nil
		},
	)
	if err != nil || !material.Confidential {
		t.Fatalf("verified recovery failed: %#v %v", material, err)
	}
	opened, err := provider.Open(ctx, "w", EncryptionProtected)
	if err != nil || !equalSecret(opened.Password, key) {
		t.Fatalf("restored vault key did not reopen: %#v %v", opened, err)
	}
}
