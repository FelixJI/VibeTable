package workspacev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestSnapshotRestoreRestoresManagedAttachmentObjects(t *testing.T) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
	migrations.Register(app)
	requireSnapshotRestore(t, app.Bootstrap())
	requireSnapshotRestore(t, app.RunAllMigrations())
	ledgerPath := filepath.Join(root, ".vibetable", "audit")
	ledger, err := auditledger.Open(ledgerPath)
	requireSnapshotRestore(t, err)
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir, WorkspaceID: testWorkspaceID,
		SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() {}, DeferBackgroundWorkers: true,
	})
	requireSnapshotRestore(t, err)
	definition, fileField, record := createSnapshotAttachmentFixture(t, ctx, app)
	manager, err := attachments.New()
	requireSnapshotRestore(t, err)
	original := []byte("snapshot original cobaltarchive")
	originalRef := setSnapshotAttachment(
		t, ctx, app, manager, definition, record.Id, fileField.Identity.FieldID,
		nil, "original", "notes.txt", original)
	token, _ := runtime.coordinator.Current()
	target, _, err := runtime.snapshots.Capture(ctx, snapshot.CaptureRequest{
		WorkspaceID: testWorkspaceID, Authority: token.Authority(),
		Trigger: snapshot.TriggerManual, Pinned: true,
	})
	requireSnapshotRestore(t, err)
	setSnapshotAttachment(
		t, ctx, app, manager, definition, record.Id, fileField.Identity.FieldID,
		[]string{originalRef.StoredName}, "replacement", "replacement.txt",
		[]byte("replacement magentaafter"))
	if _, err := manager.Open(ctx, app, originalRef.DownloadCapability); err == nil {
		t.Fatal("replaced attachment remained readable through its old authority")
	}
	previewRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID, TargetMode: "currentWorkspace",
	})
	preview, err := runtime.previewSnapshotRestore(ctx, nil, previewRaw)
	requireSnapshotRestore(t, err)
	applyRaw, _ := json.Marshal(applySnapshotRestoreParams{
		PlanID: preview.(map[string]any)["planId"].(string), Confirmed: true,
	})
	wire := json.RawMessage(`{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":7,"sequence":1,"operationId":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"}`)
	_, err = runtime.applySnapshotRestore(ctx, wire, applyRaw)
	requireSnapshotRestore(t, err)
	requireSnapshotRestore(t, runtime.Close(ctx))
	requireSnapshotRestore(t, ledger.Close())
	requireSnapshotRestore(t, app.ResetBootstrapState())
	if installed, err := ApplyPendingSnapshotRestore(
		ctx, dataDir, testWorkspaceID,
	); err != nil || !installed {
		t.Fatalf("offline install = %v, %v", installed, err)
	}
	reopenedApp := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
	requireSnapshotRestore(t, reopenedApp.Bootstrap())
	t.Cleanup(func() { _ = reopenedApp.ResetBootstrapState() })
	reopenedLedger, err := auditledger.Open(ledgerPath)
	requireSnapshotRestore(t, err)
	t.Cleanup(func() { _ = reopenedLedger.Close() })
	reopened, err := Open(ctx, Options{
		App: reopenedApp, DataDir: dataDir, WorkspaceID: testWorkspaceID,
		SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID,
		Ledger: reopenedLedger, DeferBackgroundWorkers: true,
	})
	requireSnapshotRestore(t, err)
	t.Cleanup(func() { _ = reopened.Close(ctx) })
	requireSnapshotRestore(t, reopened.CompletePendingSnapshotRestore(ctx))
	reopened.searchTaskWG.Wait()
	restoredDefinition, err := schemaexecution.Describe(
		ctx, reopenedApp, definition.Snapshot.TableID,
	)
	requireSnapshotRestore(t, err)
	restoredRecord, err := reopenedApp.FindRecordById(
		restoredDefinition.PhysicalName, record.Id,
	)
	requireSnapshotRestore(t, err)
	restoredManager, _ := attachments.New()
	refs, err := restoredManager.Refs(
		ctx, reopenedApp, restoredDefinition, restoredRecord,
		fileField.Identity.FieldID,
	)
	if err != nil || len(refs) != 1 {
		t.Fatalf("restored refs = %#v, %v", refs, err)
	}
	download, err := restoredManager.Open(ctx, reopenedApp, refs[0].DownloadCapability)
	requireSnapshotRestore(t, err)
	restored, readErr := io.ReadAll(download.Reader)
	closeErr := download.Reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(restored, original) {
		t.Fatalf("restored attachment = %q, read=%v close=%v", restored, readErr, closeErr)
	}
	if hits := querySearch(t, reopened.search, "cobaltarchive"); len(hits) != 1 || hits[0].Kind != "attachment" {
		t.Fatalf("restored attachment search hits = %#v", hits)
	}
	if hits := querySearch(t, reopened.search, "magentaafter"); len(hits) != 0 {
		t.Fatalf("replacement attachment remained searchable: %#v", hits)
	}
}

