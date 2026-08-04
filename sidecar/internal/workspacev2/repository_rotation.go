package workspacev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

const (
	keyRotationJournalName = "key-rotation-journal.json"
	keyRotationPrepared    = "prepared"
	keyRotationChanged     = "formatChanged"
	keyRotationVerified    = "verified"
	keyRotationVault       = "vaultSwapped"
)

type RepositoryRotation struct {
	WorkspaceID string
	RecoveryKey []byte
}

type keyRotationJournal struct {
	FormatVersion int    `json:"formatVersion"`
	WorkspaceID   string `json:"workspaceId"`
	RotationID    string `json:"rotationId"`
	Phase         string `json:"phase"`
	BackupPrimary string `json:"repositoryProof"`
	BackupSecond  string `json:"blobConfigProof"`
}

type keyRotationFault func(string) error

func RotateProtectedRepository(
	ctx context.Context,
	dataDir string,
	workspaceID string,
) (RepositoryRotation, error) {
	paths, _, err := validateBinding(dataDir, workspaceID)
	if err != nil {
		return RepositoryRotation{}, err
	}
	intent, found, err := readKeyRotationIntent(paths)
	if err != nil {
		return RepositoryRotation{}, err
	}
	if !found || intent.WorkspaceID != workspaceID {
		return RepositoryRotation{},
			errors.New("repository.key_rotation_intent_required")
	}
	provider := objectrepo.NewKeyProvider(objectrepo.WindowsCredentialVault{})
	if intent.State == keyRotationIntentCompleted {
		lockCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		release, lockErr := objectrepo.AcquireWorkspaceRepositorySession(
			lockCtx,
			paths.coordination,
		)
		if errors.Is(lockErr, context.DeadlineExceeded) {
			return RepositoryRotation{},
				errors.New("repository.workspace_must_be_closed")
		}
		if lockErr != nil {
			return RepositoryRotation{}, lockErr
		}
		defer release()
		current, err := provider.Open(
			ctx,
			workspaceID,
			objectrepo.EncryptionProtected,
		)
		if err != nil {
			return RepositoryRotation{}, err
		}
		defer clearBytes(current.Password)
		return RepositoryRotation{
			WorkspaceID: workspaceID,
			RecoveryKey: append([]byte(nil), current.Password...),
		}, nil
	}
	return rotateProtectedRepository(
		ctx,
		dataDir,
		workspaceID,
		provider,
		nil,
	)
}

