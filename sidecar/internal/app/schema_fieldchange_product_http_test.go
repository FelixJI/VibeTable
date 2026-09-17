package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// Exercise the actual HTTP envelope, dispatcher, persistent planner/executor
// and migration service; no transport replies or domain results are scripted.
func TestSchemaFieldChangeProductHTTPLifecycle(t *testing.T) {
	pb := schemaProductStore(t)
	mux := schemaProductMux(t, pb)
	call := func(method string, input any, target any) productrpc.ResponseEnvelope {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response := schemaProductRequestForMethod(t, mux, context.Background(), method, string(raw), schemaListWire)
		if target != nil {
			if response.Error != nil {
				t.Fatalf("%s: %+v", method, response.Error)
			}
			if err := json.Unmarshal(response.Result, target); err != nil {
				t.Fatal(err)
			}
		}
		return response
	}
	assertError := func(response productrpc.ResponseEnvelope, code string) {
		t.Helper()
		if response.Error == nil || response.Error.Code != productrpc.CodeProductData {
			t.Fatalf("expected %s: %+v", code, response)
		}
		raw, err := json.Marshal(response.Error.Data)
		if err != nil {
			t.Fatal(err)
		}
		var data struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if data.Code != code {
			t.Fatalf("expected %s: %s", code, raw)
		}
	}
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	create := v2.TableCreateIntent{DisplayName: "HTTP lifecycle", OperationID: "http-create", Actor: actor}
	var table v2.TableCreateReceipt
	call("schema.table.create", create, &table)
	// An old workspace session is rejected before creating another table.
	stale, _ := json.Marshal(v2.TableCreateIntent{DisplayName: "stale", OperationID: "stale-create", Actor: actor})
	rejected := schemaProductRequestForMethod(t, mux, context.Background(), "schema.table.create", string(stale), strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1))
	if rejected.Error == nil || rejected.Error.Code != productrpc.CodeInvalidRequest {
		t.Fatalf("stale session accepted: %+v", rejected)
	}
	tables, err := pb.FindAllRecords("vibetable_tables")
	if err != nil || len(tables) != 1 {
		t.Fatalf("stale write reached authority: %v %v", tables, err)
	}
	defaults, err := v2.RecommendedDefaults(v2.LogicalText)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision, Actor: actor,
		Draft: &v2.FieldDraft{DisplayName: "Amount", LogicalType: v2.LogicalText, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display}}
	var plan v2.FieldChangePlan
	call("field.change.plan", intent, &plan)
	var receipt v2.ApplyReceipt
	call("field.change.apply", v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "http-apply", Actor: actor, Confirmations: []string{}}, &receipt)
	if receipt.Definition == nil {
		t.Fatal("missing applied definition")
	}
	// Capture a valid plan produced by the real planner with an already elapsed
	// clock. The executor must reject its expiry and leave the field set intact.
	catalog := fieldchange.NewCatalog(pb)
	store := fieldchange.NewPocketBasePlanStore(pb)
	oldPlanner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil), fieldchange.WithClock(func() time.Time { return time.Now().Add(-time.Hour) }))
	expiredIntent := intent
	expiredIntent.ExpectedSchemaRev = receipt.SchemaRevision
	expired, err := oldPlanner.Plan(context.Background(), expiredIntent)
	if err != nil {
		t.Fatal(err)
	}
	assertError(call("field.change.apply", v2.ApplyRequest{PlanID: expired.PlanID, PlanHash: expired.PlanHash, OperationID: "expired-apply", Actor: actor, Confirmations: []string{}}, nil), "field.change.plan_expired")
	fields, err := catalog.Fields(context.Background(), table.TableID, true)
	if err != nil || len(fields) != 1 {
		t.Fatalf("expired plan wrote: %v %v", fields, err)
	}
	// Enqueue a real conversion plan without starting its worker, making the
	// cancellable phase deterministic while testing actual persisted job state.
	tableRecord, err := pb.FindFirstRecordByData("vibetable_tables", "table_id", table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(tableRecord.GetString("collection_id"))
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Set(receipt.Definition.Identity.PhysicalName, "12")
	row.Set(receipt.Definition.Value.Presence.PhysicalName, true)
	if err := pb.Save(row); err != nil {
		t.Fatal(err)
	}
	numberDefaults, err := v2.RecommendedDefaults(v2.LogicalNumber)
	if err != nil {
		t.Fatal(err)
	}
	convert := v2.FieldChangeIntent{Action: v2.ActionConvert, TableID: table.TableID, FieldID: receipt.FieldID, ExpectedSchemaRev: receipt.SchemaRevision, Actor: actor, ConversionRule: "block",
		Draft: &v2.FieldDraft{DisplayName: "Amount", LogicalType: v2.LogicalNumber, Value: numberDefaults.Value, Constraints: numberDefaults.Constraints, Storage: numberDefaults.Storage, Display: numberDefaults.Display}}
	var conversion v2.FieldChangePlan
	call("field.change.plan", convert, &conversion)
	if !conversion.CreatesMigration || !conversion.CanApply {
		t.Fatalf("not a migration plan: %+v", conversion)
	}
	scheduler := fieldchange.NewMigrationService(pb, store)
	t.Cleanup(scheduler.Shutdown)
	jobID, err := scheduler.Enqueue(context.Background(), pb, conversion, "http-enqueue")
	if err != nil {
		t.Fatal(err)
	}
	var status v2.MigrationStatus
	call("field.change.status", map[string]string{"jobId": jobID}, &status)
	if !status.CanCancel {
		t.Fatalf("job not cancellable: %+v", status)
	}
	call("field.change.cancel", map[string]string{"jobId": jobID}, &status)
	if status.Phase != v2.MigrationCancelled || status.CanCancel {
		t.Fatalf("cancel=%+v", status)
	}
	call("field.change.status", map[string]string{"jobId": jobID}, &status)
	if status.Phase != v2.MigrationCancelled {
		t.Fatal("cancel did not persist")
	}
	retire := v2.FieldChangeIntent{Action: v2.ActionRetire, TableID: table.TableID, FieldID: receipt.FieldID, ExpectedSchemaRev: receipt.SchemaRevision, Actor: actor}
	call("field.change.plan", retire, &plan)
	call("field.change.apply", v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "http-retire", Actor: actor, Confirmations: plan.Confirmations}, &receipt)
	var recycled recycledFieldsResult
	call("field.recycleBin.list", map[string]string{"tableId": table.TableID}, &recycled)
	if len(recycled.Fields) != 1 || recycled.Fields[0].Identity.FieldID != receipt.FieldID {
		t.Fatalf("recycle=%+v", recycled)
	}
	purge := v2.FieldChangeIntent{Action: v2.ActionPurge, TableID: table.TableID, FieldID: receipt.FieldID, ExpectedSchemaRev: receipt.SchemaRevision, Actor: actor, Confirmation: "Amount", BackupReceipt: "invalid-proof"}
	call("field.change.plan", purge, &plan)
	request := v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "http-purge", Actor: actor, Confirmations: []string{}}
	assertError(call("field.change.apply", request, nil), "field.change.confirmation_required")
	request.Confirmations = plan.Confirmations
	assertError(call("field.change.apply", request, nil), "field.purge.backup_required")
	call("field.recycleBin.list", map[string]string{"tableId": table.TableID}, &recycled)
	if len(recycled.Fields) != 1 {
		t.Fatal("unverified purge removed field")
	}
	assertError(call("schema.delete", map[string]string{"tableId": table.TableID, "expectedRevision": table.SchemaRevision}, nil), "schema.revision_conflict")
	var removed struct {
		Deleted bool `json:"deleted"`
	}
	call("schema.delete", map[string]string{"tableId": table.TableID, "expectedRevision": receipt.SchemaRevision}, &removed)
	if !removed.Deleted {
		t.Fatal("table was not deleted")
	}
}
