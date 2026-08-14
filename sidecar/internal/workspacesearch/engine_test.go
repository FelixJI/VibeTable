package workspacesearch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

func TestSearchUsesLockedFTSNormalizesUnicodeAndSupportsCJK(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	enabled, err := engine.FTS5Enabled(ctx)
	if err != nil || !enabled {
		t.Fatalf("FTS5Enabled() = %v, %v", enabled, err)
	}
	upsert(t, engine, source("record", "record-1", "rev-1", "ＦＵＳＳ", "Fuß quarterly report", true))
	upsert(t, engine, source("file", "doc-1", "rev-2", "项目报告", "离线数据工作台交付", true))

	latin := query(t, engine, request("fuss"))
	cjk := query(t, engine, request("数据工作台"))
	shortCJK := query(t, engine, request("数据"))

	if len(latin.Hits) != 1 || latin.Hits[0].CanonicalId != "record-1" {
		t.Fatalf("latin hits = %#v", latin.Hits)
	}
	if len(cjk.Hits) != 1 || cjk.Hits[0].CanonicalId != "doc-1" {
		t.Fatalf("CJK hits = %#v", cjk.Hits)
	}
	if len(shortCJK.Hits) != 1 || shortCJK.Hits[0].CanonicalId != "doc-1" {
		t.Fatalf("short CJK fallback hits = %#v", shortCJK.Hits)
	}
}

func TestSearchFiltersHistoryPaginationAndStaleCursor(t *testing.T) {
	engine := testEngine(t)
	upsert(t, engine, source("file", "doc-1", "rev-1", "Report old", "alpha", true))
	upsert(t, engine, source("file", "doc-1", "rev-2", "Report current", "alpha", true))
	second := source("attachment", "att-1", "rev-3", "Attachment", "alpha", true)
	mime := "application/pdf"
	second.MIMEType = &mime
	upsert(t, engine, second)

	currentRequest := request("alpha")
	currentRequest.Limit = 1
	currentRequest.Filters = []contracts.SearchFilter{{
		Field: "kind", Operator: "eq", Value: "file",
	}}
	current := query(t, engine, currentRequest)
	if len(current.Hits) != 1 || current.Hits[0].SourceRevision != "rev-2" || current.NextCursor != nil {
		t.Fatalf("current result = %#v", current)
	}
	historyRequest := request("alpha")
	historyRequest.Scope = "history"
	historyRequest.Limit = 1
	history := query(t, engine, historyRequest)
	if history.NextCursor == nil {
		t.Fatal("history query did not return a cursor")
	}
	historyRequest.Cursor = history.NextCursor
	secondPage := query(t, engine, historyRequest)
	if len(secondPage.Hits) != 1 {
		t.Fatalf("second page = %#v", secondPage)
	}
	upsert(t, engine, source("record", "record-2", "rev-4", "New", "alpha", true))
	if _, err := engine.Query(context.Background(), historyRequest); err == nil || err.Error() != "workspace_search.cursor_stale" {
		t.Fatalf("stale cursor error = %v", err)
	}
}

func TestSearchRejectsMalformedAndTamperedCursorsWithoutOpeningAResult(t *testing.T) {
	engine := testEngine(t)
	upsert(t, engine, source("record", "one", "rev-1", "One", "alpha", true))
	upsert(t, engine, source("record", "two", "rev-1", "Two", "alpha", true))
	value := request("alpha")
	value.Limit = 1
	first := query(t, engine, value)
	if first.NextCursor == nil {
		t.Fatal("first page did not return a cursor")
	}
	valid := *first.NextCursor
	for _, cursor := range []string{"%", valid[:len(valid)-1] + "A"} {
		value.Cursor = &cursor
		if _, err := engine.Query(context.Background(), value); err == nil ||
			PublicErrorCode(err) != "workspace_search.cursor_stale" {
			t.Fatalf("cursor %q error = %v", cursor, err)
		}
	}
}