func rotateProtectedRepository(
	ctx context.Context,
	dataDir string,
	workspaceID string,
	provider *objectrepo.KeyProvider,
	fault keyRotationFault,
) (_ RepositoryRotation, err error) {
	paths, manifest, err := validateBinding(dataDir, workspaceID)
	if err != nil {
		return RepositoryRotation{}, err
	}
	if objectrepo.EncryptionMode(manifest.EncryptionMode) !=
		objectrepo.EncryptionProtected {
		return RepositoryRotation{},
			errors.New("repository.protected_mode_required")
	}
	rotation, err := objectrepo.NewWorkspaceRepositoryRotation(
		workspaceRepositorySpec(
			paths,
			manifest.RepositoryFormat,
			nil,
		),
	)
	if err != nil {
		return RepositoryRotation{}, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	release, err := objectrepo.AcquireWorkspaceRepositorySession(
		lockCtx,
		paths.coordination,
	)
	if errors.Is(err, context.DeadlineExceeded) {
		return RepositoryRotation{},
			errors.New("repository.workspace_must_be_closed")
	}
	if err != nil {
		return RepositoryRotation{}, err
	}
	defer release()

	journal, found, err := readKeyRotationJournal(paths)
	if err != nil {
		return RepositoryRotation{}, err
	}
	if !found {
		journal, err = prepareKeyRotation(
			ctx,
			paths,
			workspaceID,
			provider,
			rotation,
		)
		if err != nil {
			return RepositoryRotation{}, err
		}
		if err := injectKeyRotationFault(fault, keyRotationPrepared); err != nil {
			return RepositoryRotation{}, err
		}
	}
	staged, err := provider.OpenStagedProtectedRotation(
		ctx,
		workspaceID,
		journal.RotationID,
	)
	if err != nil {
		if journal.Phase != keyRotationVault {
			return RepositoryRotation{}, err
		}
		staged, err = provider.Open(
			ctx,
			workspaceID,
			objectrepo.EncryptionProtected,
		)
		if err != nil {
			return RepositoryRotation{}, err
		}
		staged.RecoveryKey = append([]byte(nil), staged.Password...)
	}
	defer clearBytes(staged.Password)
	defer clearBytes(staged.RecoveryKey)

	if journal.Phase == keyRotationPrepared {
		newValid := rotation.VerifyPassword(
			ctx,
			staged.Password,
		) == nil
		if !newValid {
			current, openErr := provider.Open(
				ctx,
				workspaceID,
				objectrepo.EncryptionProtected,
			)
			if openErr != nil {
				return RepositoryRotation{}, openErr
			}
			defer clearBytes(current.Password)
			oldErr := rotation.VerifyPassword(
				ctx,
				current.Password,
			)
			if oldErr != nil {
				if err := restoreKeyRotationFormat(
					ctx,
					paths,
					journal,
					rotation,
				); err != nil {
					return RepositoryRotation{}, errors.Join(oldErr, err)
				}
				oldErr = rotation.VerifyPassword(
					ctx,
					current.Password,
				)
			}
			if oldErr != nil {
				return RepositoryRotation{}, oldErr
			}
			if err := rotation.ChangePassword(
				ctx,
				current.Password,
				staged.Password,
			); err != nil {
				return RepositoryRotation{}, err
			}
			if err := injectKeyRotationFault(
				fault,
				"afterFormatChangeBeforeJournal",
			); err != nil {
				return RepositoryRotation{}, err
			}
		}
		journal.Phase = keyRotationChanged
		if err := writeKeyRotationJournal(paths, journal); err != nil {
			return RepositoryRotation{}, err
		}
	}
	if journal.Phase == keyRotationChanged {
		if err := rotation.VerifyPassword(
			ctx,
			staged.Password,
		); err != nil {
			return RepositoryRotation{}, err
		}
		journal.Phase = keyRotationVerified
		if err := writeKeyRotationJournal(paths, journal); err != nil {
			return RepositoryRotation{}, err
		}
		if err := injectKeyRotationFault(fault, keyRotationVerified); err != nil {
			return RepositoryRotation{}, err
		}
	}
	if journal.Phase == keyRotationVerified {
		if err := provider.CommitStagedProtectedRotation(
			ctx,
			workspaceID,
			journal.RotationID,
		); err != nil {
			return RepositoryRotation{}, err
		}
		journal.Phase = keyRotationVault
		if err := writeKeyRotationJournal(paths, journal); err != nil {
			return RepositoryRotation{}, err
		}
		if err := injectKeyRotationFault(fault, keyRotationVault); err != nil {
			return RepositoryRotation{}, err
		}
	}
	if err := provider.DiscardStagedProtectedRotation(
		ctx,
		workspaceID,
		journal.RotationID,
	); err != nil {
		return RepositoryRotation{}, err
	}
	if err := completeKeyRotationIntentIfPresent(
		paths,
		workspaceID,
	); err != nil {
		return RepositoryRotation{}, err
	}
	if err := cleanupKeyRotation(paths, journal.RotationID); err != nil {
		return RepositoryRotation{}, err
	}
	return RepositoryRotation{
		WorkspaceID: workspaceID,
		RecoveryKey: append([]byte(nil), staged.RecoveryKey...),
	}, nil
}

func completeKeyRotationIntentIfPresent(
	paths workspacePaths,
	workspaceID string,
) error {
	intent, found, err := readKeyRotationIntent(paths)
	if err != nil || !found {
		return err
	}
	if intent.WorkspaceID != workspaceID {
		return errors.New("repository.key_rotation_intent_corrupt")
	}
	intent.State = keyRotationIntentCompleted
	return writeKeyRotationIntent(paths, intent)
}

func prepareKeyRotation(
	ctx context.Context,
	paths workspacePaths,
	workspaceID string,
	provider *objectrepo.KeyProvider,
	rotation objectrepo.WorkspaceRepositoryRotation,
) (keyRotationJournal, error) {
	plan, err := provider.PreviewProtectedRotation(ctx, workspaceID)
	if err != nil {
		return keyRotationJournal{}, err
	}
	defer clearBytes(plan.CurrentPassword)
	defer clearBytes(plan.NewPassword)
	defer clearBytes(plan.RecoveryKey)
	rotationID := uuid.NewString()
	root := keyRotationRoot(paths, rotationID)
	proof, err := rotation.Backup(ctx, root)
	if err != nil {
		return keyRotationJournal{}, err
	}
	if err := provider.StageProtectedRotation(
		ctx,
		plan,
		rotationID,
	); err != nil {
		return keyRotationJournal{}, err
	}
	journal := keyRotationJournal{
		FormatVersion: 2,
		WorkspaceID:   workspaceID,
		RotationID:    rotationID,
		Phase:         keyRotationPrepared,
		BackupPrimary: proof.PrimaryProof,
		BackupSecond:  proof.SecondaryProof,
	}
	if err := writeKeyRotationJournal(paths, journal); err != nil {
		_ = provider.DiscardStagedProtectedRotation(
			context.Background(),
			workspaceID,
			rotationID,
		)
		return keyRotationJournal{}, err
	}
	return journal, nil
}

func restoreKeyRotationFormat(
	ctx context.Context,
	paths workspacePaths,
	journal keyRotationJournal,
	rotation objectrepo.WorkspaceRepositoryRotation,
) error {
	return rotation.Restore(
		ctx,
		keyRotationRoot(paths, journal.RotationID),
		objectrepo.WorkspaceRepositoryRotationProof{
			PrimaryProof:   journal.BackupPrimary,
			SecondaryProof: journal.BackupSecond,
		},
	)
}

func keyRotationRoot(paths workspacePaths, rotationID string) string {
	return filepath.Join(paths.temp, "key-rotation", rotationID)
}

func keyRotationJournalPath(paths workspacePaths) string {
	return filepath.Join(paths.coordination, keyRotationJournalName)
}

func writeKeyRotationJournal(
	paths workspacePaths,
	journal keyRotationJournal,
) error {
	if err := validateKeyRotationJournal(journal); err != nil {
		return err
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(
		paths.coordination,
		".key-rotation-*.tmp",
	)
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceGrantedFile(name, keyRotationJournalPath(paths))
}

func readKeyRotationJournal(
	paths workspacePaths,
) (keyRotationJournal, bool, error) {
	raw, err := readFileBounded(keyRotationJournalPath(paths), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return keyRotationJournal{}, false, nil
	}
	if err != nil {
		return keyRotationJournal{}, false, err
	}
	var journal keyRotationJournal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return keyRotationJournal{}, false,
			errors.New("repository.rotation_journal_corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return keyRotationJournal{}, false,
			errors.New("repository.rotation_journal_corrupt")
	}
	if err := validateKeyRotationJournal(journal); err != nil {
		return keyRotationJournal{}, false, err
	}
	return journal, true, nil
}

func validateKeyRotationJournal(journal keyRotationJournal) error {
	proof := objectrepo.WorkspaceRepositoryRotationProof{
		PrimaryProof:   journal.BackupPrimary,
		SecondaryProof: journal.BackupSecond,
	}
	if journal.FormatVersion != 2 ||
		!validUUID(journal.WorkspaceID) ||
		!validUUID(journal.RotationID) ||
		!proof.ValidFormat() {
		return errors.New("repository.rotation_journal_corrupt")
	}
	switch journal.Phase {
	case keyRotationPrepared, keyRotationChanged,
		keyRotationVerified, keyRotationVault:
		return nil
	default:
		return errors.New("repository.rotation_journal_corrupt")
	}
}

func writeDurablePrivateFile(path string, raw []byte) error {
	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func cleanupKeyRotation(paths workspacePaths, rotationID string) error {
	if err := os.RemoveAll(keyRotationRoot(paths, rotationID)); err != nil {
		return err
	}
	err := os.Remove(keyRotationJournalPath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func injectKeyRotationFault(fault keyRotationFault, phase string) error {
	if fault == nil {
		return nil
	}
	return fault(phase)
}
