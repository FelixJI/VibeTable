package objectrepo

import (
	"context"
	"crypto/rand"
	"errors"
)

type EncryptionMode string

const (
	EncryptionNone       EncryptionMode = "none"
	EncryptionConvenient EncryptionMode = "convenient"
	EncryptionProtected  EncryptionMode = "protected"

	// ConvenientPassword is intentionally public. This mode prevents accidental
	// format inspection; it does not protect against deliberate access.
	ConvenientPassword = "password"
	// NoneFormatPassword is only a repository-format parameter. It provides no
	// confidentiality and is intentionally embedded in the application.
	NoneFormatPassword = "vibetable-public-format-credential-v1"
)

var (
	ErrKeyMissing           = errors.New("repository.key_missing")
	ErrKeyModeInvalid       = errors.New("repository.key_mode_invalid")
	ErrRotationPlanRequired = errors.New("repository.key_rotation_plan_required")
)

type CredentialVault interface {
	Read(context.Context, string) ([]byte, error)
	Write(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

type KeyMaterial struct {
	Mode         EncryptionMode
	Password     []byte
	RecoveryKey  []byte
	Confidential bool
	Warning      string
}

type KeyProvider struct {
	vault CredentialVault
}

func NewKeyProvider(vault CredentialVault) *KeyProvider {
	return &KeyProvider{vault: vault}
}

func (provider *KeyProvider) Open(ctx context.Context, workspaceID string, mode EncryptionMode) (KeyMaterial, error) {
	switch mode {
	case EncryptionNone:
		return KeyMaterial{
			Mode: mode, Password: []byte(NoneFormatPassword), Confidential: false,
			Warning: "不加密：任何能访问仓库的人都能读取内容。",
		}, nil
	case EncryptionConvenient:
		return KeyMaterial{
			Mode: mode, Password: []byte(ConvenientPassword), Confidential: false,
			Warning: "便捷加密使用公开固定口令，只防格式误读，不能抵抗有意访问。",
		}, nil
	case EncryptionProtected:
		if provider.vault == nil {
			return KeyMaterial{}, ErrKeyMissing
		}
		key, err := provider.vault.Read(ctx, vaultName(workspaceID))
		if err != nil || len(key) < 32 {
			return KeyMaterial{}, ErrKeyMissing
		}
		return KeyMaterial{Mode: mode, Password: append([]byte(nil), key...), Confidential: true}, nil
	default:
		return KeyMaterial{}, ErrKeyModeInvalid
	}
}

func (provider *KeyProvider) CreateProtected(ctx context.Context, workspaceID string) (KeyMaterial, error) {
	if provider.vault == nil {
		return KeyMaterial{}, ErrKeyMissing
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return KeyMaterial{}, err
	}
	if err := provider.vault.Write(ctx, vaultName(workspaceID), key); err != nil {
		return KeyMaterial{}, err
	}
	return KeyMaterial{
		Mode: EncryptionProtected, Password: key,
		RecoveryKey: append([]byte(nil), key...), Confidential: true,
		Warning: "请离线保存恢复密钥；凭据库丢失时需要它恢复。",
	}, nil
}

// RestoreProtected verifies a recovery key against the existing repository
// before writing it to the OS credential vault. A wrong key therefore cannot
// replace or create durable credential state.
func (provider *KeyProvider) RestoreProtected(
	ctx context.Context,
	workspaceID string,
	recoveryKey []byte,
	verify func(context.Context, []byte) error,
) (KeyMaterial, error) {
	if provider.vault == nil || workspaceID == "" ||
		len(recoveryKey) != 32 || verify == nil {
		return KeyMaterial{}, ErrKeyMissing
	}
	candidate := append([]byte(nil), recoveryKey...)
	if err := verify(ctx, candidate); err != nil {
		clearSecret(candidate)
		return KeyMaterial{}, err
	}
	if err := provider.vault.Write(
		ctx,
		vaultName(workspaceID),
		candidate,
	); err != nil {
		clearSecret(candidate)
		return KeyMaterial{}, err
	}
	return KeyMaterial{
		Mode: EncryptionProtected, Password: candidate,
		RecoveryKey:  append([]byte(nil), recoveryKey...),
		Confidential: true,
		Warning:      "恢复密钥已验证并保存到本机凭据库。",
	}, nil
}

func (provider *KeyProvider) DeleteProtected(
	ctx context.Context,
	workspaceID string,
) error {
	if provider.vault == nil || workspaceID == "" {
		return ErrKeyMissing
	}
	return provider.vault.Delete(ctx, vaultName(workspaceID))
}

func (provider *KeyProvider) RotateProtected(ctx context.Context, workspaceID string) (KeyMaterial, error) {
	return KeyMaterial{}, ErrRotationPlanRequired
}

type KeyRotationPlan struct {
	WorkspaceID     string
	CurrentPassword []byte
	NewPassword     []byte
	RecoveryKey     []byte
}

func (provider *KeyProvider) PreviewProtectedRotation(
	ctx context.Context,
	workspaceID string,
) (KeyRotationPlan, error) {
	current, err := provider.Open(ctx, workspaceID, EncryptionProtected)
	if err != nil {
		return KeyRotationPlan{}, err
	}
	next := make([]byte, 32)
	if _, err := rand.Read(next); err != nil {
		return KeyRotationPlan{}, err
	}
	return KeyRotationPlan{
		WorkspaceID: workspaceID, CurrentPassword: current.Password,
		NewPassword: next, RecoveryKey: append([]byte(nil), next...),
	}, nil
}

func (provider *KeyProvider) StageProtectedRotation(
	ctx context.Context,
	plan KeyRotationPlan,
	rotationID string,
) error {
	if provider.vault == nil || plan.WorkspaceID == "" ||
		rotationID == "" || len(plan.NewPassword) < 32 {
		return ErrRotationPlanRequired
	}
	return provider.vault.Write(
		ctx,
		rotationVaultName(plan.WorkspaceID, rotationID),
		plan.NewPassword,
	)
}

func (provider *KeyProvider) OpenStagedProtectedRotation(
	ctx context.Context,
	workspaceID string,
	rotationID string,
) (KeyMaterial, error) {
	if provider.vault == nil || workspaceID == "" || rotationID == "" {
		return KeyMaterial{}, ErrRotationPlanRequired
	}
	key, err := provider.vault.Read(
		ctx,
		rotationVaultName(workspaceID, rotationID),
	)
	if err != nil || len(key) < 32 {
		clearSecret(key)
		return KeyMaterial{}, ErrKeyMissing
	}
	return KeyMaterial{
		Mode: EncryptionProtected, Password: key,
		RecoveryKey: append([]byte(nil), key...), Confidential: true,
	}, nil
}

func (provider *KeyProvider) CommitStagedProtectedRotation(
	ctx context.Context,
	workspaceID string,
	rotationID string,
) error {
	staged, err := provider.OpenStagedProtectedRotation(
		ctx,
		workspaceID,
		rotationID,
	)
	if err != nil {
		return err
	}
	defer clearSecret(staged.Password)
	defer clearSecret(staged.RecoveryKey)
	return provider.vault.Write(
		ctx,
		vaultName(workspaceID),
		staged.Password,
	)
}

func (provider *KeyProvider) DiscardStagedProtectedRotation(
	ctx context.Context,
	workspaceID string,
	rotationID string,
) error {
	if provider.vault == nil || workspaceID == "" || rotationID == "" {
		return ErrRotationPlanRequired
	}
	return provider.vault.Delete(
		ctx,
		rotationVaultName(workspaceID, rotationID),
	)
}

func (provider *KeyProvider) ApplyProtectedRotation(
	ctx context.Context,
	plan KeyRotationPlan,
	rekey func(context.Context, []byte, []byte) error,
	verify func(context.Context, []byte) error,
) (KeyMaterial, error) {
	if provider.vault == nil || plan.WorkspaceID == "" ||
		len(plan.CurrentPassword) == 0 || len(plan.NewPassword) < 32 ||
		rekey == nil || verify == nil {
		return KeyMaterial{}, ErrRotationPlanRequired
	}
	current, err := provider.Open(ctx, plan.WorkspaceID, EncryptionProtected)
	if err != nil || !equalSecret(current.Password, plan.CurrentPassword) {
		return KeyMaterial{}, ErrKeyMissing
	}
	if err := rekey(ctx, plan.CurrentPassword, plan.NewPassword); err != nil {
		return KeyMaterial{}, err
	}
	if err := verify(ctx, plan.NewPassword); err != nil {
		_ = rekey(ctx, plan.NewPassword, plan.CurrentPassword)
		return KeyMaterial{}, err
	}
	if err := provider.vault.Write(ctx, vaultName(plan.WorkspaceID), plan.NewPassword); err != nil {
		_ = rekey(ctx, plan.NewPassword, plan.CurrentPassword)
		return KeyMaterial{}, err
	}
	return KeyMaterial{
		Mode: EncryptionProtected, Password: append([]byte(nil), plan.NewPassword...),
		RecoveryKey: append([]byte(nil), plan.RecoveryKey...), Confidential: true,
		Warning: "密钥已轮换；请用新的恢复密钥替换旧副本。",
	}, nil
}

func equalSecret(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func clearSecret(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func vaultName(workspaceID string) string {
	return "VibeTable/workspace/" + workspaceID + "/repository"
}

func rotationVaultName(workspaceID string, rotationID string) string {
	return vaultName(workspaceID) + "/rotation/" + rotationID
}
