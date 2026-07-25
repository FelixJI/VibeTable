package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

type uppercaseFormula struct {
	called bool
}

func (calculator *uppercaseFormula) Calculate(
	_ context.Context,
	app core.App,
	_ schema.TableDefinition,
	record *core.Record,
) (map[string]any, error) {
	if _, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id='formula_notes'"); err != nil {
		return nil, err
	}
	calculator.called = true
	return map[string]any{"fld_computed": strings.ToUpper(record.GetString("title"))}, nil
}

type committedOutboxPublisher struct {
	app       core.App
	called    bool
	committed bool
}

func (publisher *committedOutboxPublisher) Publish(
	_ context.Context,
	event mutation.DataChangedEvent,
) error {
	publisher.called = true
	if _, err := publisher.app.FindFirstRecordByFilter(
		"vibetable_outbox", "event_id={:event}", dbx.Params{"event": event.EventID},
	); err == nil {
		publisher.committed = true
	}
	return errors.New("external publisher unavailable")
}

func TestMutationKernelAppliesInsertAtomicallyAndReplaysIdempotently(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("notes", "notes", []schema.FieldDefinition{
			field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 8, 30, 0, 0, time.UTC)
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return now }),
		mutation.WithIDGenerator(func(kind string) string {
			switch kind {
			case "record":
				return "rec000000000001"
			case "changeSet":
				return "chg_01HZX"
			case "event":
				return "evt_data_0001"
			default:
				return "generated"
			}
		}),
	)
	request := mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "req_insert", IdempotencyKey: "idem_insert",
		TableID: "notes", SchemaRevision: definition.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind: mutation.OperationInsert, Values: map[string]any{"title": "first"},
		}},
		Actor: mutation.Actor{Type: "user", ID: "local"},
	}
	receipt, err := kernel.Apply(ctx, request)
	if err != nil {
		t.Fatalf("Apply(insert): %#v", err)
	}
	if receipt.Status != mutation.StatusApplied || receipt.NewRevision == nil ||
		*receipt.NewRevision != "data_0001" || len(receipt.AffectedRows) != 1 ||
		receipt.AffectedRows[0].Revision != "row_0001" ||
		receipt.AffectedRows[0].RecordID != "rec000000000001" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	collection, _ := app.FindCollectionByNameOrId("notes")
	record, err := app.FindRecordById(collection, "rec000000000001")
	if err != nil || record.GetString("title") != "first" {
		t.Fatalf("stored record = %#v, err=%v", record, err)
	}
	assertRecordCount(t, app, "vibetable_audit_events", 1)
	assertRecordCount(t, app, "vibetable_idempotency_keys", 1)
	assertRecordCount(t, app, "vibetable_outbox", 1)
	audit, _ := app.FindFirstRecordByFilter("vibetable_audit_events", "request_id='req_insert'")
	if audit.GetFloat("sequence") != 1 || audit.GetString("operation") != "insert" ||
		audit.GetFloat("data_revision") != 1 ||
		audit.GetString("actor_type") != "user" ||
		audit.GetString("actor_id") != "local" ||
		!audit.GetDateTime("occurred_at").Time().Equal(now) {
		t.Fatalf("audit envelope = %#v", audit)
	}
	var after map[string]any
	auditAfter, _ := json.Marshal(audit.GetRaw("after_json"))
	if json.Unmarshal(auditAfter, &after) != nil || after["title"] != "first" {
		t.Fatalf("audit after image = %s", auditAfter)
	}
	outbox, _ := app.FindFirstRecordByFilter("vibetable_outbox", "event_id='evt_data_0001'")
	if outbox.GetFloat("attempts") != 0 || outbox.GetString("status") != "pending" {
		t.Fatalf("outbox state = %#v", outbox)
	}
	eventRaw, _ := json.Marshal(outbox.GetRaw("payload_json"))
	var event mutation.DataChangedEvent
	if err := mutation.DecodeStrict(eventRaw, &event); err != nil ||
		event.Sequence != 1 || event.OccurredAt != "2026-07-24T08:30:00Z" ||
		event.DataRevision != "data_0001" || event.TableID != "notes" {
		t.Fatalf("outbox event = %#v, decode=%v", event, err)
	}
	tableMeta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id='notes'")
	if got := int64(tableMeta.GetFloat("data_revision")); got != 1 {
		t.Fatalf("data revision = %d", got)
	}

	replayed, err := kernel.Apply(ctx, request)
	if err != nil {
		t.Fatalf("Apply(replay): %v", err)
	}
	if replayed.Status != mutation.StatusReplayed ||
		replayed.ChangeSetID == nil || *replayed.ChangeSetID != "chg_01HZX" {
		t.Fatalf("replayed receipt = %#v", replayed)
	}
	assertRecordCount(t, app, "vibetable_audit_events", 1)
	assertRecordCount(t, app, "vibetable_outbox", 1)
}

