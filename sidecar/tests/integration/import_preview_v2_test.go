package integration_test

import (
	"context"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	"github.com/vibetable/vibetable/sidecar/internal/importvalue"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestAuthoritativeRawPreviewDistinguishesInsertAndUpdate(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"tbl_raw_preview", "t_raw_preview", []schema.FieldDefinition{},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(
		catalog, catalog, store, v2.NewIdentityAllocator(nil),
	)
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}

	requiredDraft := fieldDraftForIntegration(t, v2.LogicalText, "Required")
	requiredDraft.Value.Required = true
	_ = applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		requiredDraft, actor, "op_raw_preview_required",
	)
	number := applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "Amount"),
		actor, "op_raw_preview_number",
	)
	revisions, err := catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	service := importvalue.New(catalog)

	update, err := service.Preview(ctx, importvalue.Request{
		Contract: importvalue.Contract, TableID: table.TableID,
		SchemaRevision: revisions.Schema,
		Rows: []importvalue.Row{{
			Mode: fieldvalue.Update,
			Values: map[string]any{
				number.FieldID: "$1,234.50",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Rows[0].Diagnostics) != 0 ||
		update.Rows[0].Values[number.FieldID] != float64(1234.5) {
		t.Fatalf("update raw preview = %#v", update.Rows[0])
	}

	insert, err := service.Preview(ctx, importvalue.Request{
		Contract: importvalue.Contract, TableID: table.TableID,
		SchemaRevision: revisions.Schema,
		Rows: []importvalue.Row{{
			Mode: fieldvalue.Insert,
			Values: map[string]any{
				number.FieldID: "$1,234.50",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(insert.Rows[0].Diagnostics) != 1 ||
		insert.Rows[0].Diagnostics[0].Code != "field.value.required" {
		t.Fatalf("insert raw preview diagnostics = %#v", insert.Rows[0].Diagnostics)
	}
}
