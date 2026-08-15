package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type uppercaseFormula struct {
	called bool
}

func (calculator *uppercaseFormula) Calculate(
	_ context.Context,
	app core.App,
	definition schemaexecution.Table,
	record *core.Record,
) (map[string]any, error) {
	if _, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": definition.Snapshot.TableID}); err != nil {
		return nil, err
	}
	var title, computed string
	for _, field := range definition.Snapshot.Fields {
		switch field.DisplayName {
		case "title":
			title = field.Identity.PhysicalName
		case "computed":
			computed = field.Identity.PhysicalName
		}
	}
	calculator.called = true
	return map[string]any{computed: strings.ToUpper(record.GetString(title))}, nil
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
	definition := createV2IntegrationTable(t, ctx, app, "notes", "mutation_notes_table")
	title := createV2IntegrationField(
		t, ctx, app, definition.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_notes_title",
	)
	definition.SchemaRevision = title.SchemaRevision
	if title.Definition == nil {
		t.Fatal("V2 title fixture omitted field definition")
	}
	idempotencyBefore, err := app.FindAllRecords("vibetable_idempotency_keys")
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
		TableID: definition.TableID, SchemaRevision: definition.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind: mutation.OperationInsert, Values: map[string]any{title.Definition.Identity.PhysicalName: "first"},
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
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	record, err := app.FindRecordById(collection, "rec000000000001")
	if err != nil || record.GetString(title.Definition.Identity.PhysicalName) != "first" {
		t.Fatalf("stored record = %#v, err=%v", record, err)
	}
	assertRecordCount(t, app, "vibetable_audit_events", 1)
	assertRecordCount(t, app, "vibetable_idempotency_keys", len(idempotencyBefore)+1)
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
	if json.Unmarshal(auditAfter, &after) != nil || after[title.Definition.Identity.PhysicalName] != "first" {
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
		event.SchemaRevision != definition.SchemaRevision ||
		event.DataRevision != *receipt.NewRevision || event.TableID != definition.TableID {
		t.Fatalf("outbox event = %#v, decode=%v", event, err)
	}
	tableMeta, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": definition.TableID})
	if err != nil {
		t.Fatal(err)
	}
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

func TestMutationKernelReturnsAuthoritativeAutoDatesAndRejectsForgery(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(t, ctx, app, "autodate receipts", "mutation_autodate_table")
	title := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_autodate_title")
	createdDraft := fieldDraftForIntegration(t, v2.LogicalAutoDate, "created_at")
	createdDraft.AutoDate = &v2.AutoDateSpec{Role: "createdAt"}
	created := createV2IntegrationField(t, ctx, app, definition.TableID, createdDraft, "mutation_autodate_created")
	updatedDraft := fieldDraftForIntegration(t, v2.LogicalAutoDate, "updated_at")
	updatedDraft.AutoDate = &v2.AutoDateSpec{Role: "updatedAt"}
	updatedField := createV2IntegrationField(t, ctx, app, definition.TableID, updatedDraft, "mutation_autodate_updated")
	definition.SchemaRevision = updatedField.SchemaRevision
	if title.Definition == nil || created.Definition == nil || updatedField.Definition == nil {
		t.Fatal("V2 autoDate fixture omitted field definition")
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	recordID := "autodaterecord1"
	insertRequest := mutationRequest(
		definition.TableID, definition.SchemaRevision, "autodate-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "first"},
		},
	)
	inserted, err := kernel.Apply(ctx, insertRequest)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	serverValues := inserted.ComputedFields[recordID]
	createdRaw, createdOK := serverValues[created.Definition.Identity.PhysicalName].(string)
	updatedRaw, updatedOK := serverValues[updatedField.Definition.Identity.PhysicalName].(string)
	createdAt, createdErr := time.Parse(time.RFC3339Nano, createdRaw)
	updatedAt, updatedErr := time.Parse(time.RFC3339Nano, updatedRaw)
	if !createdOK || !updatedOK || createdErr != nil || updatedErr != nil ||
		createdAt.IsZero() || updatedAt.IsZero() ||
		createdAt.Location() != time.UTC || updatedAt.Location() != time.UTC {
		t.Fatalf("authoritative autoDate values = %#v", serverValues)
	}

	replayed, err := kernel.Apply(ctx, insertRequest)
	if err != nil || replayed.Status != mutation.StatusReplayed ||
		!reflect.DeepEqual(replayed.ComputedFields, inserted.ComputedFields) {
		t.Fatalf("idempotent replay = %#v, err=%v", replayed, err)
	}

	var updated mutation.Receipt
	lastTitle := "first"
	deadline := time.Now().Add(2 * time.Second)
	for attempt := 0; ; attempt++ {
		nextTitle := fmt.Sprintf("updated-%d", attempt)
		updated, err = kernel.Apply(ctx, mutationRequest(
			definition.TableID,
			definition.SchemaRevision,
			fmt.Sprintf("autodate-update-%d", attempt),
			mutation.Operation{
				Kind: mutation.OperationUpdate, RecordID: &recordID,
				Values: map[string]any{title.Definition.Identity.PhysicalName: nextTitle},
			},
		))
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		lastTitle = nextTitle
		candidate, parseErr := time.Parse(
			time.RFC3339Nano,
			updated.ComputedFields[recordID][updatedField.Definition.Identity.PhysicalName].(string),
		)
		if parseErr == nil && candidate.After(updatedAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("updatedAt did not advance: %#v", updated.ComputedFields)
		}
	}
	if updated.ComputedFields[recordID][created.Definition.Identity.PhysicalName] != createdRaw {
		t.Fatalf("createdAt changed: %#v", updated.ComputedFields[recordID])
	}
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(
		app, querySource)

	offsetValue := createdAt.In(time.FixedZone("UTC+8", 8*60*60)).
		Format(time.RFC3339Nano)
	page, err := port.QueryPage(ctx, definition.TableID, query.TableQuery{
		Filters: []query.FilterExpression{{
			Field: created.Definition.Identity.PhysicalName, Operator: query.OperatorEqual,
			Value: offsetValue, Logic: query.LogicAnd,
		}},
		Limit: 10,
	})
	if err != nil || len(page.Rows) != 1 ||
		page.Rows[0][created.Definition.Identity.PhysicalName] != createdRaw {
		t.Fatalf("autoDate RFC equality/read = %#v, err=%v", page, err)
	}
	lastUpdatedRaw := updated.ComputedFields[recordID][updatedField.Definition.Identity.PhysicalName].(string)
	lastUpdatedAt, _ := time.Parse(time.RFC3339Nano, lastUpdatedRaw)
	deadline = time.Now().Add(2 * time.Second)
	for attempt := 0; ; attempt++ {
		sameValue, sameErr := kernel.Apply(ctx, mutationRequest(
			definition.TableID,
			definition.SchemaRevision,
			fmt.Sprintf("autodate-same-value-%d", attempt),
			mutation.Operation{
				Kind: mutation.OperationUpdate, RecordID: &recordID,
				Values: map[string]any{title.Definition.Identity.PhysicalName: lastTitle},
			},
		))
		if sameErr != nil {
			t.Fatalf("same-value save: %v", sameErr)
		}
		candidate, parseErr := time.Parse(
			time.RFC3339Nano,
			sameValue.ComputedFields[recordID][updatedField.Definition.Identity.PhysicalName].(string),
		)
		if parseErr == nil && candidate.After(lastUpdatedAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("same-value Save did not advance updatedAt: %#v", sameValue)
		}
	}

	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	beforeFailed, err := app.FindRecordById(collection, recordID)
	if err != nil {
		t.Fatal(err)
	}
	beforeCreated := beforeFailed.GetString(created.Definition.Identity.PhysicalName)
	beforeUpdated := beforeFailed.GetString(updatedField.Definition.Identity.PhysicalName)
	forged := mutationRequest(
		definition.TableID, definition.SchemaRevision, "autodate-forged",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{
				title.Definition.Identity.PhysicalName:        "forged",
				updatedField.Definition.Identity.PhysicalName: "2000-01-01T00:00:00Z",
			},
		},
	)
	_, err = kernel.Apply(ctx, forged)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.field.read_only" {
		t.Fatalf("forged autoDate = %#v", err)
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil || record.GetString(title.Definition.Identity.PhysicalName) == "forged" ||
		record.GetString(created.Definition.Identity.PhysicalName) != beforeCreated ||
		record.GetString(updatedField.Definition.Identity.PhysicalName) != beforeUpdated {
		t.Fatalf("forged mutation was partially applied: %#v, err=%v", record, err)
	}
	pbUpdated := collection.Fields.GetByName(updatedField.Definition.Identity.PhysicalName).(*core.AutodateField)
	pbUpdated.OnCreate = true
	pbUpdated.OnUpdate = false
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "autodate-corrupt",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "must not write"},
		},
	))
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.schema.autodate_invalid" {
		t.Fatalf("corrupt autoDate schema mutation = %#v", err)
	}
}