func TestMutationKernelGeneratedRecordIDPassesPocketBaseValidation(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("generated_ids", "generated_ids", []schema.FieldDefinition{
			field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
	).Apply(ctx, mutationRequest(
		"generated_ids", definition.SchemaRevision, "generated-id-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert,
			Values: map[string]any{
				"title": "created with a generated id",
			},
		},
	))
	if err != nil {
		t.Fatalf("generated-id insert: %#v", err)
	}
	if len(receipt.AffectedRows) != 1 {
		t.Fatalf("affected rows = %#v", receipt.AffectedRows)
	}
	recordID := receipt.AffectedRows[0].RecordID
	if len(recordID) != 15 {
		t.Fatalf("generated record id = %q", recordID)
	}
	for _, char := range recordID {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			t.Fatalf("generated record id = %q", recordID)
		}
	}
	collection, err := app.FindCollectionByNameOrId("generated_ids")
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil || record.GetString("title") != "created with a generated id" {
		t.Fatalf("stored record = %#v, err=%v", record, err)
	}
}

func TestMutationKernelBatchGuardsAndFailuresAreAtomic(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	title := field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText)
	title.Nullable = false
	title.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRequired, Value: true,
	}}
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition:       baseTable("guarded_notes", "guarded_notes", []schema.FieldDefinition{title}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var idMu sync.Mutex
	idSequence := 0
	kernel := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithIDGenerator(func(kind string) string {
			idMu.Lock()
			defer idMu.Unlock()
			idSequence++
			return fmt.Sprintf("%s_%06d", kind, idSequence)
		}),
	)
	firstID := "rec000000000011"
	insert := mutationRequest(
		"guarded_notes", definition.SchemaRevision, "insert-1",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &firstID, Values: map[string]any{"title": "one"}},
	)
	insertReceipt, err := kernel.Apply(ctx, insert)
	if err != nil {
		t.Fatal(err)
	}
	update := mutationRequest(
		"guarded_notes", definition.SchemaRevision, "update-1",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{"title": "two"}},
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{"title": "three"}},
	)
	update.ExpectedRevision = stringAddress("row_0001")
	update.ExpectedDigest = &insertReceipt.AffectedRows[0].Digest
	updated, err := kernel.Apply(ctx, update)
	if err != nil {
		t.Fatalf("guarded update: %#v", err)
	}
	if len(updated.AffectedRows) != 2 ||
		updated.AffectedRows[0].Revision != "row_0002" ||
		updated.AffectedRows[1].Revision != "row_0002" ||
		updated.AffectedRows[0].Digest != updated.AffectedRows[1].Digest {
		t.Fatalf("same-record batch revisions = %#v", updated.AffectedRows)
	}
	collection, _ := app.FindCollectionByNameOrId("guarded_notes")
	record, _ := app.FindRecordById(collection, firstID)
	if record.GetString("title") != "three" {
		t.Fatalf("final title = %q", record.GetString("title"))
	}

	stale := update
	stale.IdempotencyKey, stale.RequestID = "stale-key", "stale-request"
	_, err = kernel.Apply(ctx, stale)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "mutation.revision_conflict" {
		t.Fatalf("stale error = %#v", err)
	}
	staleDigest := update
	staleDigest.IdempotencyKey, staleDigest.RequestID = "stale-digest-key", "stale-digest-request"
	staleDigest.ExpectedRevision = nil
	_, err = kernel.Apply(ctx, staleDigest)
	if !errors.As(err, &productErr) || productErr.Code != "mutation.digest_conflict" {
		t.Fatalf("stale digest error = %#v", err)
	}

	for _, faultPoint := range []string{"after_record", "after_audit", "after_outbox", "before_commit"} {
		faultKernel := mutation.New(app, mutation.MetadataSchemaSource{},
			mutation.WithFaultInjector(func(point string) error {
				if point == faultPoint {
					return errors.New("injected")
				}
				return nil
			}),
		)
		key := "fault-" + faultPoint
		faultRequest := mutationRequest(
			"guarded_notes", definition.SchemaRevision, key,
			mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{"title": "rolled-back"}},
		)
		if _, err := faultKernel.Apply(ctx, faultRequest); err == nil {
			t.Fatalf("%s mutation unexpectedly succeeded", faultPoint)
		}
		record, _ = app.FindRecordById(collection, firstID)
		if record.GetString("title") != "three" {
			t.Fatalf("%s changed title to %q", faultPoint, record.GetString("title"))
		}
		if _, err := app.FindFirstRecordByFilter(
			"vibetable_idempotency_keys", "key={:key}", dbx.Params{"key": key},
		); err == nil {
			t.Fatalf("%s left an idempotency record", faultPoint)
		}
		assertRecordCount(t, app, "vibetable_audit_events", 3)
		assertRecordCount(t, app, "vibetable_outbox", 2)
		tableMeta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id='guarded_notes'")
		if int64(tableMeta.GetFloat("data_revision")) != 2 {
			t.Fatalf("%s changed data revision", faultPoint)
		}
	}

	invalid := mutationRequest(
		"guarded_notes", definition.SchemaRevision, "invalid-key",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{"title": ""}},
	)
	_, err = kernel.Apply(ctx, invalid)
	if !errors.As(err, &productErr) || productErr.Code != "mutation.validation.failed" {
		t.Fatalf("PB validation error = %#v", err)
	}
	record, _ = app.FindRecordById(collection, firstID)
	if record.GetString("title") != "three" {
		t.Fatalf("PB validation failure changed title to %q", record.GetString("title"))
	}

	secondID := "rec000000000012"
	if _, err := kernel.Apply(ctx, mutationRequest(
		"guarded_notes", definition.SchemaRevision, "insert-2",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &secondID, Values: map[string]any{"title": "second"}},
	)); err != nil {
		t.Fatal(err)
	}
	invalidGuard := mutationRequest(
		"guarded_notes", definition.SchemaRevision, "invalid-guard",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{"title": "x"}},
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &secondID, Values: map[string]any{"title": "y"}},
	)
	invalidGuard.ExpectedRevision = stringAddress("row_0002")
	_, err = kernel.Apply(ctx, invalidGuard)
	if !errors.As(err, &productErr) || productErr.Code != "mutation.guard.invalid" {
		t.Fatalf("multi-record guard = %#v", err)
	}
	thirdID := "rec000000000013"
	mixedGuard := mutationRequest(
		"guarded_notes", definition.SchemaRevision, "mixed-guard",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &firstID,
			Values: map[string]any{"title": "guarded"},
		},
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &thirdID,
			Values: map[string]any{"title": "unguarded insert"},
		},
	)
	mixedGuard.ExpectedRevision = stringAddress("row_0002")
	if _, err := kernel.Apply(ctx, mixedGuard); !errors.As(err, &productErr) ||
		productErr.Code != "mutation.guard.invalid" {
		t.Fatalf("mixed insert guard = %#v", err)
	}
	atomicFailure := mutationRequest(
		"guarded_notes", definition.SchemaRevision, "atomic-failure",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{"title": "must-rollback"}},
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &secondID, Values: map[string]any{"title": ""}},
	)
	if _, err := kernel.Apply(ctx, atomicFailure); err == nil {
		t.Fatal("invalid second operation did not fail")
	}
	first, _ := app.FindRecordById(collection, firstID)
	second, _ := app.FindRecordById(collection, secondID)
	if first.GetString("title") != "three" || second.GetString("title") != "second" {
		t.Fatalf("batch was partially applied: first=%q second=%q",
			first.GetString("title"), second.GetString("title"))
	}
	deleteReinsert := mutationRequest(
		"guarded_notes", definition.SchemaRevision, "delete-reinsert",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &firstID},
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &firstID,
			Values: map[string]any{"title": "replacement"},
		},
	)
	if _, err := kernel.Apply(ctx, deleteReinsert); !errors.As(err, &productErr) ||
		productErr.Code != "mutation.record.deleted_in_batch" {
		t.Fatalf("delete/reinsert batch = %#v", err)
	}
	first, _ = app.FindRecordById(collection, firstID)
	if first.GetString("title") != "three" {
		t.Fatalf("delete/reinsert rollback title = %q", first.GetString("title"))
	}
}

