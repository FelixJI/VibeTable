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
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
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
	ReplicaRoot   string `json:"replicaRoot,omitempty"`
	SourceMode    string `json:"sourceMode,omitempty"`
	TargetMode    string `json:"targetMode,omitempty"`
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
	replicaRoot string,
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
	if intent.ReplicaRoot != "" && !sameName(filepath.Clean(intent.ReplicaRoot), filepath.Clean(replicaRoot)) {
		return RepositoryRotation{}, errors.New("workspace.identity_mismatch")
	}
	provider := objectrepo.NewKeyProvider(objectrepo.WindowsCredentialVault{})
	target := objectrepo.EncryptionMode(intent.TargetMode)
	if target == "" {
		target = objectrepo.EncryptionProtected
	}
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
		_, manifest, err := validateBinding(dataDir, workspaceID)
		if err != nil || manifest.EncryptionMode != string(target) {
			return RepositoryRotation{}, errors.Join(errors.New("workspace.identity_mismatch"), err)
		}
		if _, err := rotationReplicaManifest(manifest, paths, replicaRoot, string(target), string(target), true); err != nil {
			return RepositoryRotation{}, err
		}
		current, err := provider.Open(
			ctx,
			workspaceID,
			target,
		)
		if err != nil {
			return RepositoryRotation{}, err
		}
		defer clearBytes(current.Password)
		return RepositoryRotation{
			WorkspaceID: workspaceID,
			RecoveryKey: rotationRecoveryKey(target, current.Password),
		}, nil
	}
	return rotateRepositoryMode(
		ctx,
		dataDir,
		workspaceID,
		provider,
		target,
		replicaRoot,
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
	return rotateRepositoryMode(ctx, dataDir, workspaceID, provider, objectrepo.EncryptionProtected, "", fault)
}

