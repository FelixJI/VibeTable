package workspacev2

import (
	"context"
	"errors"
	"fmt"

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
	repositoryCreated := false
	defer func() {
		if !cleanup {
			return
		}
		if repositoryCreated {
			_ = objectrepo.RemoveWorkspaceRepository(
				workspaceRepositorySpec(
					paths,
					manifest.RepositoryFormat,
					nil,
				),
			)
		}
		if mode == objectrepo.EncryptionProtected {
			_ = provider.DeleteProtected(context.Background(), workspaceID)
		}
	}()
	repository, err := objectrepo.CreateWorkspaceRepository(
		ctx,
		workspaceRepositorySpec(
			paths,
			manifest.RepositoryFormat,
			keys.Password,
		),
	)
	if err != nil {
		return RepositoryInitialization{}, err
	}
	repositoryCreated = true
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
	if objectrepo.EncryptionMode(manifest.EncryptionMode) !=
		objectrepo.EncryptionProtected {
		return errors.New("repository.protected_mode_required")
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
			rotation, err := objectrepo.NewWorkspaceRepositoryRotation(
				workspaceRepositorySpec(
					paths,
					manifest.RepositoryFormat,
					nil,
				),
			)
			if err != nil {
				return err
			}
			if err := rotation.VerifyPassword(ctx, candidate); err != nil {
				if errors.Is(
					err,
					objectrepo.ErrRepositoryNotInitialized,
				) {
					return errors.Join(
						errors.New("repository.config_missing"),
						err,
					)
				}
				return errors.Join(
					errors.New("repository.recovery_key_invalid"),
					err,
				)
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
	removeErr := objectrepo.RemoveWorkspaceRepository(
		workspaceRepositorySpec(
			paths,
			manifest.RepositoryFormat,
			nil,
		),
	)
	var keyErr error
	if objectrepo.EncryptionMode(manifest.EncryptionMode) ==
		objectrepo.EncryptionProtected {
		keyErr = objectrepo.NewKeyProvider(
			objectrepo.WindowsCredentialVault{},
		).DeleteProtected(ctx, workspaceID)
	}
	return errors.Join(removeErr, keyErr)
}

func workspaceRepositorySpec(
	paths workspacePaths,
	format string,
	password []byte,
) objectrepo.WorkspaceRepositorySpec {
	return objectrepo.WorkspaceRepositorySpec{
		Format:           format,
		CoordinationRoot: paths.coordination,
		ObjectsRoot:      paths.objects,
		Password:         password,
	}
}