func TestMutationKernelGeneratedRecordIDPassesPocketBaseValidation(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(
		t, ctx, app, "generated ids", "mutation_generated_ids_table",
	)
	title := createV2IntegrationField(
		t, ctx, app, definition.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_generated_ids_title",
	)
	definition.SchemaRevision = title.SchemaRevision
	if title.Definition == nil {
		t.Fatal("V2 title fixture omitted field definition")
	}
	receipt, err := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
	).Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "generated-id-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert,
			Values: map[string]any{
				title.Definition.Identity.PhysicalName: "created with a generated id",
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
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil || record.GetString(title.Definition.Identity.PhysicalName) != "created with a generated id" {
		t.Fatalf("stored record = %#v, err=%v", record, err)
	}
}

func TestMutationKernelBatchGuardsAndFailuresAreAtomic(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(
		t, ctx, app, "guarded notes", "mutation_guarded_notes_table",
	)
	titleDraft := fieldDraftForIntegration(t, v2.LogicalText, "title")
	titleDraft.Value.Required = true
	title := createV2IntegrationField(
		t, ctx, app, definition.TableID, titleDraft, "mutation_guarded_notes_title",
	)
	definition.SchemaRevision = title.SchemaRevision
	if title.Definition == nil {
		t.Fatal("V2 title fixture omitted field definition")
	}
	titleName := title.Definition.Identity.PhysicalName
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
		definition.TableID, definition.SchemaRevision, "insert-1",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &firstID, Values: map[string]any{titleName: "one"}},
	)
	insertReceipt, err := kernel.Apply(ctx, insert)
	if err != nil {
		t.Fatal(err)
	}
	update := mutationRequest(
		definition.TableID, definition.SchemaRevision, "update-1",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{titleName: "two"}},
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{titleName: "three"}},
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
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	record, _ := app.FindRecordById(collection, firstID)
	if record.GetString(titleName) != "three" {
		t.Fatalf("final title = %q", record.GetString(titleName))
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
			definition.TableID, definition.SchemaRevision, key,
			mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{titleName: "rolled-back"}},
		)
		if _, err := faultKernel.Apply(ctx, faultRequest); err == nil {
			t.Fatalf("%s mutation unexpectedly succeeded", faultPoint)
		}
		record, _ = app.FindRecordById(collection, firstID)
		if record.GetString(titleName) != "three" {
			t.Fatalf("%s changed title to %q", faultPoint, record.GetString(titleName))
		}
		if _, err := app.FindFirstRecordByFilter(
			"vibetable_idempotency_keys", "key={:key}", dbx.Params{"key": key},
		); err == nil {
			t.Fatalf("%s left an idempotency record", faultPoint)
		}
		assertRecordCount(t, app, "vibetable_audit_events", 3)
		assertRecordCount(t, app, "vibetable_outbox", 2)
		tableMeta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": definition.TableID})
		if int64(tableMeta.GetFloat("data_revision")) != 2 {
			t.Fatalf("%s changed data revision", faultPoint)
		}
	}

	invalid := mutationRequest(
		definition.TableID, definition.SchemaRevision, "invalid-key",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{titleName: nil}},
	)
	_, err = kernel.Apply(ctx, invalid)
	if !errors.As(err, &productErr) || productErr.Code != "mutation.field.invalid_value" ||
		productErr.Path == nil || *productErr.Path != "operations[0].values."+titleName ||
		productErr.Details["fieldId"] != title.FieldID {
		t.Fatalf("PB validation error = %#v", err)
	}
	record, _ = app.FindRecordById(collection, firstID)
	if record.GetString(titleName) != "three" {
		t.Fatalf("PB validation failure changed title to %q", record.GetString(titleName))
	}

	secondID := "rec000000000012"
	if _, err := kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "insert-2",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &secondID, Values: map[string]any{titleName: "second"}},
	)); err != nil {
		t.Fatal(err)
	}
	invalidGuard := mutationRequest(
		definition.TableID, definition.SchemaRevision, "invalid-guard",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{titleName: "x"}},
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &secondID, Values: map[string]any{titleName: "y"}},
	)
	invalidGuard.ExpectedRevision = stringAddress("row_0002")
	_, err = kernel.Apply(ctx, invalidGuard)
	if !errors.As(err, &productErr) || productErr.Code != "mutation.guard.invalid" {
		t.Fatalf("multi-record guard = %#v", err)
	}
	thirdID := "rec000000000013"
	mixedGuard := mutationRequest(
		definition.TableID, definition.SchemaRevision, "mixed-guard",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &firstID,
			Values: map[string]any{titleName: "guarded"},
		},
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &thirdID,
			Values: map[string]any{titleName: "unguarded insert"},
		},
	)
	mixedGuard.ExpectedRevision = stringAddress("row_0002")
	if _, err := kernel.Apply(ctx, mixedGuard); !errors.As(err, &productErr) ||
		productErr.Code != "mutation.guard.invalid" {
		t.Fatalf("mixed insert guard = %#v", err)
	}
	atomicFailure := mutationRequest(
		definition.TableID, definition.SchemaRevision, "atomic-failure",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &firstID, Values: map[string]any{titleName: "must-rollback"}},
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &secondID, Values: map[string]any{titleName: nil}},
	)
	if _, err := kernel.Apply(ctx, atomicFailure); err == nil {
		t.Fatal("invalid second operation did not fail")
	}
	first, _ := app.FindRecordById(collection, firstID)
	second, _ := app.FindRecordById(collection, secondID)
	if first.GetString(titleName) != "three" || second.GetString(titleName) != "second" {
		t.Fatalf("batch was partially applied: first=%q second=%q",
			first.GetString(titleName), second.GetString(titleName))
	}
	deleteReinsert := mutationRequest(
		definition.TableID, definition.SchemaRevision, "delete-reinsert",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &firstID},
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &firstID,
			Values: map[string]any{titleName: "replacement"},
		},
	)
	if _, err := kernel.Apply(ctx, deleteReinsert); !errors.As(err, &productErr) ||
		productErr.Code != "mutation.record.deleted_in_batch" {
		t.Fatalf("delete/reinsert batch = %#v", err)
	}
	first, _ = app.FindRecordById(collection, firstID)
	if first.GetString(titleName) != "three" {
		t.Fatalf("delete/reinsert rollback title = %q", first.GetString(titleName))
	}
}

