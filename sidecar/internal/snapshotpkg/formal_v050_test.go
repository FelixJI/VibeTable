package snapshotpkg

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFormalV050SnapshotProducerInputs(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "formal-v0.5.0")
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	plain := read("plain.vtsnapshot")
	var decrypted bytes.Buffer
	if err := (AgeNative{}).DecryptPassphrase("vibetable-corpus-v050", bytes.NewReader(read("passphrase.vtsnapshot")), &decrypted); err != nil {
		t.Fatal(err)
	}
	expected := Metadata{
		FormatVersion: 2,
		WorkspaceID:   "acb519c1-192b-49c8-8a53-bdf60de1eeaf",
		SnapshotID:    "8d848b54-b098-4b9d-a968-c53bcfa8f21e",
		WriterVersion: "2.0.0", MinimumAppVersion: "2.0.0",
	}
	var contents []map[string][]byte
	for _, raw := range [][]byte{plain, decrypted.Bytes()} {
		inspection, err := Inspect(bytes.NewReader(raw), int64(len(raw)), DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if inspection.Manifest.Metadata != expected {
			t.Fatalf("unexpected old metadata: %#v", inspection.Manifest.Metadata)
		}
		archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		entries := map[string][]byte{}
		for _, file := range archive.File {
			stream, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(stream)
			closeErr := stream.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("read %s: %v / %v", file.Name, err, closeErr)
			}
			entries[file.Name] = content
		}
		contents = append(contents, entries)
	}
	if !reflect.DeepEqual(contents[0], contents[1]) {
		t.Fatal("formal exports do not contain identical snapshot entries")
	}
	truncated := read("truncated.vtsnapshot")
	if !bytes.Equal(truncated, plain[:len(plain)-22]) {
		t.Fatal("rejection input differs from documented truncation")
	}
	if _, err := Inspect(bytes.NewReader(truncated), int64(len(truncated)), DefaultLimits()); !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("truncated input: %v", err)
	}
}