func TestMutationKernelArchiveRestoreAndDeleteUseAuditHistory(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	maxSelected := 1
	status := field("status_id", "status", schema.FieldKindScalar, schema.DataTypeSelect)
	status.Nullable = false
	status.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintEnum, Multiple: false, MinSelected: 1,
		MaxSelected: &maxSelected,
		Options: []schema.SelectOption{
			{Value: "active", DisplayName: "Active"},
			{Value: "paused", DisplayName: "Paused"},
			{Value: "archived", DisplayName: "Archived"},
		},
	}}
	definition := baseTable("archivable", "archivable", []schema.FieldDefinition{status})
	definition.ArchivePolicy = schema.ArchivePolicy{
		Mode: schema.ArchiveModeStatus, FieldID: stringAddress("status_id"),
		ArchivedValue: "archived",
	}
	applied, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: definition, ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var idMu sync.Mutex
	id := 0
	currentTime := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	kernel := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return currentTime }),
		mutation.WithIDGenerator(func(kind string) string {
			idMu.Lock()
			defer idMu.Unlock()
			id++
			return fmt.Sprintf("%s_%d", kind, id)
		}),
	)
	recordID := "rec000000000021"
	steps := []struct {
		key       string
		operation mutation.Operation
		revision  string
		restored  string
	}{
		{"insert", mutation.Operation{Kind: mutation.OperationInsert, RecordID: &recordID, Values: map[string]any{"status": "active"}}, "row_0001", ""},
		{"archive-1", mutation.Operation{Kind: mutation.OperationArchive, RecordID: &recordID}, "row_0002", ""},
		{"restore-1", mutation.Operation{Kind: mutation.OperationRestore, RecordID: &recordID}, "row_0003", "active"},
		{"pause", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &recordID, Values: map[string]any{"status": "paused"}}, "row_0004", ""},
		{"archive-2", mutation.Operation{Kind: mutation.OperationArchive, RecordID: &recordID}, "row_0005", ""},
		{"restore-2", mutation.Operation{Kind: mutation.OperationRestore, RecordID: &recordID}, "row_0006", "paused"},
		{"delete", mutation.Operation{Kind: mutation.OperationDelete, RecordID: &recordID}, "row_0007", ""},
	}
	for _, step := range steps {
		currentTime = currentTime.Add(time.Minute)
		receipt, err := kernel.Apply(ctx, mutationRequest(
			"archivable", applied.SchemaRevision, step.key, step.operation,
		))
		if err != nil {
			t.Fatalf("%s: %#v", step.key, err)
		}
		if receipt.AffectedRows[0].Revision != step.revision {
			t.Fatalf("%s revision = %q", step.key, receipt.AffectedRows[0].Revision)
		}
		if step.restored != "" {
			collection, _ := app.FindCollectionByNameOrId("archivable")
			record, _ := app.FindRecordById(collection, recordID)
			if record.GetString("status") != step.restored {
				t.Fatalf("restore status = %q", record.GetString("status"))
			}
		}
		if step.key == "insert" || step.key == "archive-1" {
			target := "archived"
			if step.key == "archive-1" {
				target = "active"
			}
			_, bypassErr := kernel.Apply(ctx, mutationRequest(
				"archivable", applied.SchemaRevision, "bypass-"+step.key,
				mutation.Operation{
					Kind: mutation.OperationUpdate, RecordID: &recordID,
					Values: map[string]any{"status": target},
				},
			))
			var productErr *mutation.ProductError
			if !errors.As(bypassErr, &productErr) ||
				productErr.Code != "mutation.archive.requires_operation" {
				t.Fatalf("%s archive bypass = %#v", step.key, bypassErr)
			}
		}
	}
	collection, _ := app.FindCollectionByNameOrId("archivable")
	if _, err := app.FindRecordById(collection, recordID); err == nil {
		t.Fatal("deleted record still exists")
	}
	assertRecordCount(t, app, "vibetable_audit_events", 7)
	tableMeta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id='archivable'")
	if got := int64(tableMeta.GetFloat("data_revision")); got != 7 {
		t.Fatalf("data revision = %d", got)
	}
}

