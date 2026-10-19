package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

func TestInternalMetadataMigrationAndAllowlistedCRUD(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)

	for _, namespace := range metadata.Namespaces() {
		collection, err := app.FindCollectionByNameOrId(
			"vibetable_" + string(namespace),
		)
		if err != nil {
			t.Fatalf("collection %s: %v", namespace, err)
		}
		if collection.Fields.GetByName("logical_id") == nil ||
			collection.Fields.GetByName("payload_json") == nil {
			t.Fatalf("%s lacks unified metadata fields", namespace)
		}
		if collection.GetIndex(
			"uniq_vibetable_"+string(namespace)+"_logical_id",
		) == "" {
			t.Fatalf("%s lacks logical_id uniqueness", namespace)
		}
	}

	service := metadata.New(app)
	ctx := context.Background()
	for _, namespace := range metadata.Namespaces() {
		receipt, err := service.Upsert(ctx, metadata.UpsertRequest{
			Namespace: namespace,
			LogicalID: "example",
			Payload: json.RawMessage(
				`{"b":2,"a":1}`,
			),
			ExpectedRevision: "",
			IdempotencyKey:   "create-" + string(namespace),
		})
		if err != nil {
			t.Fatalf("upsert %s: %#v", namespace, err)
		}
		if receipt.Status != metadata.StatusApplied ||
			receipt.Item.LogicalID != "example" ||
			!strings.HasPrefix(receipt.Item.Revision, "sha256:") {
			t.Fatalf("receipt %s = %#v", namespace, receipt)
		}
		items, err := service.List(ctx, namespace)
		if err != nil || len(items) != 1 ||
			items[0].Revision != receipt.Item.Revision {
			t.Fatalf("list %s = %#v, err=%v", namespace, items, err)
		}
	}
	empty, err := service.Upsert(ctx, metadata.UpsertRequest{
		Namespace:        metadata.NamespaceSharedSettings,
		LogicalID:        "empty",
		Payload:          json.RawMessage(`{}`),
		ExpectedRevision: "",
		IdempotencyKey:   "create-empty-setting",
	})
	if err != nil || string(empty.Item.Payload) != `{}` {
		t.Fatalf("empty payload upsert = %#v, err=%v", empty, err)
	}

	if _, err := service.List(ctx, metadata.Namespace("users")); !metadata.IsError(
		err, "metadata.namespace.invalid",
	) {
		t.Fatalf("arbitrary namespace error = %#v", err)
	}
}

