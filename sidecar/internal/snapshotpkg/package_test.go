package snapshotpkg

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type memoryPackageCapability struct {
	*bytes.Reader
	closed bool
}

func (capability *memoryPackageCapability) Size() int64 { return capability.Reader.Size() }
func (capability *memoryPackageCapability) Close() error {
	capability.closed = true
	return nil
}

type memoryPathResolver struct {
	raw       []byte
	opened    int
	lastGrant string
	source    *memoryPackageCapability
}

func (resolver *memoryPathResolver) OpenSnapshotPackage(
	_ context.Context,
	grant string,
	_ *string,
) (PackageReadCapability, error) {
	resolver.opened++
	resolver.lastGrant = grant
	resolver.source = &memoryPackageCapability{Reader: bytes.NewReader(resolver.raw)}
	return resolver.source, nil
}

type memoryImportTarget struct {
	beginCalls int
	beginErr   error
	staging    *memoryImportStaging
}

func (target *memoryImportTarget) Begin(
	_ context.Context,
	inspection Inspection,
	mode TargetMode,
) (ImportStaging, error) {
	target.beginCalls++
	if target.beginErr != nil {
		return nil, target.beginErr
	}
	target.staging = &memoryImportStaging{
		entries: map[string]*bytes.Buffer{},
		receipt: ImportOperation{
			OperationID: "import-1",
			WorkspaceID: inspection.Manifest.Metadata.WorkspaceID,
			SnapshotID:  inspection.Manifest.Metadata.SnapshotID,
			TargetMode:  mode,
		},
	}
	return target.staging, nil
}

type memoryImportStaging struct {
	entries   map[string]*bytes.Buffer
	receipt   ImportOperation
	committed bool
	aborted   bool
}

type bufferWriteCloser struct{ *bytes.Buffer }

func (bufferWriteCloser) Close() error { return nil }

func (staging *memoryImportStaging) CreateEntry(
	_ context.Context,
	name string,
	_ int64,
) (io.WriteCloser, error) {
	buffer := &bytes.Buffer{}
	staging.entries[name] = buffer
	return bufferWriteCloser{Buffer: buffer}, nil
}
func (staging *memoryImportStaging) Commit(context.Context) (ImportOperation, error) {
	staging.committed = true
	return staging.receipt, nil
}
func (staging *memoryImportStaging) Abort(context.Context) error {
	staging.aborted = true
	return nil
}

func TestRoundTripVerifiesHashesAndWorkspaceMAC(t *testing.T) {
	var output bytes.Buffer
	key := []byte("workspace-secret")
	if err := Export(&output, Metadata{
		FormatVersion: 2, WorkspaceID: "w", SnapshotID: "s",
		WriterVersion: "2.0.0", MinimumAppVersion: "2.0.0",
	}, map[string][]byte{"objects/a": []byte("content"), "metadata/readme.txt": []byte("hello")}, key); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(bytes.NewReader(output.Bytes()), int64(output.Len()), DefaultLimits(), key)
	if err != nil || !inspection.TrustedForOriginalWorkspace {
		t.Fatalf("inspection failed: %#v %v", inspection, err)
	}
	if err := RequireOriginalWorkspaceTrust(inspection); err != nil {
		t.Fatal(err)
	}
	thirdParty, err := Inspect(bytes.NewReader(output.Bytes()), int64(output.Len()), DefaultLimits(), nil)
	if err != nil || thirdParty.TrustedForOriginalWorkspace ||
		!errors.Is(RequireOriginalWorkspaceTrust(thirdParty), ErrUntrusted) {
		t.Fatalf("third-party package trust confusion: %#v %v", thirdParty, err)
	}
}

func TestInspectRejectsTraversalDuplicateAndResourceBomb(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", `windows\escape`} {
		var output bytes.Buffer
		archive := zip.NewWriter(&output)
		part, _ := archive.Create(name)
		_, _ = part.Write([]byte("bad"))
		_ = archive.Close()
		if _, err := Inspect(bytes.NewReader(output.Bytes()), int64(output.Len()), DefaultLimits(), nil); err == nil {
			t.Fatalf("accepted unsafe name %q", name)
		}
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	first, _ := archive.Create("same")
	_, _ = first.Write([]byte("one"))
	second, _ := archive.Create("same")
	_, _ = second.Write([]byte("two"))
	_ = archive.Close()
	if _, err := Inspect(bytes.NewReader(output.Bytes()), int64(output.Len()), DefaultLimits(), nil); err == nil {
		t.Fatal("accepted duplicate entry")
	}
}