func TestMutationKernelDeletedAtArchiveRoundTrip(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	deletedAt := field(
		"deleted_at_id", "deleted_at",
		schema.FieldKindScalar, schema.DataTypeDateTime,
	)
	definition := baseTable(
		"soft_delete_notes", "soft_delete_notes",
		[]schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			deletedAt,
		},
	)
	definition.ArchivePolicy = schema.ArchivePolicy{
		Mode: schema.ArchiveModeDeletedAt, FieldID: stringAddress("deleted_at_id"),
	}
	applied, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: definition, ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return now }),
	)
	recordID := "rec000000000022"
	if _, err := kernel.Apply(ctx, mutationRequest(
		"soft_delete_notes", applied.SchemaRevision, "deleted-at-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{"title": "soft"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		"soft_delete_notes", applied.SchemaRevision, "deleted-at-bypass",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{"deleted_at": now},
		},
	)); err == nil {
		t.Fatal("direct deleted_at archive was accepted")
	} else {
		var productErr *mutation.ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "mutation.archive.requires_operation" {
			t.Fatalf("direct deleted_at error = %#v", err)
		}
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		"soft_delete_notes", applied.SchemaRevision, "deleted-at-archive",
		mutation.Operation{Kind: mutation.OperationArchive, RecordID: &recordID},
	)); err != nil {
		t.Fatal(err)
	}
	collection, _ := app.FindCollectionByNameOrId("soft_delete_notes")
	record, _ := app.FindRecordById(collection, recordID)
	if !record.GetDateTime("deleted_at").Time().Equal(now) {
		t.Fatalf("deleted_at = %v, want %v", record.GetRaw("deleted_at"), now)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		"soft_delete_notes", applied.SchemaRevision, "deleted-at-restore",
		mutation.Operation{Kind: mutation.OperationRestore, RecordID: &recordID},
	)); err != nil {
		t.Fatal(err)
	}
	record, _ = app.FindRecordById(collection, recordID)
	if !record.GetDateTime("deleted_at").IsZero() {
		t.Fatalf("restored deleted_at = %v", record.GetRaw("deleted_at"))
	}
}

