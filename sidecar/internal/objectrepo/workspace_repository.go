package objectrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

const workspaceRepositoryFormat = "kopia-v3"

var ErrRepositoryNotInitialized = errors.New(
	"repository.not_initialized",
)

// WorkspaceRepository is the complete high-level repository capability used
// by a live workspace. Concrete storage-engine handles stay inside objectrepo.
type WorkspaceRepository interface {
	Repository
	RetentionInventorySource
	RetentionMaintainer
	RepositoryUsageSource
	Close(context.Context) error
}

// WorkspaceRepositorySpec identifies a repository by workspace-owned roots.
// Callers provide the manifest format as data but do not construct
// storage-engine configuration or blob paths.
type WorkspaceRepositorySpec struct {
	Format           string
	CoordinationRoot string
	ObjectsRoot      string
	Password         []byte
}

// OpenWorkspaceRepository opens an existing workspace repository.
func OpenWorkspaceRepository(
	ctx context.Context,
	spec WorkspaceRepositorySpec,
) (WorkspaceRepository, error) {
	configFile, _, err := workspaceRepositoryPaths(spec)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(configFile); errors.Is(err, os.ErrNotExist) {
		return nil, ErrRepositoryNotInitialized
	} else if err != nil {
		return nil, err
	}
	return OpenKopia(ctx, configFile, string(spec.Password))
}

// OpenOrCreateWorkspaceRepository opens an existing repository or creates an
// empty one when both the engine configuration and object store are absent.
func OpenOrCreateWorkspaceRepository(
	ctx context.Context,
	spec WorkspaceRepositorySpec,
) (repository WorkspaceRepository, created bool, err error) {
	configFile, _, err := workspaceRepositoryPaths(spec)
	if err != nil {
		return nil, false, err
	}
	if _, statErr := os.Lstat(configFile); statErr == nil {
		repository, err = OpenKopia(
			ctx,
			configFile,
			string(spec.Password),
		)
		return repository, false, err
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, false, statErr
	}
	repository, err = createNewWorkspaceRepository(
		ctx,
		spec,
		"repository.config_missing",
		createKopiaWorkspaceRepository,
	)
	return repository, err == nil, err
}

// CreateWorkspaceRepository is the one-shot onboarding entry point.
func CreateWorkspaceRepository(
	ctx context.Context,
	spec WorkspaceRepositorySpec,
) (WorkspaceRepository, error) {
	return createNewWorkspaceRepository(
		ctx,
		spec,
		"repository.already_initialized",
		createKopiaWorkspaceRepository,
	)
}

type workspaceRepositoryCreator func(
	context.Context,
	string,
	string,
	string,
) (WorkspaceRepository, error)

func createKopiaWorkspaceRepository(
	ctx context.Context,
	storageRoot string,
	configFile string,
	password string,
) (WorkspaceRepository, error) {
	return CreateKopiaFilesystem(
		ctx,
		storageRoot,
		configFile,
		password,
	)
}

