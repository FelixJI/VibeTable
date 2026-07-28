package workspacev2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	FormatVersion  int    `json:"formatVersion"`
	WorkspaceID    string `json:"workspaceId"`
	RotationID     string `json:"rotationId"`
	Phase          string `json:"phase"`
	RepositoryHash string `json:"repositoryHash"`
	BlobConfigHash string `json:"blobConfigHash"`
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
		configPath := filepath.Join(
			paths.coordination,
			"kopia.repository.config",
		)
		lockCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		release, lockErr := objectrepo.AcquireRepositorySession(
			lockCtx,
			configPath,
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
	if manifest.RepositoryFormat != "kopia-v3" ||
		objectrepo.EncryptionMode(manifest.EncryptionMode) !=
			objectrepo.EncryptionProtected {
		return RepositoryRotation{},
			errors.New("repository.protected_mode_required")
	}
	configPath := filepath.Join(
		paths.coordination,
		"kopia.repository.config",
	)
	storagePath := filepath.Join(paths.objects, "kopia")
	lockCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	release, err := objectrepo.AcquireRepositorySession(
		lockCtx,
		configPath,
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
			configPath,
			storagePath,
			workspaceID,
			provider,
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
		newValid := verifyRepositoryWithPassword(
			ctx,
			configPath,
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
			repository, oldErr := openVerifiedRepository(
				ctx,
				configPath,
				current.Password,
			)
			if oldErr != nil {
				if err := restoreKeyRotationFormat(
					ctx,
					paths,
					storagePath,
					journal,
				); err != nil {
					return RepositoryRotation{}, errors.Join(oldErr, err)
				}
				repository, oldErr = openVerifiedRepository(
					ctx,
					configPath,
					current.Password,
				)
			}
			if oldErr != nil {
				return RepositoryRotation{}, oldErr
			}
			changeErr := repository.ChangePassword(
				ctx,
				string(staged.Password),
			)
			closeErr := repository.Close(context.WithoutCancel(ctx))
			if err := errors.Join(changeErr, closeErr); err != nil {
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
		if err := verifyRepositoryWithPassword(
			ctx,
			configPath,
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
	configPath string,
	storagePath string,
	workspaceID string,
	provider *objectrepo.KeyProvider,
) (keyRotationJournal, error) {
	if _, err := os.Stat(configPath); err != nil {
		return keyRotationJournal{}, errors.Join(
			errors.New("repository.config_missing"),
			err,
		)
	}
	plan, err := provider.PreviewProtectedRotation(ctx, workspaceID)
	if err != nil {
		return keyRotationJournal{}, err
	}
	defer clearBytes(plan.CurrentPassword)
	defer clearBytes(plan.NewPassword)
	defer clearBytes(plan.RecoveryKey)
	backup, err := objectrepo.ReadPasswordFormatBackup(ctx, storagePath)
	if err != nil {
		return keyRotationJournal{}, err
	}
	rotationID := uuid.NewString()
	root := keyRotationRoot(paths, rotationID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return keyRotationJournal{}, err
	}
	if err := writeDurablePrivateFile(
		filepath.Join(root, "kopia.repository"),
		backup.Repository,
	); err != nil {
		return keyRotationJournal{}, err
	}
	if err := writeDurablePrivateFile(
		filepath.Join(root, "kopia.blobcfg"),
		backup.BlobConfig,
	); err != nil {
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
		FormatVersion: 1,
		WorkspaceID:   workspaceID,
		RotationID:    rotationID,
		Phase:         keyRotationPrepared,
		RepositoryHash: hashBytes(
			backup.Repository,
		),
		BlobConfigHash: hashBytes(backup.BlobConfig),
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

func openVerifiedRepository(
	ctx context.Context,
	configPath string,
	password []byte,
) (*objectrepo.KopiaRepository, error) {
	repository, err := objectrepo.OpenKopia(
		ctx,
		configPath,
		string(password),
	)
	if err != nil {
		return nil, err
	}
	roots := repository.AllObjectRoots()
	report, err := repository.Verify(ctx, roots)
	if err != nil || !report.Valid {
		_ = repository.Close(context.WithoutCancel(ctx))
		return nil, errors.Join(
			errors.New("repository.rotation_verify_failed"),
			err,
		)
	}
	return repository, nil
}

func verifyRepositoryWithPassword(
	ctx context.Context,
	configPath string,
	password []byte,
) error {
	repository, err := openVerifiedRepository(ctx, configPath, password)
	if err != nil {
		return err
	}
	return repository.Close(context.WithoutCancel(ctx))
}

func restoreKeyRotationFormat(
	ctx context.Context,
	paths workspacePaths,
	storagePath string,
	journal keyRotationJournal,
) error {
	root := keyRotationRoot(paths, journal.RotationID)
	repositoryBlob, err := os.ReadFile(filepath.Join(root, "kopia.repository"))
	if err != nil || hashBytes(repositoryBlob) != journal.RepositoryHash {
		return errors.Join(
			errors.New("repository.rotation_backup_corrupt"),
			err,
		)
	}
	blobConfig, err := os.ReadFile(filepath.Join(root, "kopia.blobcfg"))
	if err != nil || hashBytes(blobConfig) != journal.BlobConfigHash {
		return errors.Join(
			errors.New("repository.rotation_backup_corrupt"),
			err,
		)
	}
	return objectrepo.RestorePasswordFormatBackup(
		ctx,
		storagePath,
		objectrepo.PasswordFormatBackup{
			Repository: repositoryBlob,
			BlobConfig: blobConfig,
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
	if journal.FormatVersion != 1 ||
		!validUUID(journal.WorkspaceID) ||
		!validUUID(journal.RotationID) ||
		len(journal.RepositoryHash) != 64 ||
		len(journal.BlobConfigHash) != 64 {
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

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func injectKeyRotationFault(fault keyRotationFault, phase string) error {
	if fault == nil {
		return nil
	}
	return fault(phase)
}