func TestMutationKernelConcurrentIdempotencyConflictExpiryAndRestart(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("idem_notes", "idem_notes", []schema.FieldDefinition{
			field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 8, 30, 0, 0, time.UTC)
	var generatorMu sync.Mutex
	generated := 0
	generator := func(kind string) string {
		generatorMu.Lock()
		defer generatorMu.Unlock()
		generated++
		return fmt.Sprintf("%s_%06d", kind, generated)
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return now }),
		mutation.WithIDGenerator(generator),
	)
	kernel2 := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return now }),
		mutation.WithIDGenerator(generator),
	)
	recordID := "rec000000000031"
	request := mutationRequest(
		"idem_notes", definition.SchemaRevision, "shared-key",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &recordID, Values: map[string]any{"title": "one"}},
	)
	type result struct {
		receipt mutation.Receipt
		err     error
	}
	results := make(chan result, 2)
	for index := range 2 {
		target := kernel
		if index == 1 {
			target = kernel2
		}
		go func() {
			receipt, err := target.Apply(ctx, request)
			results <- result{receipt, err}
		}()
	}
	statuses := map[mutation.ReceiptStatus]int{}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Apply: %#v", result.err)
		}
		statuses[result.receipt.Status]++
	}
	if statuses[mutation.StatusApplied] != 1 ||
		statuses[mutation.StatusReplayed]+statuses[mutation.StatusPending] != 1 {
		t.Fatalf("concurrent statuses = %#v", statuses)
	}
	assertRecordCount(t, app, "vibetable_audit_events", 1)

	restarted := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return now }),
	)
	replayed, err := restarted.Apply(ctx, request)
	if err != nil || replayed.Status != mutation.StatusReplayed {
		t.Fatalf("restart replay = %#v, %#v", replayed, err)
	}
	idempotency, _ := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys", "key='shared-key'",
	)
	idempotency.Set("status", "pending")
	if err := app.Save(idempotency); err != nil {
		t.Fatal(err)
	}
	persistedPending, err := restarted.Apply(ctx, request)
	if err != nil || persistedPending.Status != mutation.StatusPending {
		t.Fatalf("persisted pending = %#v, %#v", persistedPending, err)
	}
	idempotency.Set("status", "applied")
	if err := app.Save(idempotency); err != nil {
		t.Fatal(err)
	}
	idempotency.Set("status", "unknown")
	if err := app.Save(idempotency); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Apply(ctx, request); err == nil {
		t.Fatal("corrupt idempotency status was accepted")
	} else {
		var productErr *mutation.ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "mutation.storage.failed" {
			t.Fatalf("corrupt idempotency error = %#v", err)
		}
	}
	idempotency.Set("status", "applied")
	if err := app.Save(idempotency); err != nil {
		t.Fatal(err)
	}
	conflict := request
	conflict.Operations = []mutation.Operation{{
		Kind: mutation.OperationUpdate, RecordID: &recordID,
		Values: map[string]any{"title": "different"},
	}}
	_, err = restarted.Apply(ctx, conflict)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "mutation.idempotency_conflict" {
		t.Fatalf("idempotency conflict = %#v", err)
	}

	idempotency.Set("expires_at", now.Add(-time.Minute))
	if err := app.Save(idempotency); err != nil {
		t.Fatal(err)
	}
	expiredReuse := conflict
	expiredReuse.RequestID = "req-expired-reuse"
	reused, err := kernel.Apply(ctx, expiredReuse)
	if err != nil || reused.Status != mutation.StatusApplied {
		t.Fatalf("expired key reuse = %#v, %#v", reused, err)
	}
	collection, _ := app.FindCollectionByNameOrId("idem_notes")
	record, _ := app.FindRecordById(collection, recordID)
	if record.GetString("title") != "different" {
		t.Fatalf("expired key reuse title = %q", record.GetString("title"))
	}
}

