package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestSchemaCatalogPersistsQueryableReadOnlyView(t *testing.T) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer func() {
		if app != nil {
			resetApp(t, app)
		}
	}()
	ctx := context.Background()
	catalog := schemaapi.New(app)

	title := field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText)
	status := field("status_id", "status", schema.FieldKindScalar, schema.DataTypeShortText)
	source, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"view_source", "view_source",
			[]schema.FieldDefinition{title, status},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	for index, values := range []map[string]any{
		{"title": "Alpha", "status": "open"},
		{"title": "Beta", "status": "closed"},
	} {
		recordID := []string{"viewsource00001", "viewsource00002"}[index]
		if _, err := kernel.Apply(ctx, mutationRequest(
			source.TableID, source.SchemaRevision, "view-source-"+recordID,
			mutation.Operation{
				Kind: mutation.OperationInsert, RecordID: &recordID, Values: values,
			},
		)); err != nil {
			t.Fatal(err)
		}
	}

	viewTitle := title
	viewTitle.FieldID = "view_title_id"
	viewTitle.ReadOnly = true
	viewStatus := status
	viewStatus.FieldID = "view_status_id"
	viewStatus.ReadOnly = true
	viewDefinition := baseTable(
		"open_items_view", "open_items_view",
		[]schema.FieldDefinition{viewTitle, viewStatus},
	)
	viewDefinition.Kind = schema.TableKindView
	viewDefinition.View = &schema.ViewSpec{SourceTableID: source.TableID}
	created, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: viewDefinition, ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatalf("create view: %#v", err)
	}
	collection, err := app.FindCollectionByNameOrId(created.PhysicalName)
	if err != nil || !collection.IsView() || collection.ViewQuery == "" {
		t.Fatalf("persisted collection is not a PB view: collection=%#v err=%v", collection, err)
	}
	resetApp(t, app)
	app = bootstrapApp(t, dataDir)
	catalog = schemaapi.New(app)
	kernel = mutation.New(app, mutation.MetadataSchemaSource{})
	collection, err = app.FindCollectionByNameOrId(created.PhysicalName)
	if err != nil || !collection.IsView() || collection.ViewQuery == "" {
		t.Fatalf("restarted collection is not a PB view: collection=%#v err=%v", collection, err)
	}
	described, err := catalog.Describe(ctx, created.TableID)
	if err != nil || described.Kind != schema.TableKindView ||
		described.View == nil || described.View.SourceTableID != source.TableID {
		t.Fatalf("described view mismatch: %#v err=%v", described, err)
	}

	querySource, err := queryschema.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(app, querySource, []byte("0123456789abcdef0123456789abcdef"))
	page, err := port.QueryPage(ctx, created.TableID, query.TableQuery{Limit: 10})
	if err != nil || len(page.Rows) != 2 {
		t.Fatalf("query view rows=%#v err=%v", page.Rows, err)
	}

	_, err = kernel.Preview(ctx, mutationRequest(
		created.TableID, created.SchemaRevision, "view-write",
		mutation.Operation{
			Kind:     mutation.OperationUpdate,
			RecordID: stringAddress("viewsource00001"),
			Values:   map[string]any{"title": "changed"},
		},
	))
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.table.read_only" {
		t.Fatalf("view mutation error = %#v", err)
	}
	_, err = kernel.Apply(ctx, mutationRequest(
		created.TableID, created.SchemaRevision, "view-write-apply",
		mutation.Operation{
			Kind:     mutation.OperationUpdate,
			RecordID: stringAddress("viewsource00001"),
			Values:   map[string]any{"title": "changed"},
		},
	))
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.table.read_only" {
		t.Fatalf("direct view apply error = %#v", err)
	}
	sourceCollection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	sourceRecord, err := app.FindRecordById(
		sourceCollection, "viewsource00001",
	)
	if err != nil || sourceRecord.GetString("title") != "Alpha" {
		t.Fatalf("source changed after rejected view apply: record=%#v err=%v", sourceRecord, err)
	}
	page, err = port.QueryPage(ctx, created.TableID, query.TableQuery{Limit: 10})
	if err != nil || len(page.Rows) != 2 {
		t.Fatalf("query view after rejected apply rows=%#v err=%v", page.Rows, err)
	}
	for _, row := range page.Rows {
		if row["title"] == "changed" {
			t.Fatalf("view changed after rejected apply: %#v", page.Rows)
		}
	}
	_, err = catalog.DeleteTable(ctx, source.TableID, 1)
	var schemaErr *schema.ProductError
	if !errors.As(err, &schemaErr) ||
		schemaErr.Code != "schema.table.referenced" {
		t.Fatalf("view source delete error = %#v", err)
	}
}

func TestSchemaCatalogRejectsViewSourceTypeAndProjectionMismatches(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)
	source, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"typed_source", "typed_source",
			[]schema.FieldDefinition{
				field("count_id", "count", schema.FieldKindScalar, schema.DataTypeInteger),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := field("view_count_id", "count", schema.FieldKindScalar, schema.DataTypeShortText)
	wrong.ReadOnly = true
	view := baseTable("typed_view", "typed_view", []schema.FieldDefinition{wrong})
	view.Kind = schema.TableKindView
	view.View = &schema.ViewSpec{SourceTableID: source.TableID}
	_, err = catalog.ValidateChange(ctx, schemaapi.Change{
		Definition: view, ExpectedRevision: 0,
	})
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.view.field_mismatch" ||
		productErr.Path != "fields[0].dataType" {
		t.Fatalf("view mismatch error = %#v", err)
	}
}