func TestInspectRejectsExcessivePathAndCompressionRatio(t *testing.T) {
	t.Run("path", func(t *testing.T) {
		var output bytes.Buffer
		archive := zip.NewWriter(&output)
		writer, err := archive.Create("objects/" + strings.Repeat("a", 80))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write([]byte("x"))
		_ = archive.Close()
		limits := DefaultLimits()
		limits.MaxPathBytes = 40
		if _, err := Inspect(
			bytes.NewReader(output.Bytes()),
			int64(output.Len()),
			limits,
			nil,
		); !errors.Is(err, ErrInvalidPackage) {
			t.Fatalf("long path was accepted: %v", err)
		}
	})
	t.Run("compression-ratio", func(t *testing.T) {
		entries := map[string][]byte{
			"objects/highly-compressible": bytes.Repeat([]byte("0"), 1<<20),
		}
		var output bytes.Buffer
		if err := Export(&output, Metadata{
			FormatVersion: 2, WorkspaceID: "w", SnapshotID: "s",
			WriterVersion: "2.0.0", MinimumAppVersion: "2.0.0",
		}, entries, nil); err != nil {
			t.Fatal(err)
		}
		limits := DefaultLimits()
		limits.MaxCompressionRatio = 10
		if _, err := Inspect(
			bytes.NewReader(output.Bytes()),
			int64(output.Len()),
			limits,
			nil,
		); !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("compression bomb was accepted: %v", err)
		}
	})
}

func TestTamperedEntryFailsBeforeImport(t *testing.T) {
	var output bytes.Buffer
	if err := Export(&output, Metadata{
		FormatVersion: 2, WorkspaceID: "w", SnapshotID: "s",
	}, map[string][]byte{"object": []byte("content")}, nil); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Replace(output.Bytes(), []byte("content"), []byte("altered"), 1)
	if bytes.Equal(raw, output.Bytes()) {
		t.Skip("compressed content not directly replaceable")
	}
	if _, err := Inspect(bytes.NewReader(raw), int64(len(raw)), DefaultLimits(), nil); err == nil {
		t.Fatal("accepted tampered package")
	}
}

func TestNativeAgeEnvelopeRoundTripsOfficialFormat(t *testing.T) {
	identity, recipient, err := GenerateAgeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := (AgeNative{}).Encrypt(recipient, bytes.NewReader([]byte("snapshot package")), &encrypted); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encrypted.Bytes(), []byte("age-encryption.org/v1")) {
		t.Fatalf("not an official age envelope: %q", encrypted.Bytes()[:min(32, encrypted.Len())])
	}
	var decrypted bytes.Buffer
	if err := (AgeNative{}).Decrypt(identity, bytes.NewReader(encrypted.Bytes()), &decrypted); err != nil {
		t.Fatal(err)
	}
	if decrypted.String() != "snapshot package" {
		t.Fatalf("round trip mismatch: %q", decrypted.String())
	}
}

func TestNativeAgePassphraseUsesScryptAndRejectsWrongCredential(t *testing.T) {
	var encrypted bytes.Buffer
	native := AgeNative{}
	if err := native.EncryptPassphrase(
		"correct horse battery staple",
		bytes.NewReader([]byte("portable snapshot")),
		&encrypted,
	); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encrypted.Bytes(), []byte("-> scrypt ")) {
		t.Fatal("age envelope does not use the standard scrypt stanza")
	}
	if err := native.DecryptPassphrase(
		"wrong",
		bytes.NewReader(encrypted.Bytes()),
		io.Discard,
	); !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("wrong passphrase was not rejected: %v", err)
	}
	var decrypted bytes.Buffer
	if err := native.DecryptPassphrase(
		"correct horse battery staple",
		bytes.NewReader(encrypted.Bytes()),
		&decrypted,
	); err != nil {
		t.Fatal(err)
	}
	if decrypted.String() != "portable snapshot" {
		t.Fatalf("unexpected plaintext: %q", decrypted.String())
	}
}

func TestNativeAgeEnvelopeIsReadableByBundledCLI(t *testing.T) {
	executable := os.Getenv("VIBETABLE_AGE_CLI")
	if executable == "" {
		t.Skip("set VIBETABLE_AGE_CLI to the bundled fixed age executable")
	}
	identity, recipient, err := GenerateAgeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := (AgeNative{}).Encrypt(recipient, bytes.NewReader([]byte("interop")), &encrypted); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "--decrypt", "--identity", identityPath)
	command.Stdin = bytes.NewReader(encrypted.Bytes())
	output, err := command.Output()
	if err != nil || string(output) != "interop" {
		t.Fatalf("age CLI interop failed: %q %v", output, err)
	}
	command = exec.Command(executable, "--encrypt", "--recipient", recipient)
	command.Stdin = strings.NewReader("cli-to-native")
	encryptedByCLI, err := command.Output()
	if err != nil {
		t.Fatalf("age CLI encryption failed: %v", err)
	}
	var nativeOutput bytes.Buffer
	if err := (AgeNative{}).Decrypt(
		identity,
		bytes.NewReader(encryptedByCLI),
		&nativeOutput,
	); err != nil || nativeOutput.String() != "cli-to-native" {
		t.Fatalf("native API did not read CLI envelope: %q %v", nativeOutput.String(), err)
	}
}

