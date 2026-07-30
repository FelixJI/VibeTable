package objectrepo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"

	kopiarepo "github.com/kopia/kopia/repo"
	kopiablob "github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/blob/filesystem"
	kopiaformat "github.com/kopia/kopia/repo/format"
)

// AcquireRepositorySession prevents a trusted offline repository operation
// from racing a live workspace Runtime.
func AcquireRepositorySession(
	ctx context.Context,
	configFile string,
) (func() error, error) {
	absolute, err := filepath.Abs(configFile)
	if err != nil {
		return nil, err
	}
	return acquireProcessLock(ctx, absolute+".vibetable.session.lock")
}

// ChangePassword invokes Kopia's public password-change API. Callers must
// close this handle and independently reopen the repository before publishing
// the new credential.
func (repository *KopiaRepository) ChangePassword(
	ctx context.Context,
	newPassword string,
) error {
	if newPassword == "" {
		return ErrKeyMissing
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.repository == nil {
		return errors.New("repository.closed")
	}
	writer, ok := repository.repository.(kopiarepo.DirectRepositoryWriter)
	if !ok {
		return errors.New("repository.password_change_unsupported")
	}
	if err := writer.FormatManager().ChangePassword(ctx, newPassword); err != nil {
		return errors.Join(
			errors.New("repository.password_change_unsupported"),
			err,
		)
	}
	return nil
}

// AllObjectRoots returns the complete set of public objects tracked by the
// repository state. It is used by offline verification after a key change.
func (repository *KopiaRepository) AllObjectRoots() []ObjectID {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result := make([]ObjectID, 0, len(repository.state.Objects))
	for value := range repository.state.Objects {
		result = append(result, ObjectID(value))
	}
	return result
}

type PasswordFormatBackup struct {
	Repository []byte
	BlobConfig []byte
}

// ReadPasswordFormatBackup snapshots the two encrypted format blobs that
// Kopia rewrites during ChangePassword. Keeping these encrypted bytes makes a
// mid-call process death recoverable without persisting either password.
func ReadPasswordFormatBackup(
	ctx context.Context,
	storageRoot string,
) (PasswordFormatBackup, error) {
	storage, err := filesystem.New(
		ctx,
		&filesystem.Options{Path: storageRoot},
		false,
	)
	if err != nil {
		return PasswordFormatBackup{}, err
	}
	defer storage.Close(ctx)
	repositoryBlob, err := readBlob(
		ctx,
		storage,
		kopiaformat.KopiaRepositoryBlobID,
	)
	if err != nil {
		return PasswordFormatBackup{}, err
	}
	blobConfig, err := readBlob(
		ctx,
		storage,
		kopiaformat.KopiaBlobCfgBlobID,
	)
	if err != nil {
		return PasswordFormatBackup{}, err
	}
	return PasswordFormatBackup{
		Repository: repositoryBlob,
		BlobConfig: blobConfig,
	}, nil
}

// RestorePasswordFormatBackup restores the old encrypted format. Blob config
// is written first so an interruption before the repository blob write still
// leaves the old repository blob paired with its old blob config.
func RestorePasswordFormatBackup(
	ctx context.Context,
	storageRoot string,
	backup PasswordFormatBackup,
) error {
	if len(backup.Repository) == 0 || len(backup.BlobConfig) == 0 {
		return errors.New("repository.password_backup_invalid")
	}
	storage, err := filesystem.New(
		ctx,
		&filesystem.Options{Path: storageRoot},
		false,
	)
	if err != nil {
		return err
	}
	defer storage.Close(ctx)
	if err := storage.PutBlob(
		ctx,
		kopiaformat.KopiaBlobCfgBlobID,
		immutableBlobBytes(backup.BlobConfig),
		kopiablob.PutOptions{},
	); err != nil {
		return err
	}
	return storage.PutBlob(
		ctx,
		kopiaformat.KopiaRepositoryBlobID,
		immutableBlobBytes(backup.Repository),
		kopiablob.PutOptions{},
	)
}

type blobOutput struct {
	bytes.Buffer
}

func (output *blobOutput) Length() int {
	return output.Len()
}

func readBlob(
	ctx context.Context,
	storage kopiablob.Storage,
	id kopiablob.ID,
) ([]byte, error) {
	var output blobOutput
	if err := storage.GetBlob(ctx, id, 0, -1, &output); err != nil {
		return nil, err
	}
	return append([]byte(nil), output.Bytes()...), nil
}

type immutableBlobBytes []byte

func (value immutableBlobBytes) WriteTo(writer io.Writer) (int64, error) {
	count, err := writer.Write(value)
	return int64(count), err
}

func (value immutableBlobBytes) Length() int {
	return len(value)
}

func (value immutableBlobBytes) Reader() io.ReadSeekCloser {
	return readSeekNopCloser{Reader: bytes.NewReader(value)}
}

type readSeekNopCloser struct {
	*bytes.Reader
}

func (readSeekNopCloser) Close() error {
	return nil
}