func TestMutationKernelArchiveRestoreAndDeleteUseAuditHistory(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(t, ctx, app, "archivable", "mutation_archivable_table")
	statusDraft := fieldDraftForIntegration(t, v2.LogicalSelect, "status")
	statusDraft.Value.Required = true
	statusDraft.Select = &v2.SelectSpec{Options: []v2.SelectOption{
		{Label: "Active", Color: "#16a34a", Order: 10, State: v2.OptionActive},
		{Label: "Paused", Color: "#f59e0b", Order: 20, State: v2.OptionActive},
		{Label: "Archived", Color: "#64748b", Order: 30, State: v2.OptionActive},
	}}
	status := createV2IntegrationField(t, ctx, app, definition.TableID, statusDraft, "mutation_archivable_status")
	createdDraft := fieldDraftForIntegration(t, v2.LogicalAutoDate, "created_at")
	createdDraft.AutoDate = &v2.AutoDateSpec{Role: "createdAt"}
	created := createV2IntegrationField(t, ctx, app, definition.TableID, createdDraft, "mutation_archivable_created")
	updatedDraft := fieldDraftForIntegration(t, v2.LogicalAutoDate, "updated_at")
	updatedDraft.AutoDate = &v2.AutoDateSpec{Role: "updatedAt"}
	updated := createV2IntegrationField(t, ctx, app, definition.TableID, updatedDraft, "mutation_archivable_updated")
	if status.Definition == nil || status.Definition.Select == nil || len(status.Definition.Select.Options) != 3 || created.Definition == nil || updated.Definition == nil {
		t.Fatal("V2 archive fixture omitted field definition")
	}
	active, paused, archived := status.Definition.Select.Options[0].OptionID, status.Definition.Select.Options[1].OptionID, status.Definition.Select.Options[2].OptionID
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	statusID := status.FieldID
	applied, err := lifecycle.Configure(ctx, v2.TableSettingsIntent{
		TableID: definition.TableID, ExpectedSchemaRev: updated.SchemaRevision,
		ArchivePolicy: v2.ArchivePolicy{Mode: "status", FieldID: &statusID, ArchivedValue: archived},
		OperationID:   "mutation_archivable_archive_policy", Actor: v2.Actor{ID: "local-user", Kind: "user"},
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
		{"insert", mutation.Operation{Kind: mutation.OperationInsert, RecordID: &recordID, Values: map[string]any{status.Definition.Identity.PhysicalName: active}}, "row_0001", ""},
		{"archive-1", mutation.Operation{Kind: mutation.OperationArchive, RecordID: &recordID}, "row_0002", ""},
		{"restore-1", mutation.Operation{Kind: mutation.OperationRestore, RecordID: &recordID}, "row_0003", active},
		{"pause", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &recordID, Values: map[string]any{status.Definition.Identity.PhysicalName: paused}}, "row_0004", ""},
		{"archive-2", mutation.Operation{Kind: mutation.OperationArchive, RecordID: &recordID}, "row_0005", ""},
		{"restore-2", mutation.Operation{Kind: mutation.OperationRestore, RecordID: &recordID}, "row_0006", paused},
		{"delete", mutation.Operation{Kind: mutation.OperationDelete, RecordID: &recordID}, "row_0007", ""},
	}
	var createdValue any
	var previousUpdated time.Time
	for _, step := range steps {
		if !previousUpdated.IsZero() && step.key != "delete" {
			for !time.Now().UTC().After(previousUpdated.Add(time.Millisecond)) {
				time.Sleep(time.Millisecond)
			}
		}
		currentTime = currentTime.Add(time.Minute)
		receipt, err := kernel.Apply(ctx, mutationRequest(
			definition.TableID, applied.SchemaRevision, step.key, step.operation,
		))
		if err != nil {
			t.Fatalf("%s: %#v", step.key, err)
		}
		if receipt.AffectedRows[0].Revision != step.revision {
			t.Fatalf("%s revision = %q", step.key, receipt.AffectedRows[0].Revision)
		}
		if step.key != "delete" {
			values := receipt.ComputedFields[recordID]
			if createdValue == nil {
				createdValue = values[created.Definition.Identity.PhysicalName]
			}
			nextUpdated, parseErr := time.Parse(
				time.RFC3339Nano,
				values[updated.Definition.Identity.PhysicalName].(string),
			)
			if values[created.Definition.Identity.PhysicalName] != createdValue || parseErr != nil ||
				(!previousUpdated.IsZero() && !nextUpdated.After(previousUpdated)) {
				t.Fatalf("%s autoDate receipt = %#v", step.key, values)
			}
			previousUpdated = nextUpdated
		}
		if step.restored != "" {
			collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
			record, _ := app.FindRecordById(collection, recordID)
			if record.GetString(status.Definition.Identity.PhysicalName) != step.restored {
				t.Fatalf("restore status = %q", record.GetString(status.Definition.Identity.PhysicalName))
			}
		}
		if step.key == "insert" || step.key == "archive-1" {
			target := archived
			if step.key == "archive-1" {
				target = active
			}
			_, bypassErr := kernel.Apply(ctx, mutationRequest(
				definition.TableID, applied.SchemaRevision, "bypass-"+step.key,
				mutation.Operation{
					Kind: mutation.OperationUpdate, RecordID: &recordID,
					Values: map[string]any{status.Definition.Identity.PhysicalName: target},
				},
			))
			var productErr *mutation.ProductError
			if !errors.As(bypassErr, &productErr) ||
				productErr.Code != "mutation.archive.requires_operation" {
				t.Fatalf("%s archive bypass = %#v", step.key, bypassErr)
			}
		}
	}
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	if _, err := app.FindRecordById(collection, recordID); err == nil {
		t.Fatal("deleted record still exists")
	}
	assertRecordCount(t, app, "vibetable_audit_events", 7)
	tableMeta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": definition.TableID})
	if got := int64(tableMeta.GetFloat("data_revision")); got != 7 {
		t.Fatalf("data revision = %d", got)
	}
}