func createSnapshotAttachmentFixture(t *testing.T, ctx context.Context, app core.App) (
	schemaexecution.Table, v2.FieldDefinition, *core.Record,
) {
	t.Helper()
	lifecycle, err := schemacore.NewTableLifecycle(app)
	requireSnapshotRestore(t, err)
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: "Snapshot attachments", OperationID: "snapshot_attachment_table",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	requireSnapshotRestore(t, err)
	recommended, err := v2.RecommendedDefaults(v2.LogicalFile)
	requireSnapshotRestore(t, err)
	draft := v2.FieldDraft{
		DisplayName: "Documents", LogicalType: v2.LogicalFile,
		Value: recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
		File: &v2.FileSpec{
			MaxFiles: 1, MaxBytesPerFile: 4096,
			AllowedMIMETypes: []string{"text/plain"}, Thumbs: []string{}, Protected: true,
		},
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		ExpectedSchemaRev: table.SchemaRevision, Draft: &draft,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil || !plan.CanApply {
		t.Fatalf("attachment field plan = %#v, %v", plan.Errors, err)
	}
	receipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "snapshot_attachment_field",
		Actor:       v2.Actor{ID: "local-user", Kind: "user"},
	})
	requireSnapshotRestore(t, err)
	definition, err := schemaexecution.Describe(ctx, app, table.TableID)
	requireSnapshotRestore(t, err)
	field, found := definition.Field(receipt.FieldID)
	if !found {
		t.Fatal("attachment field was not projected")
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	requireSnapshotRestore(t, err)
	record := core.NewRecord(collection)
	requireSnapshotRestore(t, app.Save(record))
	return definition, field, record
}

func setSnapshotAttachment(
	t *testing.T, ctx context.Context, app core.App, manager *attachments.Manager,
	definition schemaexecution.Table, recordID, fieldID string, removed []string,
	handle, name string, content []byte,
) attachments.Ref {
	t.Helper()
	requireSnapshotRestore(t, manager.Stage(handle, name, content))
	defer manager.Drop(handle)
	err := app.RunInTransaction(func(txApp core.App) error {
		record, err := txApp.FindRecordById(definition.PhysicalName, recordID)
		if err != nil {
			return err
		}
		finalize, err := manager.Prepare(
			ctx, txApp, definition, record, mutation.AttachmentChange{
				FieldID: fieldID, UploadHandles: []string{handle},
				RemoveStoredNames: removed,
			},
		)
		if err != nil {
			return err
		}
		if err := txApp.Save(record); err != nil {
			return err
		}
		return finalize(txApp, record)
	})
	requireSnapshotRestore(t, err)
	record, err := app.FindRecordById(definition.PhysicalName, recordID)
	requireSnapshotRestore(t, err)
	refs, err := manager.Refs(ctx, app, definition, record, fieldID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("attachment refs = %#v, %v", refs, err)
	}
	return refs[0]
}

func requireSnapshotRestore(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func updateSnapshotStorage(t *testing.T, app core.App, remove, name, content string) {
	t.Helper()
	storage, err := app.NewFilesystem()
	requireSnapshotRestore(t, err)
	if remove != "" {
		err = storage.Delete(remove)
	}
	requireSnapshotRestore(t, errors.Join(err, storage.Upload([]byte(content), name), storage.Close()))
}

func requireRestoreFile(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("restore file %s = %q, %v", path, raw, err)
	}
}

func TestSnapshotRestoreValidatesWindowsStorageKeysBeforeAttachmentStaging(t *testing.T) {
	for _, test := range []struct {
		name string
		keys []string
	}{
		{name: "alternate data stream", keys: []string{"pb_data/record/receipt.pdf:stream"}},
		{name: "trailing dot", keys: []string{"pb_data/record/receipt.pdf."}},
		{name: "trailing space", keys: []string{"pb_data/record /receipt.pdf"}},
		{name: "control character", keys: []string{"pb_data/record/receipt\x1f.pdf"}},
		{name: "reserved name with extension", keys: []string{"pb_data/record/cOn.txt"}},
		{name: "reserved com port", keys: []string{"pb_data/record/COM9"}},
		{name: "reserved printer port", keys: []string{"pb_data/record/lPt1.pdf"}},
		{name: "reserved superscript com port", keys: []string{"pb_data/record/COM¹.txt"}},
		{name: "reserved superscript printer port", keys: []string{"pb_data/record/lPt².PDF"}},
		{
			name: "case insensitive collision",
			keys: []string{
				"pb_data/record/Receipt.PDF",
				"PB_DATA/RECORD/receipt.pdf",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths, _, err := stageSnapshotAttachmentKeys(t, test.keys, false)
			if err == nil || err.Error() != "restore.path_invalid" {
				t.Fatalf("snapshot with Windows-invalid storage keys = %v", err)
			}
			operationID := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
			if _, statErr := os.Lstat(restoreStagingRoot(paths, operationID)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected snapshot left partial storage staging: %v", statErr)
			}
			if _, statErr := os.Lstat(restoreJournalPath(paths)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected snapshot wrote restore journal: %v", statErr)
			}
		})
	}

	t.Run("valid PocketBase key", func(t *testing.T) {
		const key = "pb_data/records/receipt_2026-final.PDF"
		_, journal, err := stageSnapshotAttachmentKeys(t, []string{key}, true)
		if err != nil {
			t.Fatalf("valid PocketBase storage key = %v", err)
		}
		if _, found := journal.Attachments[key]; !found {
			t.Fatalf("valid PocketBase storage key missing from journal: %#v", journal.Attachments)
		}
	})
}

