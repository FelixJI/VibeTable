package workspacev2

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/snapshotpkg"
)

const secureSnapshotPackageWorkspaceID = "11111111-1111-4111-8111-111111111111"

func TestSnapshotPackageBudgetsKeepExportAndImportMemoryCompatible(
	t *testing.T,
) {
	if maxSnapshotPackagePayloadBytes >= maxSnapshotPackageContainer ||
		maxSnapshotPackageContainer+
			maxSnapshotPackagePayloadBytes+
			maxSnapshotPackageMetadata > maxSnapshotWorkingSet {
		t.Fatalf(
			"payload=%d container=%d metadata=%d workingSet=%d",
			maxSnapshotPackagePayloadBytes,
			maxSnapshotPackageContainer,
			maxSnapshotPackageMetadata,
			maxSnapshotWorkingSet,
		)
	}
}

func TestPrepareSnapshotPackageSourceKeepsPlainPackageOnGrantedFile(t *testing.T) {
	metadata, entries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "snapshot.zip")
	if err := os.WriteFile(source, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	prepared, encrypted, err := prepareSnapshotPackageSource(source, nil)
	if err != nil || encrypted {
		t.Fatalf("prepared=%#v encrypted=%t err=%v", prepared, encrypted, err)
	}
	if prepared.Size() != int64(archive.Len()) {
		t.Fatalf("size=%d want=%d", prepared.Size(), archive.Len())
	}
	if prepared.workingSetBytes != int64(archive.Len()) {
		t.Fatalf(
			"working set=%d want=%d",
			prepared.workingSetBytes,
			archive.Len(),
		)
	}
	inspection, err := snapshotpkg.Inspect(
		prepared.ReaderAt(),
		prepared.Size(),
		snapshotpkg.DefaultLimits(),
		nil,
	)
	if err != nil || inspection.Manifest.Metadata != metadata {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}

	credential := "not-applicable"
	if _, _, err := prepareSnapshotPackageSource(
		source,
		&credential,
	); err == nil || err.Error() != "snapshot.credential_not_applicable" {
		t.Fatalf("credential on plaintext error=%v", err)
	}
}

func TestSnapshotPackageSourceBindingDistinguishesIdenticalReplacement(
	t *testing.T,
) {
	metadata, entries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	var agePackage bytes.Buffer
	const passphrase = "identity is independent of encryption"
	if err := (snapshotpkg.AgeNative{}).EncryptPassphrase(
		passphrase,
		bytes.NewReader(archive.Bytes()),
		&agePackage,
	); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		raw       []byte
		encrypted bool
	}{
		{name: "plain", raw: archive.Bytes()},
		{name: "age", raw: agePackage.Bytes(), encrypted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "package")
			if err := os.WriteFile(path, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			original, err := openSnapshotPackageSource(path)
			if err != nil {
				t.Fatal(err)
			}
			originalBinding := original.Binding()
			if original.Encrypted() != test.encrypted {
				t.Fatalf("encrypted=%t", original.Encrypted())
			}
			if err := original.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			replacement, err := openSnapshotPackageSource(path)
			if err != nil {
				t.Fatal(err)
			}
			defer replacement.Close()
			replacementBinding := replacement.Binding()
			if replacementBinding.Hash != originalBinding.Hash ||
				replacementBinding.Size != originalBinding.Size ||
				replacementBinding.Identity == originalBinding.Identity {
				t.Fatalf(
					"original=%#v replacement=%#v",
					originalBinding,
					replacementBinding,
				)
			}
		})
	}
}

func TestSnapshotPackageSourceReadsFromImmutableStaging(
	t *testing.T,
) {
	metadata, entries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.zip")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := openSnapshotPackageSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if cap(source.staged) != len(source.staged) {
		t.Fatalf(
			"staging capacity=%d want exact size=%d",
			cap(source.staged),
			len(source.staged),
		)
	}
	staged := source.staged
	defer source.Close()

	replacement := bytes.Repeat([]byte{'x'}, archive.Len())
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := source.prepare(nil, maxSnapshotWorkingSet)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if _, err := snapshotpkg.Inspect(
		prepared.ReaderAt(),
		prepared.Size(),
		snapshotpkg.DefaultLimits(),
		nil,
	); err != nil {
		t.Fatalf("immutable staged package was not retained: %v", err)
	}
	if err := source.VerifyUnchanged(); err == nil {
		t.Fatal("source mutation was not detected")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	for index, value := range staged {
		if value != 0 {
			t.Fatalf("staged byte %d was not cleared", index)
		}
	}
}