func rotateRepositoryMode(
	ctx context.Context, dataDir, workspaceID string, provider *objectrepo.KeyProvider,
	target objectrepo.EncryptionMode, replicaRoot string, fault keyRotationFault,
) (_ RepositoryRotation, err error) {
	paths, manifest, err := validateBinding(dataDir, workspaceID)
	if err != nil {
		return RepositoryRotation{}, err
	}
	if (manifest.EncryptionMode != "protected" && manifest.EncryptionMode != "convenient") ||
		(target != objectrepo.EncryptionProtected && target != objectrepo.EncryptionConvenient) {
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
		if _, err := rotationReplicaManifest(manifest, paths, replicaRoot, manifest.EncryptionMode, string(target), false); err != nil {
			return RepositoryRotation{}, err
		}
		journal, err = prepareKeyRotation(
			ctx,
			paths,
			workspaceID,
			provider,
			rotation,
			objectrepo.EncryptionMode(manifest.EncryptionMode), target,
			replicaRoot,
		)
		if err != nil {
			return RepositoryRotation{}, err
		}
		if err := injectKeyRotationFault(fault, keyRotationPrepared); err != nil {
			return RepositoryRotation{}, err
		}
	}
	if journal.SourceMode == "" {
		journal.SourceMode = "protected"
	}
	if journal.TargetMode == "" {
		journal.TargetMode = "protected"
	}
	if journal.TargetMode != string(target) {
		return RepositoryRotation{}, errors.New("repository.key_rotation_target_conflict")
	}
	// Older protected-only journals predate the trusted replica-root binding.
	if journal.ReplicaRoot == "" && replicaRoot != "" && manifest.StorageMode == "mirrored" &&
		journal.SourceMode == "protected" && journal.TargetMode == "protected" {
		if _, err := rotationReplicaManifest(manifest, paths, replicaRoot, "protected", "protected", false); err != nil {
			return RepositoryRotation{}, err
		}
		journal.ReplicaRoot = replicaRoot
		if err := writeKeyRotationJournal(paths, journal); err != nil {
			return RepositoryRotation{}, err
		}
	}
	if journal.WorkspaceID != workspaceID ||
		!sameName(filepath.Clean(journal.ReplicaRoot), filepath.Clean(replicaRoot)) ||
		(manifest.EncryptionMode != journal.SourceMode &&
			(journal.Phase != keyRotationVault || manifest.EncryptionMode != journal.TargetMode)) {
		return RepositoryRotation{}, errors.New("repository.rotation_journal_corrupt")
	}
	if _, err := rotationReplicaManifest(manifest, paths, replicaRoot, journal.SourceMode, journal.TargetMode, journal.Phase == keyRotationVault); err != nil {
		return RepositoryRotation{}, err
	}
	var staged objectrepo.KeyMaterial
	if target == objectrepo.EncryptionConvenient {
		staged, err = provider.Open(ctx, workspaceID, target)
	} else {
		staged, err = provider.OpenStagedProtectedRotation(ctx, workspaceID, journal.RotationID)
		if err != nil && journal.Phase == keyRotationVault {
			staged, err = provider.Open(ctx, workspaceID, target)
			staged.RecoveryKey = append([]byte(nil), staged.Password...)
		}
	}
	if err != nil {
		return RepositoryRotation{}, err
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
				objectrepo.EncryptionMode(journal.SourceMode),
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
		if target == objectrepo.EncryptionProtected {
			if err := provider.CommitStagedProtectedRotation(
				ctx,
				workspaceID,
				journal.RotationID,
			); err != nil {
				return RepositoryRotation{}, err
			}
		}
		journal.Phase = keyRotationVault
		if err := writeKeyRotationJournal(paths, journal); err != nil {
			return RepositoryRotation{}, err
		}
		if err := injectKeyRotationFault(fault, keyRotationVault); err != nil {
			return RepositoryRotation{}, err
		}
	}
	if err := rotation.VerifyPassword(ctx, staged.Password); err != nil {
		return RepositoryRotation{}, err
	}
	// Publish the manifest only after the new repository password verifies.
	// Keep the journal until credential cleanup completes so restart can finish.
	replicaManifest, err := rotationReplicaManifest(manifest, paths, replicaRoot, journal.SourceMode, journal.TargetMode, true)
	if err != nil {
		return RepositoryRotation{}, err
	}
	manifest.EncryptionMode = string(target)
	manifestRaw, marshalErr := json.Marshal(manifest)
	if marshalErr != nil {
		return RepositoryRotation{}, marshalErr
	}
	if replicaManifest != "" {
		if err := replaceRotationManifest(replicaManifest, manifestRaw); err != nil {
			return RepositoryRotation{}, err
		}
		if err := injectKeyRotationFault(fault, "replicaManifestChanged"); err != nil {
			return RepositoryRotation{}, err
		}
	}
	if err := replaceRotationManifest(paths.manifest, manifestRaw); err != nil {
		return RepositoryRotation{}, err
	}
	if err := injectKeyRotationFault(fault, "manifestChanged"); err != nil {
		return RepositoryRotation{}, err
	}
	if target == objectrepo.EncryptionConvenient && journal.SourceMode == "protected" {
		if err := provider.DeleteProtected(ctx, workspaceID); err != nil {
			return RepositoryRotation{}, err
		}
		if err := injectKeyRotationFault(fault, "credentialDeleted"); err != nil {
			return RepositoryRotation{}, err
		}
	}
	if target == objectrepo.EncryptionProtected {
		if err := provider.DiscardStagedProtectedRotation(
			ctx,
			workspaceID,
			journal.RotationID,
		); err != nil {
			return RepositoryRotation{}, err
		}
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
	source, target objectrepo.EncryptionMode,
	replicaRoot string,
) (keyRotationJournal, error) {
	plan, err := provider.PreviewModeRotation(ctx, workspaceID, source, target)
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
	if target == objectrepo.EncryptionProtected {
		if err := provider.StageProtectedRotation(
			ctx,
			plan,
			rotationID,
		); err != nil {
			return keyRotationJournal{}, err
		}
	}
	journal := keyRotationJournal{
		ReplicaRoot: replicaRoot,
		SourceMode:  string(source), TargetMode: string(target),
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
	if journal.ReplicaRoot != "" && !filepath.IsAbs(journal.ReplicaRoot) {
		return errors.New("repository.rotation_journal_corrupt")
	}
	for _, mode := range []string{journal.SourceMode, journal.TargetMode} {
		if mode != "" && mode != "protected" && mode != "convenient" {
			return errors.New("repository.rotation_journal_corrupt")
		}
	}
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

func rotationRecoveryKey(mode objectrepo.EncryptionMode, password []byte) []byte {
	if mode == objectrepo.EncryptionConvenient {
		return nil
	}
	return append([]byte(nil), password...)
}

// Both manifests are advanced under the durable rotation journal. Startup stays
// fenced by its host intent until an interrupted publication has been resumed.
func rotationReplicaManifest(
	manifest contractsv2.WorkspaceManifest, paths workspacePaths, replicaRoot, source, target string,
	allowTarget bool,
) (string, error) {
	if manifest.StorageMode != "mirrored" {
		if replicaRoot != "" {
			return "", errors.New("workspace.identity_mismatch")
		}
		return "", nil
	}
	if replicaRoot == "" || sameName(filepath.Clean(replicaRoot), paths.root) {
		return "", errors.New("replica.selected_root_required")
	}
	selected, err := readReplicaManifest(replicaRoot, manifest.WorkspaceID)
	if err != nil {
		return "", err
	}
	if selected.EncryptionMode != source && (!allowTarget || selected.EncryptionMode != target) {
		return "", errors.New("workspace.identity_mismatch")
	}
	selected.EncryptionMode = manifest.EncryptionMode
	localRaw, localErr := json.Marshal(manifest)
	selectedRaw, selectedErr := json.Marshal(selected)
	if localErr != nil || selectedErr != nil || !bytes.Equal(localRaw, selectedRaw) {
		return "", errors.New("workspace.identity_mismatch")
	}
	return filepath.Join(replicaRoot, ".vibetable", "workspace.json"), nil
}

func replaceRotationManifest(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".rotation-manifest-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
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
	if err := file.Close(); err != nil {
		return err
	}
	return replaceGrantedFile(name, path)
}
