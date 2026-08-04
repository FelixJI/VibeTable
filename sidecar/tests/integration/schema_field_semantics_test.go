package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestTypedSelectValuesRoundTripThroughRealPocketBase(t *testing.T) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	ctx := context.Background()

	maxOne := 1
	status := field("status_id", "status", schema.FieldKindScalar, schema.DataTypeSelect)
	status.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintEnum, MaxSelected: &maxOne,
		Options: []schema.SelectOption{
			{Value: json.Number("1"), DisplayName: "Number one"},
			{Value: "1", DisplayName: "String one"},
			{Value: true, DisplayName: "Enabled"},
		},
	}}
	maxTwo := 2
	flags := field("flags_id", "flags", schema.FieldKindScalar, schema.DataTypeMultiSelect)
	flags.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintEnum, Multiple: true, MaxSelected: &maxTwo,
		Options: []schema.SelectOption{
			{Value: true, DisplayName: "Enabled"},
			{Value: json.Number("2"), DisplayName: "Number two"},
			{Value: "2", DisplayName: "String two"},
		},
	}}
	catalog := schemaapi.New(app)
	definition, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"typed_enums", "typed_enums",
			[]schema.FieldDefinition{status, flags},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatalf("create typed enum table: %#v", err)
	}
	described, err := catalog.Describe(ctx, definition.TableID)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONValue(t, described.Fields[0].Constraints[0].Options[0].Value, json.Number("1"))
	assertJSONValue(t, described.Fields[0].Constraints[0].Options[1].Value, "1")
	assertJSONValue(t, described.Fields[0].Constraints[0].Options[2].Value, true)

	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	rows := []struct {
		id     string
		key    string
		status any
		flags  []any
	}{
		{"typedenum000001", "typed-number", json.Number("1"), []any{true, json.Number("2")}},
		{"typedenum000002", "typed-string", "1", []any{"2"}},
		{"typedenum000003", "typed-bool", true, []any{true}},
	}
	for _, row := range rows {
		if _, err := kernel.Apply(ctx, mutationRequest(
			definition.TableID, definition.SchemaRevision, row.key,
			mutation.Operation{
				Kind: mutation.OperationInsert, RecordID: &row.id,
				Values: map[string]any{"status": row.status, "flags": row.flags},
			},
		)); err != nil {
			t.Fatalf("insert %s: %#v", row.key, err)
		}
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	numberRecord, _ := app.FindRecordById(collection, rows[0].id)
	stringRecord, _ := app.FindRecordById(collection, rows[1].id)
	if numberRecord.GetString("status") == stringRecord.GetString("status") {
		t.Fatalf("typed number and string collapsed in storage: %q", numberRecord.GetString("status"))
	}

	source, err := queryschema.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(
		app,
		source)

	page, err := port.QueryPage(ctx, definition.TableID, query.TableQuery{
		Sorts: []query.SortCondition{{Field: "id", Direction: query.SortAscending}},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("query typed enums: %v", err)
	}
	if len(page.Rows) != 3 {
		t.Fatalf("typed enum rows = %d", len(page.Rows))
	}
	assertJSONValue(t, page.Rows[0]["status"], json.Number("1"))
	assertJSONValue(t, page.Rows[0]["flags"], []any{true, json.Number("2")})
	assertJSONValue(t, page.Rows[1]["status"], "1")
	assertJSONValue(t, page.Rows[1]["flags"], []any{"2"})
	assertJSONValue(t, page.Rows[2]["status"], true)

	for _, testCase := range []struct {
		name  string
		value any
		id    string
	}{
		{"number", json.Number("1"), rows[0].id},
		{"string", "1", rows[1].id},
		{"boolean", true, rows[2].id},
	} {
		t.Run("filter_"+testCase.name, func(t *testing.T) {
			filtered, err := port.QueryPage(ctx, definition.TableID, query.TableQuery{
				Filters: []query.FilterExpression{{
					Field: "status", Operator: query.OperatorEqual, Value: testCase.value,
				}},
				Limit: 10,
			})
			if err != nil {
				t.Fatalf("filter %s: %v", testCase.name, err)
			}
			if len(filtered.Rows) != 1 || filtered.Rows[0]["id"] != testCase.id {
				t.Fatalf("filter %s rows = %#v", testCase.name, filtered.Rows)
			}
		})
	}
}

func assertJSONValue(t *testing.T, got, want any) {
	t.Helper()
	gotRaw, gotErr := json.Marshal(got)
	wantRaw, wantErr := json.Marshal(want)
	if gotErr != nil || wantErr != nil || string(gotRaw) != string(wantRaw) {
		t.Fatalf("JSON value = %s (%T), want %s (%T)", gotRaw, got, wantRaw, want)
	}
}

func TestMutationKernelAppliesDefaultsAndValidatesProductOnlyFieldSemantics(
	t *testing.T,
) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	ctx := context.Background()

	count := field("count_id", "count", schema.FieldKindScalar, schema.DataTypeInteger)
	count.Nullable = false
	count.DefaultValue = 3
	count.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintDefault, Value: 3,
	}}
	timeField := field("time_id", "starts_at", schema.FieldKindScalar, schema.DataTypeTime)
	uuidField := field("uuid_id", "external_id", schema.FieldKindScalar, schema.DataTypeUUID)
	listField := field("list_id", "tags", schema.FieldKindScalar, schema.DataTypeList)
	jsonField := field("json_id", "metadata", schema.FieldKindScalar, schema.DataTypeJSON)
	jsonField.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintJSONSchema,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"count"},
			"properties": map[string]any{
				"count": map[string]any{"type": "integer", "minimum": 1},
			},
		},
	}}
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"semantic_rows", "semantic_rows",
			[]schema.FieldDefinition{count, timeField, uuidField, listField, jsonField},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatalf("create semantic table: %#v", err)
	}

	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	recordID := "semanticrow0001"
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.TableID, definition.SchemaRevision, "semantic-valid",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{
				"starts_at":   "23:59:59.125",
				"external_id": "c56a4180-65aa-42ec-a945-5fd21dec0538",
				"tags":        []any{"alpha", float64(2)},
				"metadata":    map[string]any{"count": float64(2)},
			},
		},
	))
	if err != nil {
		t.Fatalf("valid semantic mutation: %#v", err)
	}
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	record, err := app.FindRecordById(collection, recordID)
	if err != nil || record.GetInt("count") != 3 {
		t.Fatalf("default was not applied: record=%#v err=%v", record, err)
	}

	invalid := []struct {
		name  string
		field string
		value any
	}{
		{name: "time", field: "starts_at", value: "24:00:00"},
		{name: "uuid", field: "external_id", value: "not-a-uuid"},
		{name: "list", field: "tags", value: map[string]any{"wrong": true}},
		{name: "jsonSchema", field: "metadata", value: map[string]any{"count": "wrong"}},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			badID := "invalidrow00001"
			_, err := kernel.Apply(ctx, mutationRequest(
				definition.TableID, definition.SchemaRevision, "semantic-"+testCase.name,
				mutation.Operation{
					Kind: mutation.OperationInsert, RecordID: &badID,
					Values: map[string]any{
						"starts_at":    "23:59:59",
						"external_id":  "c56a4180-65aa-42ec-a945-5fd21dec0538",
						"tags":         []any{},
						"metadata":     map[string]any{"count": float64(2)},
						testCase.field: testCase.value,
					},
				},
			))
			var productErr *mutation.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "mutation.field.invalid_value" ||
				productErr.Path == nil ||
				*productErr.Path != "operations[0].values."+testCase.field {
				t.Fatalf("invalid %s error = %#v", testCase.name, err)
			}
		})
	}
}