func TestMutationKernelReturnsPendingForAnActiveIdenticalRequest(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("pending_notes", "pending_notes", []schema.FieldDefinition{
			field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var blockOnce sync.Once
	leader := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFaultInjector(func(point string) error {
			if point == "after_record" {
				blockOnce.Do(func() {
					close(entered)
					<-release
				})
			}
			return nil
		}),
	)
	follower := mutation.New(app, mutation.MetadataSchemaSource{})
	recordID := "rec000000000032"
	request := mutationRequest(
		"pending_notes", definition.SchemaRevision, "pending-shared",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{"title": "one"},
		},
	)
	type applyResult struct {
		receipt mutation.Receipt
		err     error
	}
	done := make(chan applyResult, 1)
	go func() {
		receipt, err := leader.Apply(ctx, request)
		done <- applyResult{receipt: receipt, err: err}
	}()
	<-entered

	pending, err := follower.Apply(ctx, request)
	if err != nil || pending.Status != mutation.StatusPending ||
		pending.ChangeSetID != nil ||
		len(pending.AffectedRows) != 0 {
		t.Fatalf("pending receipt = %#v, err=%v", pending, err)
	}
	conflict := request
	conflict.RequestID = "different-request"
	if _, err := follower.Apply(ctx, conflict); err == nil {
		t.Fatal("active idempotency conflict was accepted")
	} else {
		var productErr *mutation.ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "mutation.idempotency_conflict" {
			t.Fatalf("active conflict = %#v", err)
		}
	}
	secondID := "rec000000000033"
	queued := mutationRequest(
		"pending_notes", definition.SchemaRevision, "queued-key",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &secondID,
			Values: map[string]any{"title": "queued"},
		},
	)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := follower.Apply(canceled, queued); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled queued mutation = %#v", err)
	}
	close(release)
	applied := <-done
	if applied.err != nil || applied.receipt.Status != mutation.StatusApplied {
		t.Fatalf("leader result = %#v, err=%v", applied.receipt, applied.err)
	}
	replayed, err := follower.Apply(ctx, request)
	if err != nil || replayed.Status != mutation.StatusReplayed {
		t.Fatalf("post-commit replay = %#v, err=%v", replayed, err)
	}
	if applied, err := follower.Apply(ctx, queued); err != nil ||
		applied.Status != mutation.StatusApplied {
		t.Fatalf("retry after canceled queue = %#v, err=%v", applied, err)
	}
}

func TestMutationKernelFormulaUsesTransactionAndPublisherFailureDoesNotRollback(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	computed := field("fld_computed", "computed", schema.FieldKindFormula, schema.DataTypeFormula)
	computed.StorageType = schema.StorageText
	computed.ReadOnly = true
	computed.Formula = &schema.FormulaSpec{
		Language: "cel-v1", Source: "upper(title)",
		ResultType: schema.DataTypeShortText, Version: 1, Status: "ready",
	}
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("formula_notes", "formula_notes", []schema.FieldDefinition{
			field("fld_title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			computed,
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	formulas := &uppercaseFormula{}
	publisher := &committedOutboxPublisher{app: app}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(formulas),
		mutation.WithPublisher(publisher),
	)
	recordID := "rec000000000041"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		"formula_notes", definition.SchemaRevision, "formula-key",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{"title": "hello"},
		},
	))
	if err != nil {
		t.Fatalf("formula mutation: %#v", err)
	}
	if !formulas.called || !publisher.called || !publisher.committed {
		t.Fatalf("seams: formula=%v publisher=%v committed=%v", formulas.called, publisher.called, publisher.committed)
	}
	if got := receipt.ComputedFields[recordID]["computed"]; got != "HELLO" {
		t.Fatalf("computed receipt value = %#v", got)
	}
	collection, _ := app.FindCollectionByNameOrId("formula_notes")
	record, err := app.FindRecordById(collection, recordID)
	if err != nil || record.GetString("computed") != "HELLO" {
		t.Fatalf("stored computed field = %#v, %v", record, err)
	}
	assertRecordCount(t, app, "vibetable_outbox", 1)
	assertRecordCount(t, app, "vibetable_idempotency_keys", 1)
	outbox, _ := app.FindAllRecords("vibetable_outbox")
	if len(outbox) != 1 || outbox[0].GetString("status") != "pending" ||
		outbox[0].GetFloat("attempts") != 0 {
		t.Fatalf("publisher failure changed durable outbox = %#v", outbox)
	}
	if len(receipt.Warnings) != 1 ||
		receipt.Warnings[0].Code != "mutation.realtime.publish_pending" ||
		!receipt.Warnings[0].Retryable {
		t.Fatalf("publisher failure warning = %#v", receipt.Warnings)
	}
}

