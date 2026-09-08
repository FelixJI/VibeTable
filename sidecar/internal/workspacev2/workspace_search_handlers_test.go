package workspacev2

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestSearchProjectionWorkerHonorsCancellationBeforeTouchingRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime := &Runtime{}
	runtime.searchProjectionWG.Add(1)
	runtime.runSearchProjectionWorker(ctx)
}

func TestQuiesceWorkspaceSearchCancelsAndJoinsBothOwners(t *testing.T) {
	runtime := &Runtime{}
	projectionContext, projectionCancel := context.WithCancel(context.Background())
	rebuildContext, rebuildCancel := context.WithCancel(context.Background())
	runtime.searchProjectionCancel = projectionCancel
	runtime.searchTaskCancel = rebuildCancel
	projectionStopped := make(chan struct{})
	rebuildStopped := make(chan struct{})
	runtime.searchProjectionWG.Add(1)
	go func() {
		defer runtime.searchProjectionWG.Done()
		<-projectionContext.Done()
		close(projectionStopped)
	}()
	runtime.searchTaskWG.Add(1)
	go func() {
		defer runtime.searchTaskWG.Done()
		<-rebuildContext.Done()
		close(rebuildStopped)
	}()

	runtime.quiesceWorkspaceSearch()

	select {
	case <-projectionStopped:
	default:
		t.Fatal("projection owner was not joined")
	}
	select {
	case <-rebuildStopped:
	default:
		t.Fatal("rebuild owner was not joined")
	}
	if runtime.searchProjectionCancel != nil || runtime.searchTaskCancel != nil ||
		!runtime.searchProjectionPaused {
		t.Fatalf("quiesced search state = %#v", runtime)
	}
}

func TestEnsureSearchProjectionReplaysChangedRecordWithoutFullRebuild(t *testing.T) {
	tableID, recordID := "table-search", "record000000001"
	ctx, app, runtime := openSearchTestRuntime(t, func(app *pocketbase.PocketBase) {
		seedSearchTable(t, app, tableID, recordID, "alpha payload")
		insertSearchOutboxEvent(t, app, "event-initial", tableID, recordID, mutation.DataChangeInsert)
	})
	target, _, err := runtime.searchProjectionSourceTail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := runtime.collectSearchSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.search.RebuildProjection(ctx, sources, target, nil); err != nil {
		t.Fatal(err)
	}
	sentinelTable, sentinelRecord := "sentinel-table", "sentinel-record"
	if err := runtime.search.Upsert(ctx, workspacesearch.SourceDocument{
		Kind: "record", CanonicalID: "sentinel", SourceRevision: "sentinel-revision",
		Title: "sentinel", Body: "preserve marker", RevisionTime: "2026-08-12T00:00:00Z",
		TableID: &sentinelTable, RecordID: &sentinelRecord, Status: "active", Current: true,
		Metadata:   []contracts.SearchMetadataItem{},
		OpenTarget: contracts.SearchOpenTarget{Kind: "record", TableId: &sentinelTable, RecordId: &sentinelRecord},
	}); err != nil {
		t.Fatal(err)
	}
	row, err := app.FindRecordById("search_records", recordID)
	if err != nil {
		t.Fatal(err)
	}
	row.Set("body_value", "beta payload")
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	insertUnrelatedOutboxRow(t, app)
	insertSearchOutboxEvent(t, app, "event-update", tableID, recordID, mutation.DataChangeUpdate)
	if err := runtime.ensureSearchProjection(ctx); err != nil {
		t.Fatal(err)
	}
	if hits := querySearch(t, runtime.search, "beta"); len(hits) != 1 {
		t.Fatalf("updated hits = %#v", hits)
	}
	if hits := querySearch(t, runtime.search, "alpha"); len(hits) != 0 {
		t.Fatalf("stale hits = %#v", hits)
	}
	if hits := querySearch(t, runtime.search, "preserve"); len(hits) != 1 {
		t.Fatalf("incremental replay replaced the full projection: %#v", hits)
	}
}

