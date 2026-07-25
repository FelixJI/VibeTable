package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestE2EMutationBarrierIsDormantWithoutExplicitArm(t *testing.T) {
	t.Setenv(e2eMutationBarrierDirectoryEnvironment, "")

	if injector := newE2EMutationBarrierFromEnvironment(); injector != nil {
		t.Fatal("unarmed E2E mutation barrier was enabled")
	}
}

func TestE2EMutationBarrierSignalsAfterRecordAndBlocksUntilRelease(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(e2eMutationBarrierDirectoryEnvironment, directory)
	armPath := filepath.Join(directory, e2eMutationBarrierArmFile)
	if err := os.WriteFile(armPath, []byte("armed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	injector := newE2EMutationBarrierFromEnvironment()
	if injector == nil {
		t.Fatal("armed E2E mutation barrier was not enabled")
	}
	if err := injector("before_commit"); err != nil {
		t.Fatalf("unrelated fault point failed: %v", err)
	}

	completed := make(chan error, 1)
	go func() {
		completed <- injector(e2eMutationBarrierPoint)
	}()

	readyPath := filepath.Join(directory, e2eMutationBarrierReadyFile)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("E2E mutation barrier did not publish readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	var ready e2eMutationBarrierReady
	if err := json.Unmarshal(raw, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.ProcessID != os.Getpid() || ready.Point != e2eMutationBarrierPoint {
		t.Fatalf("unexpected readiness payload: %#v", ready)
	}
	select {
	case err := <-completed:
		t.Fatalf("barrier returned before release: %v", err)
	default:
	}

	releasePath := filepath.Join(directory, e2eMutationBarrierReleaseFile)
	if err := os.WriteFile(releasePath, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("released barrier failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("barrier did not return after release")
	}
	if _, err := os.Stat(armPath); !os.IsNotExist(err) {
		t.Fatalf("arm file still exists: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(directory, e2eMutationBarrierClaimedFile),
	); err != nil {
		t.Fatalf("claim file missing: %v", err)
	}
}
