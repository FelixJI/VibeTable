package app

import (
	"context"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

func TestAutomaticDatesPreserveUnknownHistoryAndMaintainUTCTimestamps(t *testing.T) {
	pb := schemaProductStore(t)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "时间记录", OperationID: "date-table", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pb.FindAllRecords("vibetable_tables")
	if err != nil || len(tables) != 1 {
		t.Fatalf("tables: %v %v", tables, err)
	}
	collection, err := pb.FindCollectionByNameOrId(tables[0].GetString("collection_id"))
	if err != nil {
		t.Fatal(err)
	}
	old := core.NewRecord(collection)
	if err := pb.Save(old); err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(pb)
	store := fieldchange.NewPocketBasePlanStore(pb)
	planner := fieldchange.NewPlanner(catalog, catalog, store, nil)
	executor := fieldchange.NewExecutor(pb, store)
	names := map[string]string{}
	for _, role := range []string{"createdAt", "updatedAt"} {
		revisions, err := catalog.Revisions(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		defaults, _ := v2.RecommendedDefaults(v2.LogicalAutoDate)
		plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID,
			ExpectedSchemaRev: revisions.Schema, Actor: actor,
			Draft: &v2.FieldDraft{DisplayName: role, LogicalType: v2.LogicalAutoDate,
				Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
				AutoDate: &v2.AutoDateSpec{Role: role}}})
		if err != nil || !plan.CanApply {
			t.Fatalf("plan: %+v %v", plan, err)
		}
		if len(plan.Warnings) != 1 || plan.Warnings[0].Code != "field.auto_date.history_unavailable" {
			t.Fatalf("history warning: %+v", plan.Warnings)
		}
		receipt, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "date-" + role, Actor: actor, Confirmations: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		field, err := catalog.Field(ctx, table.TableID, receipt.FieldID)
		if err != nil {
			t.Fatal(err)
		}
		names[role] = field.Identity.PhysicalName
		revisions, err = catalog.Revisions(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		otherRole := "createdAt"
		if role == otherRole {
			otherRole = "updatedAt"
		}
		_, err = planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: table.TableID,
			FieldID: receipt.FieldID, ExpectedSchemaRev: revisions.Schema, Actor: actor,
			Draft: &v2.FieldDraft{DisplayName: role, LogicalType: v2.LogicalAutoDate,
				Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
				AutoDate: &v2.AutoDateSpec{Role: otherRole}}})
		if err == nil {
			t.Fatal("timestamp role change accepted")
		}
	}
	collection, err = pb.FindCollectionByNameOrId(collection.Id)
	if err != nil {
		t.Fatal(err)
	}
	old, err = pb.FindRecordById(collection, old.Id)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !old.GetDateTime(name).IsZero() {
			t.Fatal("fabricated historical timestamp")
		}
	}
	if err := pb.Save(old); err != nil {
		t.Fatal(err)
	}
	if !old.GetDateTime(names["createdAt"]).IsZero() || old.GetDateTime(names["updatedAt"]).IsZero() {
		t.Fatal("old record timestamp semantics changed")
	}
	record := core.NewRecord(collection)
	if err := pb.Save(record); err != nil {
		t.Fatal(err)
	}
	created := record.GetDateTime(names["createdAt"])
	updated := record.GetDateTime(names["updatedAt"])
	if created.IsZero() || updated.IsZero() || !strings.HasSuffix(created.String(), "Z") {
		t.Fatalf("timestamps: %v %v", created, updated)
	}
	// Seed an earlier stored update time so the assertion needs no clock sleeps.
	past, _ := types.ParseDateTime("2000-01-01T00:00:00Z")
	record.SetRaw(names["updatedAt"], past)
	if err := pb.Save(record); err != nil {
		t.Fatal(err)
	}
	record, err = pb.FindRecordById(collection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if err := pb.Save(record); err != nil {
		t.Fatal(err)
	}
	if record.GetDateTime(names["createdAt"]).String() != created.String() || !record.GetDateTime(names["updatedAt"]).Time().After(past.Time()) {
		t.Fatalf("creation timestamp changed or update timestamp did not advance: created=%v original=%v updated=%v", record.GetDateTime(names["createdAt"]), created, record.GetDateTime(names["updatedAt"]))
	}
}
