package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

func TestInternalMetadataMigrationAndAllowlistedCRUD(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
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
	app := bootstrapApp(t, t.TempDir())
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

func TestDashboardCommitIsAtomicAcrossDashboardAndPanels(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	service := metadata.New(app)
	ctx := context.Background()

	first, err := service.CommitDashboard(ctx, metadata.DashboardCommitRequest{
		IdempotencyKey: "dashboard-create",
		Dashboard: metadata.ItemMutation{
			LogicalID: "sales",
			Payload:   json.RawMessage(`{"title":"Sales"}`),
		},
		Panels: []metadata.ItemMutation{
			{
				LogicalID: "revenue",
				Payload:   json.RawMessage(`{"chart":"line"}`),
			},
			{
				LogicalID: "orders",
				Payload:   json.RawMessage(`{"chart":"number"}`),
			},
		},
	})
	if err != nil || len(first.Panels) != 2 {
		t.Fatalf("first commit = %#v, err=%v", first, err)
	}
	if first.ChangeSetID == "" || len(first.EmittedEvents) != 2 {
		t.Fatalf("dashboard trace receipt = %#v", first)
	}
	assertMetadataMutationTrace(
		t, app, first.ChangeSetID, first.EmittedEvents,
		"metadata:dashboards", "sales", "insert", 3,
	)
	for _, logicalID := range []string{"revenue", "orders"} {
		record, findErr := app.FindFirstRecordByFilter(
			"vibetable_panels",
			"logical_id={:logicalId}",
			dbx.Params{"logicalId": logicalID},
		)
		if findErr != nil ||
			record.GetString("dashboard_id") != "sales" {
			t.Fatalf(
				"panel %s dashboard link = %q, err=%v",
				logicalID, record.GetString("dashboard_id"), findErr,
			)
		}
	}
	replayed, err := service.CommitDashboard(
		ctx,
		metadata.DashboardCommitRequest{
			IdempotencyKey: "dashboard-create",
			Dashboard: metadata.ItemMutation{
				LogicalID: "sales",
				Payload:   json.RawMessage(`{"title":"Sales"}`),
			},
			Panels: []metadata.ItemMutation{
				{
					LogicalID: "revenue",
					Payload:   json.RawMessage(`{"chart":"line"}`),
				},
				{
					LogicalID: "orders",
					Payload:   json.RawMessage(`{"chart":"number"}`),
				},
			},
		},
	)
	if err != nil || replayed.Status != metadata.StatusReplayed ||
		replayed.ChangeSetID != first.ChangeSetID ||
		len(replayed.EmittedEvents) != len(first.EmittedEvents) {
		t.Fatalf("dashboard replay = %#v, err=%v", replayed, err)
	}
	assertMetadataMutationTrace(
		t, app, replayed.ChangeSetID, replayed.EmittedEvents,
		"metadata:dashboards", "sales", "insert", 3,
	)

	_, err = service.CommitDashboard(ctx, metadata.DashboardCommitRequest{
		IdempotencyKey: "dashboard-stale",
		Dashboard: metadata.ItemMutation{
			LogicalID:        "sales",
			Payload:          json.RawMessage(`{"title":"Changed"}`),
			ExpectedRevision: first.Dashboard.Revision,
		},
		Panels: []metadata.ItemMutation{{
			LogicalID:        "revenue",
			Payload:          json.RawMessage(`{"chart":"bar"}`),
			ExpectedRevision: "sha256:" + strings.Repeat("f", 64),
		}},
	})
	if !metadata.IsError(err, "metadata.revision_conflict") {
		t.Fatalf("stale dashboard commit error = %#v", err)
	}
	dashboards, listErr := service.List(ctx, metadata.NamespaceDashboards)
	if listErr != nil || len(dashboards) != 1 ||
		dashboards[0].Revision != first.Dashboard.Revision {
		t.Fatalf("dashboard partially committed: %#v, err=%v", dashboards, listErr)
	}
	panels, listErr := service.List(ctx, metadata.NamespacePanels)
	if listErr != nil || len(panels) != 2 {
		t.Fatalf("panels after rollback: %#v, err=%v", panels, listErr)
	}
	firstPanelRevisions := map[string]string{}
	for _, panel := range first.Panels {
		firstPanelRevisions[panel.LogicalID] = panel.Revision
	}
	for _, panel := range panels {
		if panel.Revision != firstPanelRevisions[panel.LogicalID] {
			t.Fatalf("panel partially committed: %#v", panel)
		}
	}

	_, err = service.CommitDashboard(ctx, metadata.DashboardCommitRequest{
		IdempotencyKey: "other-dashboard-panel",
		Dashboard: metadata.ItemMutation{
			LogicalID: "other",
			Payload:   json.RawMessage(`{"title":"Other"}`),
		},
		Panels: []metadata.ItemMutation{{
			LogicalID:        "revenue",
			Payload:          json.RawMessage(`{"chart":"bar"}`),
			ExpectedRevision: firstPanelRevisions["revenue"],
		}},
	})
	if !metadata.IsError(err, "metadata.dashboard.invalid") {
		t.Fatalf("cross-dashboard panel error = %#v", err)
	}
	otherDashboards, listErr := service.List(
		ctx, metadata.NamespaceDashboards,
	)
	if listErr != nil || len(otherDashboards) != 1 {
		t.Fatalf(
			"cross-dashboard commit was not rolled back: %#v, err=%v",
			otherDashboards, listErr,
		)
	}

	_, err = service.CommitDashboard(ctx, metadata.DashboardCommitRequest{
		IdempotencyKey: "dashboard-duplicate-panel",
		Dashboard: metadata.ItemMutation{
			LogicalID:        "sales",
			Payload:          json.RawMessage(`{"title":"Sales"}`),
			ExpectedRevision: first.Dashboard.Revision,
		},
		Panels: []metadata.ItemMutation{{
			LogicalID: "orders",
			Payload:   json.RawMessage(`{"chart":"number"}`),
		}},
		DeletePanels: []metadata.ItemDelete{{
			LogicalID: "orders",
		}},
	})
	var productErr *metadata.Error
	if !errors.As(err, &productErr) ||
		productErr.Code != "metadata.dashboard.invalid" {
		t.Fatalf("duplicate panel operation error = %#v", err)
	}
}

func TestDashboardDeleteCascadesPanelsWithOneChangeSetAndDurableReplay(
	t *testing.T,
) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	service := metadata.New(app)
	ctx := context.Background()
	created, err := service.CommitDashboard(
		ctx,
		metadata.DashboardCommitRequest{
			IdempotencyKey: "dashboard-cascade-create",
			Dashboard: metadata.ItemMutation{
				LogicalID: "sales-cascade",
				Payload:   json.RawMessage(`{"title":"Sales"}`),
			},
			Panels: []metadata.ItemMutation{
				{
					LogicalID: "cascade-revenue",
					Payload:   json.RawMessage(`{"chart":"line"}`),
				},
				{
					LogicalID: "cascade-orders",
					Payload:   json.RawMessage(`{"chart":"number"}`),
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CommitDashboard(
		ctx,
		metadata.DashboardCommitRequest{
			IdempotencyKey: "dashboard-cascade-other",
			Dashboard: metadata.ItemMutation{
				LogicalID: "other-cascade",
				Payload:   json.RawMessage(`{"title":"Other"}`),
			},
			Panels: []metadata.ItemMutation{{
				LogicalID: "other-panel",
				Payload:   json.RawMessage(`{"chart":"line"}`),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	stale := metadata.DeleteRequest{
		Namespace: metadata.NamespaceDashboards,
		LogicalID: "sales-cascade",
		ExpectedRevision: "sha256:" +
			strings.Repeat("0", 64),
		IdempotencyKey: "dashboard-cascade-stale",
	}
	if _, err := service.Delete(ctx, stale); !metadata.IsError(
		err,
		"metadata.revision_conflict",
	) {
		t.Fatalf("stale dashboard cascade error = %#v", err)
	}
	panelsAfterStale, err := service.List(ctx, metadata.NamespacePanels)
	if err != nil || len(panelsAfterStale) != 3 {
		t.Fatalf(
			"panels after stale cascade = %#v, err=%v",
			panelsAfterStale,
			err,
		)
	}
	staleAudits, err := app.FindRecordsByFilter(
		"vibetable_audit_events",
		"request_id='metadata:dashboard-cascade-stale'",
		"",
		0,
		0,
	)
	if err != nil || len(staleAudits) != 0 {
		t.Fatalf("stale cascade audits = %#v, err=%v", staleAudits, err)
	}

	request := metadata.DeleteRequest{
		Namespace:        metadata.NamespaceDashboards,
		LogicalID:        "sales-cascade",
		ExpectedRevision: created.Dashboard.Revision,
		IdempotencyKey:   "dashboard-cascade-delete",
	}
	deleted, err := service.Delete(ctx, request)
	if err != nil || !deleted.Deleted ||
		deleted.Status != metadata.StatusApplied ||
		len(deleted.EmittedEvents) != 2 {
		t.Fatalf("dashboard cascade delete = %#v, err=%v", deleted, err)
	}
	audits, err := app.FindRecordsByFilter(
		"vibetable_audit_events",
		"change_set_id={:changeSet}",
		"+sequence",
		0,
		0,
		dbx.Params{"changeSet": deleted.ChangeSetID},
	)
	if err != nil || len(audits) != 3 {
		t.Fatalf("dashboard cascade audits = %#v, err=%v", audits, err)
	}
	wantAudit := map[string]string{
		"cascade-orders":  "metadata:panels",
		"cascade-revenue": "metadata:panels",
		"sales-cascade":   "metadata:dashboards",
	}
	for _, audit := range audits {
		recordID := audit.GetString("record_id")
		if audit.GetString("operation") != "delete" ||
			audit.GetString("table_id") != wantAudit[recordID] {
			t.Fatalf("dashboard cascade audit = %#v", audit)
		}
		delete(wantAudit, recordID)
	}
	if len(wantAudit) != 0 {
		t.Fatalf("missing cascade audits = %#v", wantAudit)
	}
	eventTables := map[string]bool{}
	for _, eventID := range deleted.EmittedEvents {
		event := loadMetadataEvent(t, app, eventID)
		if event.ChangeSetID == nil ||
			*event.ChangeSetID != deleted.ChangeSetID ||
			event.Operation != mutation.DataChangeDelete {
			t.Fatalf("dashboard cascade event = %#v", event)
		}
		eventTables[event.TableID] = true
	}
	if !eventTables["metadata:dashboards"] ||
		!eventTables["metadata:panels"] ||
		len(eventTables) != 2 {
		t.Fatalf("dashboard cascade event tables = %#v", eventTables)
	}

	replayed, err := service.Delete(ctx, request)
	if err != nil ||
		replayed.Status != metadata.StatusReplayed ||
		replayed.ChangeSetID != deleted.ChangeSetID ||
		len(replayed.EmittedEvents) != len(deleted.EmittedEvents) {
		t.Fatalf("dashboard cascade replay = %#v, err=%v", replayed, err)
	}
	auditsAfterReplay, err := app.FindRecordsByFilter(
		"vibetable_audit_events",
		"change_set_id={:changeSet}",
		"",
		0,
		0,
		dbx.Params{"changeSet": deleted.ChangeSetID},
	)
	if err != nil || len(auditsAfterReplay) != 3 {
		t.Fatalf(
			"dashboard cascade replay audits = %#v, err=%v",
			auditsAfterReplay,
			err,
		)
	}

	reused := request
	reused.LogicalID = "other-cascade"
	reused.ExpectedRevision = other.Dashboard.Revision
	if _, err := service.Delete(ctx, reused); !metadata.IsError(
		err,
		"metadata.idempotency_conflict",
	) {
		t.Fatalf("dashboard delete idempotency conflict = %#v", err)
	}
	dashboards, err := service.List(ctx, metadata.NamespaceDashboards)
	if err != nil || len(dashboards) != 1 ||
		dashboards[0].LogicalID != "other-cascade" {
		t.Fatalf("dashboards after cascade = %#v, err=%v", dashboards, err)
	}
	panels, err := service.List(ctx, metadata.NamespacePanels)
	if err != nil || len(panels) != 1 ||
		panels[0].LogicalID != "other-panel" {
		t.Fatalf("panels after cascade = %#v, err=%v", panels, err)
	}
}

func TestDashboardCascadeTraceFailureRollsBackAllDeletes(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	ctx := context.Background()
	created, err := metadata.New(app).CommitDashboard(
		ctx,
		metadata.DashboardCommitRequest{
			IdempotencyKey: "dashboard-rollback-create",
			Dashboard: metadata.ItemMutation{
				LogicalID: "dashboard-rollback",
				Payload:   json.RawMessage(`{"title":"Rollback"}`),
			},
			Panels: []metadata.ItemMutation{
				{
					LogicalID: "rollback-a",
					Payload:   json.RawMessage(`{"chart":"line"}`),
				},
				{
					LogicalID: "rollback-b",
					Payload:   json.RawMessage(`{"chart":"number"}`),
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	failing := metadata.New(
		app,
		metadata.WithIDGenerator(func(kind string) string {
			if kind == "changeSet" {
				return "changeSet_dashboard_delete_failure"
			}
			return created.EmittedEvents[0]
		}),
	)
	_, err = failing.Delete(ctx, metadata.DeleteRequest{
		Namespace:        metadata.NamespaceDashboards,
		LogicalID:        "dashboard-rollback",
		ExpectedRevision: created.Dashboard.Revision,
		IdempotencyKey:   "dashboard-rollback-delete",
	})
	if !metadata.IsError(err, "metadata.storage.failed") {
		t.Fatalf("dashboard cascade trace failure = %#v", err)
	}
	dashboards, err := failing.List(ctx, metadata.NamespaceDashboards)
	if err != nil || len(dashboards) != 1 {
		t.Fatalf("dashboard rollback = %#v, err=%v", dashboards, err)
	}
	panels, err := failing.List(ctx, metadata.NamespacePanels)
	if err != nil || len(panels) != 2 {
		t.Fatalf("panel rollback = %#v, err=%v", panels, err)
	}
	for collection, filter := range map[string]string{
		"vibetable_audit_events": "change_set_id=" +
			"'changeSet_dashboard_delete_failure'",
		"vibetable_idempotency_keys": "key=" +
			"'metadata:dashboard-rollback-delete'",
	} {
		records, err := app.FindRecordsByFilter(
			collection,
			filter,
			"",
			0,
			0,
		)
		if err != nil || len(records) != 0 {
			t.Fatalf(
				"%s rollback records = %d, err=%v",
				collection,
				len(records),
				err,
			)
		}
	}
}

func TestInternalMetadataTraceFailureRollsBackBusinessAndIdempotency(
	t *testing.T,
) {
	app := bootstrapApp(t, t.TempDir())
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