func createNewWorkspaceRepository(
	ctx context.Context,
	spec WorkspaceRepositorySpec,
	preexistingError string,
	create workspaceRepositoryCreator,
) (WorkspaceRepository, error) {
	configFile, storageRoot, err := workspaceRepositoryPaths(spec)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Lstat(configFile); statErr == nil {
		return nil, errors.New(preexistingError)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if entries, readErr := os.ReadDir(storageRoot); readErr == nil &&
		len(entries) != 0 {
		return nil, errors.New(preexistingError)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	repository, err := create(
		ctx,
		storageRoot,
		configFile,
		string(spec.Password),
	)
	if err == nil {
		return repository, nil
	}
	// Both targets were proven absent or empty immediately before this create
	// attempt. Only artifacts produced by that attempt are eligible for
	// cleanup; the pre-existing paths above return before reaching this block.
	var closeErr error
	if repository != nil {
		closeErr = repository.Close(context.WithoutCancel(ctx))
	}
	configErr := os.Remove(configFile)
	if errors.Is(configErr, os.ErrNotExist) {
		configErr = nil
	}
	storageErr := os.RemoveAll(storageRoot)
	return nil, errors.Join(err, closeErr, configErr, storageErr)
}

// RemoveWorkspaceRepository removes only the engine-owned configuration and
// object-store child directories resolved from validated workspace roots.
func RemoveWorkspaceRepository(spec WorkspaceRepositorySpec) error {
	configFile, storageRoot, err := workspaceRepositoryPaths(spec)
	if err != nil {
		return err
	}
	configErr := os.Remove(configFile)
	if errors.Is(configErr, os.ErrNotExist) {
		configErr = nil
	}
	return errors.Join(configErr, os.RemoveAll(storageRoot))
}

// AcquireWorkspaceRepositorySession prevents trusted offline work from racing
// a live workspace without exposing the engine configuration filename.
func AcquireWorkspaceRepositorySession(
	ctx context.Context,
	coordinationRoot string,
) (func() error, error) {
	configFile, err := workspaceRepositoryConfigPath(coordinationRoot)
	if err != nil {
		return nil, err
	}
	return AcquireRepositorySession(ctx, configFile)
}

type WorkspaceRepositoryRotationProof struct {
	PrimaryHash   string
	SecondaryHash string
}

// WorkspaceRepositoryRotation is the offline password-rotation boundary.
// Its backup is opaque to workspace callers; engine blob names and password
// format types remain private to objectrepo.
type WorkspaceRepositoryRotation interface {
	Backup(
		context.Context,
		string,
	) (WorkspaceRepositoryRotationProof, error)
	VerifyPassword(context.Context, []byte) error
	ChangePassword(context.Context, []byte, []byte) error
	Restore(
		context.Context,
		string,
		WorkspaceRepositoryRotationProof,
	) error
}

type workspaceRepositoryRotation struct {
	spec WorkspaceRepositorySpec
}

func NewWorkspaceRepositoryRotation(
	spec WorkspaceRepositorySpec,
) (WorkspaceRepositoryRotation, error) {
	if _, _, err := workspaceRepositoryPaths(spec); err != nil {
		return nil, err
	}
	spec.Password = nil
	return &workspaceRepositoryRotation{spec: spec}, nil
}

func (rotation *workspaceRepositoryRotation) Backup(
	ctx context.Context,
	backupRoot string,
) (WorkspaceRepositoryRotationProof, error) {
	configFile, storageRoot, err := workspaceRepositoryPaths(rotation.spec)
	if err != nil {
		return WorkspaceRepositoryRotationProof{}, err
	}
	if info, statErr := os.Lstat(configFile); statErr != nil ||
		!info.Mode().IsRegular() {
		return WorkspaceRepositoryRotationProof{}, errors.Join(
			errors.New("repository.config_missing"),
			statErr,
		)
	}
	backup, err := ReadPasswordFormatBackup(ctx, storageRoot)
	if err != nil {
		return WorkspaceRepositoryRotationProof{}, err
	}
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return WorkspaceRepositoryRotationProof{}, err
	}
	if err := writePrivateBackup(
		filepath.Join(backupRoot, "kopia.repository"),
		backup.Repository,
	); err != nil {
		return WorkspaceRepositoryRotationProof{}, err
	}
	if err := writePrivateBackup(
		filepath.Join(backupRoot, "kopia.blobcfg"),
		backup.BlobConfig,
	); err != nil {
		return WorkspaceRepositoryRotationProof{}, err
	}
	return WorkspaceRepositoryRotationProof{
		PrimaryHash:   workspaceBackupHash(backup.Repository),
		SecondaryHash: workspaceBackupHash(backup.BlobConfig),
	}, nil
}

func (rotation *workspaceRepositoryRotation) VerifyPassword(
	ctx context.Context,
	password []byte,
) error {
	repository, err := OpenWorkspaceRepository(
		ctx,
		rotation.withPassword(password),
	)
	if err != nil {
		return err
	}
	concrete, ok := repository.(*KopiaRepository)
	if !ok {
		_ = repository.Close(context.WithoutCancel(ctx))
		return errors.New("repository.rotation_verify_failed")
	}
	report, verifyErr := concrete.Verify(ctx, concrete.AllObjectRoots())
	closeErr := concrete.Close(context.WithoutCancel(ctx))
	if verifyErr != nil || !report.Valid {
		return errors.Join(
			errors.New("repository.rotation_verify_failed"),
			verifyErr,
			closeErr,
		)
	}
	return closeErr
}

func (rotation *workspaceRepositoryRotation) ChangePassword(
	ctx context.Context,
	currentPassword []byte,
	newPassword []byte,
) error {
	repository, err := OpenWorkspaceRepository(
		ctx,
		rotation.withPassword(currentPassword),
	)
	if err != nil {
		return err
	}
	concrete, ok := repository.(*KopiaRepository)
	if !ok {
		_ = repository.Close(context.WithoutCancel(ctx))
		return errors.New("repository.password_change_unsupported")
	}
	report, verifyErr := concrete.Verify(ctx, concrete.AllObjectRoots())
	if verifyErr != nil || !report.Valid {
		return errors.Join(
			errors.New("repository.rotation_verify_failed"),
			verifyErr,
			concrete.Close(context.WithoutCancel(ctx)),
		)
	}
	changeErr := concrete.ChangePassword(ctx, string(newPassword))
	closeErr := concrete.Close(context.WithoutCancel(ctx))
	return errors.Join(changeErr, closeErr)
}

func (rotation *workspaceRepositoryRotation) Restore(
	ctx context.Context,
	backupRoot string,
	proof WorkspaceRepositoryRotationProof,
) error {
	_, storageRoot, err := workspaceRepositoryPaths(rotation.spec)
	if err != nil {
		return err
	}
	repositoryBlob, err := os.ReadFile(
		filepath.Join(backupRoot, "kopia.repository"),
	)
	if err != nil ||
		workspaceBackupHash(repositoryBlob) != proof.PrimaryHash {
		return errors.Join(
			errors.New("repository.rotation_backup_corrupt"),
			err,
		)
	}
	blobConfig, err := os.ReadFile(
		filepath.Join(backupRoot, "kopia.blobcfg"),
	)
	if err != nil ||
		workspaceBackupHash(blobConfig) != proof.SecondaryHash {
		return errors.Join(
			errors.New("repository.rotation_backup_corrupt"),
			err,
		)
	}
	return RestorePasswordFormatBackup(
		ctx,
		storageRoot,
		PasswordFormatBackup{
			Repository: repositoryBlob,
			BlobConfig: blobConfig,
		},
	)
}

func (rotation *workspaceRepositoryRotation) withPassword(
	password []byte,
) WorkspaceRepositorySpec {
	spec := rotation.spec
	spec.Password = password
	return spec
}

func workspaceRepositoryPaths(
	spec WorkspaceRepositorySpec,
) (string, string, error) {
	if spec.Format != workspaceRepositoryFormat {
		return "", "", errors.New("repository.format_unsupported")
	}
	configFile, err := workspaceRepositoryConfigPath(
		spec.CoordinationRoot,
	)
	if err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(spec.ObjectsRoot) {
		return "", "", errors.New("repository.path_not_absolute")
	}
	objectsRoot := filepath.Clean(spec.ObjectsRoot)
	return configFile, filepath.Join(objectsRoot, "kopia"), nil
}

func workspaceRepositoryConfigPath(
	coordinationRoot string,
) (string, error) {
	if !filepath.IsAbs(coordinationRoot) {
		return "", errors.New("repository.path_not_absolute")
	}
	return filepath.Join(
		filepath.Clean(coordinationRoot),
		"kopia.repository.config",
	), nil
}

func writePrivateBackup(path string, raw []byte) error {
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

func workspaceBackupHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