func TestInternalMetadataCASIdempotencyAndDeleteReplay(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	service := metadata.New(app)
	ctx := context.Background()

	create := metadata.UpsertRequest{
		Namespace:        metadata.NamespacePresets,
		LogicalID:        "compact",
		Payload:          json.RawMessage(`{"columns":["name"]}`),
		ExpectedRevision: "",
		IdempotencyKey:   "preset-create",
	}
	applied, err := service.Upsert(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	assertMetadataMutationTrace(
		t, app, applied.ChangeSetID, applied.EmittedEvents,
		"metadata:presets", "compact", "insert", 1,
	)
	replayed, err := service.Upsert(ctx, create)
	if err != nil || replayed.Status != metadata.StatusReplayed ||
		replayed.Item.Revision != applied.Item.Revision ||
		replayed.ChangeSetID != applied.ChangeSetID ||
		len(replayed.EmittedEvents) != 1 ||
		replayed.EmittedEvents[0] != applied.EmittedEvents[0] {
		t.Fatalf("replay = %#v, err=%v", replayed, err)
	}
	assertMetadataMutationTrace(
		t, app, replayed.ChangeSetID, replayed.EmittedEvents,
		"metadata:presets", "compact", "insert", 1,
	)
	reused := create
	reused.Payload = json.RawMessage(`{"columns":["id"]}`)
	if _, err := service.Upsert(ctx, reused); !metadata.IsError(
		err, "metadata.idempotency_conflict",
	) {
		t.Fatalf("idempotency conflict = %#v", err)
	}
	stale := create
	stale.IdempotencyKey = "preset-stale"
	stale.ExpectedRevision = "sha256:" + strings.Repeat("0", 64)
	if _, err := service.Upsert(ctx, stale); !metadata.IsError(
		err, "metadata.revision_conflict",
	) {
		t.Fatalf("stale upsert = %#v", err)
	}

	update := create
	update.IdempotencyKey = "preset-update"
	update.ExpectedRevision = applied.Item.Revision
	update.Payload = json.RawMessage(`{"columns":["name","id"]}`)
	updated, err := service.Upsert(ctx, update)
	if err != nil || updated.Item.Revision == applied.Item.Revision {
		t.Fatalf("update = %#v, err=%v", updated, err)
	}
	assertMetadataMutationTrace(
		t, app, updated.ChangeSetID, updated.EmittedEvents,
		"metadata:presets", "compact", "update", 1,
	)
	deleteRequest := metadata.DeleteRequest{
		Namespace:        metadata.NamespacePresets,
		LogicalID:        "compact",
		ExpectedRevision: updated.Item.Revision,
		IdempotencyKey:   "preset-delete",
	}
	deleted, err := service.Delete(ctx, deleteRequest)
	if err != nil || deleted.Status != metadata.StatusApplied ||
		!deleted.Deleted {
		t.Fatalf("delete = %#v, err=%v", deleted, err)
	}
	assertMetadataMutationTrace(
		t, app, deleted.ChangeSetID, deleted.EmittedEvents,
		"metadata:presets", "compact", "delete", 1,
	)
	deleteReplay, err := service.Delete(ctx, deleteRequest)
	if err != nil || deleteReplay.Status != metadata.StatusReplayed ||
		!deleteReplay.Deleted ||
		deleteReplay.ChangeSetID != deleted.ChangeSetID ||
		len(deleteReplay.EmittedEvents) != 1 ||
		deleteReplay.EmittedEvents[0] != deleted.EmittedEvents[0] {
		t.Fatalf("delete replay = %#v, err=%v", deleteReplay, err)
	}
	items, err := service.List(ctx, metadata.NamespacePresets)
	if err != nil || len(items) != 0 {
		t.Fatalf("items after delete = %#v, err=%v", items, err)
	}
}

func TestInternalMetadataTraceFailureRollsBackBusinessAndIdempotency(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	changeSetSequence := 0
	service := metadata.New(
		app,
		metadata.WithIDGenerator(func(kind string) string {
			if kind == "changeSet" {
				changeSetSequence++
				return fmt.Sprintf(
					"changeSet_metadata_%d",
					changeSetSequence,
				)
			}
			return "event_metadata_duplicate"
		}),
	)
	ctx := context.Background()
	if _, err := service.Upsert(ctx, metadata.UpsertRequest{
		Namespace:        metadata.NamespacePresets,
		LogicalID:        "first",
		Payload:          json.RawMessage(`{"name":"First"}`),
		ExpectedRevision: "",
		IdempotencyKey:   "trace-first",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := service.Upsert(ctx, metadata.UpsertRequest{
		Namespace:        metadata.NamespacePresets,
		LogicalID:        "second",
		Payload:          json.RawMessage(`{"name":"Second"}`),
		ExpectedRevision: "",
		IdempotencyKey:   "trace-second",
	})
	if !metadata.IsError(err, "metadata.storage.failed") {
		t.Fatalf("duplicate outbox error = %#v", err)
	}
	items, err := service.List(ctx, metadata.NamespacePresets)
	if err != nil || len(items) != 1 || items[0].LogicalID != "first" {
		t.Fatalf("business rollback items = %#v, err=%v", items, err)
	}
	for collection, filter := range map[string]string{
		"vibetable_audit_events":     "change_set_id='changeSet_metadata_2'",
		"vibetable_idempotency_keys": "key='metadata:trace-second'",
	} {
		records, err := app.FindRecordsByFilter(
			collection, filter, "", 0, 0,
		)
		if err != nil || len(records) != 0 {
			t.Fatalf(
				"%s rollback records = %d, err=%v",
				collection, len(records), err,
			)
		}
	}
}

func loadMetadataEvent(
	t *testing.T,
	app core.App,
	eventID string,
) mutation.DataChangedEvent {
	t.Helper()
	record, err := app.FindFirstRecordByFilter(
		"vibetable_outbox",
		"event_id={:event}",
		dbx.Params{"event": eventID},
	)
	if err != nil {
		t.Fatalf("outbox event %s: %v", eventID, err)
	}
	raw, err := json.Marshal(record.GetRaw("payload_json"))
	if err != nil {
		t.Fatal(err)
	}
	var event mutation.DataChangedEvent
	if err := mutation.DecodeStrict(raw, &event); err != nil {
		t.Fatalf("decode outbox event %s: %v", eventID, err)
	}
	return event
}

func assertMetadataMutationTrace(
	t *testing.T,
	app core.App,
	changeSetID string,
	emittedEvents []string,
	tableID string,
	recordID string,
	operation string,
	wantAuditCount int,
) {
	t.Helper()
	if changeSetID == "" || len(emittedEvents) == 0 {
		t.Fatalf(
			"missing metadata trace: changeSet=%q events=%#v",
			changeSetID, emittedEvents,
		)
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_audit_events",
		"change_set_id={:changeSet}",
		"sequence",
		0,
		0,
		dbx.Params{"changeSet": changeSetID},
	)
	if err != nil || len(records) != wantAuditCount {
		t.Fatalf(
			"audit records = %d, want %d, err=%v",
			len(records), wantAuditCount, err,
		)
	}
	first := records[0]
	if first.GetString("table_id") != tableID ||
		first.GetString("record_id") != recordID ||
		first.GetString("operation") != operation {
		t.Fatalf("first audit record = %#v", first)
	}
	for _, eventID := range emittedEvents {
		event := loadMetadataEvent(t, app, eventID)
		if event.ChangeSetID == nil ||
			*event.ChangeSetID != changeSetID {
			t.Fatalf("outbox event change set = %#v", event)
		}
	}
}
