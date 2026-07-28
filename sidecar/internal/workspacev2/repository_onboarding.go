package workspacev2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

type RepositoryInitialization struct {
	WorkspaceID    string
	EncryptionMode objectrepo.EncryptionMode
	RecoveryKey    []byte
}

// InitializeRepository is a one-shot onboarding seam. It validates the
// manifest before writing, refuses every pre-existing repository, and returns a
// protected recovery key only on the process' trusted stdout channel.
func InitializeRepository(
	ctx context.Context,
	dataDir string,
	workspaceID string,
	fenceEpoch uint64,
	claimID string,
) (_ RepositoryInitialization, err error) {
	paths, manifest, err := validateBinding(dataDir, workspaceID)
	if err != nil {
		return RepositoryInitialization{}, err
	}
	if manifest.RepositoryFormat != "kopia-v3" {
		return RepositoryInitialization{}, errors.New(
			"repository.format_unsupported",
		)
	}
	configPath := filepath.Join(
		paths.coordination,
		"kopia.repository.config",
	)
	storagePath := filepath.Join(paths.objects, "kopia")
	if _, statErr := os.Lstat(configPath); statErr == nil {
		return RepositoryInitialization{}, errors.New(
			"repository.already_initialized",
		)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return RepositoryInitialization{}, statErr
	}
	if entries, readErr := os.ReadDir(storagePath); readErr == nil &&
		len(entries) != 0 {
		return RepositoryInitialization{}, errors.New(
			"repository.already_initialized",
		)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return RepositoryInitialization{}, readErr
	}
	if err := os.MkdirAll(paths.coordination, 0o700); err != nil {
		return RepositoryInitialization{}, err
	}
	if err := os.MkdirAll(paths.objects, 0o700); err != nil {
		return RepositoryInitialization{}, err
	}
	mode := objectrepo.EncryptionMode(manifest.EncryptionMode)
	provider := objectrepo.NewKeyProvider(
		objectrepo.WindowsCredentialVault{},
	)
	var keys objectrepo.KeyMaterial
	if mode == objectrepo.EncryptionProtected {
		keys, err = provider.CreateProtected(ctx, workspaceID)
	} else {
		keys, err = provider.Open(ctx, workspaceID, mode)
	}
	if err != nil {
		return RepositoryInitialization{}, err
	}
	defer clearBytes(keys.Password)
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		_ = os.Remove(configPath)
		if repositoryStorageTargetSafe(paths.objects, storagePath) {
			_ = os.RemoveAll(storagePath)
		}
		if mode == objectrepo.EncryptionProtected {
			_ = provider.DeleteProtected(context.Background(), workspaceID)
		}
	}()
	repository, err := objectrepo.CreateKopiaFilesystem(
		ctx,
		storagePath,
		configPath,
		string(keys.Password),
	)
	if err != nil {
		return RepositoryInitialization{}, err
	}
	authority := objectrepo.Authority{
		WorkspaceID: workspaceID,
		FenceEpoch:  fenceEpoch,
		ClaimID:     claimID,
	}
	acceptErr := repository.AcceptAuthority(ctx, nil, authority)
	report, verifyErr := repository.Verify(ctx, nil)
	closeErr := repository.Close(context.WithoutCancel(ctx))
	if err := errors.Join(acceptErr, verifyErr, closeErr); err != nil {
		return RepositoryInitialization{}, err
	}
	if !report.Valid {
		return RepositoryInitialization{}, errors.New(
			"repository.initialization_verify_failed",
		)
	}
	cleanup = false
	return RepositoryInitialization{
		WorkspaceID:    workspaceID,
		EncryptionMode: mode,
		RecoveryKey:    append([]byte(nil), keys.RecoveryKey...),
	}, nil
}

func RestoreProtectedRepository(
	ctx context.Context,
	dataDir string,
	workspaceID string,
	recoveryKey []byte,
) error {
	paths, manifest, err := validateBinding(dataDir, workspaceID)
	if err != nil {
		return err
	}
	if manifest.RepositoryFormat != "kopia-v3" ||
		objectrepo.EncryptionMode(manifest.EncryptionMode) !=
			objectrepo.EncryptionProtected {
		return errors.New("repository.protected_mode_required")
	}
	configPath := filepath.Join(
		paths.coordination,
		"kopia.repository.config",
	)
	if info, statErr := os.Lstat(configPath); statErr != nil ||
		!info.Mode().IsRegular() {
		return errors.Join(errors.New("repository.config_missing"), statErr)
	}
	provider := objectrepo.NewKeyProvider(
		objectrepo.WindowsCredentialVault{},
	)
	if existing, openErr := provider.Open(
		ctx,
		workspaceID,
		objectrepo.EncryptionProtected,
	); openErr == nil {
		clearBytes(existing.Password)
		return errors.New("repository.key_already_available")
	} else if !errors.Is(openErr, objectrepo.ErrKeyMissing) {
		return openErr
	}
	material, err := provider.RestoreProtected(
		ctx,
		workspaceID,
		recoveryKey,
		func(ctx context.Context, candidate []byte) error {
			repository, err := objectrepo.OpenKopia(
				ctx,
				configPath,
				string(candidate),
			)
			if err != nil {
				return err
			}
			pins, pinErr := repository.ListPins(ctx)
			var roots []objectrepo.ObjectID
			for _, pin := range pins {
				roots = append(roots, pin.Roots...)
			}
			report, verifyErr := repository.Verify(ctx, roots)
			closeErr := repository.Close(context.WithoutCancel(ctx))
			if err := errors.Join(pinErr, verifyErr, closeErr); err != nil {
				return err
			}
			if !report.Valid {
				return errors.New("repository.recovery_key_invalid")
			}
			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("restore protected repository key: %w", err)
	}
	clearBytes(material.Password)
	clearBytes(material.RecoveryKey)
	return nil
}

func RollbackRepositoryInitialization(
	ctx context.Context,
	dataDir string,
	workspaceID string,
) error {
	paths, manifest, err := validateBinding(dataDir, workspaceID)
	if err != nil {
		return err
	}
	configPath := filepath.Join(
		paths.coordination,
		"kopia.repository.config",
	)
	storagePath := filepath.Join(paths.objects, "kopia")
	if !repositoryStorageTargetSafe(paths.objects, storagePath) {
		return errors.New("repository.cleanup_target_invalid")
	}
	configErr := os.Remove(configPath)
	if errors.Is(configErr, os.ErrNotExist) {
		configErr = nil
	}
	storageErr := os.RemoveAll(storagePath)
	var keyErr error
	if objectrepo.EncryptionMode(manifest.EncryptionMode) ==
		objectrepo.EncryptionProtected {
		keyErr = objectrepo.NewKeyProvider(
			objectrepo.WindowsCredentialVault{},
		).DeleteProtected(ctx, workspaceID)
	}
	return errors.Join(configErr, storageErr, keyErr)
}

func repositoryStorageTargetSafe(parent string, target string) bool {
	parentAbsolute, parentErr := filepath.Abs(parent)
	targetAbsolute, targetErr := filepath.Abs(target)
	if parentErr != nil || targetErr != nil {
		return false
	}
	relative, err := filepath.Rel(parentAbsolute, targetAbsolute)
	return err == nil && relative != "." &&
		relative != ".." &&
		!filepath.IsAbs(relative) &&
		!startsWithParentTraversal(relative)
}

func startsWithParentTraversal(relative string) bool {
	separator := string(filepath.Separator)
	return len(relative) >= 3 &&
		relative[:3] == ".."+separator
}
