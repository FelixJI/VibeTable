package replica

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

func tableOnlyConflictDatabase(t *testing.T, marker string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "table.db")
	if err := os.WriteFile(path, strictSnapshotDatabase(t, marker), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO _collections VALUES ('tasks','0','base','tasks','[{"name":"title","type":"text"}]','[]','{}','',''); CREATE TABLE tasks(id TEXT PRIMARY KEY, title TEXT); INSERT INTO tasks VALUES ('row-1',?);`, marker)
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTableOnlyFilesystemRemoteAndManagerConflictCandidates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	options, repository, _ := productionManagerFixture(t, &productionRemote{}, now)
	authority := objectrepo.Authority{WorkspaceID: options.WorkspaceID, FenceEpoch: 1, ClaimID: options.Authority.CurrentAuthority().ClaimID}
	records := make([]snapshot.Record, 3)
	for i, marker := range []string{"seed", "left", "right"} {
		records[i] = strictSnapshotFixture(t, repository, authority, uint64(i+1), uint64(i+1), tableOnlyConflictDatabase(t, marker), nil)
	}
	selectedRoot := t.TempDir()
	writeMirroredManifest(t, selectedRoot, options.WorkspaceID)
	remote, err := CreateFilesystemRemote(ctx, selectedRoot, options.WorkspaceID, repository)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := remote.VerifyIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var baseHash string
	for i, record := range records {
		checkpoint, err := makeCheckpoint(ctx, identity, repository, record)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := remote.ReplicateCheckpoint(ctx, checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		deviceID := options.DeviceID
		if i == 2 {
			deviceID = "55555555-5555-4555-8555-555555555555"
		}
		publication, err := SealPublication(Publication{
			WorkspaceID:             options.WorkspaceID,
			Claim:                   Claim{WorkspaceID: options.WorkspaceID, DeviceID: deviceID, ClaimID: authority.ClaimID, FenceEpoch: 1, Nonce: receipt.CheckpointID, Strength: Advisory, Mode: Writable, IssuedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)},
			PreviousPublicationHash: baseHash, SnapshotID: record.SnapshotID, CatalogRevision: record.CatalogRevision, CheckpointID: receipt.CheckpointID, CreatedAt: now.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.AppendPublication(ctx, publication); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			baseHash = publication.CanonicalHash
		}
	}
	options.Remote = remote
	options.Catalog = productionCatalog{records: records[:2]}
	engine, err := conflictresolution.OpenEngine(filepath.Join(t.TempDir(), "conflicts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	options.Conflicts = engine
	// No production dependency scanner is installed: discovery must preserve an
	// incomplete graph, never fabricate permission to apply the table conflict.
	manager, err := OpenManager(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	check := func(t *testing.T, candidate conflictresolution.Candidate, record snapshot.Record) {
		t.Helper()
		table, exists := candidate.Tables["tasks"]
		if candidate.SnapshotID != record.SnapshotID || candidate.BusinessDatabaseObjectID != string(record.ObjectMap["database"]) || len(candidate.Files) != 0 || len(candidate.AttachmentObjects) != 0 || len(candidate.Tables) != 1 || !exists || table.RecordsObjectID == "" {
			t.Fatalf("table-only candidate = %#v", candidate)
		}
	}
	t.Run("empty_history_rejects_inconsistent_bundle", func(t *testing.T) {
		bundle, err := remote.RecoverCheckpoint(ctx, records[0].SnapshotID, records[0].CatalogRevision)
		if err != nil {
			t.Fatal(err)
		}
		checkEmptyConflictHistoryBoundaries(t, bundle)
	})
	t.Run("remote_discovery", func(t *testing.T) {
		incoming, err := remote.DiscoverConflicts(ctx, ConflictScan{WorkspaceID: options.WorkspaceID, ReplicaID: identity.ReplicaID, LocalSnapshotID: records[1].SnapshotID, LocalCatalogRevision: records[1].CatalogRevision, NextSnapshotSequence: 3})
		if err != nil {
			t.Fatal(err)
		}
		if len(incoming) != 1 || incoming[0].Set.Base.SnapshotID != records[0].SnapshotID {
			t.Fatalf("incoming = %#v", incoming)
		}
		check(t, incoming[0].Set.Replica, records[2])
	})
	t.Run("manager_local_candidate", func(t *testing.T) {
		for _, record := range records[:2] {
			candidate, err := manager.snapshotConflictCandidate(ctx, record)
			if err != nil {
				t.Fatal(err)
			}
			check(t, candidate, record)
		}
	})
	t.Run("manager_discovery_persists_three_candidates", func(t *testing.T) {
		if err := manager.discoverConflicts(ctx); err != nil {
			t.Fatal(err)
		}
		sets, cursor, err := engine.List(ctx, options.WorkspaceID, nil, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(sets) != 1 || cursor != nil {
			t.Fatalf("sets = %#v", sets)
		}
		set := sets[0]
		check(t, set.Base, records[0])
		check(t, set.Local, records[1])
		check(t, set.Replica, records[2])
		if set.Dependencies.Complete || len(set.RootPinIDs) == 0 || set.State != conflictresolution.StatePending {
			t.Fatalf("unproven conflict = %#v", set)
		}
		if set.Base.Tables["tasks"].RecordsObjectID == set.Local.Tables["tasks"].RecordsObjectID || set.Local.Tables["tasks"].RecordsObjectID == set.Replica.Tables["tasks"].RecordsObjectID {
			t.Fatal("divergent table rows collapsed")
		}
	})
}

func checkEmptyConflictHistoryBoundaries(t *testing.T, original FilesystemRecoveryBundle) {
	t.Helper()
	encode := func(value any) []byte {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, name := range []string{
		"missing_head", "wrong_head_name", "missing_history_root", "null_history_root",
		"missing_revision", "null_revision", "revision_mismatch", "empty_history_with_revision",
		"nonempty_history_with_zero_revision", "missing_nonempty_history", "malformed_history",
		"wrong_history_workspace", "wrong_history_format", "wrong_workspace", "wrong_format",
		"nonempty_files", "nonempty_attachments",
	} {
		t.Run(name, func(t *testing.T) {
			var bundle FilesystemRecoveryBundle
			if err := json.Unmarshal(encode(original), &bundle); err != nil {
				t.Fatal(err)
			}
			rootID := bundle.Snapshot.ObjectMap["file-state-root"]
			var fileState map[string]any
			if err := json.Unmarshal(bundle.Objects[rootID], &fileState); err != nil {
				t.Fatal(err)
			}
			headID := objectrepo.ManifestID(fileState["sourceRoot"].(string))
			head := bundle.Manifests[headID]
			var reference map[string]any
			if err := json.Unmarshal(head.Payload, &reference); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "wrong_head_name":
				head.Name = "filehistory-root"
			case "missing_history_root":
				delete(reference, "historyRoot")
			case "null_history_root":
				reference["historyRoot"] = nil
			case "missing_revision":
				delete(reference, "fileRevision")
			case "null_revision":
				reference["fileRevision"] = nil
			case "revision_mismatch":
				bundle.Snapshot.FileRevision = 1
			case "empty_history_with_revision":
				reference["fileRevision"] = 1
				bundle.Snapshot.FileRevision = 1
			case "nonempty_history_with_zero_revision":
				reference["historyRoot"] = "manifest_missing"
			case "missing_nonempty_history", "malformed_history", "wrong_history_workspace", "wrong_history_format":
				reference["historyRoot"] = "manifest_history"
				reference["fileRevision"] = 1
				bundle.Snapshot.FileRevision = 1
				if name != "missing_nonempty_history" {
					payload := []byte(`{"formatVersion":3,"workspaceId":"` + bundle.Snapshot.WorkspaceID + `","documents":[]}`)
					if name == "malformed_history" {
						payload = []byte(`{`)
					}
					if name == "wrong_history_workspace" {
						payload = []byte(`{"formatVersion":3,"workspaceId":"other","documents":[]}`)
					}
					if name == "wrong_history_format" {
						payload = []byte(`{"formatVersion":1,"workspaceId":"` + bundle.Snapshot.WorkspaceID + `","documents":[]}`)
					}
					bundle.Manifests["manifest_history"] = objectrepo.ManifestRecord{ID: "manifest_history", Name: "filehistory-root", Payload: payload}
				}
			case "wrong_workspace":
				reference["workspaceId"] = "other"
			case "wrong_format":
				reference["formatVersion"] = 2
			case "nonempty_files":
				fileState["files"] = map[string]objectrepo.ObjectID{"document": bundle.Snapshot.ObjectMap["database"]}
			case "nonempty_attachments":
				fileState["attachments"] = map[string]objectrepo.ObjectID{"tasks/row-1/file": bundle.Snapshot.ObjectMap["database"]}
			}
			head.Payload = encode(reference)
			bundle.Manifests[headID] = head
			if name == "missing_head" {
				delete(bundle.Manifests, headID)
			}
			bundle.Objects[rootID] = encode(fileState)
			if _, err := filesystemConflictCandidate(bundle); !errors.Is(err, ErrVerificationInvalid) {
				t.Fatalf("inconsistent bundle accepted: %v", err)
			}
		})
	}
}
