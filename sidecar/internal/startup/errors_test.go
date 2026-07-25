package startup

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFaultGateDiskFullReturnsStableActionableError(t *testing.T) {
	err := checkDataDirectory(t.TempDir(), func(string) error {
		return syscall.ENOSPC
	})
	assertStable(t, err, CodeStorageFull, "free disk space")
}

func TestFaultGateFileLockReturnsStableActionableError(t *testing.T) {
	err := checkDataDirectory(t.TempDir(), func(string) error {
		return errors.New(
			"sharing violation: file is used by another process; access is denied",
		)
	})
	assertStable(t, err, CodeStorageLocked, "close the other VibeTable instance")
}

func TestFaultGateReadOnlyDirectoryReturnsStableActionableError(t *testing.T) {
	err := checkDataDirectory(t.TempDir(), func(string) error {
		return os.ErrPermission
	})
	assertStable(t, err, CodeStorageReadOnly, "writable local data directory")
}

func TestFaultGatePortOccupiedReturnsStableActionableError(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	_, bindErr := net.Listen("tcp4", occupied.Addr().String())
	if bindErr == nil {
		t.Fatal("second listener unexpectedly acquired occupied port")
	}
	stable := Classify("bind loopback listener", bindErr)
	assertStable(t, stable, CodePortUnavailable, "loopback port")
}

func TestFaultGateCorruptMigrationStopsBeforeStartup(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifest, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ValidateMigrationManifest(func() error {
		raw, readErr := os.ReadFile(manifest)
		if readErr != nil {
			return readErr
		}
		if string(raw) != "{}" {
			return errors.New("migration manifest checksum mismatch")
		}
		return nil
	})
	assertStable(t, err, CodeMigrationCorrupt, "known-good backup")
}

func TestFaultGateRealWritableProbeDoesNotLeaveSentinel(t *testing.T) {
	root := t.TempDir()
	if err := CheckDataDirectory(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".vibetable-startup-") {
			t.Fatalf("startup probe leaked sentinel %q", entry.Name())
		}
	}
}

func TestFaultGateDataPathThatIsAFileIsActionable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := CheckDataDirectory(path)
	var stable *Error
	if !errors.As(err, &stable) || stable.Code == "" ||
		!strings.Contains(stable.Message, "retry") {
		t.Fatalf("error = %#v", err)
	}
}

func assertStable(t *testing.T, err error, code, guidance string) {
	t.Helper()
	var stable *Error
	if !errors.As(err, &stable) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if stable.Code != code || !strings.Contains(stable.Message, guidance) {
		t.Fatalf("stable error = %#v", stable)
	}
}