func TestSearchHandlesLongSnippetsHistoricalRowsAndClosedStorage(t *testing.T) {
	engine := testEngine(t)
	value := source(
		"record", "history", "rev-1", "Historical",
		strings.Repeat("prefix ", 45)+"needle", false,
	)
	upsert(t, engine, value)
	search := request("needle")
	search.Scope = "history"
	result := query(t, engine, search)
	if len(result.Hits) != 1 || result.Hits[0].Snippet == nil ||
		!strings.HasSuffix(*result.Hits[0].Snippet, "…") {
		t.Fatalf("long snippet result = %#v", result)
	}

	if code := PublicErrorCode(errors.New("private storage detail")); code != "workspace_search.internal_failed" {
		t.Fatalf("generic public error code = %q", code)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Upsert(context.Background(), value); err == nil {
		t.Fatal("upsert on closed storage unexpectedly succeeded")
	}
	if err := engine.Invalidate(context.Background()); err == nil {
		t.Fatal("invalidate on closed storage unexpectedly succeeded")
	}
	if _, err := engine.Status(context.Background()); err == nil {
		t.Fatal("status on closed storage unexpectedly succeeded")
	}
	if _, err := engine.Query(context.Background(), search); err == nil {
		t.Fatal("query on closed storage unexpectedly succeeded")
	}
}

func TestSearchRejectsUnknownFilterAndDoesNotInterpretFTSSyntax(t *testing.T) {
	engine := testEngine(t)
	upsert(t, engine, source("record", "record-1", "rev-1", "Literal", "alpha OR beta", true))
	result := query(t, engine, request("alpha OR beta"))
	if len(result.Hits) != 1 {
		t.Fatalf("quoted query hits = %#v", result.Hits)
	}
	invalid := request("alpha")
	invalid.Filters = []contracts.SearchFilter{{Field: "rawSql", Operator: "eq", Value: "1"}}
	if _, err := engine.Query(context.Background(), invalid); err == nil || err.Error() != "workspace_search.filter_invalid" {
		t.Fatalf("invalid filter error = %v", err)
	}
}

func TestSearchRejectsFilterOperatorAndValueTypeMismatches(t *testing.T) {
	engine := testEngine(t)
	for _, filter := range []contracts.SearchFilter{
		{Field: "sizeBytes", Operator: "contains", Value: "10"},
		{Field: "sizeBytes", Operator: "gte", Value: "10"},
		{Field: "revisionTime", Operator: "after", Value: true},
		{Field: "revisionTime", Operator: "after", Value: "not-a-date"},
		{Field: "kind", Operator: "eq", Value: "unknown"},
		{Field: "tableId", Operator: "gt", Value: "table-1"},
		{Field: "status", Operator: "contains", Value: nil},
	} {
		request := request("alpha")
		request.Filters = []contracts.SearchFilter{filter}
		if _, err := engine.Query(context.Background(), request); err == nil ||
			err.Error() != "workspace_search.filter_invalid" {
			t.Fatalf("filter %#v error = %v", filter, err)
		}
	}
}

func TestSearchStorageFaultsMapToStablePublicCodes(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt.db")
		if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(path)
		if err == nil || PublicErrorCode(err) != "workspace_search.corrupt" {
			t.Fatalf("error = %v, code = %s", err, PublicErrorCode(err))
		}
	})

	t.Run("busy", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "busy.db")
		engine, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()
		locker, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		locker.SetMaxOpenConns(1)
		defer locker.Close()
		if _, err := locker.Exec(`PRAGMA busy_timeout=10; BEGIN IMMEDIATE`); err != nil {
			t.Fatal(err)
		}
		defer locker.Exec(`ROLLBACK`)
		err = engine.Upsert(
			context.Background(),
			source("record", "record-1", "revision-1", "Busy", "alpha", true),
		)
		if err == nil || PublicErrorCode(err) != "workspace_search.busy" {
			t.Fatalf("error = %v, code = %s", err, PublicErrorCode(err))
		}
	})

	t.Run("full", func(t *testing.T) {
		engine := testEngine(t)
		var pages int
		if err := engine.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.db.Exec(`PRAGMA max_page_count=` + fmt.Sprint(pages)); err != nil {
			t.Fatal(err)
		}
		value := source("record", "record-large", "revision-1", "Full", strings.Repeat("x", 1<<20), true)
		err := engine.Upsert(context.Background(), value)
		if err == nil || PublicErrorCode(err) != "workspace_search.disk_full" {
			t.Fatalf("error = %v, code = %s", err, PublicErrorCode(err))
		}
	})
}