func TestImporterConsumesPathGrantVerifiesThenCommitsStaging(t *testing.T) {
	var archive bytes.Buffer
	if err := Export(&archive, Metadata{
		FormatVersion: 2, WorkspaceID: "workspace-1", SnapshotID: "snapshot-1",
	}, map[string][]byte{"objects/a": []byte("content")}, nil); err != nil {
		t.Fatal(err)
	}
	paths := &memoryPathResolver{raw: archive.Bytes()}
	target := &memoryImportTarget{}
	importer, err := NewImporter(paths, target, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := importer.Import(
		context.Background(), "grant-import-1", nil, nil,
		TargetNewWorkspace, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if paths.opened != 1 || paths.lastGrant != "grant-import-1" || !paths.source.closed {
		t.Fatalf("path grant lifetime was not bounded: %#v", paths)
	}
	if target.beginCalls != 1 || target.staging == nil ||
		!target.staging.committed || target.staging.aborted ||
		target.staging.entries["objects/a"].String() != "content" ||
		operation.WorkspaceID != "workspace-1" {
		t.Fatalf("import did not atomically publish verified staging: %#v %#v", target, operation)
	}
}

func TestImporterFailsBeforeStagingForCorruptOrUntrustedCurrentPackage(t *testing.T) {
	for _, test := range []struct {
		name     string
		raw      []byte
		key      []byte
		mode     TargetMode
		current  string
		expected error
	}{
		{
			name: "truncated", raw: []byte("not a zip"), mode: TargetNewWorkspace,
			expected: ErrInvalidPackage,
		},
		{
			name: "untrusted current", mode: TargetCurrentWorkspace, current: "workspace-1",
			expected: ErrUntrusted,
		},
		{
			name: "wrong workspace", mode: TargetCurrentWorkspace, current: "workspace-2",
			expected: ErrWorkspaceConflict,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := test.raw
			if raw == nil {
				var archive bytes.Buffer
				if err := Export(&archive, Metadata{
					FormatVersion: 2, WorkspaceID: "workspace-1", SnapshotID: "snapshot-1",
				}, map[string][]byte{"objects/a": []byte("content")}, []byte("workspace-key")); err != nil {
					t.Fatal(err)
				}
				raw = archive.Bytes()
			}
			paths := &memoryPathResolver{raw: raw}
			target := &memoryImportTarget{}
			importer, err := NewImporter(paths, target, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			_, err = importer.Import(
				context.Background(), "grant", nil, test.key, test.mode, test.current,
			)
			if !errors.Is(err, test.expected) {
				t.Fatalf("error = %v, want %v", err, test.expected)
			}
			if target.beginCalls != 0 {
				t.Fatal("formal target was reached before package trust was established")
			}
		})
	}
}

func TestImporterAbortsStagingWhenPublicationReceiptIsInvalid(t *testing.T) {
	var archive bytes.Buffer
	if err := Export(&archive, Metadata{
		FormatVersion: 2, WorkspaceID: "workspace-1", SnapshotID: "snapshot-1",
	}, map[string][]byte{"objects/a": []byte("content")}, nil); err != nil {
		t.Fatal(err)
	}
	paths := &memoryPathResolver{raw: archive.Bytes()}
	target := &memoryImportTarget{}
	importer, err := NewImporter(paths, target, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	targetBegin := target
	_ = targetBegin
	// The target receipt is generated during Begin, so override it from a thin
	// wrapper after staging creation.
	badTarget := &receiptMutatingTarget{inner: target}
	importer.target = badTarget
	if _, err := importer.Import(
		context.Background(), "grant", nil, nil, TargetNewWorkspace, "",
	); err == nil {
		t.Fatal("invalid publication receipt was accepted")
	}
	if target.staging == nil || !target.staging.aborted {
		t.Fatal("invalid publication did not abort staging")
	}
}

func TestFirstReleaseCompatibilityCorpusIsReadableByImporterAndStandardZip(t *testing.T) {
	path := filepath.Join(
		"..", "..", "..", "contracts", "v2", "fixtures",
		"snapshot-package-v2.vtsnapshot",
	)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(file, info.Size(), DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Manifest.Metadata.FormatVersion != 2 ||
		inspection.Manifest.Metadata.WriterVersion != "0.1.0" ||
		inspection.Manifest.Metadata.MinimumAppVersion != "0.1.0" ||
		len(inspection.Manifest.Entries) != 3 {
		t.Fatalf("unexpected corpus package: %#v", inspection)
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil || len(archive.File) != 4 {
		t.Fatalf("standard ZIP reader failed: entries=%d err=%v", len(archive.File), err)
	}
}

type receiptMutatingTarget struct{ inner *memoryImportTarget }

func (target *receiptMutatingTarget) Begin(
	ctx context.Context,
	inspection Inspection,
	mode TargetMode,
) (ImportStaging, error) {
	staging, err := target.inner.Begin(ctx, inspection, mode)
	if err == nil {
		target.inner.staging.receipt.WorkspaceID = "other-workspace"
	}
	return staging, err
}