func TestMutationKernelDrainsCommittedOutboxAfterRequestCancellation(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	definition, err := schemaapi.New(app).ApplyChange(
		context.Background(),
		schemaapi.Change{
			Definition: baseTable(
				"cancelled_publish_notes",
				"cancelled_publish_notes",
				[]schema.FieldDefinition{
					field(
						"title",
						"title",
						schema.FieldKindScalar,
						schema.DataTypeShortText,
					),
				},
			),
			ExpectedRevision: 0,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	hub := realtime.New(app)
	subscription, err := hub.Subscribe(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithPublisher(hub),
		mutation.WithFaultInjector(func(point string) error {
			if point == "before_commit" {
				// before_commit runs after the final in-transaction ctx.Err()
				// check. Cancelling here proves the subsequent successful commit
				// cannot make live delivery depend on the request lifetime.
				cancelRequest()
			}
			return nil
		}),
	)
	recordID := "rec000000000091"
	receipt, err := kernel.Apply(
		requestCtx,
		mutationRequest(
			definition.TableID,
			definition.SchemaRevision,
			"cancelled-publish-key",
			mutation.Operation{
				Kind:     mutation.OperationInsert,
				RecordID: &recordID,
				Values:   map[string]any{"title": "committed"},
			},
		),
	)
	if err != nil || receipt.Status != mutation.StatusApplied {
		t.Fatalf("committed mutation = %#v, err=%v", receipt, err)
	}
	if requestCtx.Err() != context.Canceled {
		t.Fatalf("request context error = %v", requestCtx.Err())
	}
	if len(receipt.Warnings) != 0 {
		t.Fatalf("committed live drain warnings = %#v", receipt.Warnings)
	}

	select {
	case delivered := <-subscription.Events:
		if delivered.ID != receipt.EmittedEvents[0] ||
			delivered.Topic != "data.changed" {
			t.Fatalf("delivered event = %#v", delivered)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("committed outbox was not drained to the live subscriber")
	}
}

func TestMutationKernelRealFormulaCalculatorComputesAndPreservesErrors(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("formula_orders", "formula_orders", []schema.FieldDefinition{
			field("quantity_id", "quantity", schema.FieldKindScalar, schema.DataTypeFloat),
			formulaField("double_id", "double_quantity", schema.DataTypeFloat, "quantity * 2.0"),
			formulaField("ratio_id", "ratio", schema.DataTypeFloat, "1.0 / quantity"),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	calculator := formula.NewCalculator(formula.NewCompiler(formula.DefaultLimits()))
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(calculator),
	)
	recordID := "rec000000000091"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		"formula_orders", definition.SchemaRevision, "formula-real-ok",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{"quantity": 4.0},
		},
	))
	if err != nil {
		t.Fatalf("real formula mutation: %#v", err)
	}
	if receipt.ComputedFields[recordID]["double_quantity"] != 8.0 ||
		receipt.ComputedFields[recordID]["ratio"] != 0.25 {
		t.Fatalf("computed values = %#v", receipt.ComputedFields)
	}

	failedRecordID := "rec000000000092"
	_, err = kernel.Apply(ctx, mutationRequest(
		"formula_orders", definition.SchemaRevision, "formula-real-error",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &failedRecordID,
			Values: map[string]any{"quantity": 0.0},
		},
	))
	var formulaErr *formula.Error
	if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.divide_by_zero" {
		t.Fatalf("formula error = %#v, want formula.divide_by_zero", err)
	}
	collection, _ := app.FindCollectionByNameOrId("formula_orders")
	if _, findErr := app.FindRecordById(collection, failedRecordID); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("failed formula mutation persisted record: %v", findErr)
	}
}