func TestProjectionCheckpointPromotesAtomicallyWithCorpus(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	first := ProjectionCheckpoint{
		BusinessOutboxRowID: 8, FileHeadRevision: 3, MutationRevision: 11,
	}
	if err := engine.RebuildProjection(
		ctx,
		[]SourceDocument{source("record", "record-1", "rev-1", "First", "alpha", true)},
		first,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if actual, err := engine.ProjectionCheckpoint(ctx); err != nil || actual != first {
		t.Fatalf("checkpoint = %#v, %v", actual, err)
	}

	invalid := source("record", "", "rev-2", "Invalid", "beta", true)
	second := ProjectionCheckpoint{
		BusinessOutboxRowID: 9, FileHeadRevision: 4, MutationRevision: 12,
	}
	if err := engine.RebuildProjection(ctx, []SourceDocument{invalid}, second, nil); err == nil {
		t.Fatal("invalid projection unexpectedly promoted")
	}
	if actual, err := engine.ProjectionCheckpoint(ctx); err != nil || actual != first {
		t.Fatalf("failed rebuild advanced checkpoint = %#v, %v", actual, err)
	}
	result := query(t, engine, request("alpha"))
	if len(result.Hits) != 1 || result.Hits[0].CanonicalId != "record-1" {
		t.Fatalf("failed rebuild replaced corpus = %#v", result.Hits)
	}
}

func TestInvalidateClearsProjectionCheckpoint(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	if err := engine.RebuildProjection(
		ctx,
		[]SourceDocument{source("file", "doc-1", "rev-1", "File", "alpha", true)},
		ProjectionCheckpoint{
			BusinessOutboxRowID: 12, FileHeadRevision: 5, MutationRevision: 16,
		},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := engine.Invalidate(ctx); err != nil {
		t.Fatal(err)
	}
	if actual, err := engine.ProjectionCheckpoint(ctx); err != nil || actual != (ProjectionCheckpoint{}) {
		t.Fatalf("checkpoint after invalidate = %#v, %v", actual, err)
	}
}

func TestInvalidatePersistsRebuildRequiredUntilSuccessfulPromotion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "search.db")
	engine, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.RebuildProjection(
		ctx,
		[]SourceDocument{source("record", "record-1", "rev-1", "Old", "stale", true)},
		ProjectionCheckpoint{MutationRevision: 7},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := engine.Invalidate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err := reopened.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "degraded" || status.ErrorCode == nil ||
		*status.ErrorCode != "workspace_search.restore_rebuild_required" {
		t.Fatalf("status after restart = %#v", status)
	}
	if _, err := reopened.Query(ctx, request("stale")); err == nil ||
		err.Error() != "workspace_search.restore_rebuild_required" {
		t.Fatalf("query while rebuild is required = %v", err)
	}

	invalid := source("record", "", "rev-2", "Invalid", "invalid", true)
	if err := reopened.RebuildProjection(
		ctx, []SourceDocument{invalid}, ProjectionCheckpoint{MutationRevision: 8}, nil,
	); err == nil {
		t.Fatal("invalid rebuild unexpectedly succeeded")
	}
	status, err = reopened.Status(ctx)
	if err != nil || status.State != "degraded" {
		t.Fatalf("failed rebuild cleared required state: %#v, %v", status, err)
	}

	if err := reopened.RebuildProjection(
		ctx,
		[]SourceDocument{source("record", "record-2", "rev-2", "Restored", "current", true)},
		ProjectionCheckpoint{MutationRevision: 8},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	status, err = reopened.Status(ctx)
	if err != nil || status.State != "ready" || status.ErrorCode != nil {
		t.Fatalf("status after successful rebuild = %#v, %v", status, err)
	}
	result, err := reopened.Query(ctx, request("current"))
	if err != nil || len(result.Hits) != 1 || result.Hits[0].CanonicalId != "record-2" {
		t.Fatalf("rebuilt result = %#v, %v", result, err)
	}
}

func TestCorruptRebuildMarkerFailsClosedForStatusAndQuery(t *testing.T) {
	engine := testEngine(t)
	if _, err := engine.db.Exec(
		`UPDATE search_meta SET value='unknown' WHERE key='rebuild_required'`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Status(context.Background()); err == nil ||
		err.Error() != "workspace_search.rebuild_state_corrupt" {
		t.Fatalf("status with corrupt rebuild marker = %v", err)
	}
	if _, err := engine.Query(context.Background(), request("anything")); err == nil ||
		err.Error() != "workspace_search.rebuild_state_corrupt" {
		t.Fatalf("query with corrupt rebuild marker = %v", err)
	}
}

func TestApplyProjectionChangesUpsertsAndTombstonesAffectedSourcesAtomically(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	table, firstRecord, secondRecord := "table-1", "record-1", "record-2"
	first := source("record", table+":"+firstRecord, "rev-1", "First", "alpha", true)
	first.TableID, first.RecordID = &table, &firstRecord
	second := source("record", table+":"+secondRecord, "rev-1", "Second", "beta", true)
	second.TableID, second.RecordID = &table, &secondRecord
	if err := engine.RebuildProjection(
		ctx, []SourceDocument{first, second}, ProjectionCheckpoint{MutationRevision: 1}, nil,
	); err != nil {
		t.Fatal(err)
	}
	updated := first
	updated.SourceRevision, updated.Body = "rev-2", "gamma"
	checkpoint := ProjectionCheckpoint{
		BusinessOutboxRowID: 2, MutationRevision: 2,
	}
	if err := engine.ApplyProjectionChanges(ctx, ProjectionChanges{
		Records: []RecordProjectionKey{
			{TableID: table, RecordID: firstRecord},
			{TableID: table, RecordID: secondRecord},
		},
		Sources: []SourceDocument{updated},
	}, checkpoint); err != nil {
		t.Fatal(err)
	}
	if hits := query(t, engine, request("gamma")).Hits; len(hits) != 1 {
		t.Fatalf("updated hits = %#v", hits)
	}
	if hits := query(t, engine, request("beta")).Hits; len(hits) != 0 {
		t.Fatalf("tombstoned hits = %#v", hits)
	}
	if actual, err := engine.ProjectionCheckpoint(ctx); err != nil || actual != checkpoint {
		t.Fatalf("checkpoint = %#v, %v", actual, err)
	}
}

func TestRebuildReportsProgressPublishesStatusAndCanTombstone(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	initial, err := engine.Status(ctx)
	if err != nil || initial.State != "idle" || initial.Generation != 0 || initial.Processed != 0 {
		t.Fatalf("initial status = %#v, %v", initial, err)
	}

	sources := make([]SourceDocument, 101)
	for index := range sources {
		sources[index] = source(
			"record",
			fmt.Sprintf("record-%03d", index),
			"rev-1",
			fmt.Sprintf("Record %03d", index),
			"rebuild corpus",
			true,
		)
	}
	var progress [][2]int
	if err := engine.RebuildWithProgress(ctx, sources, func(processed, total int) {
		progress = append(progress, [2]int{processed, total})
	}); err != nil {
		t.Fatal(err)
	}
	if len(progress) != 2 || progress[0] != [2]int{100, 101} || progress[1] != [2]int{101, 101} {
		t.Fatalf("progress = %#v", progress)
	}
	ready, err := engine.Status(ctx)
	if err != nil || ready.State != "ready" || ready.Generation != 1 || ready.Processed != 101 ||
		ready.Total == nil || *ready.Total != 101 {
		t.Fatalf("ready status = %#v, %v", ready, err)
	}

	if err := engine.Tombstone(ctx, "record", "record-000", "rev-2", "2026-08-12T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if hits := query(t, engine, request("Record 000")).Hits; len(hits) != 0 {
		t.Fatalf("current tombstoned hits = %#v", hits)
	}
	history := request("Record 000")
	history.Scope = "history"
	if hits := query(t, engine, history).Hits; len(hits) != 2 {
		t.Fatalf("history tombstoned hits = %#v", hits)
	}

	if err := engine.Rebuild(ctx, sources[:1]); err != nil {
		t.Fatal(err)
	}
	if hits := query(t, engine, request("rebuild corpus")).Hits; len(hits) != 1 {
		t.Fatalf("rebuilt hits = %#v", hits)
	}
}

func TestCurrentFileProjectionStatesRoundTripsMetadataAndRejectsCorruption(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	documentID, mime, extension := "document-1", "text/plain", "txt"
	size := int64(42)
	file := source("file", documentID, "rev-7", "Notes", "offline", true)
	file.DocumentID, file.MIMEType, file.Extension, file.SizeBytes = &documentID, &mime, &extension, &size
	file.Status = "indexed"
	file.Metadata = []contracts.SearchMetadataItem{
		{Key: "relativePath", Value: "notes/today.txt"},
		{Key: "documentStatus", Value: "available"},
	}
	if err := engine.Upsert(ctx, file); err != nil {
		t.Fatal(err)
	}
	states, err := engine.CurrentFileProjectionStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := FileProjectionState{
		SourceRevision: "rev-7", Title: "Notes", RelativePath: "notes/today.txt",
		DocumentStatus: "available", MIMEType: mime, Extension: extension,
		SizeBytes: size, Status: "indexed",
	}
	if states[documentID] != want {
		t.Fatalf("state = %#v", states[documentID])
	}

	if _, err := engine.db.Exec(`UPDATE search_documents SET metadata_json='{' WHERE document_id=?`, documentID); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.CurrentFileProjectionStates(ctx); err == nil || err.Error() != "workspace_search.index_corrupt" {
		t.Fatalf("corrupt metadata error = %v", err)
	}
}

func TestProjectionMutationRejectsInvalidKeysAndCheckpoint(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	invalidCheckpoint := ProjectionCheckpoint{BusinessOutboxRowID: -1}
	if err := engine.RebuildProjection(ctx, nil, invalidCheckpoint, nil); err == nil ||
		err.Error() != "workspace_search.checkpoint_invalid" {
		t.Fatalf("rebuild checkpoint error = %v", err)
	}
	if err := engine.ApplyProjectionChanges(ctx, ProjectionChanges{}, invalidCheckpoint); err == nil ||
		err.Error() != "workspace_search.checkpoint_invalid" {
		t.Fatalf("incremental checkpoint error = %v", err)
	}
	for _, changes := range []ProjectionChanges{
		{Records: []RecordProjectionKey{{TableID: "", RecordID: "record-1"}}},
		{Records: []RecordProjectionKey{{TableID: "table-1", RecordID: " "}}},
		{Documents: []string{" "}},
	} {
		if err := engine.ApplyProjectionChanges(ctx, changes, ProjectionCheckpoint{}); err == nil ||
			err.Error() != "workspace_search.projection_key_invalid" {
			t.Fatalf("invalid projection key error = %v", err)
		}
	}
}

func TestProjectionCheckpointRejectsMissingAndCorruptMetadata(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
		remove           bool
	}{
		{"missing business tail", "business_outbox_rowid", "", true},
		{"invalid business tail", "business_outbox_rowid", "bad", false},
		{"negative business tail", "business_outbox_rowid", "-1", false},
		{"invalid file tail", "file_head_revision", "bad", false},
		{"invalid mutation tail", "mutation_revision", "bad", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := testEngine(t)
			var err error
			if test.remove {
				_, err = engine.db.Exec(`DELETE FROM search_meta WHERE key=?`, test.key)
			} else {
				_, err = engine.db.Exec(`UPDATE search_meta SET value=? WHERE key=?`, test.value, test.key)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.ProjectionCheckpoint(context.Background())
			if err == nil {
				t.Fatal("corrupt checkpoint unexpectedly accepted")
			}
		})
	}
}

func TestSearchAcceptsEverySortAndNumericFilterRepresentation(t *testing.T) {
	engine := testEngine(t)
	ctx := context.Background()
	for index, numeric := range []any{int(12), int64(12), uint64(12), float64(12)} {
		value := source("file", fmt.Sprintf("doc-%d", index), "rev-1", fmt.Sprintf("Title %d", index), "alpha", true)
		size, documentID := int64(12), fmt.Sprintf("doc-%d", index)
		value.SizeBytes, value.DocumentID = &size, &documentID
		if err := engine.Upsert(ctx, value); err != nil {
			t.Fatal(err)
		}
		search := request("alpha")
		search.Filters = []contracts.SearchFilter{{Field: "sizeBytes", Operator: "eq", Value: numeric}}
		search.Sorts = []contracts.SearchSort{
			{Field: "score", Direction: "desc"},
			{Field: "revisionTime", Direction: "asc"},
			{Field: "title", Direction: "desc"},
		}
		if result, err := engine.Query(ctx, search); err != nil || len(result.Hits) == 0 {
			t.Fatalf("numeric %T result = %#v, %v", numeric, result, err)
		}
	}
	search := request("alpha")
	search.Sorts = []contracts.SearchSort{{Field: "sizeBytes", Direction: "asc"}}
	if _, err := engine.Query(ctx, search); err != nil {
		t.Fatal(err)
	}
}

func TestSearchRejectsEveryMalformedRequestDimension(t *testing.T) {
	tooManyFilters := make([]contracts.SearchFilter, 21)
	tooManySorts := make([]contracts.SearchSort, 4)
	for index := range tooManySorts {
		tooManySorts[index] = contracts.SearchSort{Field: "title", Direction: "asc"}
	}
	tests := []struct {
		name   string
		mutate func(*contracts.SearchRequest)
		code   string
	}{
		{"version", func(value *contracts.SearchRequest) { value.ContractVersion = "2.0" }, "workspace_search.request_invalid"},
		{"blank query", func(value *contracts.SearchRequest) { value.Query = " " }, "workspace_search.request_invalid"},
		{"logic", func(value *contracts.SearchRequest) { value.Logic = "xor" }, "workspace_search.request_invalid"},
		{"scope", func(value *contracts.SearchRequest) { value.Scope = "all" }, "workspace_search.request_invalid"},
		{"zero limit", func(value *contracts.SearchRequest) { value.Limit = 0 }, "workspace_search.request_invalid"},
		{"large limit", func(value *contracts.SearchRequest) { value.Limit = 201 }, "workspace_search.request_invalid"},
		{"many filters", func(value *contracts.SearchRequest) { value.Filters = tooManyFilters }, "workspace_search.request_invalid"},
		{"many sorts", func(value *contracts.SearchRequest) { value.Sorts = tooManySorts }, "workspace_search.request_invalid"},
		{"sort direction", func(value *contracts.SearchRequest) {
			value.Sorts = []contracts.SearchSort{{Field: "title", Direction: "up"}}
		}, "workspace_search.sort_invalid"},
		{"sort field", func(value *contracts.SearchRequest) {
			value.Sorts = []contracts.SearchSort{{Field: "raw", Direction: "asc"}}
		}, "workspace_search.sort_invalid"},
	}
	engine := testEngine(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := request("alpha")
			test.mutate(&value)
			_, err := engine.Query(context.Background(), value)
			if err == nil || PublicErrorCode(err) != test.code {
				t.Fatalf("error = %v, code = %s", err, PublicErrorCode(err))
			}
		})
	}
}

