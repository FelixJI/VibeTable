package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateNameRejectsTraversalAndAmbiguousNames(t *testing.T) {
	for _, name := range []string{
		"manual_001.zip",
		"release-20260724.zip",
	} {
		if err := ValidateName(name); err != nil {
			t.Fatalf("ValidateName(%q): %v", name, err)
		}
	}
	for _, name := range []string{
		"", "../backup.zip", "Backup.zip", "backup",
		"backup.zip.exe", "a/b.zip", "_backup.zip",
	} {
		if err := ValidateName(name); !IsError(
			err, "backup.name_invalid",
		) {
			t.Fatalf("ValidateName(%q) error = %#v", name, err)
		}
	}
}

func TestApplyPendingRestoreRejectsTamperingBeforeMovingLiveData(t *testing.T) {
	dataDir := t.TempDir()
	live := filepath.Join(dataDir, "data.db")
	if err := os.WriteFile(live, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(
		filepath.Join(dataDir, "backups"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dataDir, restoreMarkerName),
		[]byte(`{"name":"missing.zip","sha256":"`+
			strings.Repeat("0", 64)+`"} trailing`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyPendingRestore(dataDir)
	if err == nil || applied {
		t.Fatalf("tampered marker result = %v, %v", applied, err)
	}
	raw, readErr := os.ReadFile(live)
	if readErr != nil || string(raw) != "live" {
		t.Fatalf("live data changed: %q, %v", raw, readErr)
	}
}

func TestMoveDirectoryContentsRollsBackPartialMove(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(
			filepath.Join(source, name),
			[]byte(name),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(
		filepath.Join(destination, "b.txt"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	if err := moveDirectoryContents(
		source, destination, map[string]bool{},
	); err == nil {
		t.Fatal("move with destination conflict unexpectedly succeeded")
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(source, name)); err != nil {
			t.Fatalf("%s was not rolled back: %v", name, err)
		}
	}
}