func TestRestoreJournalRejectsWindowsInvalidAttachmentKeys(t *testing.T) {
	paths, journal, err := stageSnapshotAttachmentKeys(
		t,
		[]string{"pb_data/records/receipt.pdf"},
		true,
	)
	requireSnapshotRestore(t, err)
	requireSnapshotRestore(t, os.Remove(restoreJournalPath(paths)))
	for _, test := range []struct {
		name string
		keys []string
	}{
		{name: "alternate data stream", keys: []string{"pb_data/record/receipt.pdf:stream"}},
		{name: "trailing dot", keys: []string{"pb_data/record/receipt.pdf."}},
		{name: "reserved device", keys: []string{"pb_data/record/nUl.bin"}},
		{name: "reserved superscript com port", keys: []string{"pb_data/record/COM¹.txt"}},
		{name: "reserved superscript printer port", keys: []string{"pb_data/record/lPt².PDF"}},
		{
			name: "case insensitive collision",
			keys: []string{
				"pb_data/record/Receipt.PDF",
				"PB_DATA/RECORD/receipt.pdf",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := journal
			candidate.Attachments = make(map[string]restoreStagedFile, len(test.keys))
			for _, key := range test.keys {
				candidate.Attachments[key] = restoreStagedFile{Hash: "sha256:test", Size: 1}
			}
			err := writeRestoreJournal(paths, candidate)
			if err == nil || err.Error() != "restore.journal_corrupt" {
				t.Fatalf("journal with Windows-invalid storage keys = %v", err)
			}
			if _, statErr := os.Lstat(restoreJournalPath(paths)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected journal was persisted: %v", statErr)
			}
			raw, marshalErr := json.Marshal(candidate)
			requireSnapshotRestore(t, marshalErr)
			requireSnapshotRestore(t, os.WriteFile(restoreJournalPath(paths), raw, 0o600))
			if _, found, readErr := readRestoreJournal(paths); readErr == nil ||
				readErr.Error() != "restore.journal_corrupt" || found {
				t.Fatalf("read Windows-invalid restore journal = found %v, %v", found, readErr)
			}
			requireSnapshotRestore(t, os.Remove(restoreJournalPath(paths)))
		})
	}
}

func stageSnapshotAttachmentKeys(
	t *testing.T,
	keys []string,
	useExistingObject bool,
) (workspacePaths, pendingSnapshotRestore, error) {
	t.Helper()
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	requireSnapshotRestore(t, app.Bootstrap())
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	createAuditOutbox(t, app)
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	requireSnapshotRestore(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir, WorkspaceID: testWorkspaceID,
		SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() {}, DeferBackgroundWorkers: true,
	})
	requireSnapshotRestore(t, err)
	t.Cleanup(func() { _ = runtime.Close(ctx) })
	token, _ := runtime.coordinator.Current()
	record, _, err := runtime.snapshots.Capture(ctx, snapshot.CaptureRequest{
		WorkspaceID: testWorkspaceID, Authority: token.Authority(),
		Trigger: snapshot.TriggerManual, Pinned: true,
	})
	requireSnapshotRestore(t, err)
	bundle, err := snapshot.LoadSnapshotBundle(ctx, runtime.repository, record)
	requireSnapshotRestore(t, err)
	var historyRoot objectrepo.ManifestID
	if bundle.HistoryRoot != nil {
		historyRoot = bundle.HistoryRoot.ID
	}
	objectID := objectrepo.ObjectID("obj_missing")
	if useExistingObject {
		objectID = record.ObjectMap["database"]
	}
	for _, key := range keys {
		record.ObjectMap["attachment:"+key] = objectID
	}
	receipt := protocolv2.OperationReceipt{
		OperationID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		WorkspaceID: testWorkspaceID,
		Method:      "snapshot.applyRestore",
		Scope:       protocolv2.WorkspaceScope,
		RequestHash: "sha256:test",
		Result:      json.RawMessage(`{"state":"prepared"}`),
	}
	_, stageErr := runtime.coordinator.Write(
		ctx,
		token,
		func(ctx context.Context, intent writecoordinator.WriteIntent) error {
			staged, stageHistoryErr := runtime.history.StageSnapshotRestore(
				ctx,
				intent,
				historyRoot,
			)
			if stageHistoryErr != nil {
				return stageHistoryErr
			}
			return runtime.stagePendingSnapshotRestore(
				ctx, 1, receipt, record, record, staged,
			)
		},
	)
	paths := mustResolvePaths(t, dataDir)
	journal, _, readErr := readRestoreJournal(paths)
	if stageErr == nil && readErr != nil {
		t.Fatalf("read staged restore journal: %v", readErr)
	}
	return paths, journal, stageErr
}

func TestRestoreReadsAttachmentBackendFromTargetDatabase(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
	requireSnapshotRestore(t, app.Bootstrap())
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	target := filepath.Join(dataDir, "data.db")
	requireSnapshotRestore(t, verifyLocalRestoreStorage(target))
	app.Settings().S3 = core.S3Config{
		Enabled: true, Bucket: "target", Region: "local", Endpoint: "https://s3.invalid",
		AccessKey: "access", Secret: "secret",
	}
	requireSnapshotRestore(t, app.Save(app.Settings()))
	if err := verifyLocalRestoreStorage(target); err == nil || err.Error() != "restore.attachment_storage_unsupported" {
		t.Fatalf("target S3 storage backend = %v", err)
	}
	requireSnapshotRestore(t, app.ResetBootstrapState())
}

func TestSnapshotRestoreStorageMatcherRejectsInvalidRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage")
	requireSnapshotRestore(t, os.WriteFile(root, []byte("not a directory"), 0o600))
	if matched, err := snapshotRestoreStorageMatches(root, nil); err == nil || matched {
		t.Fatalf("invalid storage root match = %v, %v", matched, err)
	}
	root = filepath.Join(t.TempDir(), "storage")
	requireSnapshotRestore(t, os.MkdirAll(filepath.Join(root, "expected.txt"), 0o700))
	if matched, err := snapshotRestoreStorageMatches(root, map[string]restoreStagedFile{"expected.txt": {Hash: "x"}}); err == nil || matched {
		t.Fatalf("invalid inner node match = %v, %v", matched, err)
	}
}