func TestMutationKernelDeletedAtArchiveRoundTrip(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(t, ctx, app, "soft delete notes", "mutation_soft_delete_table")
	title := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_soft_delete_title")
	deletedAt := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalDateTime, "deleted_at"), "mutation_soft_delete_deleted_at")
	if title.Definition == nil || deletedAt.Definition == nil {
		t.Fatal("V2 deletedAt fixture omitted field definition")
	}
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	deletedAtID := deletedAt.FieldID
	applied, err := lifecycle.Configure(ctx, v2.TableSettingsIntent{
		TableID: definition.TableID, ExpectedSchemaRev: deletedAt.SchemaRevision,
		ArchivePolicy: v2.ArchivePolicy{Mode: "deletedAt", FieldID: &deletedAtID},
		OperationID:   "mutation_soft_delete_archive_policy", Actor: v2.Actor{ID: "local-user", Kind: "user"},
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
		definition.TableID, applied.SchemaRevision, "deleted-at-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "soft"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		definition.TableID, applied.SchemaRevision, "deleted-at-bypass",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{deletedAt.Definition.Identity.PhysicalName: now},
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
		definition.TableID, applied.SchemaRevision, "deleted-at-archive",
		mutation.Operation{Kind: mutation.OperationArchive, RecordID: &recordID},
	)); err != nil {
		t.Fatal(err)
	}
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	record, _ := app.FindRecordById(collection, recordID)
	if !record.GetDateTime(deletedAt.Definition.Identity.PhysicalName).Time().Equal(now) {
		t.Fatalf("deleted_at = %v, want %v", record.GetRaw(deletedAt.Definition.Identity.PhysicalName), now)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		definition.TableID, applied.SchemaRevision, "deleted-at-restore",
		mutation.Operation{Kind: mutation.OperationRestore, RecordID: &recordID},
	)); err != nil {
		t.Fatal(err)
	}
	record, _ = app.FindRecordById(collection, recordID)
	if !record.GetDateTime(deletedAt.Definition.Identity.PhysicalName).IsZero() {
		t.Fatalf("restored deleted_at = %v", record.GetRaw(deletedAt.Definition.Identity.PhysicalName))
	}
}