func TestPrepareSnapshotPackageSourceDecryptsOnlyIntoClearableMemory(t *testing.T) {
	metadata, entries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	const passphrase = "correct horse battery staple"
	if err := (snapshotpkg.AgeNative{}).EncryptPassphrase(
		passphrase,
		bytes.NewReader(archive.Bytes()),
		&encrypted,
	); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "snapshot.age")
	if err := os.WriteFile(source, encrypted.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	before := directoryEntryNames(t, root)

	prepared, isEncrypted, err := prepareSnapshotPackageSource(
		source,
		snapshotPackageCredential(passphrase),
	)
	if err != nil || !isEncrypted {
		t.Fatalf(
			"prepared=%#v encrypted=%t err=%v",
			prepared,
			isEncrypted,
			err,
		)
	}
	if prepared.Size() != int64(archive.Len()) ||
		len(prepared.plaintext) != archive.Len() ||
		prepared.workingSetBytes != int64(archive.Len()) {
		t.Fatalf(
			"prepared size=%d plaintext=%d want=%d",
			prepared.Size(),
			len(prepared.plaintext),
			archive.Len(),
		)
	}
	if opened, err := openSnapshotPackageSource(source); err != nil {
		t.Fatal(err)
	} else {
		encryptedPrepared, prepareErr := opened.prepare(
			snapshotPackageCredential(passphrase),
			maxSnapshotWorkingSet,
		)
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		if len(opened.staged) != 0 {
			t.Fatal("encrypted ciphertext staging was not released after decrypt")
		}
		if err := encryptedPrepared.Close(); err != nil {
			t.Fatal(err)
		}
		if err := opened.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := snapshotpkg.Inspect(
		prepared.ReaderAt(),
		prepared.Size(),
		snapshotpkg.DefaultLimits(),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	plaintext := prepared.plaintext
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	for index, value := range plaintext {
		if value != 0 {
			t.Fatalf("plaintext byte %d was not cleared", index)
		}
	}
	after := directoryEntryNames(t, root)
	if !slices.Equal(before, after) {
		t.Fatalf("prepare left disk artifacts: before=%v after=%v", before, after)
	}
}

func TestInspectPreparedPackageRejectsAggregateWorkingSetOverflow(
	t *testing.T,
) {
	metadata, entries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	prepared := &preparedSnapshotPackage{
		readerAt:        bytes.NewReader(archive.Bytes()),
		size:            int64(archive.Len()),
		workingSetBytes: maxSnapshotWorkingSet,
	}
	if _, _, err := inspectPreparedPackage(
		prepared,
		nil,
		true,
	); !errors.Is(err, snapshotpkg.ErrResourceLimit) {
		t.Fatalf("aggregate budget error = %v", err)
	}
}

func TestInspectPreparedPackageBoundsDecompressionByRemainingWorkingSet(
	t *testing.T,
) {
	metadata, entries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	prepared := &preparedSnapshotPackage{
		readerAt: bytes.NewReader(archive.Bytes()),
		size:     int64(archive.Len()),
		// Leave one byte available. Inspect must pass that remaining budget
		// into the ZIP reader instead of using its much larger defaults.
		workingSetBytes: maxSnapshotWorkingSet - 1,
	}
	if _, _, err := inspectPreparedPackage(
		prepared,
		nil,
		true,
	); !errors.Is(err, snapshotpkg.ErrResourceLimit) {
		t.Fatalf("remaining working-set budget error = %v", err)
	}
}

func TestInspectPreparedPackageAllocatesEntriesAtVerifiedSize(t *testing.T) {
	metadata, fixtureEntries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(
		&archive,
		metadata,
		fixtureEntries,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	prepared := &preparedSnapshotPackage{
		readerAt:        bytes.NewReader(archive.Bytes()),
		size:            int64(archive.Len()),
		workingSetBytes: int64(archive.Len()),
	}
	_, entries, err := inspectPreparedPackage(prepared, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range entries {
		if cap(raw) != len(raw) {
			t.Fatalf(
				"entry %q capacity=%d want exact size=%d",
				name,
				cap(raw),
				len(raw),
			)
		}
	}
}

func TestInspectPreparedPackageAcceptsBoundedHighlyCompressiblePayload(
	t *testing.T,
) {
	metadata, entries := secureSnapshotPackageFixture()
	entries["objects/compressible"] = bytes.Repeat([]byte{0}, 1<<20)
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	prepared := &preparedSnapshotPackage{
		readerAt:        bytes.NewReader(archive.Bytes()),
		size:            int64(archive.Len()),
		workingSetBytes: int64(archive.Len()),
	}
	if _, decoded, err := inspectPreparedPackage(
		prepared,
		nil,
		true,
	); err != nil {
		t.Fatalf("bounded compressible package rejected: %v", err)
	} else {
		clearSnapshotPackageEntries(decoded)
	}
}

func TestPrepareSnapshotPackageSourceFailsClosedAtPlaintextLimit(t *testing.T) {
	metadata, entries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	const passphrase = "bounded package"
	if err := (snapshotpkg.AgeNative{}).EncryptPassphrase(
		passphrase,
		bytes.NewReader(archive.Bytes()),
		&encrypted,
	); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "snapshot.age")
	if err := os.WriteFile(source, encrypted.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	before := directoryEntryNames(t, root)
	prepared, isEncrypted, err := prepareSnapshotPackageSourceWithLimit(
		source,
		snapshotPackageCredential(passphrase),
		int64(archive.Len()-1),
	)
	if prepared != nil || !isEncrypted ||
		!errors.Is(err, snapshotpkg.ErrResourceLimit) {
		t.Fatalf(
			"prepared=%#v encrypted=%t err=%v",
			prepared,
			isEncrypted,
			err,
		)
	}
	after := directoryEntryNames(t, root)
	if !slices.Equal(before, after) {
		t.Fatalf("failed prepare left disk artifacts: %v -> %v", before, after)
	}
}

func TestPrepareSnapshotPackageSourceAcceptsAgeIdentity(t *testing.T) {
	metadata, entries := secureSnapshotPackageFixture()
	var archive bytes.Buffer
	if err := snapshotpkg.Export(&archive, metadata, entries, nil); err != nil {
		t.Fatal(err)
	}
	identity, recipient, err := snapshotpkg.GenerateAgeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := (snapshotpkg.AgeNative{}).EncryptRecipients(
		[]string{recipient},
		bytes.NewReader(archive.Bytes()),
		&encrypted,
	); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "snapshot.age")
	if err := os.WriteFile(source, encrypted.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	credential := " \t" + identity + "\r\n"
	prepared, isEncrypted, err := prepareSnapshotPackageSource(
		source,
		&credential,
	)
	if err != nil || !isEncrypted {
		t.Fatalf("prepared=%#v encrypted=%t err=%v", prepared, isEncrypted, err)
	}
	defer prepared.Close()
	if _, err := snapshotpkg.Inspect(
		prepared.ReaderAt(),
		prepared.Size(),
		snapshotpkg.DefaultLimits(),
		nil,
	); err != nil {
		t.Fatal(err)
	}
}

func TestStreamAgeSnapshotPackageProducesInspectablePackage(t *testing.T) {
	metadata, entries := secureSnapshotPackageFixture()
	identity, recipient, err := snapshotpkg.GenerateAgeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := streamAgeSnapshotPackage(
		&encrypted,
		metadata,
		entries,
		nil,
		[]string{recipient},
		"",
	); err != nil {
		t.Fatal(err)
	}
	var plaintext bytes.Buffer
	if err := (snapshotpkg.AgeNative{}).Decrypt(
		identity,
		bytes.NewReader(encrypted.Bytes()),
		&plaintext,
	); err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(plaintext.Bytes())
	inspection, err := snapshotpkg.Inspect(
		reader,
		int64(reader.Len()),
		snapshotpkg.DefaultLimits(),
		nil,
	)
	if err != nil || inspection.Manifest.Metadata != metadata {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
}

func TestStreamAgeSnapshotPackageSupportsPassphrase(t *testing.T) {
	metadata, entries := secureSnapshotPackageFixture()
	const passphrase = "streamed package passphrase"
	var encrypted bytes.Buffer
	if err := streamAgeSnapshotPackage(
		&encrypted,
		metadata,
		entries,
		nil,
		nil,
		passphrase,
	); err != nil {
		t.Fatal(err)
	}
	var plaintext bytes.Buffer
	if err := (snapshotpkg.AgeNative{}).DecryptPassphrase(
		passphrase,
		bytes.NewReader(encrypted.Bytes()),
		&plaintext,
	); err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(plaintext.Bytes())
	if _, err := snapshotpkg.Inspect(
		reader,
		int64(reader.Len()),
		snapshotpkg.DefaultLimits(),
		nil,
	); err != nil {
		t.Fatal(err)
	}
}

func TestStreamAgeSnapshotPackageJoinsProducerOnConsumerFailure(t *testing.T) {
	metadata, entries := secureSnapshotPackageFixture()
	_, recipient, err := snapshotpkg.GenerateAgeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("simulated encrypted output failure")
	done := make(chan error, 1)
	go func() {
		done <- streamAgeSnapshotPackage(
			failingSnapshotPackageWriter{err: sentinel},
			metadata,
			entries,
			nil,
			[]string{recipient},
			"",
		)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, sentinel) {
			t.Fatalf("stream error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streaming export leaked a blocked ZIP producer")
	}

	invalidEntries := map[string][]byte{"../escape": []byte("unsafe")}
	if err := streamAgeSnapshotPackage(
		io.Discard,
		metadata,
		invalidEntries,
		nil,
		[]string{recipient},
		"",
	); !errors.Is(err, snapshotpkg.ErrInvalidPackage) {
		t.Fatalf("producer failure=%v", err)
	}
}

type failingSnapshotPackageWriter struct {
	err error
}

func (writer failingSnapshotPackageWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

func secureSnapshotPackageFixture() (
	snapshotpkg.Metadata,
	map[string][]byte,
) {
	return snapshotpkg.Metadata{
			FormatVersion:     2,
			WorkspaceID:       secureSnapshotPackageWorkspaceID,
			SnapshotID:        "33333333-3333-4333-8333-333333333333",
			WriterVersion:     "2.0.0",
			MinimumAppVersion: "2.0.0",
		}, map[string][]byte{
			"snapshot/catalog.json": []byte(`{"proof":"catalog"}`),
			"objects/database":      []byte("plaintext database proof"),
		}
}

func snapshotPackageCredential(value string) *string {
	return &value
}

func directoryEntryNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Name())
	}
	return result
}