func TestHashIsOneWayAndSensitiveFieldsNeverEnterQueryOrAuditPayloads(t *testing.T) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	ctx := context.Background()

	hashField := field("hash_id", "password_hash", schema.FieldKindScalar, schema.DataTypeHash)
	hashField.Nullable = false
	hashField.Editor.Kind = "hash"
	secretField := field("secret_id", "api_secret", schema.FieldKindScalar, schema.DataTypeSecret)
	secretField.Nullable = false
	secretField.Editor.Kind = "secret"
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"credentials", "credentials",
			[]schema.FieldDefinition{hashField, secretField},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatalf("create credentials table: %#v", err)
	}

	recordID := "credential00001"
	const password = "correct horse battery staple"
	const secret = "sk-do-not-leak"
	_, err = mutation.New(app, mutation.MetadataSchemaSource{}).Apply(
		ctx,
		mutationRequest(
			definition.TableID, definition.SchemaRevision, "credential-insert",
			mutation.Operation{
				Kind: mutation.OperationInsert, RecordID: &recordID,
				Values: map[string]any{"password_hash": password, "api_secret": secret},
			},
		),
	)
	if err != nil {
		t.Fatalf("insert credentials: %#v", err)
	}
	collection, _ := app.FindCollectionByNameOrId(definition.PhysicalName)
	if collection.Fields.GetByName("password_hash").Type() != core.FieldTypePassword {
		t.Fatalf("hash field compiled as %q, want password", collection.Fields.GetByName("password_hash").Type())
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil {
		t.Fatal(err)
	}
	storedHash := record.GetString("password_hash:hash")
	if storedHash == "" || storedHash == password || !strings.HasPrefix(storedHash, "$2") {
		t.Fatalf("hash storage is not one-way bcrypt: %q", storedHash)
	}

	source, err := queryschema.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := source.DescribeQueryTable(ctx, app, definition.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exposed := descriptor.Fields["password_hash"]; exposed {
		t.Fatal("one-way hash was exposed through QueryPort")
	}
	if _, exposed := descriptor.Fields["api_secret"]; exposed {
		t.Fatal("secret was exposed through QueryPort")
	}

	audit, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events", "request_id='req-credential-insert'",
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(audit.GetRaw("after_json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), password) ||
		strings.Contains(string(raw), secret) ||
		strings.Contains(string(raw), storedHash) {
		t.Fatalf("audit image leaked sensitive material: %s", raw)
	}
}

func TestRestrictDeletePolicyRejectsReferencedTarget(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)

	users, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("restrict_users", "restrict_users", []schema.FieldDefinition{
			field("name_id", "name", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := field("owner_id", "owner", schema.FieldKindRelation, schema.DataTypeRelation)
	owner.Relation = &schema.RelationSpec{
		TargetTableID: users.TableID, Cardinality: "one", DeletePolicy: "restrict",
	}
	owner.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRelation, TargetTableID: users.TableID,
		Cardinality: "one", DeletePolicy: "restrict",
	}}
	posts, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("restrict_posts", "restrict_posts", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			owner,
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	userID := "restrictusr0001"
	// PocketBase IDs are collection-local. A source row may legitimately have
	// the same ID as its target and must still count as a cross-table reference.
	postID := userID
	if _, err := kernel.Apply(ctx, mutationRequest(
		users.TableID, users.SchemaRevision, "restrict-user-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &userID,
			Values: map[string]any{"name": "Ada"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		posts.TableID, posts.SchemaRevision, "restrict-post-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &postID,
			Values: map[string]any{"title": "Referenced", "owner": userID},
		},
	)); err != nil {
		t.Fatal(err)
	}

	_, err = kernel.Apply(ctx, mutationRequest(
		users.TableID, users.SchemaRevision, "restrict-user-delete",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &userID},
	))
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.relation.restricted" {
		t.Fatalf("restricted delete error = %#v", err)
	}
	userCollection, _ := app.FindCollectionByNameOrId(users.PhysicalName)
	if _, findErr := app.FindRecordById(userCollection, userID); findErr != nil {
		t.Fatalf("restricted target was deleted: %v", findErr)
	}
}

func TestRestrictDeletePolicyChecksActualJunctionReferences(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)

	products, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"restrict_products",
			"restrict_products",
			[]schema.FieldDefinition{
				field("name_id", "name", schema.FieldKindScalar, schema.DataTypeShortText),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	orders, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"restrict_orders",
			"restrict_orders",
			[]schema.FieldDefinition{
				field("number_id", "number", schema.FieldKindScalar, schema.DataTypeShortText),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	orderRelation := relationField(
		"order_id", "order", orders.TableID, "one", false,
	)
	productRelation := relationField(
		"product_id", "product", products.TableID, "one", false,
	)
	junction, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"restrict_order_products",
			"restrict_order_products",
			[]schema.FieldDefinition{orderRelation, productRelation},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	orders.Fields = append(orders.Fields, junctionProjection(
		"products_id",
		"products",
		schema.RelationSpec{
			Mode:                  "junction",
			TargetTableID:         products.TableID,
			Cardinality:           "many",
			DeletePolicy:          "restrict",
			JunctionTableID:       stringPtr(junction.TableID),
			JunctionSourceFieldID: "order_id",
			JunctionTargetFieldID: "product_id",
		},
	))
	orders, err = catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: orders, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	orderID := "jorder000000001"
	unreferencedID := "jproduct0000001"
	referencedID := "jproduct0000002"
	junctionID := "jlink0000000001"
	for _, insertion := range []struct {
		tableID  string
		revision string
		request  string
		recordID string
		values   map[string]any
	}{
		{
			products.TableID, products.SchemaRevision, "insert-unreferenced",
			unreferencedID, map[string]any{"name": "Unused"},
		},
		{
			products.TableID, products.SchemaRevision, "insert-referenced",
			referencedID, map[string]any{"name": "Used"},
		},
		{
			orders.TableID, orders.SchemaRevision, "insert-order",
			orderID, map[string]any{"number": "O-1"},
		},
		{
			junction.TableID, junction.SchemaRevision, "insert-link",
			junctionID, map[string]any{
				"order": orderID, "product": referencedID,
			},
		},
	} {
		if _, applyErr := kernel.Apply(ctx, mutationRequest(
			insertion.tableID,
			insertion.revision,
			insertion.request,
			mutation.Operation{
				Kind:     mutation.OperationInsert,
				RecordID: &insertion.recordID,
				Values:   insertion.values,
			},
		)); applyErr != nil {
			t.Fatalf("%s: %v", insertion.request, applyErr)
		}
	}

	if _, err := kernel.Apply(ctx, mutationRequest(
		products.TableID,
		products.SchemaRevision,
		"delete-unreferenced-product",
		mutation.Operation{
			Kind: mutation.OperationDelete, RecordID: &unreferencedID,
		},
	)); err != nil {
		t.Fatalf("unreferenced junction target delete: %v", err)
	}
	_, err = kernel.Apply(ctx, mutationRequest(
		products.TableID,
		products.SchemaRevision,
		"delete-referenced-product",
		mutation.Operation{
			Kind: mutation.OperationDelete, RecordID: &referencedID,
		},
	))
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.relation.restricted" {
		t.Fatalf("referenced junction target delete error = %#v", err)
	}
}