func TestSnapshotRestoreStorageMatcherChecksSizeBeforeReading(t *testing.T) {
	root := t.TempDir()
	requireSnapshotRestore(t, os.WriteFile(filepath.Join(root, "object"), []byte("too large"), 0o600))
	matched, err := snapshotRestoreStorageMatchesWithHasher(
		root, map[string]restoreStagedFile{"object": {Hash: "x", Size: 1}},
		func(string) (string, int64, error) {
			t.Fatal("size-mismatched attachment was read")
			return "", 0, nil
		},
	)
	if err != nil || matched {
		t.Fatalf("size-mismatched storage match = %v, %v", matched, err)
	}
}

func TestSnapshotRestoreRollbackPreservesLiveCreatedAfterQuarantine(t *testing.T) {
	paths := mustResolvePaths(t, filepath.Join(createWorkspace(t, testWorkspaceID), ".vibetable", "data"))
	journal := pendingSnapshotRestore{
		OperationID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		Phase:       restorePhaseInstalling, PreviousStorage: true,
	}
	rollback := restoreRollbackRoot(paths, journal.OperationID)
	live := filepath.Join(paths.data, "storage")
	requireSnapshotRestore(t, os.MkdirAll(rollback, 0o700))
	requireSnapshotRestore(t, os.MkdirAll(live, 0o700))
	requireSnapshotRestore(t, os.WriteFile(filepath.Join(live, "old.txt"), []byte("old"), 0o600))
	err := rollbackSnapshotRestoreStorageWithMatcher(paths, journal, func(quarantine string, _ map[string]restoreStagedFile) (bool, error) {
		requireRestoreFile(t, filepath.Join(quarantine, "old.txt"), "old")
		requireSnapshotRestore(t, os.MkdirAll(live, 0o700))
		requireSnapshotRestore(t, os.WriteFile(filepath.Join(live, "foreign.txt"), []byte("foreign"), 0o600))
		return false, nil
	})
	if err == nil {
		t.Fatal("foreign live race was accepted")
	}
	requireRestoreFile(t, filepath.Join(live, "foreign.txt"), "foreign")
	requireRestoreFile(t, filepath.Join(rollback, "storage-quarantine", "old.txt"), "old")
}

