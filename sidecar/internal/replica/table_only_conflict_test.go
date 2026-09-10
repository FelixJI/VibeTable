package replica

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func tableOnlyConflictDatabaseFactory(t *testing.T) func(string) []byte {
	t.Helper()
	closeApp := func(app *pocketbase.PocketBase) {
		event := &core.TerminateEvent{App: app}
		if err := app.OnTerminate().Trigger(event, func(event *core.TerminateEvent) error { return event.App.ResetBootstrapState() }); err != nil {
			t.Fatal(err)
		}
	}
	openApp := func(directory string) *pocketbase.PocketBase {
		app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: directory, HideStartBanner: true})
		migrations.Register(app)
		if err := app.Bootstrap(); err != nil {
			t.Fatal(err)
		}
		return app
	}
	baselineDir := t.TempDir()
	app := openApp(baselineDir)
	if err := app.RunAllMigrations(); err != nil {
		closeApp(app)
		t.Fatal(err)
	}
	if err := writecoordinator.EnsurePocketBaseReceiptTable(context.Background(), app); err != nil {
		closeApp(app)
		t.Fatal(err)
	}
	collection := core.NewBaseCollection("tasks", "tasks")
	collection.Fields.Add(&core.TextField{Name: "title"})
	if err := app.Save(collection); err != nil {
		closeApp(app)
		t.Fatal(err)
	}
	definitionCollection, err := app.FindCollectionByNameOrId("vibetable_tables")
	if err != nil {
		closeApp(app)
		t.Fatal(err)
	}
	definition := core.NewRecord(definitionCollection)
	for key, value := range map[string]any{"table_id": "tbl_seed", "collection_id": collection.Id, "physical_name": "tasks", "display_name": "Seed", "kind": "table", "schema_revision": 1, "data_revision": 1, "archive_policy": "manual"} {
		definition.Set(key, value)
	}
	if err := app.Save(definition); err != nil {
		closeApp(app)
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Id = "seedrow00000001"
	record.Set("title", "seed")
	if err := app.Save(record); err != nil {
		closeApp(app)
		t.Fatal(err)
	}
	settingsCollection, err := app.FindCollectionByNameOrId("vibetable_shared_settings")
	if err != nil {
		closeApp(app)
		t.Fatal(err)
	}
	settings := core.NewRecord(settingsCollection)
	settings.Set("logical_id", "fixture-setting")
	settings.Set("payload_json", pbtypes.JSONRaw(`{"value":"unchanged"}`))
	if err := app.Save(settings); err != nil {
		closeApp(app)
		t.Fatal(err)
	}
	closeApp(app)
	files := map[string][]byte{}
	for _, name := range []string{"data.db", "auxiliary.db"} {
		raw, err := os.ReadFile(filepath.Join(baselineDir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = raw
	}
	return func(marker string) []byte {
		directory := t.TempDir()
		for name, raw := range files {
			if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		branch := openApp(directory)
		record, err := branch.FindRecordById("tasks", "seedrow00000001")
		if err != nil {
			closeApp(branch)
			t.Fatal(err)
		}
		record.Set("title", marker)
		if err := branch.Save(record); err != nil {
			closeApp(branch)
			t.Fatal(err)
		}
		snapshotPath := filepath.Join(directory, "snapshot.db")
		if _, err := branch.DB().NewQuery("VACUUM INTO {:path}").Bind(dbx.Params{"path": snapshotPath}).Execute(); err != nil {
			closeApp(branch)
			t.Fatal(err)
		}
		closeApp(branch)
		raw, err := os.ReadFile(snapshotPath)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
}

func TestTableOnlyFilesystemRemoteAndManagerConflictCandidates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	options, repository, _ := productionManagerFixture(t, &productionRemote{}, now)
	authority := objectrepo.Authority{WorkspaceID: options.WorkspaceID, FenceEpoch: 1, ClaimID: options.Authority.CurrentAuthority().ClaimID}
	databaseFor := tableOnlyConflictDatabaseFactory(t)
	records := make([]snapshot.Record, 3)
	for i, marker := range []string{"seed", "left", "right"} {
		records[i] = strictSnapshotFixture(t, repository, authority, uint64(i+1), uint64(i+1), databaseFor(marker), nil)
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
	var baseHash, localHash string
	publish := func(record snapshot.Record, deviceID, previous string, i int) Publication {
		t.Helper()
		checkpoint, err := makeCheckpoint(ctx, identity, repository, record)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := remote.ReplicateCheckpoint(ctx, checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		publication, err := SealPublication(Publication{
			WorkspaceID:             options.WorkspaceID,
			Claim:                   Claim{WorkspaceID: options.WorkspaceID, DeviceID: deviceID, ClaimID: authority.ClaimID, FenceEpoch: 1, Nonce: receipt.CheckpointID, Strength: Advisory, Mode: Writable, IssuedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)},
			PreviousPublicationHash: previous, SnapshotID: record.SnapshotID, CatalogRevision: record.CatalogRevision, CheckpointID: receipt.CheckpointID, CreatedAt: now.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.AppendPublication(ctx, publication); err != nil {
			t.Fatal(err)
		}
		return publication
	}
	for i, record := range records {
		deviceID := options.DeviceID
		if i == 2 {
			deviceID = "55555555-5555-4555-8555-555555555555"
		}
		publication := publish(record, deviceID, baseHash, i)
		if i == 0 {
			baseHash = publication.CanonicalHash
		}
		if i == 1 {
			localHash = publication.CanonicalHash
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
		if candidate.SnapshotID != record.SnapshotID || candidate.BusinessDatabaseObjectID != string(record.ObjectMap["database"]) || len(candidate.Files) != 0 || len(candidate.AttachmentObjects) != 0 || !exists || table.ItemID != "tbl_seed" || table.DisplayName != "Seed" || table.RecordsObjectID == "" {
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
	t.Run("recovery_publication_failure_never_adds_conflict_or_pin", func(t *testing.T) {
		before, err := repository.ListPins(ctx)
		if err != nil {
			t.Fatal(err)
		}
		publisher := options.RecoveryPublisher.(*productionRecoveryPublisher)
		fault := errors.New("recovery catalog unavailable")
		publisher.err = fault
		if err := manager.discoverConflicts(ctx); !errors.Is(err, fault) {
			t.Fatalf("publication failure: %v", err)
		}
		publisher.err = nil
		after, err := repository.ListPins(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("failed discovery leaked pins: %#v %v", after, err)
		}
		sets, _, err := engine.List(ctx, options.WorkspaceID, nil, 100)
		if err != nil || len(sets) != 0 {
			t.Fatalf("failed recovery published conflict: %#v %v", sets, err)
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
		plan := conflictresolution.BuildPlan(set.Base, set.Local, set.Replica)
		if len(plan.Tables) != 1 || plan.Tables[0].TableID != "tasks" {
			t.Fatalf("expected exactly one business table conflict: %#v", plan.Tables)
		}
		if set.Base.Tables["tasks"].RecordsObjectID == set.Local.Tables["tasks"].RecordsObjectID || set.Local.Tables["tasks"].RecordsObjectID == set.Replica.Tables["tasks"].RecordsObjectID {
			t.Fatal("divergent table rows collapsed")
		}
	})
	t.Run("same_branch_same_content_does_not_duplicate_or_reset_conflict", func(t *testing.T) {
		before, _, err := engine.List(ctx, options.WorkspaceID, nil, 100)
		if err != nil || len(before) != 1 {
			t.Fatalf("before: %#v %v", before, err)
		}
		next := strictSnapshotFixture(t, repository, authority, 4, 2, databaseFor("left"), nil)
		publication := publish(next, options.DeviceID, localHash, 3)
		manager.catalog = productionCatalog{records: []snapshot.Record{records[0], records[1], next}}
		if err := manager.discoverConflicts(ctx); err != nil {
			t.Fatal(err)
		}
		after, _, err := engine.List(ctx, options.WorkspaceID, nil, 100)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("equivalent discovery replaced state/pins or duplicated: before=%#v after=%#v err=%v", before, after, err)
		}
		changed := strictSnapshotFixture(t, repository, authority, 5, 3, databaseFor("different local edit"), nil)
		publish(changed, options.DeviceID, publication.CanonicalHash, 4)
		manager.catalog = productionCatalog{records: []snapshot.Record{records[0], records[1], next, changed}}
		if err := manager.discoverConflicts(ctx); err != nil {
			t.Fatal(err)
		}
		different, _, err := engine.List(ctx, options.WorkspaceID, nil, 100)
		if err != nil || len(different) != 2 {
			t.Fatalf("different edit collapsed: %#v %v", different, err)
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

func TestEquivalentDiscoveryPreservesPreparedPlanAndRootPins(t *testing.T) {
	ctx := context.Background()
	engine, err := conflictresolution.OpenEngine(filepath.Join(t.TempDir(), "conflicts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	table := func(records, db string) conflictresolution.Candidate {
		return conflictresolution.Candidate{SnapshotID: records, Revision: 1, Tables: map[string]conflictresolution.TableState{"physical": {TableID: "physical", ItemID: "tbl_records", Kind: conflictresolution.TableItem, RecordsObjectID: records, DatabaseObjectID: db}}}
	}
	original := conflictresolution.Set{ConflictID: "conflict-prepared", WorkspaceID: "workspace", State: conflictresolution.StatePending, Revision: 1, Base: table("base", "db-base"), Local: table("local", "db-local"), Replica: table("replica", "db-replica"), RootPinIDs: []string{"original-pin"}, Dependencies: conflictresolution.DependencyGraph{Complete: true, Edges: map[string][]string{"physical": {}}}, CreatedAt: time.Now().UTC()}
	if err := engine.Add(ctx, original); err != nil {
		t.Fatal(err)
	}
	preview, err := engine.Preview(ctx, original.ConflictID, []conflictresolution.Choice{{ItemID: "tbl_records", Kind: conflictresolution.TableItem, Side: conflictresolution.Local}})
	if err != nil || !preview.Valid {
		t.Fatalf("preview=%#v %v", preview, err)
	}
	incoming := original
	incoming.ConflictID = "different-discovery-id"
	incoming.Local = table("local", "different-db")
	incoming.Local.SnapshotID = "later-local-snapshot"
	incoming.Local.Revision++
	manager := &Manager{workspace: original.WorkspaceID, conflicts: engine}
	duplicate, err := manager.hasEquivalentConflict(ctx, incoming)
	if err != nil || !duplicate {
		t.Fatalf("duplicate=%v %v", duplicate, err)
	}
	prepared, err := engine.SetForPlan(ctx, preview.PlanID)
	if err != nil || !reflect.DeepEqual(prepared, original) {
		t.Fatalf("prepared plan/set/pins changed: %#v %v", prepared, err)
	}
	sets, cursor, err := engine.List(ctx, original.WorkspaceID, nil, 200)
	if err != nil || cursor != nil || len(sets) != 1 {
		t.Fatalf("sets=%#v cursor=%v err=%v", sets, cursor, err)
	}
}
