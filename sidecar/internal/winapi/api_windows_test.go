//go:build windows

package winapi

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMoveFileExReplacesAndFlushesExistingTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.tmp")
	target := filepath.Join(root, "target.dat")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		t.Fatal(err)
	}
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := MoveFileEx(
		sourcePtr,
		targetPtr,
		MoveFileReplaceExisting|MoveFileWriteThrough,
	); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "new" {
		t.Fatalf("replacement = %q, err = %v", raw, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
}

func TestFileLockIsExclusiveAndCanBeReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	locked, err := TryLockFile(first.Fd())
	if err != nil || !locked {
		t.Fatalf("first lock = %v, err = %v", locked, err)
	}
	locked, err = TryLockFile(second.Fd())
	if err != nil || locked {
		t.Fatalf("competing lock = %v, err = %v", locked, err)
	}
	if err := UnlockFile(first.Fd()); err != nil {
		t.Fatal(err)
	}
	locked, err = TryLockFile(second.Fd())
	if err != nil || !locked {
		t.Fatalf("lock after release = %v, err = %v", locked, err)
	}
	if err := UnlockFile(second.Fd()); err != nil {
		t.Fatal(err)
	}
}