func TestResolveWorkspaceSearchHitRefreshesFromAuthorityAndRemovesMissingSource(t *testing.T) {
	tableID, recordID := "table-search", "record000000001"
	ctx, app, runtime := openSearchTestRuntime(t, func(app *pocketbase.PocketBase) {
		seedSearchTable(t, app, tableID, recordID, "alpha payload")
	})
	target, _, err := runtime.searchProjectionSourceTail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := runtime.collectSearchSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.search.RebuildProjection(ctx, sources, target, nil); err != nil {
		t.Fatal(err)
	}
	stale := querySearch(t, runtime.search, "alpha")[0]
	row, err := app.FindRecordById("search_records", recordID)
	if err != nil {
		t.Fatal(err)
	}
	row.Set("body_value", "beta payload")
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}

	resolved := resolveSearchHit(t, runtime, contracts.SearchResolveRequest{
		ContractVersion: "1.0", Scope: "current", Hit: stale,
	})
	if resolved.Status != "stale" || resolved.Hit.SourceRevision == stale.SourceRevision {
		t.Fatalf("resolved = %#v", resolved)
	}
	if hits := querySearch(t, runtime.search, "alpha"); len(hits) != 0 {
		t.Fatalf("stale index row remains: %#v", hits)
	}
	if hits := querySearch(t, runtime.search, "beta"); len(hits) != 1 {
		t.Fatalf("authority refresh was not indexed: %#v", hits)
	}
	current := resolveSearchHit(t, runtime, contracts.SearchResolveRequest{
		ContractVersion: "1.0", Scope: "current", Hit: resolved.Hit,
	})
	if current.Status != "current" || current.Hit.HitId != resolved.Hit.HitId {
		t.Fatalf("current = %#v", current)
	}

	if err := app.Delete(row); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(contracts.SearchResolveRequest{
		ContractVersion: "1.0", Scope: "current", Hit: resolved.Hit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.resolveWorkspaceSearchHit(ctx, nil, raw); err == nil ||
		err.Error() != "workspace_search.hit_missing" {
		t.Fatalf("missing resolve error = %v", err)
	}
	if hits := querySearch(t, runtime.search, "beta"); len(hits) != 0 {
		t.Fatalf("missing authority left a searchable hit: %#v", hits)
	}
}

func TestFileExtractionErrorCodeRoundTripsWithoutIndexingFailedContent(t *testing.T) {
	ctx, _, runtime := openSearchTestRuntime(t, nil)
	token, _ := runtime.coordinator.Current()
	saved, err := runtime.history.Save(ctx, filehistory.SaveRequest{
		Token: token, DocumentID: "22222222-2222-4222-8222-222222222222",
		Path: "corrupt.pdf", Kind: filehistory.RevisionFormal,
		Content:  []byte("%PDF-1.7\n<< /Filter /FlateDecode >>\nstream\nforbiddenpayload\nendstream"),
		MimeType: "application/pdf", CreatedBy: "test", DeviceID: testClaimID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := runtime.collectFileSearchSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].Body != "" ||
		sources[0].Status != "failed" {
		t.Fatalf("file sources = %#v, err=%v", sources, err)
	}
	if err := runtime.search.RebuildProjection(
		ctx, sources, workspacesearch.ProjectionCheckpoint{}, nil,
	); err != nil {
		t.Fatal(err)
	}
	hits := querySearch(t, runtime.search, "corrupt")
	if len(hits) != 1 {
		t.Fatalf("filename hits = %#v", hits)
	}
	assertSearchMetadata(t, hits[0].Metadata, map[string]any{
		"extractionStatus": "failed", "extractionErrorCode": "extract.pdf_stream_invalid",
	})
	if hits := querySearch(t, runtime.search, "forbiddenpayload"); len(hits) != 0 {
		t.Fatalf("failed extraction content was indexed: %#v", hits)
	}
	if _, err := runtime.history.Delete(
		ctx, token, saved.Document.DocumentID, &saved.Revision.RevisionID,
	); err != nil {
		t.Fatal(err)
	}
	sources, err = runtime.collectFileSearchSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].Status != "deleted" ||
		!sources[0].Current {
		t.Fatalf("deleted file sources = %#v, err=%v", sources, err)
	}
	assertSearchMetadata(t, sources[0].Metadata, map[string]any{
		"documentStatus": "deleted", "extractionStatus": "failed",
		"extractionErrorCode": "extract.pdf_stream_invalid",
	})
	if err := runtime.search.RebuildProjection(
		ctx, sources, workspacesearch.ProjectionCheckpoint{}, nil,
	); err != nil {
		t.Fatal(err)
	}
	if hits := querySearch(t, runtime.search, "corrupt"); len(hits) != 0 {
		t.Fatalf("deleted file became searchable: %#v", hits)
	}
	unavailableRepository := &unavailableOpenRepository{Repository: runtime.repository}
	runtime.history, err = filehistory.OpenCurrent(
		ctx, unavailableRepository, runtime.coordinator, runtime.headStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	unavailableRepository.unavailable = true
	sources, err = runtime.collectFileSearchSources(ctx)
	if err != nil || len(sources) != 1 {
		t.Fatalf("unavailable sources = %#v, err=%v", sources, err)
	}
	assertSearchMetadata(t, sources[0].Metadata, map[string]any{
		"extractionStatus": "failed", "extractionErrorCode": "extract.source_unavailable",
	})
}

func openSearchTestRuntime(
	t *testing.T,
	prepare func(*pocketbase.PocketBase),
) (context.Context, *pocketbase.PocketBase, *Runtime) {
	t.Helper()
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	if prepare != nil {
		prepare(app)
	}
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir, WorkspaceID: testWorkspaceID,
		SessionEpoch: 7, FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		DeferBackgroundWorkers: true, DisableReplicaWorker: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	return ctx, app, runtime
}

type unavailableOpenRepository struct {
	objectrepo.Repository
	unavailable bool
}

func (repository *unavailableOpenRepository) Open(
	ctx context.Context, id objectrepo.ObjectID,
) (io.ReadCloser, error) {
	if repository.unavailable {
		return nil, objectrepo.ErrNotFound
	}
	return repository.Repository.Open(ctx, id)
}

func resolveSearchHit(
	t *testing.T,
	runtime *Runtime,
	request contracts.SearchResolveRequest,
) contracts.SearchResolveResult {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.resolveWorkspaceSearchHit(context.Background(), nil, raw)
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok := result.(contracts.SearchResolveResult)
	if !ok {
		t.Fatalf("resolve result type = %T", result)
	}
	return resolved
}

func TestRecordProjectionKeysAcceptsOutboxRowIDGapsAndDeduplicates(t *testing.T) {
	rows := []businessProjectionEventRow{
		projectionEventRow(t, 10, "event-10", "table-b", []string{"record-2"}, mutation.DataChangeUpdate),
		projectionEventRow(t, 12, "event-12", "table-a", []string{"record-1", "record-1"}, mutation.DataChangeInsert),
	}
	keys, full, err := recordProjectionKeys(context.Background(), rows)
	if err != nil || full {
		t.Fatalf("keys failed: full=%v err=%v", full, err)
	}
	want := []workspacesearch.RecordProjectionKey{
		{TableID: "table-a", RecordID: "record-1"},
		{TableID: "table-b", RecordID: "record-2"},
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %#v", keys)
	}
	for index := range want {
		if keys[index] != want[index] {
			t.Fatalf("keys = %#v", keys)
		}
	}
}

func TestRecordProjectionKeysRequiresFullForSourceContractChange(t *testing.T) {
	rows := []businessProjectionEventRow{
		projectionEventRow(t, 21, "event-21", "metadata:content_profiles", []string{"table-a"}, mutation.DataChangeUpdate),
	}
	keys, full, err := recordProjectionKeys(context.Background(), rows)
	if err != nil || !full || len(keys) != 0 {
		t.Fatalf("keys=%#v full=%v err=%v", keys, full, err)
	}
}

func TestRequiresFullSearchProjectionDistinguishesRetentionGapFromNaturalRowIDGap(t *testing.T) {
	ready := contracts.SearchStatus{State: "ready", Generation: 3}
	current := workspacesearch.ProjectionCheckpoint{
		BusinessOutboxRowID: 10, FileHeadRevision: 4, MutationRevision: 7,
	}
	target := workspacesearch.ProjectionCheckpoint{
		BusinessOutboxRowID: 12, FileHeadRevision: 4, MutationRevision: 8,
	}
	if requiresFullSearchProjection(ready, current, target, businessOutboxBounds{
		Minimum: 10, Maximum: 12, Count: 2,
	}) {
		t.Fatal("natural rowid gap was treated as retained-event loss")
	}
	if !requiresFullSearchProjection(ready, current, target, businessOutboxBounds{
		Minimum: 12, Maximum: 12, Count: 1,
	}) {
		t.Fatal("retention gap did not force a full rebuild")
	}
	degraded := ready
	degraded.State = "degraded"
	if !requiresFullSearchProjection(degraded, current, current, businessOutboxBounds{}) {
		t.Fatal("persisted degraded state did not force a full rebuild")
	}
}

func TestSameFileProjectionStateDetectsRenameWithoutContentRevisionChange(t *testing.T) {
	summary := filehistory.FileDocumentSummary{
		DocumentID: "document-1", RelativePath: "reports/new-name.txt",
		DisplayName: "new-name.txt", Extension: ".txt", MimeType: "text/plain",
		SizeBytes: 42, EffectiveRevisionID: "revision-1", Status: filehistory.DocumentActive,
	}
	state := workspacesearch.FileProjectionState{
		SourceRevision: "revision-1", RelativePath: "reports/old-name.txt",
		Title: "old-name.txt", Extension: ".txt", MIMEType: "text/plain",
		SizeBytes: 42, DocumentStatus: string(filehistory.DocumentActive),
	}
	if sameFileProjectionState(state, summary) {
		t.Fatal("rename was invisible to the file projection comparator")
	}
	state.RelativePath, state.Title = summary.RelativePath, summary.DisplayName
	if !sameFileProjectionState(state, summary) {
		t.Fatal("equal projection state was reported as changed")
	}
}

func projectionEventRow(
	t *testing.T,
	rowID int64,
	eventID string,
	tableID string,
	recordIDs []string,
	operation mutation.DataChangeOperation,
) businessProjectionEventRow {
	t.Helper()
	event := mutation.DataChangedEvent{
		ContractVersion: mutation.ContractVersion,
		Topic:           "data.changed", EventID: eventID, Sequence: rowID,
		OccurredAt:     time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		SchemaRevision: "schema_0001", DataRevision: "data_0001",
		TableID: tableID, RecordIDs: recordIDs, Operation: operation,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return businessProjectionEventRow{
		RowID: rowID, EventID: eventID, PayloadJSON: string(raw),
	}
}

func seedSearchTable(
	t *testing.T,
	app *pocketbase.PocketBase,
	tableID string,
	recordID string,
	body string,
) {
	t.Helper()
	physical := core.NewBaseCollection("search_records")
	physical.Fields.Add(&core.TextField{Name: "title_value"})
	physical.Fields.Add(&core.TextField{Name: "body_value"})
	if err := app.Save(physical); err != nil {
		t.Fatal(err)
	}
	tables, err := app.FindCollectionByNameOrId("vibetable_tables")
	if err != nil {
		t.Fatal(err)
	}
	table := core.NewRecord(tables)
	table.Set("table_id", tableID)
	table.Set("collection_id", physical.Id)
	table.Set("physical_name", physical.Name)
	table.Set("display_name", "Search records")
	table.Set("kind", "base")
	table.Set("schema_revision", 1)
	table.Set("data_revision", 1)
	table.Set("archive_policy", `{"mode":"none","fieldId":null,"archivedValue":null}`)
	if err := app.Save(table); err != nil {
		t.Fatal(err)
	}
	fields, err := app.FindCollectionByNameOrId("vibetable_fields")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct{ id, physical string }{
		{"field-title", "title_value"}, {"field-body", "body_value"},
	} {
		record := core.NewRecord(fields)
		record.Set("table_id", tableID)
		record.Set("field_id", field.id)
		record.Set("physical_name", field.physical)
		record.Set("display_name", field.id)
		record.Set("kind", "base")
		record.Set("data_type", "text")
		record.Set("storage_type", "text")
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	profiles, err := app.FindCollectionByNameOrId("vibetable_content_profiles")
	if err != nil {
		t.Fatal(err)
	}
	profile := core.NewRecord(profiles)
	profile.Set("logical_id", tableID)
	profile.Set("payload_json", types.JSONRaw([]byte(`{"contractVersion":"1.0","tableId":"table-search","titleFieldId":"field-title","bodyFieldId":"field-body","summaryFieldId":null,"searchableFieldIds":["field-title","field-body"]}`)))
	if err := app.Save(profile); err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(physical)
	row.Id = recordID
	row.Set("title_value", "Initial title")
	row.Set("body_value", body)
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
}

func insertSearchOutboxEvent(
	t *testing.T,
	app *pocketbase.PocketBase,
	eventID string,
	tableID string,
	recordID string,
	operation mutation.DataChangeOperation,
) {
	t.Helper()
	row := projectionEventRow(t, 1, eventID, tableID, []string{recordID}, operation)
	collection, err := app.FindCollectionByNameOrId("vibetable_outbox")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("event_id", eventID)
	record.Set("topic", "data.changed")
	record.Set("payload_json", types.JSONRaw([]byte(row.PayloadJSON)))
	record.Set("status", "pending")
	record.Set("attempts", 0)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
}

func insertUnrelatedOutboxRow(t *testing.T, app *pocketbase.PocketBase) {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("vibetable_outbox")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("event_id", "unrelated-event")
	record.Set("topic", "task.changed")
	record.Set("payload_json", types.JSONRaw([]byte(`{"kind":"unrelated"}`)))
	record.Set("status", "pending")
	record.Set("attempts", 0)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
}

func querySearch(
	t *testing.T,
	engine *workspacesearch.Engine,
	text string,
) []contracts.SearchHit {
	t.Helper()
	result, err := engine.Query(context.Background(), contracts.SearchRequest{
		ContractVersion: "1.0", Query: text, Logic: "and",
		Filters: []contracts.SearchFilter{}, Sorts: []contracts.SearchSort{},
		Scope: "current", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Hits
}

func assertSearchMetadata(
	t *testing.T,
	metadata []contracts.SearchMetadataItem,
	want map[string]any,
) {
	t.Helper()
	for _, item := range metadata {
		if value, found := want[item.Key]; found && item.Value == value {
			delete(want, item.Key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing search metadata = %#v; got=%#v", want, metadata)
	}
}