func TestSearchAcceptsAllFilterOperatorsWithOrLogic(t *testing.T) {
	engine := testEngine(t)
	tableID, fieldID, documentID := "table-1", "field-1", "document-1"
	mime, extension, size := "application/pdf", "pdf", int64(42)
	value := source("attachment", "attachment-1", "rev-1", "Filtered", "alpha", true)
	value.TableID, value.FieldID, value.DocumentID = &tableID, &fieldID, &documentID
	value.MIMEType, value.Extension, value.SizeBytes = &mime, &extension, &size
	upsert(t, engine, value)

	search := request("alpha")
	search.Logic = "or"
	search.Filters = []contracts.SearchFilter{
		{Field: "kind", Operator: "ne", Value: "file"},
		{Field: "tableId", Operator: "contains", Value: "table"},
		{Field: "fieldId", Operator: "eq", Value: fieldID},
		{Field: "mimeType", Operator: "ne", Value: "text/plain"},
		{Field: "extension", Operator: "contains", Value: "pd"},
		{Field: "status", Operator: "eq", Value: "active"},
		{Field: "sizeBytes", Operator: "gt", Value: 1},
		{Field: "sizeBytes", Operator: "gte", Value: 42},
		{Field: "sizeBytes", Operator: "lt", Value: 100},
		{Field: "sizeBytes", Operator: "lte", Value: 42},
		{Field: "revisionTime", Operator: "before", Value: "2026-08-13T00:00:00Z"},
		{Field: "revisionTime", Operator: "after", Value: "2026-08-11T00:00:00Z"},
	}
	if result, err := engine.Query(context.Background(), search); err != nil || len(result.Hits) != 1 {
		t.Fatalf("filtered result = %#v, %v", result, err)
	}
}

