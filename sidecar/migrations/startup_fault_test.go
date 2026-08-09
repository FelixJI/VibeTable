package migrations

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupMigrationFaultIsExplicitAndOneShot(t *testing.T) {
	faultFile := filepath.Join(t.TempDir(), "startup-fault.json")
	t.Setenv(startupMigrationFaultEnvironment, faultFile)

	if err := triggerStartupMigrationFault("2026080501_relation_pairs", "after-relations"); err != nil {
		t.Fatalf("unarmed fault: %v", err)
	}
	if err := os.WriteFile(
		faultFile,
		[]byte(`{"migration":"2026080501_relation_pairs","checkpoint":"after-relations"}`),
		0o600,
	); err != nil {
		t.Fatalf("arm fault: %v", err)
	}
	if err := triggerStartupMigrationFault(
		"2026080501_relation_pairs",
		"after-relations",
	); err == nil {
		t.Fatal("armed startup migration fault did not trigger")
	}
	if _, err := os.Stat(faultFile); !os.IsNotExist(err) {
		t.Fatalf("one-shot fault file still exists: %v", err)
	}
	if err := triggerStartupMigrationFault("2026080501_relation_pairs", "after-relations"); err != nil {
		t.Fatalf("consumed fault triggered twice: %v", err)
	}
}
