package workspacev2

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/vibetable/vibetable/sidecar/internal/snapshotpkg"
)

const agePackageHeader = "age-encryption.org/v1"

// preparedSnapshotPackage gives ZIP inspection code the random-access view it
// needs without ever materializing decrypted package plaintext on disk.
//
// Unencrypted packages retain the caller-granted source file. Encrypted
// packages retain a bounded in-memory plaintext buffer which Close clears.
type preparedSnapshotPackage struct {
	readerAt  io.ReaderAt
	size      int64
	plaintext []byte
	close     func() error
	closeOnce sync.Once
	closeErr  error
}

func (prepared *preparedSnapshotPackage) ReaderAt() io.ReaderAt {
	if prepared == nil {
		return nil
	}
	return prepared.readerAt
}

func (prepared *preparedSnapshotPackage) Size() int64 {
	if prepared == nil {
		return 0
	}
	return prepared.size
}

func (prepared *preparedSnapshotPackage) Close() error {
	if prepared == nil {
		return nil
	}
	prepared.closeOnce.Do(func() {
		if prepared.close != nil {
			prepared.closeErr = prepared.close()
		}
		zeroSnapshotPackageBytes(prepared.plaintext)
	})
	return prepared.closeErr
}

func prepareSnapshotPackageSource(
	source string,
	credential *string,
) (*preparedSnapshotPackage, bool, error) {
	return prepareSnapshotPackageSourceWithLimit(
		source,
		credential,
		maxSnapshotWorkingSet,
	)
}

func prepareSnapshotPackageSourceWithLimit(
	source string,
	credential *string,
	plaintextLimit int64,
) (*preparedSnapshotPackage, bool, error) {
	if plaintextLimit <= 0 {
		return nil, false, snapshotpkg.ErrResourceLimit
	}
	input, err := os.Open(source)
	if err != nil {
		return nil, false, err
	}
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, false, errors.Join(
			errors.New("snapshot.package_invalid"),
			err,
			input.Close(),
		)
	}
	prefix := make([]byte, len(agePackageHeader))
	count, readErr := input.ReadAt(prefix, 0)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, false, errors.Join(readErr, input.Close())
	}
	encrypted := count == len(prefix) &&
		string(prefix) == agePackageHeader
	if !encrypted {
		if credential != nil {
			return nil, false, errors.Join(
				errors.New("snapshot.credential_not_applicable"),
				input.Close(),
			)
		}
		return &preparedSnapshotPackage{
			readerAt: input,
			size:     info.Size(),
			close:    input.Close,
		}, false, nil
	}
	if credential == nil || strings.TrimSpace(*credential) == "" {
		return nil, true, errors.Join(
			errors.New("snapshot.credential_required"),
			input.Close(),
		)
	}

	var plaintext bytes.Buffer
	writer := &boundedSnapshotPackageBuffer{
		buffer: &plaintext,
		limit:  plaintextLimit,
	}
	trimmedCredential := strings.TrimSpace(*credential)
	if strings.HasPrefix(trimmedCredential, "AGE-SECRET-KEY-") {
		err = (snapshotpkg.AgeNative{}).Decrypt(
			trimmedCredential,
			input,
			writer,
		)
	} else {
		err = (snapshotpkg.AgeNative{}).DecryptPassphrase(
			*credential,
			input,
			writer,
		)
	}
	closeErr := input.Close()
	if err := errors.Join(err, closeErr); err != nil {
		zeroSnapshotPackageBytes(plaintext.Bytes())
		return nil, true, err
	}
	raw := plaintext.Bytes()
	return &preparedSnapshotPackage{
		readerAt:  bytes.NewReader(raw),
		size:      int64(len(raw)),
		plaintext: raw,
	}, true, nil
}

type boundedSnapshotPackageBuffer struct {
	buffer *bytes.Buffer
	limit  int64
	wrote  int64
}

func (writer *boundedSnapshotPackageBuffer) Write(raw []byte) (int, error) {
	if writer == nil || writer.buffer == nil ||
		writer.limit < writer.wrote ||
		int64(len(raw)) > writer.limit-writer.wrote {
		return 0, snapshotpkg.ErrResourceLimit
	}
	count, err := writer.buffer.Write(raw)
	writer.wrote += int64(count)
	return count, err
}

// streamAgeSnapshotPackage pipes ZIP generation directly into age encryption.
// Closing the pipe reader with the encryption error guarantees that a ZIP
// producer blocked on a failed consumer is released and joined before return.
func streamAgeSnapshotPackage(
	output io.Writer,
	metadata snapshotpkg.Metadata,
	entries map[string][]byte,
	workspaceKey []byte,
	recipients []string,
	credential string,
) error {
	plaintextReader, plaintextWriter := io.Pipe()
	exportDone := make(chan error, 1)
	go func() {
		exportErr := snapshotpkg.Export(
			plaintextWriter,
			metadata,
			entries,
			workspaceKey,
		)
		closeErr := plaintextWriter.CloseWithError(exportErr)
		exportDone <- errors.Join(exportErr, closeErr)
	}()

	var encryptErr error
	if len(recipients) > 0 {
		encryptErr = (snapshotpkg.AgeNative{}).EncryptRecipients(
			recipients,
			plaintextReader,
			output,
		)
	} else {
		encryptErr = (snapshotpkg.AgeNative{}).EncryptPassphrase(
			credential,
			plaintextReader,
			output,
		)
	}
	if encryptErr != nil {
		_ = plaintextReader.CloseWithError(encryptErr)
	} else {
		_ = plaintextReader.Close()
	}
	exportErr := <-exportDone
	return errors.Join(exportErr, encryptErr)
}

func zeroSnapshotPackageBytes(raw []byte) {
	for index := range raw {
		raw[index] = 0
	}
}