func TestMutationKernelConcurrentIdempotencyConflictExpiryAndRestart(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(t, ctx, app, "idem notes", "mutation_idem_notes_table")
	title := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_idem_notes_title")
	definition.SchemaRevision = title.SchemaRevision
	if title.Definition == nil {
		t.Fatal("V2 title fixture omitted field definition")
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
		definition.TableID, definition.SchemaRevision, "shared-key",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &recordID, Values: map[string]any{title.Definition.Identity.PhysicalName: "one"}},
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
		Values: map[string]any{title.Definition.Identity.PhysicalName: "different"},
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
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	record, _ := app.FindRecordById(collection, recordID)
	if record.GetString(title.Definition.Identity.PhysicalName) != "different" {
		t.Fatalf("expired key reuse title = %q", record.GetString(title.Definition.Identity.PhysicalName))
	}
}

func TestMutationKernelReturnsPendingForAnActiveIdenticalRequest(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(t, ctx, app, "pending notes", "mutation_pending_notes_table")
	title := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_pending_notes_title")
	definition.SchemaRevision = title.SchemaRevision
	if title.Definition == nil {
		t.Fatal("V2 title fixture omitted field definition")
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
		definition.TableID, definition.SchemaRevision, "pending-shared",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "one"},
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
		definition.TableID, definition.SchemaRevision, "queued-key",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &secondID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "queued"},
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
	definition := createV2IntegrationTable(t, ctx, app, "formula notes", "mutation_formula_notes_table")
	title := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_formula_notes_title")
	computedDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "computed")
	computedDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "upper({title})"}
	computed := createV2IntegrationFormula(t, ctx, app, definition.TableID, computedDraft, "mutation_formula_notes_computed")
	createdDraft := fieldDraftForIntegration(t, v2.LogicalAutoDate, "created_at")
	createdDraft.AutoDate = &v2.AutoDateSpec{Role: "createdAt"}
	created := createV2IntegrationField(t, ctx, app, definition.TableID, createdDraft, "mutation_formula_notes_created")
	updatedDraft := fieldDraftForIntegration(t, v2.LogicalAutoDate, "updated_at")
	updatedDraft.AutoDate = &v2.AutoDateSpec{Role: "updatedAt"}
	updated := createV2IntegrationField(t, ctx, app, definition.TableID, updatedDraft, "mutation_formula_notes_updated")
	definition.SchemaRevision = updated.SchemaRevision
	if title.Definition == nil || computed.Definition == nil || created.Definition == nil || updated.Definition == nil {
		t.Fatal("V2 formula fixture omitted field definition")
	}
	idempotencyBefore, err := app.FindAllRecords("vibetable_idempotency_keys")
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
		definition.TableID, definition.SchemaRevision, "formula-key",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "hello"},
		},
	))
	if err != nil {
		t.Fatalf("formula mutation: %#v", err)
	}
	if !formulas.called || !publisher.called || !publisher.committed {
		t.Fatalf("seams: formula=%v publisher=%v committed=%v", formulas.called, publisher.called, publisher.committed)
	}
	if got := receipt.ComputedFields[recordID][computed.Definition.Identity.PhysicalName]; got != "HELLO" {
		t.Fatalf("computed receipt value = %#v", got)
	}
	firstCreated := receipt.ComputedFields[recordID][created.Definition.Identity.PhysicalName]
	firstUpdated, parseErr := time.Parse(
		time.RFC3339Nano,
		receipt.ComputedFields[recordID][updated.Definition.Identity.PhysicalName].(string),
	)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	for !time.Now().UTC().After(firstUpdated.Add(time.Millisecond)) {
		time.Sleep(time.Millisecond)
	}
	updatedReceipt, err := kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "formula-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "world"},
		},
	))
	if err != nil {
		t.Fatalf("formula update: %#v", err)
	}
	updatedAt, parseErr := time.Parse(
		time.RFC3339Nano,
		updatedReceipt.ComputedFields[recordID][updated.Definition.Identity.PhysicalName].(string),
	)
	if parseErr != nil ||
		updatedReceipt.ComputedFields[recordID][computed.Definition.Identity.PhysicalName] != "WORLD" ||
		updatedReceipt.ComputedFields[recordID][created.Definition.Identity.PhysicalName] != firstCreated ||
		!updatedAt.After(firstUpdated) {
		t.Fatalf("formula Save autoDate receipt = %#v", updatedReceipt)
	}
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	record, err := app.FindRecordById(collection, recordID)
	if err != nil || relatedcomputation.ProjectStored(record.GetRaw(computed.Definition.Identity.PhysicalName)) != "WORLD" {
		t.Fatalf("stored computed field = %#v, %v", record, err)
	}
	assertRecordCount(t, app, "vibetable_outbox", 2)
	assertRecordCount(t, app, "vibetable_idempotency_keys", len(idempotencyBefore)+2)
	outbox, _ := app.FindAllRecords("vibetable_outbox")
	if len(outbox) != 2 || outbox[0].GetString("status") != "pending" ||
		outbox[0].GetFloat("attempts") != 0 ||
		outbox[1].GetString("status") != "pending" ||
		outbox[1].GetFloat("attempts") != 0 {
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
	ctx := context.Background()
	definition := createV2IntegrationTable(
		t, ctx, app, "cancelled publish notes", "mutation_cancelled_publish_table",
	)
	title := createV2IntegrationField(
		t, ctx, app, definition.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_cancelled_publish_title",
	)
	definition.SchemaRevision = title.SchemaRevision
	if title.Definition == nil {
		t.Fatal("V2 title fixture omitted field definition")
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
				Values:   map[string]any{title.Definition.Identity.PhysicalName: "committed"},
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
	definition := createV2IntegrationTable(t, ctx, app, "formula orders", "mutation_formula_orders_table")
	quantity := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "quantity"), "mutation_formula_orders_quantity")
	doubleDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "double_quantity")
	doubleDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "{quantity} * 2.0"}
	double := createV2IntegrationFormula(t, ctx, app, definition.TableID, doubleDraft, "mutation_formula_orders_double")
	ratioDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "ratio")
	ratioDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "1.0 / {quantity}"}
	ratio := createV2IntegrationFormula(t, ctx, app, definition.TableID, ratioDraft, "mutation_formula_orders_ratio")
	definition.SchemaRevision = ratio.SchemaRevision
	if quantity.Definition == nil || double.Definition == nil || ratio.Definition == nil {
		t.Fatal("V2 formula fixture omitted field definition")
	}
	calculator := formula.NewCalculator(formula.NewCompiler(formula.DefaultLimits()))
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(calculator),
	)
	recordID := "rec000000000091"
	receipt, err := kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "formula-real-ok",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{quantity.Definition.Identity.PhysicalName: 4.0},
		},
	))
	if err != nil {
		t.Fatalf("real formula mutation: %#v", err)
	}
	if receipt.ComputedFields[recordID][double.Definition.Identity.PhysicalName] != 8.0 ||
		receipt.ComputedFields[recordID][ratio.Definition.Identity.PhysicalName] != 0.25 {
		t.Fatalf("computed values = %#v", receipt.ComputedFields)
	}

	failedRecordID := "rec000000000092"
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "formula-real-error",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &failedRecordID,
			Values: map[string]any{quantity.Definition.Identity.PhysicalName: 0.0},
		},
	))
	var formulaErr *formula.Error
	if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.divide_by_zero" {
		t.Fatalf("formula error = %#v, want formula.divide_by_zero", err)
	}
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	if _, findErr := app.FindRecordById(collection, failedRecordID); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("failed formula mutation persisted record: %v", findErr)
	}
}

