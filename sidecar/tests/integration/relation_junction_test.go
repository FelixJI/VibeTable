package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestSchemaCatalogValidatesJunctionAndM2AReferences(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)

	orders, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("junction_orders", "junction_orders", []schema.FieldDefinition{
			field("number_id", "number", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"junction_products", "junction_services"} {
		if _, err := catalog.ApplyChange(ctx, schemaapi.Change{
			Definition: baseTable(table, table, []schema.FieldDefinition{
				field("name_id", "name", schema.FieldKindScalar, schema.DataTypeShortText),
			}),
			ExpectedRevision: 0,
		}); err != nil {
			t.Fatal(err)
		}
	}

	source := relationField(
		"order_id", "order", "junction_orders", "one", false,
	)
	target := relationField(
		"product_id", "product", "junction_products", "one", false,
	)
	if _, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"junction_rows", "junction_rows",
			[]schema.FieldDefinition{
				source,
				target,
				field("target_id", "target_id", schema.FieldKindScalar, schema.DataTypeShortText),
				field("target_table", "target_table", schema.FieldKindScalar, schema.DataTypeShortText),
				field("quantity_id", "quantity", schema.FieldKindScalar, schema.DataTypeInteger),
			},
		),
		ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}

	valid := orders
	valid.Fields = append(valid.Fields, junctionProjection(
		"items_id", "items", schema.RelationSpec{
			Mode: "junction", TargetTableID: "junction_products",
			Cardinality: "many", DeletePolicy: "cascade",
			JunctionTableID:       stringPtr("junction_rows"),
			JunctionSourceFieldID: "order_id",
			JunctionTargetFieldID: "product_id",
		},
	))
	if _, err := catalog.ValidateChange(ctx, schemaapi.Change{
		Definition: valid, ExpectedRevision: 1,
	}); err != nil {
		t.Fatalf("valid junction rejected: %#v", err)
	}

	invalid := valid
	invalid.Fields[len(invalid.Fields)-1].Relation.JunctionSourceFieldID = "quantity_id"
	_, err = catalog.ValidateChange(ctx, schemaapi.Change{
		Definition: invalid, ExpectedRevision: 1,
	})
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.relation.junction_source_invalid" {
		t.Fatalf("invalid junction source error = %#v", err)
	}

	m2a := orders
	m2a.Fields = append(m2a.Fields, junctionProjection(
		"content_id", "content", schema.RelationSpec{
			Mode: "m2a", TargetTableID: "junction_products",
			Cardinality: "many", DeletePolicy: "cascade",
			JunctionTableID:              stringPtr("junction_rows"),
			JunctionSourceFieldID:        "order_id",
			JunctionTargetFieldID:        "target_id",
			JunctionDiscriminatorFieldID: "target_table",
			AllowedTargetTableIDs: []string{
				"junction_products", "junction_services",
			},
		},
	))
	if _, err := catalog.ValidateChange(ctx, schemaapi.Change{
		Definition: m2a, ExpectedRevision: 1,
	}); err != nil {
		t.Fatalf("valid m2a rejected: %#v", err)
	}
}

