package workspacev2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/vibetable/vibetable/sidecar/internal/snapshotpkg"
)

const agePackageHeader = "age-encryption.org/v1"

type snapshotPackageSourceBinding struct {
	Hash     string
	Size     int64
	Identity string
}

type snapshotPackageSource struct {
	file      *os.File
	binding   snapshotPackageSourceBinding
	staged    []byte
	encrypted bool
	closeOnce sync.Once
	closeErr  error
}

// preparedSnapshotPackage gives ZIP inspection code the random-access view it
// needs without ever materializing decrypted package plaintext on disk.
//
// Both package forms are parsed from a bounded immutable in-memory staging
// copy. Encrypted packages additionally retain their decrypted ZIP buffer.
// Close clears every plaintext/ciphertext staging buffer.
type preparedSnapshotPackage struct {
	readerAt        io.ReaderAt
	size            int64
	workingSetBytes int64
	plaintext       []byte
	close           func() error
	closeOnce       sync.Once
	closeErr        error
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
	opened, err := openSnapshotPackageSource(source)
	if err != nil {
		return nil, false, err
	}
	prepared, err := opened.prepare(credential, plaintextLimit)
	if err != nil {
		return nil, opened.encrypted, errors.Join(err, opened.Close())
	}
	prepared.close = opened.Close
	return prepared, opened.encrypted, nil
}

func openSnapshotPackageSource(
	path string,
) (*snapshotPackageSource, error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, errors.Join(
			errors.New("snapshot.package_invalid"),
			err,
			input.Close(),
		)
	}
	if info.Size() > maxSnapshotPackageContainer {
		return nil, errors.Join(snapshotpkg.ErrResourceLimit, input.Close())
	}
	identity, err := snapshotSourceFileIdentity(input, info)
	if err != nil {
		return nil, errors.Join(err, input.Close())
	}
	binding, err := snapshotPackageBinding(input, info.Size(), identity)
	if err != nil {
		return nil, errors.Join(err, input.Close())
	}
	staged := make([]byte, int(info.Size()))
	count, readErr := io.ReadFull(
		io.NewSectionReader(input, 0, info.Size()),
		staged,
	)
	if readErr != nil || int64(count) != info.Size() {
		zeroSnapshotPackageBytes(staged)
		return nil, errors.Join(
			errors.New("snapshot.package_source_changed"),
			readErr,
			input.Close(),
		)
	}
	stagedHash := sha256.Sum256(staged)
	if binding.Hash != "sha256:"+hex.EncodeToString(stagedHash[:]) {
		zeroSnapshotPackageBytes(staged)
		return nil, errors.Join(
			errors.New("snapshot.package_source_changed"),
			input.Close(),
		)
	}
	return &snapshotPackageSource{
		file:    input,
		binding: binding,
		staged:  staged,
		encrypted: len(staged) >= len(agePackageHeader) &&
			string(staged[:len(agePackageHeader)]) == agePackageHeader,
	}, nil
}

func (source *snapshotPackageSource) Binding() snapshotPackageSourceBinding {
	if source == nil {
		return snapshotPackageSourceBinding{}
	}
	return source.binding
}

func (source *snapshotPackageSource) Encrypted() bool {
	return source != nil && source.encrypted
}

func (source *snapshotPackageSource) VerifyUnchanged() error {
	if source == nil || source.file == nil {
		return errors.New("snapshot.package_source_closed")
	}
	info, err := source.file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return errors.Join(errors.New("snapshot.package_source_changed"), err)
	}
	identity, err := snapshotSourceFileIdentity(source.file, info)
	if err != nil {
		return err
	}
	binding, err := snapshotPackageBinding(source.file, info.Size(), identity)
	if err != nil {
		return err
	}
	if binding != source.binding {
		return errors.New("snapshot.package_source_changed")
	}
	return nil
}

func (source *snapshotPackageSource) Close() error {
	if source == nil {
		return nil
	}
	source.closeOnce.Do(func() {
		source.clearStaged()
		if source.file != nil {
			source.closeErr = source.file.Close()
		}
	})
	return source.closeErr
}

func (source *snapshotPackageSource) prepare(
	credential *string,
	plaintextLimit int64,
) (*preparedSnapshotPackage, error) {
	if source == nil || source.file == nil {
		return nil, errors.New("snapshot.package_source_closed")
	}
	if plaintextLimit <= 0 {
		return nil, snapshotpkg.ErrResourceLimit
	}
	if !source.encrypted {
		if credential != nil {
			return nil, errors.New("snapshot.credential_not_applicable")
		}
		return &preparedSnapshotPackage{
			readerAt:        bytes.NewReader(source.staged),
			size:            source.binding.Size,
			workingSetBytes: int64(len(source.staged)),
		}, nil
	}
	if credential == nil || strings.TrimSpace(*credential) == "" {
		return nil, errors.New("snapshot.credential_required")
	}
	var plaintext bytes.Buffer
	decryptionLimit := min(plaintextLimit, maxSnapshotPackageContainer)
	// age adds framing and authentication overhead rather than compressing.
	// Reserving the smaller of the ciphertext size and the plaintext limit
	// prevents bytes.Buffer growth from temporarily retaining old and new
	// plaintext backing arrays alongside the staged ciphertext.
	plaintext.Grow(int(min(source.binding.Size, decryptionLimit)))
	writer := &boundedSnapshotPackageBuffer{
		buffer: &plaintext,
		limit:  decryptionLimit,
	}
	input := bytes.NewReader(source.staged)
	trimmedCredential := strings.TrimSpace(*credential)
	if strings.HasPrefix(trimmedCredential, "AGE-SECRET-KEY-") {
		err := (snapshotpkg.AgeNative{}).Decrypt(
			trimmedCredential,
			input,
			writer,
		)
		if err != nil {
			zeroSnapshotPackageBytes(plaintext.Bytes())
			return nil, err
		}
	} else {
		err := (snapshotpkg.AgeNative{}).DecryptPassphrase(
			*credential,
			input,
			writer,
		)
		if err != nil {
			zeroSnapshotPackageBytes(plaintext.Bytes())
			return nil, err
		}
	}
	raw := plaintext.Bytes()
	source.clearStaged()
	return &preparedSnapshotPackage{
		readerAt:        bytes.NewReader(raw),
		size:            int64(len(raw)),
		workingSetBytes: int64(len(raw)),
		plaintext:       raw,
	}, nil
}

func (source *snapshotPackageSource) clearStaged() {
	if source == nil {
		return
	}
	zeroSnapshotPackageBytes(source.staged)
	source.staged = nil
}

func snapshotPackageBinding(
	file *os.File,
	size int64,
	identity string,
) (snapshotPackageSourceBinding, error) {
	if file == nil || size <= 0 || strings.TrimSpace(identity) == "" {
		return snapshotPackageSourceBinding{},
			errors.New("snapshot.package_source_invalid")
	}
	hasher := sha256.New()
	count, err := io.Copy(
		hasher,
		io.NewSectionReader(file, 0, size),
	)
	if err != nil || count != size {
		return snapshotPackageSourceBinding{}, errors.Join(
			errors.New("snapshot.package_source_changed"),
			err,
		)
	}
	return snapshotPackageSourceBinding{
		Hash:     "sha256:" + hex.EncodeToString(hasher.Sum(nil)),
		Size:     size,
		Identity: identity,
	}, nil
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