func TestMutationKernelValidatesRelationsAndKeepsTableDataRevisionsIndependent(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)
	categories, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("categories", "categories", []schema.FieldDefinition{
			field("name", "name", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	relation := field("category_id", "category", schema.FieldKindRelation, schema.DataTypeRelation)
	relation.Relation = &schema.RelationSpec{
		TargetTableID: "categories", Cardinality: "one", DeletePolicy: "setNull",
	}
	relation.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRelation, TargetTableID: "categories",
		Cardinality: "one", DeletePolicy: "setNull",
	}}
	items, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("items", "items", []schema.FieldDefinition{
			field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			relation,
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	categoryID := "rec000000000051"
	if _, err := kernel.Apply(ctx, mutationRequest(
		"categories", categories.SchemaRevision, "category-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &categoryID,
			Values: map[string]any{"name": "General"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	itemID := "rec000000000052"
	invalid := mutationRequest(
		"items", items.SchemaRevision, "invalid-relation",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &itemID,
			Values: map[string]any{"title": "Bad", "category": "rec000000009999"},
		},
	)
	_, err = kernel.Apply(ctx, invalid)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.relation.target_not_found" ||
		productErr.Path == nil ||
		*productErr.Path != "operations[0].values.category" {
		t.Fatalf("invalid relation = %#v", err)
	}
	if _, err := app.FindFirstRecordByFilter("vibetable_idempotency_keys", "key='invalid-relation'"); err == nil {
		t.Fatal("invalid relation left idempotency state")
	}
	itemMeta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id='items'")
	if got := int64(itemMeta.GetFloat("data_revision")); got != 0 {
		t.Fatalf("failed item mutation data revision = %d", got)
	}
	itemMeta.Set("data_revision", -1)
	if err := app.Save(itemMeta); err != nil {
		t.Fatal(err)
	}
	corrupt := invalid
	corrupt.RequestID, corrupt.IdempotencyKey = "req-corrupt-counter", "corrupt-counter"
	corrupt.Operations[0].Values["category"] = categoryID
	_, err = kernel.Apply(ctx, corrupt)
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.metadata.invalid_data_revision" {
		t.Fatalf("corrupt data revision = %#v", err)
	}
	itemMeta.Set("data_revision", 0)
	if err := app.Save(itemMeta); err != nil {
		t.Fatal(err)
	}
	valid := invalid
	valid.RequestID, valid.IdempotencyKey = "req-valid-relation", "valid-relation"
	valid.Operations[0].Values["category"] = categoryID
	if _, err := kernel.Apply(ctx, valid); err != nil {
		t.Fatalf("valid relation: %#v", err)
	}
	categoryMeta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id='categories'")
	itemMeta, _ = app.FindFirstRecordByFilter("vibetable_tables", "table_id='items'")
	if int64(categoryMeta.GetFloat("data_revision")) != 1 ||
		int64(itemMeta.GetFloat("data_revision")) != 1 {
		t.Fatalf("independent data revisions: category=%v item=%v",
			categoryMeta.GetRaw("data_revision"), itemMeta.GetRaw("data_revision"))
	}
}

func TestMutationKernelOneThousandOperationsCommitOrFullyRollback(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("bulk_rows", "bulk_rows", []schema.FieldDefinition{
			field("value_id", "value", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	operations := make([]mutation.Operation, 1_000)
	for index := range operations {
		recordID := fmt.Sprintf("bulk%011d", index+1)
		operations[index] = mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{"value": fmt.Sprintf("row-%d", index+1)},
		}
	}
	request := mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "bulk-rollback",
		IdempotencyKey:  "bulk-rollback",
		TableID:         "bulk_rows",
		SchemaRevision:  definition.SchemaRevision,
		Operations:      operations,
		Actor:           mutation.Actor{Type: "user", ID: "scale-test"},
	}
	faultKernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFaultInjector(func(point string) error {
			if point == "before_commit" {
				return errors.New("injected disk failure")
			}
			return nil
		}),
	)
	if _, err := faultKernel.Apply(ctx, request); err == nil {
		t.Fatal("1k mutation with before_commit fault unexpectedly succeeded")
	}
	assertRecordCount(t, app, "bulk_rows", 0)
	assertRecordCount(t, app, "vibetable_audit_events", 0)
	assertRecordCount(t, app, "vibetable_idempotency_keys", 0)
	assertRecordCount(t, app, "vibetable_outbox", 0)

	request.RequestID = "bulk-commit"
	request.IdempotencyKey = "bulk-commit"
	receipt, err := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
	).Apply(ctx, request)
	if err != nil {
		t.Fatalf("1k mutation commit: %v", err)
	}
	if len(receipt.AffectedRows) != 1_000 {
		t.Fatalf("affected rows = %d", len(receipt.AffectedRows))
	}
	assertRecordCount(t, app, "bulk_rows", 1_000)
	assertRecordCount(t, app, "vibetable_audit_events", 1_000)
	assertRecordCount(t, app, "vibetable_idempotency_keys", 1)
	assertRecordCount(t, app, "vibetable_outbox", 1)
}

func TestMutationKernelDigestRoundTripsJSONAcrossFreshRecordReads(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("digest_notes", "digest_notes", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			field("payload_id", "payload", schema.FieldKindScalar, schema.DataTypeJSON),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	recordID := "rec000000000061"
	inserted, err := kernel.Apply(ctx, mutationRequest(
		"digest_notes", definition.SchemaRevision, "digest-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{
				"title": "one",
				"payload": map[string]any{
					"nested": []any{json.Number("1"), "two"},
				},
			},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	update := mutationRequest(
		"digest_notes", definition.SchemaRevision, "digest-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{"title": "two"},
		},
	)
	update.ExpectedDigest = &inserted.AffectedRows[0].Digest
	if _, err := mutation.New(app, mutation.MetadataSchemaSource{}).Apply(
		ctx, update,
	); err != nil {
		t.Fatalf("fresh-read digest guard rejected stored JSON: %#v", err)
	}
}

func assertRecordCount(t *testing.T, app core.App, collection string, want int) {
	t.Helper()
	records, err := app.FindAllRecords(collection)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != want {
		t.Fatalf("%s count = %d, want %d", collection, len(records), want)
	}
}

func mutationRequest(
	tableID string,
	schemaRevision string,
	idempotencyKey string,
	operations ...mutation.Operation,
) mutation.Request {
	return mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "req-" + idempotencyKey, IdempotencyKey: idempotencyKey,
		TableID: tableID, SchemaRevision: schemaRevision,
		Operations: operations,
		Actor:      mutation.Actor{Type: "user", ID: "local"},
	}
}

func stringAddress(value string) *string {
	return &value
}
