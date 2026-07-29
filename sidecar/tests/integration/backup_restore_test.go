package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/backup"
	"github.com/vibetable/vibetable/sidecar/internal/backupreceipt"
)

func TestBackupContainsWholeHealthyPocketBaseData(t *testing.T) {
	dataDir := t.TempDir()
	app := bootstrapApp(t, dataDir)
	defer func() {
		if app != nil {
			resetApp(t, app)
		}
	}()
	manager, err := attachments.New(
		[]byte(strings.Repeat("b", 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	service := backup.New(app, manager, testBackupReceiptKey).
		WithRestart(func() error { return nil }).
		WithNow(func() time.Time {
			return time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
		})
	result, err := service.Create(
		context.Background(), "manual_001.zip",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Backup.Name != "manual_001.zip" ||
		result.Backup.Size <= 0 ||
		len(result.Backup.SHA256) != 64 ||
		result.Receipt == "" ||
		!result.Integrity.Valid {
		t.Fatalf("backup result = %#v", result)
	}
	if err := backupreceipt.Verify(
		context.Background(), app, result.Receipt, testBackupReceiptKey,
	); err != nil {
		t.Fatalf("verify fresh backup receipt: %v", err)
	}
	if err := backupreceipt.Verify(
		context.Background(), app, result.Receipt+"tampered", testBackupReceiptKey,
	); err == nil {
		t.Fatal("tampered backup receipt was accepted")
	}
	entries, err := service.List(context.Background())
	if err != nil || len(entries) != 1 ||
		entries[0] != result.Backup {
		t.Fatalf("backup list = %#v, err=%v", entries, err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	sentinel := filepath.Join(dataDir, "created_after_backup.txt")
	if err := os.WriteFile(sentinel, []byte("must disappear"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.Restore(context.Background(), result.Backup.Name); err != nil {
		t.Fatalf("stage created backup restore: %v", err)
	}
	resetApp(t, app)
	app = nil
	applied, err := backup.ApplyPendingRestore(dataDir)
	if err != nil || !applied {
		t.Fatalf("ApplyPendingRestore() = %v, %v", applied, err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("post-backup sentinel survived restore: %v", err)
	}
	reapplied, err := backup.ApplyPendingRestore(dataDir)
	if err != nil || reapplied {
		t.Fatalf("interrupted restore recovery = %v, %v", reapplied, err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("interrupted restore did not roll back live data: %v", err)
	}

	app = bootstrapApp(t, dataDir)
	service = backup.New(app, manager, testBackupReceiptKey).
		WithRestart(func() error { return nil }).
		WithNow(func() time.Time {
			return time.Date(2026, 7, 24, 1, 0, 1, 0, time.UTC)
		})
	if err := service.Restore(context.Background(), result.Backup.Name); err != nil {
		t.Fatalf("restage created backup restore: %v", err)
	}
	resetApp(t, app)
	app = nil
	applied, err = backup.ApplyPendingRestore(dataDir)
	if err != nil || !applied {
		t.Fatalf("ApplyPendingRestore(second) = %v, %v", applied, err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("post-backup sentinel survived second restore: %v", err)
	}
	app = bootstrapApp(t, dataDir)
	if err := backup.CommitPendingRestore(dataDir); err != nil {
		t.Fatalf("CommitPendingRestore(): %v", err)
	}
	service = backup.New(app, manager, testBackupReceiptKey)
	entries, err = service.List(context.Background())
	if err != nil || len(entries) != 3 {
		t.Fatalf("backup list after restore = %#v, err=%v", entries, err)
	}
	safetyCopies := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, "pre_restore_") {
			safetyCopies++
		}
	}
	if safetyCopies != 2 {
		t.Fatalf("pre-restore safety backup count = %d, want 2", safetyCopies)
	}
}