func TestRebuildCancellationRollsBackThePreviousGeneration(t *testing.T) {
	engine := testEngine(t)
	upsert(t, engine, source("record", "previous", "rev-1", "Previous", "stable corpus", true))
	sources := make([]SourceDocument, 101)
	for index := range sources {
		sources[index] = source("record", fmt.Sprintf("next-%d", index), "rev-1", "Next", "next corpus", true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := engine.RebuildWithProgress(ctx, sources, func(processed, _ int) {
		if processed == 100 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("rebuild error = %v", err)
	}
	if hits := query(t, engine, request("stable corpus")).Hits; len(hits) != 1 {
		t.Fatalf("rolled back hits = %#v", hits)
	}
}

func TestSourceValidationRejectsUnknownKindAndIncompleteIdentity(t *testing.T) {
	engine := testEngine(t)
	for _, value := range []SourceDocument{
		source("unknown", "record-1", "rev-1", "Unknown", "alpha", true),
		source("record", "", "rev-1", "No id", "alpha", true),
		source("record", "record-1", "", "No revision", "alpha", true),
		source("record", "record-1", "rev-1", "No time", "alpha", true),
	} {
		if value.Title == "No time" {
			value.RevisionTime = ""
		}
		err := engine.Upsert(context.Background(), value)
		if err == nil || PublicErrorCode(err) != "workspace_search.source_invalid" {
			t.Fatalf("source %#v error = %v", value, err)
		}
	}
}

func testEngine(t *testing.T) *Engine {
	t.Helper()
	engine, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

func source(kind, id, revision, title, body string, current bool) SourceDocument {
	return SourceDocument{
		Kind: kind, CanonicalID: id, SourceRevision: revision,
		Title: title, Body: body, RevisionTime: "2026-08-12T00:00:00Z",
		Status: "active", Current: current,
		Metadata:   []contracts.SearchMetadataItem{},
		OpenTarget: contracts.SearchOpenTarget{Kind: kind},
	}
}

func request(text string) contracts.SearchRequest {
	return contracts.SearchRequest{
		ContractVersion: "1.0", Query: text, Logic: "and",
		Filters: []contracts.SearchFilter{}, Sorts: []contracts.SearchSort{},
		Scope: "current", Limit: 20,
	}
}

func upsert(t *testing.T, engine *Engine, value SourceDocument) {
	t.Helper()
	if err := engine.Upsert(context.Background(), value); err != nil {
		t.Fatal(err)
	}
}

func query(t *testing.T, engine *Engine, value contracts.SearchRequest) Result {
	t.Helper()
	result, err := engine.Query(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