func TestSnapshotRestoreAcceptsSnapshotWithoutFileHistory(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	shutdownRequested := false
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() { shutdownRequested = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runtime.Close(ctx)
		_ = ledger.Close()
		_ = app.ResetBootstrapState()
	})
	token, _ := runtime.coordinator.Current()
	target, created, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil || !created {
		t.Fatalf("capture target = %#v, %v, %v", target, created, err)
	}
	added, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "22222222-2222-4222-8222-222222222222",
			Path:       "plans/added-after-empty-snapshot.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("preserve this revision"),
			MimeType:   "text/plain",
			CreatedBy:  "test",
			DeviceID:   testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := runtime.history.PreviewSnapshotRestore(ctx, "")
	if err != nil ||
		len(diff.AddedAfterSnapshot) != 1 ||
		diff.AddedAfterSnapshot[0] != "plans/added-after-empty-snapshot.txt" {
		t.Fatalf("empty-root restore diff = %#v, %v", diff, err)
	}
	staged, err := runtime.history.StageSnapshotRestore(
		ctx,
		writecoordinator.WriteIntent{
			Token:            token,
			MutationRevision: added.MutationRevision + 1,
		},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Documents) != 1 ||
		staged.Documents[0].Status != filehistory.DocumentDeleted ||
		staged.Documents[0].EffectiveRevisionID !=
			added.Document.EffectiveRevisionID ||
		len(staged.Documents[0].Revisions) !=
			len(added.Document.Revisions) {
		t.Fatalf(
			"empty-root staged restore did not soft-delete with revisions intact: %#v",
			staged.Documents,
		)
	}
	previewRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "currentWorkspace",
	})
	preview, err := runtime.previewSnapshotRestore(ctx, nil, previewRaw)
	if err != nil {
		t.Fatal(err)
	}
	previewMap := preview.(map[string]any)
	changes := previewMap["changes"].([]string)
	if !containsRestorePreviewPrefix(
		changes,
		"files:added-after-snapshot:",
	) {
		t.Fatalf("empty-root restore preview omitted added file: %#v", changes)
	}
	planID := previewMap["planId"].(string)
	wire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	}`)
	applyRaw, _ := json.Marshal(applySnapshotRestoreParams{
		PlanID: planID, Confirmed: true,
	})
	if _, err := runtime.applySnapshotRestore(ctx, wire, applyRaw); err != nil {
		t.Fatal(err)
	}
	if !shutdownRequested {
		t.Fatal("restore did not request process shutdown")
	}
}

func TestSnapshotRestoreCommitsAuthorityAndRecoversFailedSearchRebuildAfterRestart(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	if _, err := app.DB().NewQuery(`
		CREATE TABLE restore_probe (value TEXT NOT NULL);
		INSERT INTO restore_probe(value) VALUES ('snapshot');
	`).Execute(); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, ".vibetable", "audit")
	ledger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	shutdownRequested := false
	options := Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() { shutdownRequested = true },
	}
	runtime, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := runtime.coordinator.Current()
	documentID := "22222222-2222-4222-8222-222222222222"
	first, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token: token, DocumentID: documentID,
			Path: "plans/q3.txt", Kind: filehistory.RevisionFormal,
			Content: []byte("snapshot"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	target, created, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil || !created {
		t.Fatalf("capture target = %#v, %v, %v", target, created, err)
	}
	bundlePreview, err := runtime.buildSnapshotRestorePreview(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if bundlePreview.HistoryRoot == "" ||
		bundlePreview.HistoryRoot != runtime.history.Root() {
		t.Fatalf(
			"restore preview history root = %q, current = %q",
			bundlePreview.HistoryRoot,
			runtime.history.Root(),
		)
	}
	invalid := target
	invalid.ObjectMap = make(
		map[string]objectrepo.ObjectID,
		len(target.ObjectMap),
	)
	for name, id := range target.ObjectMap {
		invalid.ObjectMap[name] = id
	}
	invalid.ObjectMap["file-state-root"] = invalid.ObjectMap["database"]
	if _, err := runtime.buildSnapshotRestorePreview(ctx, invalid); err == nil {
		t.Fatal("restore preview accepted an invalid snapshot bundle")
	}
	if _, err := app.DB().NewQuery(
		`UPDATE restore_probe SET value = 'later'`,
	).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token: token, DocumentID: documentID,
			ExpectedEffectiveRevision: &first.Revision.RevisionID,
			Kind:                      filehistory.RevisionFormal,
			Content:                   []byte("later"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "33333333-3333-4333-8333-333333333333",
			Path:       "plans/added-after-snapshot.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("newer"),
			MimeType:   "text/plain",
			CreatedBy:  "test",
			DeviceID:   testClaimID,
		},
	); err != nil {
		t.Fatal(err)
	}
	updateSettingsRaw, _ := json.Marshal(updateRetentionParams{
		ExpectedRevision:    1,
		SnapshotDays:        45,
		SnapshotCount:       60,
		SnapshotBuckets:     []string{"daily", "weekly"},
		FileRevisionDays:    40,
		FileRevisionCount:   120,
		FileRevisionBuckets: []string{"daily", "monthly"},
	})
	if _, err := runtime.updateRetention(
		ctx,
		nil,
		updateSettingsRaw,
	); err != nil {
		t.Fatal(err)
	}
	previewRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "currentWorkspace",
	})
	preview, err := runtime.previewSnapshotRestore(ctx, nil, previewRaw)
	if err != nil {
		t.Fatal(err)
	}
	previewMap := preview.(map[string]any)
	changes, ok := previewMap["changes"].([]string)
	if !ok || len(changes) < 2 ||
		!containsRestorePreviewChange(changes, "database:replace") ||
		!containsRestorePreviewPrefix(
			changes,
			"files:effective-pointers:",
		) ||
		!containsRestorePreviewPrefix(
			changes,
			"files:added-after-snapshot:",
		) ||
		!containsRestorePreviewChange(
			changes,
			"workspace-settings:retention",
		) {
		t.Fatalf("restore preview changes is not string[]: %#v", previewMap)
	}
	planID := previewMap["planId"].(string)
	newWorkspaceRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "newWorkspace",
	})
	if _, err := runtime.previewSnapshotRestore(
		ctx,
		nil,
		newWorkspaceRaw,
	); err == nil || err.Error() != "restore.new_workspace_broker_required" {
		t.Fatalf("new workspace restore boundary = %v", err)
	}
	wire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`)
	applyRaw, _ := json.Marshal(applySnapshotRestoreParams{
		PlanID: planID, Confirmed: true,
	})
	currentSettings, mutationRevision, err := runtime.state.retention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changedAfterPreview := currentSettings
	changedAfterPreview.PolicyRevision++
	changedAfterPreview.SnapshotCount++
	if err := runtime.state.updateRetention(
		ctx,
		currentSettings.PolicyRevision,
		changedAfterPreview,
		mutationRevision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.applySnapshotRestore(
		ctx,
		wire,
		applyRaw,
	); err == nil || err.Error() != "restore.plan_stale" {
		t.Fatalf("unbound settings change was accepted: %v", err)
	}
	currentSettings.PolicyRevision = changedAfterPreview.PolicyRevision + 1
	if err := runtime.state.updateRetention(
		ctx,
		changedAfterPreview.PolicyRevision,
		currentSettings,
		mutationRevision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.applySnapshotRestore(ctx, wire, applyRaw); err != nil {
		t.Fatal(err)
	}
	if !shutdownRequested {
		t.Fatal("restore did not request process shutdown")
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}

	installed, err := ApplyPendingSnapshotRestore(
		ctx,
		dataDir,
		testWorkspaceID,
	)
	if err != nil || !installed {
		t.Fatalf("offline install = %v, %v", installed, err)
	}
	reopenedApp := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := reopenedApp.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer reopenedApp.ResetBootstrapState()
	reopenedLedger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedLedger.Close()
	reopened, err := Open(ctx, Options{
		App: reopenedApp, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: reopenedLedger,
		DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	reopened.restoreSearchRebuild = func(context.Context) error {
		return errors.New("private search storage detail")
	}
	if err := reopened.CompletePendingSnapshotRestore(ctx); err == nil ||
		err.Error() != "workspace_search.restore_rebuild_required" {
		t.Fatalf("restore rebuild startup failure = %v", err)
	}
	reopened.restoreSearchRebuild = nil
	restoredSettings, _, err := reopened.state.retention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if restoredSettings.SnapshotDays != 30 ||
		restoredSettings.SnapshotCount != 50 ||
		restoredSettings.FileRevisionDays != 30 ||
		restoredSettings.FileRevisionCount != 100 {
		t.Fatalf("restored retention settings = %#v", restoredSettings)
	}
	replayRequest, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "restore-replay",
		"method":  "snapshot.applyRestore",
		"wire":    json.RawMessage(wire),
		"params":  json.RawMessage(applyRaw),
	})
	if err != nil {
		t.Fatal(err)
	}
	replay := reopened.Dispatcher().DispatchEnvelope(ctx, replayRequest)
	if replay.Error != nil ||
		replay.Result.(map[string]any)["state"] != "prepared" {
		t.Fatalf("restore journal receipt replay = %#v", replay)
	}
	var databaseValue string
	if err := reopenedApp.DB().NewQuery(
		`SELECT value FROM restore_probe`,
	).Row(&databaseValue); err != nil || databaseValue != "snapshot" {
		t.Fatalf("restored database value = %q, %v", databaseValue, err)
	}
	document, err := reopened.history.Inspect(documentID)
	if err != nil {
		t.Fatal(err)
	}
	effective := document.Revisions[len(document.Revisions)-1]
	if effective.Kind != filehistory.RevisionRestore ||
		effective.RestoredFromRevisionID == nil ||
		*effective.RestoredFromRevisionID != first.Revision.RevisionID {
		t.Fatalf("restored file history = %#v", document)
	}
	raw, err := os.ReadFile(filepath.Join(root, "files", "plans", "q3.txt"))
	if err != nil || string(raw) != "snapshot" {
		t.Fatalf("materialized restored file = %q, %v", raw, err)
	}
	records, err := reopened.catalog.List(ctx, testWorkspaceID)
	if err != nil || len(records) < 3 ||
		records[len(records)-1].Trigger != snapshot.TriggerRestore {
		t.Fatalf("recovery snapshot = %#v, %v", records, err)
	}
	if _, err := os.Lstat(filepath.Join(
		root,
		".vibetable",
		"coordination",
		restoreJournalName,
	)); !os.IsNotExist(err) {
		t.Fatalf("committed restore left journal: %v", err)
	}
	status, err := reopened.search.Status(ctx)
	if err != nil || status.State != "degraded" || status.ErrorCode == nil ||
		*status.ErrorCode != "workspace_search.restore_rebuild_required" {
		t.Fatalf("restore rebuild marker = %#v, %v", status, err)
	}
	searchPath := filepath.Join(
		root, ".vibetable", "coordination", "workspace-search.db",
	)
	if err := reopened.search.Close(); err != nil {
		t.Fatal(err)
	}
	restartedSearch, err := workspacesearch.Open(searchPath)
	if err != nil {
		t.Fatal(err)
	}
	reopened.search = restartedSearch
	status, err = restartedSearch.Status(ctx)
	if err != nil || status.State != "degraded" {
		t.Fatalf("restart lost rebuild marker = %#v, %v", status, err)
	}
	if err := restartedSearch.RebuildProjection(
		ctx, nil, workspacesearch.ProjectionCheckpoint{}, nil,
	); err != nil {
		t.Fatal(err)
	}
	status, err = restartedSearch.Status(ctx)
	if err != nil || status.State != "ready" || status.ErrorCode != nil {
		t.Fatalf("restart rebuild recovery = %#v, %v", status, err)
	}
}

func containsRestorePreviewChange(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsRestorePreviewPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func TestInterruptedInstalledSnapshotRestoreRollsBackBeforeReadiness(
	t *testing.T,
) {
	for _, crash := range []string{
		"before-old-move", "old-moved", "new-installed", "foreign-live-conflict",
		"invalid-quarantine", "quarantine-with-foreign-live",
		"missing-previous", "installing-missing-previous", "quarantine-missing-previous",
	} {
		t.Run(crash, func(t *testing.T) {
			testInterruptedSnapshotRestoreStorageRollback(t, crash)
		})
	}
}

func testInterruptedSnapshotRestoreStorageRollback(t *testing.T, crash string) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	if _, err := app.DB().NewQuery(`
		CREATE TABLE restore_probe (value TEXT NOT NULL);
		INSERT INTO restore_probe(value) VALUES ('snapshot');
	`).Execute(); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, ".vibetable", "audit")
	ledger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() {}, DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := runtime.coordinator.Current()
	if _, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "22222222-2222-4222-8222-222222222222",
			Path:       "probe.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("snapshot"),
			MimeType:   "text/plain",
			CreatedBy:  "test",
			DeviceID:   testClaimID,
		},
	); err != nil {
		t.Fatal(err)
	}
	updateSnapshotStorage(t, app, "", "restore-state/snapshot.txt", "snapshot storage")
	target, _, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	updateSnapshotStorage(
		t, app, "restore-state/snapshot.txt", "restore-state/live.txt", "old live storage",
	)
	if _, err := app.DB().NewQuery(
		`UPDATE restore_probe SET value = 'old-live'`,
	).Execute(); err != nil {
		t.Fatal(err)
	}
	updateSettingsRaw, _ := json.Marshal(updateRetentionParams{
		ExpectedRevision:    1,
		SnapshotDays:        45,
		SnapshotCount:       60,
		SnapshotBuckets:     []string{"daily", "weekly"},
		FileRevisionDays:    40,
		FileRevisionCount:   120,
		FileRevisionBuckets: []string{"daily", "monthly"},
	})
	if _, err := runtime.updateRetention(
		ctx,
		nil,
		updateSettingsRaw,
	); err != nil {
		t.Fatal(err)
	}
	previewRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "currentWorkspace",
	})
	preview, err := runtime.previewSnapshotRestore(ctx, nil, previewRaw)
	if err != nil {
		t.Fatal(err)
	}
	planID := preview.(map[string]any)["planId"].(string)
	wire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`)
	applyRaw, _ := json.Marshal(applySnapshotRestoreParams{
		PlanID: planID, Confirmed: true,
	})
	if _, err := runtime.applySnapshotRestore(
		ctx,
		wire,
		applyRaw,
	); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	paths := mustResolvePaths(t, dataDir)
	if crash == "new-installed" {
		journal, found, err := readRestoreJournal(paths)
		if err != nil || !found {
			t.Fatalf("requested journal = %#v, %v", journal, err)
		}
		overLimit := journal
		overLimit.Attachments = map[string]restoreStagedFile{
			"first":  {Hash: "x", Size: maxSnapshotWorkingSet},
			"second": {Hash: "x", Size: 1},
		}
		if err := validateRestoreJournal(paths, overLimit); err == nil || err.Error() != "restore.journal_corrupt" {
			t.Fatalf("oversized attachment journal validation = %v", err)
		}
		legacy := journal
		legacy.FormatVersion, legacy.Attachments = 2, nil
		legacy.StorageCaptured, legacy.PreviousStorage = false, false
		requireSnapshotRestore(t, writeRestoreJournal(paths, legacy))
		raw, err := os.ReadFile(restoreJournalPath(paths))
		if err != nil || bytes.Contains(raw, []byte(`"attachments"`)) ||
			bytes.Contains(raw, []byte(`"previousStorage"`)) {
			t.Fatalf("legacy restore journal gained v3 fields: %s, %v", raw, err)
		}
		decoded, found, err := readRestoreJournal(paths)
		if err != nil || !found || decoded.FormatVersion != 2 {
			t.Fatalf("rewritten legacy restore journal = %#v, %v", decoded, err)
		}
		requireSnapshotRestore(t, writeRestoreJournal(paths, journal))
		for _, targetSize := range []int{restoreJournalMaxBytes - 1, restoreJournalMaxBytes + 1} {
			boundary := journal
			boundary.Attachments = map[string]restoreStagedFile{"x": {Hash: "sha256:x"}}
			raw, _ := json.Marshal(boundary)
			key := string(bytes.Repeat([]byte("x"), targetSize-len(raw)+1))
			boundary.Attachments = map[string]restoreStagedFile{key: {Hash: "sha256:x"}}
			if err := writeRestoreJournal(paths, boundary); err == nil || err.Error() != "restore.journal_resource_limit" {
				t.Fatalf("restore journal size %d write = %v", targetSize, err)
			}
		}
		persisted, found, err := readRestoreJournal(paths)
		if err != nil || !found || persisted.OperationID != journal.OperationID {
			t.Fatalf("journal after rejected oversized write = %#v, %v", persisted, err)
		}
	}
	statePath := filepath.Join(paths.coordination, "workspace-v2.db")
	stateBackup := statePath + ".restore-test-backup"
	if err := os.Rename(statePath, stateBackup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, err := ApplyPendingSnapshotRestore(
		ctx,
		dataDir,
		testWorkspaceID,
	); err == nil || installed {
		t.Fatalf("settings staging fault = %v, %v", installed, err)
	}
	rollback := restoreRollbackRoot(
		paths,
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	)
	if _, err := os.Lstat(rollback); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed requested attempt left rollback directory: %v", err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stateBackup, statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rollback, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(rollback, "stale"),
		[]byte("stale requested attempt"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if installed, err := ApplyPendingSnapshotRestore(
		ctx,
		dataDir,
		testWorkspaceID,
	); err != nil || !installed {
		t.Fatalf("initial offline install = %v, %v", installed, err)
	}
	storageRoot := filepath.Join(dataDir, "storage", "restore-state")
	requireRestoreFile(t, filepath.Join(storageRoot, "snapshot.txt"), "snapshot storage")
	if _, err := os.Lstat(filepath.Join(storageRoot, "live.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install retained old live storage: %v", err)
	}
	journal, found, err := readRestoreJournal(paths)
	if err != nil || !found {
		t.Fatalf("installed journal = %#v, %v", journal, err)
	}
	journal.Phase = restorePhaseInstalling
	if crash == "missing-previous" || crash == "quarantine-missing-previous" {
		journal.Phase = restorePhaseInstalled
	}
	if crash == "foreign-live-conflict" {
		journal.PreviousStorage = false
	}
	requireSnapshotRestore(t, writeRestoreJournal(paths, journal))
	if crash == "missing-previous" || crash == "installing-missing-previous" {
		requireSnapshotRestore(t, os.RemoveAll(filepath.Join(restoreRollbackRoot(paths, journal.OperationID), "storage")))
		if installed, err := ApplyPendingSnapshotRestore(ctx, dataDir, testWorkspaceID); err == nil || installed {
			t.Fatalf("missing previous storage recovery = %v, %v", installed, err)
		}
		requireRestoreFile(t, filepath.Join(storageRoot, "snapshot.txt"), "snapshot storage")
		if _, found, err := readRestoreJournal(paths); err != nil || !found {
			t.Fatalf("ambiguous recovery lost journal: found=%v err=%v", found, err)
		}
		return
	}
	if crash == "invalid-quarantine" || crash == "quarantine-with-foreign-live" ||
		crash == "quarantine-missing-previous" {
		quarantine := filepath.Join(restoreRollbackRoot(paths, journal.OperationID), "storage-quarantine")
		requireSnapshotRestore(t, os.Rename(filepath.Dir(storageRoot), quarantine))
		if crash == "quarantine-missing-previous" {
			requireSnapshotRestore(t, os.RemoveAll(filepath.Join(restoreRollbackRoot(paths, journal.OperationID), "storage")))
		} else if crash == "invalid-quarantine" {
			requireSnapshotRestore(t, os.WriteFile(filepath.Join(quarantine, "restore-state", "snapshot.txt"), []byte("corrupt"), 0o600))
		} else {
			requireSnapshotRestore(t, os.RemoveAll(filepath.Join(restoreRollbackRoot(paths, journal.OperationID), "storage")))
			requireSnapshotRestore(t, os.MkdirAll(storageRoot, 0o700))
			requireSnapshotRestore(t, os.WriteFile(filepath.Join(storageRoot, "foreign.txt"), []byte("foreign"), 0o600))
		}
		if installed, err := ApplyPendingSnapshotRestore(ctx, dataDir, testWorkspaceID); err == nil || installed {
			t.Fatalf("quarantined storage recovery = %v, %v", installed, err)
		}
		if crash == "invalid-quarantine" {
			requireRestoreFile(t, filepath.Join(storageRoot, "snapshot.txt"), "corrupt")
		} else {
			requireRestoreFile(t, filepath.Join(quarantine, "restore-state", "snapshot.txt"), "snapshot storage")
		}
		if _, found, err := readRestoreJournal(paths); crash == "quarantine-missing-previous" && (err != nil || !found) {
			t.Fatalf("ambiguous quarantine lost journal: found=%v err=%v", found, err)
		}
		if crash == "quarantine-with-foreign-live" {
			requireRestoreFile(t, filepath.Join(storageRoot, "foreign.txt"), "foreign")
		}
		return
	}
	if crash == "foreign-live-conflict" {
		requireSnapshotRestore(t, os.RemoveAll(filepath.Join(restoreRollbackRoot(paths, journal.OperationID), "storage")))
		requireSnapshotRestore(t, os.RemoveAll(filepath.Dir(storageRoot)))
		requireSnapshotRestore(t, os.MkdirAll(storageRoot, 0o700))
		requireSnapshotRestore(t, os.WriteFile(filepath.Join(storageRoot, "foreign.txt"), []byte("foreign"), 0o600))
		if installed, err := ApplyPendingSnapshotRestore(ctx, dataDir, testWorkspaceID); err == nil || installed {
			t.Fatalf("foreign live storage recovery = %v, %v", installed, err)
		}
		requireRestoreFile(t, filepath.Join(storageRoot, "foreign.txt"), "foreign")
		return
	}
	if crash == "old-moved" || crash == "before-old-move" {
		staging := restoreStagingRoot(paths, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
		if err := os.Rename(filepath.Dir(storageRoot), filepath.Join(staging, "storage")); err != nil {
			t.Fatal(err)
		}
		if crash == "before-old-move" {
			requireSnapshotRestore(t, os.Rename(filepath.Join(rollback, "storage"), filepath.Dir(storageRoot)))
		}
	}
	// Simulate process death after installation but before Runtime health and
	// CompletePendingSnapshotRestore. The next startup must restore old-live.
	if installed, err := ApplyPendingSnapshotRestore(
		ctx,
		dataDir,
		testWorkspaceID,
	); err != nil || installed {
		t.Fatalf("interrupted restore recovery = %v, %v", installed, err)
	}
	rolledBackApp := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := rolledBackApp.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer rolledBackApp.ResetBootstrapState()
	var value string
	if err := rolledBackApp.DB().NewQuery(
		`SELECT value FROM restore_probe`,
	).Row(&value); err != nil || value != "old-live" {
		t.Fatalf("rollback database value = %q, %v", value, err)
	}
	requireRestoreFile(t, filepath.Join(storageRoot, "live.txt"), "old live storage")
	if _, err := os.Lstat(filepath.Join(storageRoot, "snapshot.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback retained snapshot storage: %v", err)
	}
	rolledBackLedger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rolledBackLedger.Close()
	rolledBack, err := Open(ctx, Options{
		App: rolledBackApp, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: rolledBackLedger,
		DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rolledBack.Close(ctx)
	rolledBackSettings, _, err := rolledBack.state.retention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBackSettings.SnapshotDays != 45 ||
		rolledBackSettings.SnapshotCount != 60 ||
		rolledBackSettings.FileRevisionDays != 40 ||
		rolledBackSettings.FileRevisionCount != 120 {
		t.Fatalf(
			"rollback retention settings = %#v",
			rolledBackSettings,
		)
	}
	if _, found, err := readRestoreJournal(mustResolvePaths(t, dataDir)); err != nil || found {
		t.Fatalf("rollback left restore journal: found=%v err=%v", found, err)
	}
	requireSnapshotRestore(t, errors.Join(rolledBack.Close(ctx), rolledBackLedger.Close(), rolledBackApp.ResetBootstrapState()))
}

func TestProtectionSnapshotReceiptReusesPublishedSnapshotAfterRetry(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	defer app.ResetBootstrapState()
	ledger, err := auditledger.Open(
		filepath.Join(root, ".vibetable", "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	token, _ := runtime.coordinator.Current()
	before, err := runtime.catalog.List(ctx, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	first, err := runtime.protectionSnapshotForOperation(
		ctx,
		token,
		operationID,
		"snapshot.applyRestore.protection",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.protectionSnapshotForOperation(
		ctx,
		token,
		operationID,
		"snapshot.applyRestore.protection",
	)
	if err != nil {
		t.Fatal(err)
	}
	after, err := runtime.catalog.List(ctx, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotID != second.SnapshotID ||
		!first.Pinned ||
		len(after) != len(before)+1 {
		t.Fatalf(
			"protection retry first=%#v second=%#v before=%d after=%d",
			first,
			second,
			len(before),
			len(after),
		)
	}
}

func mustResolvePaths(t *testing.T, dataDir string) workspacePaths {
	t.Helper()
	paths, err := resolvePaths(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}