func TestMutationKernelValidatesRelationsAndKeepsTableDataRevisionsIndependent(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	var err error
	categories := createV2IntegrationTable(t, ctx, app, "categories", "mutation_categories_table")
	name := createV2IntegrationField(t, ctx, app, categories.TableID, fieldDraftForIntegration(t, v2.LogicalText, "name"), "mutation_categories_name")
	categories.SchemaRevision = name.SchemaRevision
	items := createV2IntegrationTable(t, ctx, app, "items", "mutation_items_table")
	title := createV2IntegrationField(t, ctx, app, items.TableID, fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_items_title")
	relation := createV2IntegrationRelation(t, ctx, app, items.TableID, title.FieldID, categories.TableID, name.FieldID, "category", "items", "one", "mutation_items_category")
	items.SchemaRevision = relation.SchemaRevision
	for _, related := range relation.Related {
		if related.TableID == categories.TableID {
			categories.SchemaRevision = related.SchemaRevision
		}
	}
	if name.Definition == nil || title.Definition == nil || relation.Definition == nil {
		t.Fatal("V2 relation fixture omitted field definition")
	}
	categoryMeta, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": categories.TableID})
	if err != nil {
		t.Fatal(err)
	}
	categoryDataRevision := int64(categoryMeta.GetFloat("data_revision"))
	itemMeta, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": items.TableID})
	if err != nil {
		t.Fatal(err)
	}
	itemDataRevision := int64(itemMeta.GetFloat("data_revision"))
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	categoryID := "rec000000000051"
	if _, err := kernel.Apply(ctx, mutationRequest(
		categories.TableID, categories.SchemaRevision, "category-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &categoryID,
			Values: map[string]any{name.Definition.Identity.PhysicalName: "General"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	categoryMeta, _ = app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": categories.TableID})
	if got := int64(categoryMeta.GetFloat("data_revision")); got != categoryDataRevision+1 {
		t.Fatalf("category insert data revision = %d", got)
	}
	itemID := "rec000000000052"
	invalid := mutationRequest(
		items.TableID, items.SchemaRevision, "invalid-relation",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &itemID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "Bad", relation.Definition.Identity.PhysicalName: "rec000000009999"},
		},
	)
	_, err = kernel.Apply(ctx, invalid)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.relation.target_not_found" ||
		productErr.Path == nil ||
		*productErr.Path != "operations[0].values."+relation.Definition.Identity.PhysicalName {
		t.Fatalf("invalid relation = %#v", err)
	}
	if _, err := app.FindFirstRecordByFilter("vibetable_idempotency_keys", "key='invalid-relation'"); err == nil {
		t.Fatal("invalid relation left idempotency state")
	}
	itemMeta, _ = app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": items.TableID})
	if got := int64(itemMeta.GetFloat("data_revision")); got != itemDataRevision {
		t.Fatalf("failed item mutation data revision = %d", got)
	}
	itemMeta.Set("data_revision", -1)
	if err := app.Save(itemMeta); err != nil {
		t.Fatal(err)
	}
	corrupt := invalid
	corrupt.RequestID, corrupt.IdempotencyKey = "req-corrupt-counter", "corrupt-counter"
	corrupt.Operations[0].Values[relation.Definition.Identity.PhysicalName] = categoryID
	_, err = kernel.Apply(ctx, corrupt)
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.metadata.invalid_data_revision" {
		t.Fatalf("corrupt data revision = %#v", err)
	}
	itemMeta.Set("data_revision", itemDataRevision)
	if err := app.Save(itemMeta); err != nil {
		t.Fatal(err)
	}
	valid := invalid
	valid.RequestID, valid.IdempotencyKey = "req-valid-relation", "valid-relation"
	valid.Operations[0].Values[relation.Definition.Identity.PhysicalName] = categoryID
	if _, err := kernel.Apply(ctx, valid); err != nil {
		t.Fatalf("valid relation: %#v", err)
	}
	categoryMeta, _ = app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": categories.TableID})
	itemMeta, _ = app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": items.TableID})
	if int64(categoryMeta.GetFloat("data_revision")) != categoryDataRevision+2 ||
		int64(itemMeta.GetFloat("data_revision")) != itemDataRevision+1 {
		t.Fatalf("independent data revisions: category=%v item=%v",
			categoryMeta.GetRaw("data_revision"), itemMeta.GetRaw("data_revision"))
	}
}

func TestMutationKernelOneThousandOperationsCommitOrFullyRollback(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(t, ctx, app, "bulk rows", "mutation_bulk_rows_table")
	value := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalText, "value"), "mutation_bulk_rows_value")
	definition.SchemaRevision = value.SchemaRevision
	if value.Definition == nil {
		t.Fatal("V2 value fixture omitted field definition")
	}
	idempotencyBefore, err := app.FindAllRecords("vibetable_idempotency_keys")
	if err != nil {
		t.Fatal(err)
	}
	operations := make([]mutation.Operation, 1_000)
	for index := range operations {
		recordID := fmt.Sprintf("bulk%011d", index+1)
		operations[index] = mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{value.Definition.Identity.PhysicalName: fmt.Sprintf("row-%d", index+1)},
		}
	}
	request := mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "bulk-rollback",
		IdempotencyKey:  "bulk-rollback",
		TableID:         definition.TableID,
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
	assertRecordCount(t, app, definition.PhysicalName, 0)
	assertRecordCount(t, app, "vibetable_audit_events", 0)
	assertRecordCount(t, app, "vibetable_idempotency_keys", len(idempotencyBefore))
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
	assertRecordCount(t, app, definition.PhysicalName, 1_000)
	assertRecordCount(t, app, "vibetable_audit_events", 1_000)
	assertRecordCount(t, app, "vibetable_idempotency_keys", len(idempotencyBefore)+1)
	assertRecordCount(t, app, "vibetable_outbox", 1)
}

func TestMutationKernelDigestRoundTripsJSONAcrossFreshRecordReads(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition := createV2IntegrationTable(t, ctx, app, "digest notes", "mutation_digest_notes_table")
	title := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalText, "title"), "mutation_digest_notes_title")
	payload := createV2IntegrationField(t, ctx, app, definition.TableID, fieldDraftForIntegration(t, v2.LogicalJSON, "payload"), "mutation_digest_notes_payload")
	definition.SchemaRevision = payload.SchemaRevision
	if title.Definition == nil || payload.Definition == nil {
		t.Fatal("V2 digest fixture omitted field definition")
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	recordID := "rec000000000061"
	inserted, err := kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "digest-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{
				title.Definition.Identity.PhysicalName: "one",
				payload.Definition.Identity.PhysicalName: map[string]any{
					"nested": []any{json.Number("1"), "two"},
				},
			},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	update := mutationRequest(
		definition.TableID, definition.SchemaRevision, "digest-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "two"},
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
