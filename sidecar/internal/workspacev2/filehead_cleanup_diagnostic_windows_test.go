package workspacev2

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDiagnosticFileHeadDirectoryCleanup(t *testing.T) {
	var diagnosticRoot string
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		entries, readErr := os.ReadDir(diagnosticRoot)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Logf("[DEBUG-filehead] after failed cleanup entries=%q readErr=%v", names, readErr)
	})
	diagnosticRoot = t.TempDir()
	headPath := filepath.Join(diagnosticRoot, "filehistory-head.db")
	historyID := objectrepo.ManifestID(
		"manifest_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	fileHeadID := objectrepo.ManifestID(
		"manifest_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
	payload, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"workspaceId":   testWorkspaceID,
		"historyRoot":   historyID,
		"fileRevision":  uint64(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[objectrepo.ManifestID]objectrepo.ManifestRecord{
		fileHeadID: {
			ID:   fileHeadID,
			Name: "file-state-head",
			Labels: map[string]string{
				"type": "file-state-head",
			},
			Payload: payload,
		},
		historyID: {
			ID:   historyID,
			Name: "filehistory-root",
		},
	}
	options := ReplicaOneShotOptions{
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      testClaimID,
	}
	record := snapshot.Record{
		FileRevision:     5,
		MutationRevision: 9,
	}
	if err := installReplicaFileHead(
		context.Background(),
		record,
		manifests,
		headPath,
		options,
	); err != nil {
		t.Fatal(err)
	}
	store, err := filehistory.OpenPersistentHeadStore(headPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("[DEBUG-filehead] Close: %v", closeErr)
		}
	}()
	head, found, err := store.Load(context.Background(), testWorkspaceID)
	if err != nil || !found ||
		head.Root != historyID ||
		head.Revision != record.FileRevision ||
		head.MutationRevision != record.MutationRevision {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
}

func TestDiagnosticPlainFileDirectoryCleanup(t *testing.T) {
	var diagnosticRoot string
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		entries, readErr := os.ReadDir(diagnosticRoot)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Logf("[DEBUG-plainfile] after failed cleanup entries=%q readErr=%v", names, readErr)
	})
	diagnosticRoot = t.TempDir()
	if err := os.WriteFile(filepath.Join(diagnosticRoot, "document.txt"), make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestDiagnosticDatabaseNamedFileDirectoryCleanup(t *testing.T) {
	var diagnosticRoot string
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		entries, readErr := os.ReadDir(diagnosticRoot)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Logf("[DEBUG-dbname] after failed cleanup entries=%q readErr=%v", names, readErr)
	})
	diagnosticRoot = t.TempDir()
	if err := os.WriteFile(filepath.Join(diagnosticRoot, "filehistory-head.db"), make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticFileHeadExclusiveClose(t *testing.T) {
	var diagnosticRoot string
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		entries, readErr := os.ReadDir(diagnosticRoot)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Logf("[DEBUG-filehead] after failed cleanup entries=%q readErr=%v", names, readErr)
	})
	diagnosticRoot = t.TempDir()
	headPath := filepath.Join(diagnosticRoot, "filehistory-head.db")
	historyID := objectrepo.ManifestID(
		"manifest_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	fileHeadID := objectrepo.ManifestID(
		"manifest_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
	payload, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"workspaceId":   testWorkspaceID,
		"historyRoot":   historyID,
		"fileRevision":  uint64(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[objectrepo.ManifestID]objectrepo.ManifestRecord{
		fileHeadID: {
			ID:   fileHeadID,
			Name: "file-state-head",
			Labels: map[string]string{
				"type": "file-state-head",
			},
			Payload: payload,
		},
		historyID: {
			ID:   historyID,
			Name: "filehistory-root",
		},
	}
	options := ReplicaOneShotOptions{
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      testClaimID,
	}
	record := snapshot.Record{
		FileRevision:     5,
		MutationRevision: 9,
	}
	if err := installReplicaFileHead(
		context.Background(),
		record,
		manifests,
		headPath,
		options,
	); err != nil {
		t.Fatal(err)
	}
	store, err := filehistory.OpenPersistentHeadStore(headPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("[DEBUG-exclusive] Close: %v", closeErr)
			return
		}
		name, err := syscall.UTF16PtrFromString(headPath)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Errorf("[DEBUG-exclusive] exclusive open after Close: %v", err)
			return
		}
		if err := syscall.CloseHandle(handle); err != nil {
			t.Errorf("[DEBUG-exclusive] CloseHandle: %v", err)
		}
	}()
	head, found, err := store.Load(context.Background(), testWorkspaceID)
	if err != nil || !found ||
		head.Root != historyID ||
		head.Revision != record.FileRevision ||
		head.MutationRevision != record.MutationRevision {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
}
func TestDiagnosticCaptureDatabaseBytes(t *testing.T) {
	var diagnosticRoot string
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		entries, readErr := os.ReadDir(diagnosticRoot)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Logf("[DEBUG-filehead] after failed cleanup entries=%q readErr=%v", names, readErr)
	})
	diagnosticRoot = t.TempDir()
	headPath := filepath.Join(diagnosticRoot, "filehistory-head.db")
	historyID := objectrepo.ManifestID(
		"manifest_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	fileHeadID := objectrepo.ManifestID(
		"manifest_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
	payload, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"workspaceId":   testWorkspaceID,
		"historyRoot":   historyID,
		"fileRevision":  uint64(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[objectrepo.ManifestID]objectrepo.ManifestRecord{
		fileHeadID: {
			ID:   fileHeadID,
			Name: "file-state-head",
			Labels: map[string]string{
				"type": "file-state-head",
			},
			Payload: payload,
		},
		historyID: {
			ID:   historyID,
			Name: "filehistory-root",
		},
	}
	options := ReplicaOneShotOptions{
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      testClaimID,
	}
	record := snapshot.Record{
		FileRevision:     5,
		MutationRevision: 9,
	}
	if err := installReplicaFileHead(
		context.Background(),
		record,
		manifests,
		headPath,
		options,
	); err != nil {
		t.Fatal(err)
	}
	store, err := filehistory.OpenPersistentHeadStore(headPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(headPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("..", "..", "..", "build", "diagnostic-filehead.db"), content, 0600); err != nil {
			t.Fatal(err)
		}
	}()
	head, found, err := store.Load(context.Background(), testWorkspaceID)
	if err != nil || !found ||
		head.Root != historyID ||
		head.Revision != record.FileRevision ||
		head.MutationRevision != record.MutationRevision {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
}

func TestDiagnosticDatabaseBytesDirectoryCleanup(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "build", "diagnostic-filehead.db"))
	if err != nil {
		t.Fatal(err)
	}
	var root string
	t.Cleanup(func() {
		if t.Failed() {
			entries, err := os.ReadDir(root)
			t.Logf("[DEBUG-dbbytes] entries=%v err=%v", entries, err)
		}
	})
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "filehistory-head.db"), content, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticRawSQLiteDirectoryCleanup(t *testing.T) {
	for _, mode := range []string{"DELETE", "WAL"} {
		t.Run(mode, func(t *testing.T) {
			var root string
			t.Cleanup(func() {
				if t.Failed() {
					entries, err := os.ReadDir(root)
					t.Logf("[DEBUG-rawsqlite] entries=%v err=%v", entries, err)
				}
			})
			root = t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(root, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := db.Exec("PRAGMA journal_mode=" + mode + "; CREATE TABLE test(value TEXT); INSERT INTO test VALUES ('test')")
			closeErr := db.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("write=%v close=%v", writeErr, closeErr)
			}
		})
	}
}