func TestJunctionAndM2ADeltaUseMutationKernelAuditReplayAndRollback(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)

	orders, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("delta_orders", "delta_orders", []schema.FieldDefinition{
			field("number_id", "number", schema.FieldKindScalar, schema.DataTypeShortText),
			autoDateField("updated_at", schema.AutoDateRoleUpdatedAt),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]schema.TableDefinition{}
	for _, table := range []string{"delta_products", "delta_services"} {
		created, createErr := catalog.ApplyChange(ctx, schemaapi.Change{
			Definition: baseTable(table, table, []schema.FieldDefinition{
				field("name_id", "name", schema.FieldKindScalar, schema.DataTypeShortText),
			}),
			ExpectedRevision: 0,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		targets[table] = created
	}
	source := relationField(
		"order_id", "order", "delta_orders", "one", false,
	)
	target := relationField(
		"product_id", "product", "delta_products", "one", false,
	)
	junction, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("delta_links", "delta_links", []schema.FieldDefinition{
			source,
			target,
			field("quantity_id", "quantity", schema.FieldKindScalar, schema.DataTypeInteger),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	contentJunction, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"delta_content_links", "delta_content_links",
			[]schema.FieldDefinition{
				source,
				field("target_id", "target_id", schema.FieldKindScalar, schema.DataTypeShortText),
				field("target_table", "target_table", schema.FieldKindScalar, schema.DataTypeShortText),
				field("quantity_id", "quantity", schema.FieldKindScalar, schema.DataTypeInteger),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	withRelations := orders
	withRelations.Fields = append(
		withRelations.Fields,
		junctionProjection("items_id", "items", schema.RelationSpec{
			Mode: "junction", TargetTableID: "delta_products",
			Cardinality: "many", DeletePolicy: "cascade",
			JunctionTableID:       stringPtr("delta_links"),
			JunctionSourceFieldID: "order_id",
			JunctionTargetFieldID: "product_id",
		}),
		junctionProjection("content_id", "content", schema.RelationSpec{
			Mode: "m2a", TargetTableID: "delta_products",
			Cardinality: "many", DeletePolicy: "cascade",
			JunctionTableID:              stringPtr("delta_content_links"),
			JunctionSourceFieldID:        "order_id",
			JunctionTargetFieldID:        "target_id",
			JunctionDiscriminatorFieldID: "target_table",
			AllowedTargetTableIDs: []string{
				"delta_products", "delta_services",
			},
		}),
	)
	quantityLookup := field(
		"quantity_lookup_id", "quantity_lookup",
		schema.FieldKindLookup, schema.DataTypeLookup,
	)
	quantityLookup.StorageType = schema.StorageNumber
	quantityLookup.ReadOnly = true
	quantityLookup.Lookup = &schema.LookupSpec{
		RelationFieldID: "items_id", TargetFieldID: "quantity_id",
		JunctionFieldID: "quantity_id", Aggregate: "none",
	}
	contentNameLookup := field(
		"content_name_id", "content_name",
		schema.FieldKindLookup, schema.DataTypeLookup,
	)
	contentNameLookup.StorageType = schema.StorageText
	contentNameLookup.ReadOnly = true
	contentNameLookup.Lookup = &schema.LookupSpec{
		RelationFieldID: "content_id", TargetFieldID: "name_id",
		TargetFieldIDs: map[string]string{
			"delta_products": "name_id", "delta_services": "name_id",
		},
		Aggregate: "none",
	}
	withRelations.Fields = append(
		withRelations.Fields, quantityLookup, contentNameLookup,
	)
	orders, err = catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: withRelations, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	orderID := "deltaorder00001"
	productID := "deltaproduct001"
	serviceID := "deltaservice001"
	for _, insertion := range []struct {
		table    string
		revision string
		id       string
		field    string
		value    string
	}{
		{"delta_orders", orders.SchemaRevision, orderID, "number", "O-1"},
		{"delta_products", targets["delta_products"].SchemaRevision, productID, "name", "Product"},
		{"delta_services", targets["delta_services"].SchemaRevision, serviceID, "name", "Service"},
	} {
		if _, applyErr := kernel.Apply(ctx, mutationRequest(
			insertion.table, insertion.revision, "insert-"+insertion.id,
			mutation.Operation{
				Kind: mutation.OperationInsert, RecordID: &insertion.id,
				Values: map[string]any{insertion.field: insertion.value},
			},
		)); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	orderCollection, _ := app.FindCollectionByNameOrId("delta_orders")
	orderBeforeDelta, err := app.FindRecordById(orderCollection, orderID)
	if err != nil {
		t.Fatal(err)
	}
	updatedBeforeDelta := orderBeforeDelta.GetString("updated_at")
	sourceQuery, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(
		app,
		query.NewPort(
			app, sourceQuery),

		kernel,
	)
	add := relation.DeltaRequest{
		RelationID: "delta_orders.items_id", SourceRecordID: orderID,
		SchemaRevision: orders.SchemaRevision,
		Adds: []relation.TargetRef{{
			TableID: "delta_products", RecordID: productID, Label: "Product",
			JunctionValues: map[string]any{"quantity": 2},
		}},
		Updates: []relation.JunctionUpdate{}, Removes: []relation.TargetRef{},
		RequestID: "junction-add", IdempotencyKey: "junction-add",
		Actor: mutation.Actor{Type: "user", ID: "local"},
	}
	applied, err := service.ApplyDelta(ctx, add)
	if err != nil || applied.Receipt.Status != mutation.StatusApplied ||
		len(applied.Current) != 1 ||
		fmt.Sprint(applied.Current[0].JunctionValues["quantity"]) != "2" {
		t.Fatalf("junction add = %#v, err=%v", applied, err)
	}
	orderAfterDelta, err := app.FindRecordById(orderCollection, orderID)
	if err != nil || orderAfterDelta.GetString("updated_at") != updatedBeforeDelta {
		t.Fatalf(
			"junction-only delta touched source updatedAt: before=%q after=%q err=%v",
			updatedBeforeDelta,
			orderAfterDelta.GetString("updated_at"),
			err,
		)
	}
	replayed, err := service.ApplyDelta(ctx, add)
	if err != nil || replayed.Receipt.Status != mutation.StatusReplayed {
		t.Fatalf("junction replay = %#v, err=%v", replayed, err)
	}
	audit, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events", "request_id='junction-add'",
	)
	if err != nil || audit.GetString("table_id") != junction.TableID ||
		audit.GetString("operation") != "insert" {
		t.Fatalf("junction audit = %#v, err=%v", audit, err)
	}

	m2a := relation.DeltaRequest{
		RelationID: "delta_orders.content_id", SourceRecordID: orderID,
		SchemaRevision: orders.SchemaRevision,
		Adds: []relation.TargetRef{{
			TableID: "delta_services", RecordID: serviceID, Label: "Service",
			JunctionValues: map[string]any{"quantity": 1},
		}},
		Updates: []relation.JunctionUpdate{}, Removes: []relation.TargetRef{},
		RequestID: "m2a-add", IdempotencyKey: "m2a-add",
		Actor: mutation.Actor{Type: "user", ID: "local"},
	}
	m2aResult, err := service.ApplyDelta(ctx, m2a)
	if err != nil || len(m2aResult.Current) != 1 ||
		m2aResult.Current[0].TableID != "delta_services" {
		t.Fatalf("m2a add = %#v, err=%v", m2aResult, err)
	}
	orderRecord, err := app.FindRecordById(orderCollection, orderID)
	if err != nil {
		t.Fatal(err)
	}
	calculated, err := lookup.NewCalculator().Calculate(
		ctx, app, orders, orderRecord,
	)
	if err != nil || fmt.Sprint(calculated["quantity_lookup"]) != "2" ||
		calculated["content_name"] != "Service" {
		t.Fatalf("junction/m2a lookup = %#v, err=%v", calculated, err)
	}
	dependencies, err := app.FindRecordsByFilter(
		"vibetable_formula_dependencies",
		"source_table_id='delta_orders' && dependency_kind='lookup'",
		"+target_table_id,+target_field_id",
		0,
		0,
	)
	// Target-field changes and junction membership/context changes must all
	// enqueue the same source-table recomputation.
	if err != nil || len(dependencies) != 8 {
		t.Fatalf("lookup reverse dependencies = %#v, err=%v", dependencies, err)
	}

	bad := m2a
	bad.RequestID, bad.IdempotencyKey = "m2a-bad", "m2a-bad"
	bad.Adds[0].TableID = "delta_orders"
	_, err = service.ApplyDelta(ctx, bad)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "relation.target_invalid" {
		t.Fatalf("m2a allowlist error = %#v", err)
	}
	bypassID := "m2abypass00001"
	_, err = kernel.Apply(ctx, mutationRequest(
		"delta_content_links", contentJunction.SchemaRevision, "m2a-kernel-bypass",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &bypassID,
			Values: map[string]any{
				"order": orderID, "target_id": orderID,
				"target_table": "delta_orders", "quantity": 7,
			},
		},
	))
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.relation.m2a_target_not_allowed" {
		t.Fatalf("m2a kernel allowlist error = %#v", err)
	}
	links, err := app.FindRecordsByFilter(
		"delta_links", "", "", 0, 0,
	)
	contentLinks, contentErr := app.FindRecordsByFilter(
		"delta_content_links", "", "", 0, 0,
	)
	if err != nil || contentErr != nil ||
		len(links) != 1 || len(contentLinks) != 1 {
		t.Fatalf("failed m2a delta changed junction rows: %d, %v", len(links), err)
	}
}

func relationField(
	id, name, target, cardinality string,
	readOnly bool,
) schema.FieldDefinition {
	result := field(id, name, schema.FieldKindRelation, schema.DataTypeRelation)
	result.ReadOnly = readOnly
	result.Relation = &schema.RelationSpec{
		TargetTableID: target,
		Cardinality:   cardinality,
		DeletePolicy:  "setNull",
	}
	result.Constraints = []schema.FieldConstraint{{
		Kind:          schema.ConstraintRelation,
		TargetTableID: target,
		Cardinality:   cardinality,
		DeletePolicy:  "setNull",
	}}
	return result
}

func junctionProjection(
	id, name string,
	relation schema.RelationSpec,
) schema.FieldDefinition {
	result := field(id, name, schema.FieldKindRelation, schema.DataTypeRelation)
	result.ReadOnly = true
	result.Relation = &relation
	result.Constraints = []schema.FieldConstraint{}
	return result
}

func stringPtr(value string) *string {
	return &value
}
