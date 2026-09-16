package workspacev2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/replica"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

// Real normalized schema, PB snapshots, public conflict handlers and the actual
// appender prove that a logical choice retains its private whole-table source.
func TestConflictBusinessAndSharedSettingsPublicApplyPreservesRecovery(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "local-catalog"
		if foreign {
			name = "foreign-catalog"
		}
		t.Run(name, func(t *testing.T) { testConflictBusinessPublicRecovery(t, foreign) })
	}
}

func testConflictBusinessPublicRecovery(t *testing.T, foreign bool) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
	migrations.Register(app)
	requireSnapshotRestore(t, app.Bootstrap())
	t.Cleanup(func() {
		event := &core.TerminateEvent{App: app}
		if err := app.OnTerminate().Trigger(event, func(e *core.TerminateEvent) error { return e.App.ResetBootstrapState() }); err != nil {
			t.Error(err)
		}
	})
	requireSnapshotRestore(t, app.RunAllMigrations())
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	requireSnapshotRestore(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	transport := &foreignRecoveryRemote{verifiedAdvisoryRemote: verifiedAdvisoryRemote{identity: replica.RemoteIdentity{WorkspaceID: testWorkspaceID, ReplicaID: uuid.NewString(), Strength: replica.Advisory}}}
	var replicaRemote replica.VerifiedRemote
	if foreign {
		replicaRemote = transport
	}
	runtime, err := Open(ctx, Options{ReplicaRemote: replicaRemote, App: app, DataDir: dataDir, WorkspaceID: testWorkspaceID, SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger, RequestShutdown: func() {}, DeferBackgroundWorkers: true})
	requireSnapshotRestore(t, err)
	t.Cleanup(func() { _ = runtime.Close(ctx) })
	lifecycle, err := schemacore.NewTableLifecycle(app)
	requireSnapshotRestore(t, err)
	created, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "Forked records", OperationID: "conflict-projection-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	requireSnapshotRestore(t, err)
	definition, err := app.FindFirstRecordByData("vibetable_tables", "table_id", created.TableID)
	requireSnapshotRestore(t, err)
	collection, err := app.FindCollectionByNameOrId(definition.GetString("collection_id"))
	requireSnapshotRestore(t, err)
	collection.Fields.Add(&core.TextField{Name: "title"})
	requireSnapshotRestore(t, app.Save(collection))
	row := core.NewRecord(collection)
	row.Set("title", "base")
	requireSnapshotRestore(t, app.Save(row))
	settingsCollection, err := app.FindCollectionByNameOrId("vibetable_shared_settings")
	requireSnapshotRestore(t, err)
	setting := core.NewRecord(settingsCollection)
	setting.Set("logical_id", "user-preference")
	setting.Set("payload_json", pbtypes.JSONRaw(`{"value":"base"}`))
	requireSnapshotRestore(t, app.Save(setting))
	outbox, err := app.FindCollectionByNameOrId("vibetable_outbox")
	requireSnapshotRestore(t, err)

	capturer := runtime.snapshots
	capture := func(marker string) (snapshot.Record, conflictresolution.Candidate) {
		t.Helper()
		row.Set("title", marker)
		requireSnapshotRestore(t, app.Save(row))
		setting.Set("payload_json", pbtypes.JSONRaw(`{"value":"`+marker+`"}`))
		requireSnapshotRestore(t, app.Save(setting))
		event := core.NewRecord(outbox)
		event.Set("event_id", marker)
		event.Set("topic", "fixture")
		event.Set("payload_json", pbtypes.JSONRaw(`{"marker":"`+marker+`"}`))
		event.Set("status", "pending")
		requireSnapshotRestore(t, app.Save(event))
		token, _ := runtime.coordinator.Current()
		record, captured, err := capturer.Capture(ctx, snapshot.CaptureRequest{WorkspaceID: testWorkspaceID, Authority: token.Authority(), Trigger: snapshot.TriggerManual, Pinned: true})
		requireSnapshotRestore(t, err)
		if !captured {
			t.Fatal("manual snapshot was not captured")
		}
		database, err := readConflictObject(ctx, runtime.repository, record.ObjectMap["database"])
		requireSnapshotRestore(t, err)
		projection, err := conflictresolution.ProjectSQLiteDatabase(ctx, database, string(record.ObjectMap["database"]))
		requireSnapshotRestore(t, err)
		if _, leaked := projection.Candidates[outbox.Id]; leaked {
			t.Fatal("execution outbox exposed as a user choice")
		}
		return record, conflictresolution.Candidate{SnapshotID: record.SnapshotID, Revision: record.MutationRevision, BusinessDatabaseObjectID: string(record.ObjectMap["database"]), Settings: conflictresolution.SettingsState{ObjectID: string(record.ObjectMap["workspace-settings"])}, Files: map[string]conflictresolution.FileState{}, Tables: projection.Candidates}
	}
	baseSnapshot, base := capture("base")
	if foreign {
		foreignCatalog, err := snapshot.OpenDurableCatalog(filepath.Join(t.TempDir(), "foreign-catalog.db"))
		requireSnapshotRestore(t, err)
		t.Cleanup(func() { _ = foreignCatalog.Close() })
		coordinator, err := writecoordinator.New(testWorkspaceID, 3, testClaimID, 7)
		requireSnapshotRestore(t, err)
		token, _ := coordinator.Current()
		barrier, err := snapshot.NewCoordinatedBarrier(coordinator, token, runtime.frozenSource)
		requireSnapshotRestore(t, err)
		capturer = snapshot.NewCoordinator(runtime.repository, barrier, foreignCatalog)
	}
	replicaSnapshot, remote := capture("replica")
	var otherForeignSource snapshot.Record
	if foreign {
		token, _ := runtime.coordinator.Current()
		var created bool
		otherForeignSource, created, err = capturer.Capture(ctx, snapshot.CaptureRequest{WorkspaceID: testWorkspaceID, Authority: token.Authority(), Trigger: snapshot.TriggerManual, Pinned: true})
		requireSnapshotRestore(t, err)
		if !created || otherForeignSource.SnapshotID == replicaSnapshot.SnapshotID {
			t.Fatal("different source identity not created")
		}
	}
	capturer = runtime.snapshots
	if foreign && replicaSnapshot.SnapshotSequence != baseSnapshot.SnapshotSequence {
		t.Fatal("foreign sequence collision not reproduced")
	}
	localSnapshot, local := capture("local")
	if local.Settings.ObjectID == "" {
		t.Fatal("workspace settings source missing")
	}
	graph, err := (productionConflictDependencyScanner{repository: runtime.repository}).ScanConflictDependencies(ctx, base, local, remote)
	requireSnapshotRestore(t, err)
	engine, err := conflictresolution.OpenEngine(filepath.Join(root, ".vibetable", "coordination", "projection-conflicts.db"))
	requireSnapshotRestore(t, err)
	t.Cleanup(func() { _ = engine.Close() })
	set := conflictresolution.Set{ConflictID: uuid.NewString(), WorkspaceID: testWorkspaceID, State: conflictresolution.StatePending, Revision: 1, Base: base, Local: local, Replica: remote, Dependencies: graph, CreatedAt: time.Now().UTC()}
	if !foreign {
		requireSnapshotRestore(t, engine.Add(ctx, set))
	}
	applier, err := filehistory.NewConflictApplier(runtime.history, runtime.headStore)
	requireSnapshotRestore(t, err)
	owner := &productionReplicaConflict{runtime: runtime, conflicts: engine, applier: applier}
	if foreign {
		owner = runtime.replicaConflict
		owner.managerOptions.DependencyScanner = productionConflictDependencyScanner{repository: runtime.repository}
		// The transport supplies an immutable foreign checkpoint already materialized
		// in this repository; its independent catalog has a colliding sequence.
		roots, err := snapshot.ReachabilityObjectIDs(ctx, runtime.repository, replicaSnapshot)
		requireSnapshotRestore(t, err)
		sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })
		digest := sha256.New()
		for _, id := range roots {
			_, _ = digest.Write([]byte(id))
			_, _ = digest.Write([]byte{0})
		}
		transport.incoming = []replica.IncomingConflict{{Set: set, ReplicaSnapshot: replicaSnapshot, Roots: roots, Verification: replica.VerificationReceipt{WorkspaceID: testWorkspaceID, ReplicaID: transport.identity.ReplicaID, SnapshotID: replicaSnapshot.SnapshotID, CatalogRevision: replicaSnapshot.CatalogRevision, Reopened: true, AllRootsReadable: true, RootDigest: "sha256:" + hex.EncodeToString(digest.Sum(nil))}}}
		requireSnapshotRestore(t, owner.manager.Close())
		owner.manager, err = replica.OpenManager(ctx, owner.managerOptions)
		requireSnapshotRestore(t, err)
		requireSnapshotRestore(t, owner.manager.Synchronize(ctx))
		token, _ := runtime.coordinator.Current()
		automatic, created, err := runtime.snapshots.Capture(ctx, snapshot.CaptureRequest{WorkspaceID: testWorkspaceID, Authority: token.Authority(), Trigger: snapshot.TriggerAutomatic})
		requireSnapshotRestore(t, err)
		if created || automatic.SnapshotID != localSnapshot.SnapshotID {
			t.Fatal("automatic capture reused the foreign recovery instead of local head")
		}
		selectedBase, err := owner.latestOrProtectionSnapshot(ctx)
		requireSnapshotRestore(t, err)
		if selectedBase != localSnapshot.SnapshotID {
			t.Fatal("selected files baseline used a recovery source")
		}
	}
	dispatcher := protocolv2.New()
	dispatcher.BindSession(protocolv2.Session{WorkspaceID: testWorkspaceID, Epoch: 7})
	dispatcher.Register("conflict.inspect", protocolv2.WorkspaceScope, owner.inspectConflict)
	dispatcher.Register("conflict.preview", protocolv2.WorkspaceScope, owner.previewConflict)
	dispatcher.Register("conflict.apply", protocolv2.WorkspaceScope, owner.applyConflict)
	dispatcher.Register("snapshot.list", protocolv2.WorkspaceScope, runtime.listSnapshots)
	dispatcher.Register("snapshot.previewRestore", protocolv2.WorkspaceScope, runtime.previewSnapshotRestore)
	sequence := 0
	call := func(method string, params any) map[string]any {
		t.Helper()
		sequence++
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": uuid.NewString(), "method": method, "wire": map[string]any{"scope": "workspace", "workspaceId": testWorkspaceID, "sessionEpoch": 7, "sequence": sequence, "operationId": uuid.NewString()}, "params": params})
		requireSnapshotRestore(t, err)
		response := dispatcher.DispatchEnvelope(ctx, raw)
		if response.Error != nil {
			t.Fatalf("%s failed: %#v", method, response.Error)
		}
		resultRaw, err := json.Marshal(response.Result)
		requireSnapshotRestore(t, err)
		var result map[string]any
		requireSnapshotRestore(t, json.Unmarshal(resultRaw, &result))
		return result
	}
	inspected := call("conflict.inspect", map[string]any{"conflictId": set.ConflictID})
	items := inspected["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("exact business/settings choices: %#v", items)
	}
	byID := map[string]map[string]any{}
	for _, raw := range items {
		item := raw.(map[string]any)
		byID[item["itemId"].(string)] = item
	}
	if item := byID[created.TableID]; item == nil || item["kind"] != "table" || item["path"] != "Forked records" {
		t.Fatalf("business projection = %#v", byID)
	}
	if deps := byID[created.TableID]["dependencies"]; !reflect.DeepEqual(deps, []any{"schema:tables"}) {
		t.Fatalf("public dependency exposes physical metadata: %#v", deps)
	}
	if item := byID["metadata:shared_settings"]; item == nil || item["kind"] != "settings" || item["path"] != "shared_settings" {
		t.Fatalf("settings projection = %#v", byID)
	}
	choices := []conflictresolution.Choice{{ItemID: created.TableID, Kind: conflictresolution.TableItem, Side: conflictresolution.Replica}, {ItemID: "metadata:shared_settings", Kind: conflictresolution.SettingsItem, Side: conflictresolution.Replica}}
	for _, invalid := range []conflictresolution.Choice{
		{ItemID: settingsCollection.Id, Kind: conflictresolution.SettingsItem, Side: conflictresolution.Replica},
		{ItemID: "metadata:shared_settings", Kind: conflictresolution.TableItem, Side: conflictresolution.Replica},
		{ItemID: "metadata:shared_settings", Kind: conflictresolution.SettingsItem, Side: conflictresolution.Both},
	} {
		result := call("conflict.preview", previewConflictParams{ConflictID: set.ConflictID, Choices: []conflictresolution.Choice{choices[0], invalid}})
		if result["valid"] != false {
			t.Fatalf("invalid settings choice accepted: %#v", result)
		}
	}
	preview := call("conflict.preview", previewConflictParams{ConflictID: set.ConflictID, Choices: choices})
	if preview["valid"] != true {
		t.Fatalf("preview = %#v", preview)
	}
	applied := call("conflict.apply", map[string]any{"planId": preview["planId"]})
	if applied["state"] != "applied" {
		t.Fatalf("apply = %#v", applied)
	}
	restoredRow, err := app.FindRecordById(collection.Id, row.Id)
	requireSnapshotRestore(t, err)
	restoredSetting, err := app.FindRecordById(settingsCollection.Id, setting.Id)
	requireSnapshotRestore(t, err)
	if restoredRow.GetString("title") != "replica" || string(restoredSetting.GetRaw("payload_json").(pbtypes.JSONRaw)) != `{"value":"replica"}` {
		t.Fatalf("selected sources not applied: row=%s settings=%s", restoredRow.GetString("title"), restoredSetting.GetRaw("payload_json"))
	}
	currentDatabase, err := runtime.frozenSource.snapshotDatabase(ctx)
	requireSnapshotRestore(t, err)
	currentProjection, err := conflictresolution.ProjectSQLiteDatabase(ctx, currentDatabase, "current")
	requireSnapshotRestore(t, err)
	for id, previous := range local.Tables {
		if id != collection.Id && id != settingsCollection.Id && !conflictresolution.EqualTableContent(previous, currentProjection.Candidates[id]) {
			t.Fatalf("unselected schema/settings changed: %s", id)
		}
	}
	events, err := app.FindAllRecords(outbox.Id)
	requireSnapshotRestore(t, err)
	if len(events) != 3 {
		t.Fatalf("local execution ledger replaced: %d", len(events))
	}
	recoveryIDs := applied["recoverySnapshotIds"].([]any)
	listed := call("snapshot.list", map[string]any{"limit": 200})
	if listed["nextCursor"] != nil {
		t.Fatal("unexpected pagination")
	}
	for _, id := range recoveryIDs {
		found := false
		for _, raw := range listed["snapshots"].([]any) {
			item := raw.(map[string]any)
			if item["snapshotId"] == id && item["integrity"] == "verified" {
				found = true
			}
		}
		if !found {
			t.Fatalf("returned recovery snapshot %s is not publicly listed and verified", id)
		}
		call("snapshot.previewRestore", previewSnapshotRestoreParams{SnapshotID: id.(string), TargetMode: "currentWorkspace"})
	}
	expectedReplicaID := replicaSnapshot.SnapshotID
	if foreign {
		records, err := runtime.catalog.List(ctx, testWorkspaceID)
		requireSnapshotRestore(t, err)
		recovered := 0
		for _, record := range records {
			if record.LocalRecovery && record.SourceSnapshotID == replicaSnapshot.SnapshotID {
				recovered++
				if record.SnapshotID == replicaSnapshot.SnapshotID || !record.Pinned || record.SourceWorkspaceID != testWorkspaceID || record.RecoverySourceManifestID != string(replicaSnapshot.ManifestID) || record.ObjectMap["database"] != replicaSnapshot.ObjectMap["database"] {
					t.Fatalf("foreign source binding changed: %#v", record)
				}
				expectedReplicaID = record.SnapshotID
			}
		}
		if recovered != 1 || len(recoveryIDs) != 2 {
			t.Fatalf("recovery identities: %d, %#v", recovered, recoveryIDs)
		}
		last, found, err := runtime.catalog.Last(ctx, testWorkspaceID)
		requireSnapshotRestore(t, err)
		if !found || last.SnapshotID != localSnapshot.SnapshotID {
			t.Fatal("recovery source replaced the local head")
		}
	}
	expectedRecovery := []string{localSnapshot.SnapshotID, expectedReplicaID}
	sort.Strings(expectedRecovery)
	if !reflect.DeepEqual(recoveryIDs, []any{expectedRecovery[0], expectedRecovery[1]}) {
		t.Fatalf("recovery roots = %#v", recoveryIDs)
	}
	recoveryRaw, _ := json.Marshal(previewSnapshotRestoreParams{SnapshotID: localSnapshot.SnapshotID, TargetMode: "currentWorkspace"})
	recovery, err := runtime.previewSnapshotRestore(ctx, nil, recoveryRaw)
	requireSnapshotRestore(t, err)
	if recovery.(map[string]any)["planId"] == "" {
		t.Fatalf("loser restore preview = %#v", recovery)
	}
	// The preview must refer to the intact losing whole database, including its
	// shared settings, rather than a synthetic current-state recovery marker.
	source, err := (&workspaceConflictAppender{owner: owner}).openConflictCandidateSource(ctx, localSnapshot.ObjectMap["database"])
	requireSnapshotRestore(t, err)
	defer func() {
		if source != nil {
			source.close()
		}
	}()

	losingRow, err := source.app.FindRecordById(collection.Id, row.Id)
	requireSnapshotRestore(t, err)
	losingSetting, err := source.app.FindRecordById(settingsCollection.Id, setting.Id)
	requireSnapshotRestore(t, err)
	if losingRow.GetString("title") != "local" || string(losingSetting.GetRaw("payload_json").(pbtypes.JSONRaw)) != `{"value":"local"}` {
		t.Fatal("losing business/settings snapshot was overwritten")
	}
	source.close()
	source = nil
	if foreign {
		terminal, err := owner.conflicts.Inspect(ctx, set.ConflictID)
		requireSnapshotRestore(t, err)
		if terminal.State != conflictresolution.StateApplied || len(terminal.RootPinIDs) != 0 {
			t.Fatalf("temporary conflict pins not released: %#v", terminal)
		}
		requireSnapshotRestore(t, runtime.Close(ctx))
		runtime, err = Open(ctx, Options{ReplicaRemote: replicaRemote, App: app, DataDir: dataDir, WorkspaceID: testWorkspaceID, SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger, RequestShutdown: func() {}, DeferBackgroundWorkers: true})
		requireSnapshotRestore(t, err)
		dispatcher = protocolv2.New()
		dispatcher.BindSession(protocolv2.Session{WorkspaceID: testWorkspaceID, Epoch: 7})
		dispatcher.Register("snapshot.list", protocolv2.WorkspaceScope, runtime.listSnapshots)
		dispatcher.Register("snapshot.previewRestore", protocolv2.WorkspaceScope, runtime.previewSnapshotRestore)
		reopened, err := runtime.conflictRecoveryIDs(ctx, localSnapshot.SnapshotID, replicaSnapshot.SnapshotID)
		requireSnapshotRestore(t, err)
		if !reflect.DeepEqual(reopened, expectedRecovery) {
			t.Fatalf("recovery mapping did not survive restart: %#v", reopened)
		}
		requireSnapshotRestore(t, runtime.replicaConflict.manager.Synchronize(ctx))
		after, err := runtime.conflictRecoveryIDs(ctx, localSnapshot.SnapshotID, replicaSnapshot.SnapshotID)
		requireSnapshotRestore(t, err)
		if !reflect.DeepEqual(after, expectedRecovery) {
			t.Fatal("repeated discovery replaced the durable recovery mapping")
		}
		relisted := call("snapshot.list", map[string]any{"limit": 200})
		for _, id := range after {
			found := false
			for _, raw := range relisted["snapshots"].([]any) {
				item := raw.(map[string]any)
				if item["snapshotId"] == id && item["integrity"] == "verified" && item["pinned"] == true {
					found = true
				}
			}
			if !found {
				t.Fatalf("public recovery lost after restart: %s", id)
			}
			call("snapshot.previewRestore", previewSnapshotRestoreParams{SnapshotID: id, TargetMode: "currentWorkspace"})
		}
		pinsBefore, err := runtime.repository.ListPins(ctx)
		requireSnapshotRestore(t, err)
		invalid := otherForeignSource
		invalid.ManifestID = localSnapshot.ManifestID
		if err := runtime.PreserveRecoverySnapshot(ctx, invalid); err == nil {
			t.Fatal("mismatched source manifest accepted")
		}
		pinsAfter, err := runtime.repository.ListPins(ctx)
		requireSnapshotRestore(t, err)
		if !reflect.DeepEqual(pinsBefore, pinsAfter) {
			t.Fatal("invalid source leaked a pin")
		}
		requireSnapshotRestore(t, runtime.PreserveRecoverySnapshot(ctx, otherForeignSource))
		distinct, err := runtime.conflictRecoveryIDs(ctx, replicaSnapshot.SnapshotID, otherForeignSource.SnapshotID)
		requireSnapshotRestore(t, err)
		if len(distinct) != 2 {
			t.Fatal("different source snapshots collapsed to one recovery identity")
		}
		last, found, err := runtime.catalog.Last(ctx, testWorkspaceID)
		requireSnapshotRestore(t, err)
		if !found || last.SnapshotID != localSnapshot.SnapshotID {
			t.Fatal("new source recovery replaced the local head")
		}
		for _, publication := range transport.publications {
			if publication.SnapshotID == expectedReplicaID {
				t.Fatal("local recovery was published as a new source branch")
			}
		}
	}
}

// Publication and authority behavior use the existing verified transport double;
// only discovery supplies the independently captured source branch.
type foreignRecoveryRemote struct {
	verifiedAdvisoryRemote
	incoming []replica.IncomingConflict
}

func (remote *foreignRecoveryRemote) DiscoverConflicts(context.Context, replica.ConflictScan) ([]replica.IncomingConflict, error) {
	return remote.incoming, nil
}
