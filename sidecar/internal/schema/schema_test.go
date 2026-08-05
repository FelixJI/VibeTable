package schema_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestStorageMappingsCoverEveryProductDataType(t *testing.T) {
	cases := map[schema.DataType]schema.StorageType{
		schema.DataTypeShortText: schema.StorageText, schema.DataTypeLongText: schema.StorageEditor,
		schema.DataTypeRichText: schema.StorageEditor, schema.DataTypeBoolean: schema.StorageBool,
		schema.DataTypeInteger: schema.StorageNumber, schema.DataTypeFloat: schema.StorageNumber,
		schema.DataTypeDecimal: schema.StorageNumber, schema.DataTypeDate: schema.StorageDate,
		schema.DataTypeDateTime: schema.StorageDate, schema.DataTypeAutoDate: schema.StorageAutodate,
		schema.DataTypeTime: schema.StorageText, schema.DataTypeEmail: schema.StorageEmail,
		schema.DataTypeURL: schema.StorageURL, schema.DataTypeUUID: schema.StorageText,
		schema.DataTypeSelect: schema.StorageSelect, schema.DataTypeMultiSelect: schema.StorageSelect,
		schema.DataTypeJSON: schema.StorageJSON, schema.DataTypeGeoPoint: schema.StorageGeoPoint,
		schema.DataTypeGeoJSON: schema.StorageJSON, schema.DataTypeFile: schema.StorageFile,
		schema.DataTypeRelation: schema.StorageRelation, schema.DataTypeLookup: schema.StorageJSON,
		schema.DataTypeFormula: schema.StorageJSON, schema.DataTypeList: schema.StorageJSON,
		schema.DataTypeHash: schema.StorageText, schema.DataTypeSecret: schema.StorageText,
	}
	for dataType, want := range cases {
		capability, err := schema.CapabilityFor(dataType)
		if err != nil {
			t.Fatalf("CapabilityFor(%q): %v", dataType, err)
		}
		if capability.Storage != want {
			t.Errorf("CapabilityFor(%q).Storage = %q, want %q", dataType, capability.Storage, want)
		}
	}
}

func TestFrozenTableDefinitionFixtureDecodesAndRoundTripsExactly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v1", "fixtures", "table-definition.json"))
	if err != nil {
		t.Fatal(err)
	}
	var definition schema.TableDefinition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatalf("decode frozen fixture: %v", err)
	}
	if definition.ContractVersion != "1.0" || definition.SchemaRevision != "schema_0007" {
		t.Fatalf("wire header mismatch: %#v", definition)
	}
	if definition.ArchivePolicy.Mode != schema.ArchiveModeStatus ||
		len(definition.Fields) != 11 ||
		definition.Fields[5].Relation.Cardinality != "one" ||
		definition.Fields[6].Lookup.RelationFieldID != "fld_product" ||
		definition.Fields[7].Formula.Status != "ready" ||
		definition.Fields[8].AttachmentPolicy.MaxBytesPerFile != 10485760 ||
		definition.Indexes[0].Name != "ux_order_lines_sku" {
		t.Fatalf("frozen v1 shapes were not preserved: %#v", definition)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("encode frozen fixture: %v", err)
	}
	var before, after any
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("fixture semantic roundtrip changed\nbefore=%s\nafter=%s", raw, encoded)
	}
	err = schema.Validate(definition)
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "schema.constraint.unsupported" {
		t.Fatalf("fixture capability validation = %#v, want stable unsupported error", err)
	}
}

func TestFrozenTableDefinitionRejectsUnknownPropertiesAtEveryNestedShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "contracts", "v1", "fixtures", "table-definition.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(map[string]any){
		"table": func(value map[string]any) {
			value["typo"] = true
		},
		"archive policy": func(value map[string]any) {
			value["archivePolicy"].(map[string]any)["typo"] = true
		},
		"field": func(value map[string]any) {
			value["fields"].([]any)[0].(map[string]any)["typo"] = true
		},
		"editor": func(value map[string]any) {
			value["fields"].([]any)[0].(map[string]any)["editor"].(map[string]any)["typo"] = true
		},
		"relation": func(value map[string]any) {
			value["fields"].([]any)[5].(map[string]any)["relation"].(map[string]any)["typo"] = true
		},
		"lookup": func(value map[string]any) {
			value["fields"].([]any)[6].(map[string]any)["lookup"].(map[string]any)["typo"] = true
		},
		"formula": func(value map[string]any) {
			value["fields"].([]any)[7].(map[string]any)["formula"].(map[string]any)["typo"] = true
		},
		"attachment policy": func(value map[string]any) {
			value["fields"].([]any)[8].(map[string]any)["attachmentPolicy"].(map[string]any)["typo"] = true
		},
		"auto date": func(value map[string]any) {
			value["fields"].([]any)[9].(map[string]any)["autoDate"].(map[string]any)["typo"] = true
		},
		"index": func(value map[string]any) {
			value["indexes"].([]any)[0].(map[string]any)["typo"] = true
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var definition schema.TableDefinition
			if err := json.Unmarshal(encoded, &definition); err == nil {
				t.Fatal("unknown property was silently accepted")
			}
		})
	}
}

func TestAutoDateSpecStrictlyRoundTripsAndRejectsUnknownProperties(t *testing.T) {
	field := autoDateField("created_at", schema.AutoDateRoleCreatedAt)
	raw, err := json.Marshal(field)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped schema.FieldDefinition
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if roundTripped.AutoDate == nil ||
		roundTripped.AutoDate.Role != schema.AutoDateRoleCreatedAt {
		t.Fatalf("autoDate round trip = %#v", roundTripped.AutoDate)
	}
	var invalid schema.FieldDefinition
	if err := json.Unmarshal([]byte(`{
		"fieldId":"created_at","physicalName":"created_at","displayName":"Created",
		"kind":"system","dataType":"autoDate","storageType":"autodate",
		"nullable":false,"defaultValue":null,"constraints":[],
		"editor":{"kind":"readonly","config":{}},"readOnly":true,
		"autoDate":{"role":"createdAt","unexpected":true},
		"formula":null,"relation":null,"lookup":null,"attachmentPolicy":null
	}`), &invalid); err == nil {
		t.Fatal("unknown autoDate property was accepted")
	}
}

func TestViewDefinitionRoundTripsAndRejectsRawQueryProperties(t *testing.T) {
	definition := validDefinition()
	definition.Kind = schema.TableKindView
	definition.View = &schema.ViewSpec{SourceTableID: "orders"}
	definition.Fields[0].ReadOnly = true
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped schema.TableDefinition
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if roundTripped.View == nil || roundTripped.View.SourceTableID != "orders" {
		t.Fatalf("view round trip = %#v", roundTripped.View)
	}

	var invalid schema.TableDefinition
	if err := json.Unmarshal([]byte(`{
		"contractVersion":"1.0","tableId":"view","physicalName":"view",
		"displayName":"View","kind":"view","schemaRevision":"schema_0000",
		"archivePolicy":{"mode":"none","fieldId":null,"archivedValue":null},
		"view":{"sourceTableId":"orders","query":"select * from orders"},
		"fields":[],"indexes":[]
	}`), &invalid); err == nil {
		t.Fatal("view contract accepted a raw query property")
	}
}

func TestBaseTableMayBootstrapEmptyButViewStillRequiresAProjection(t *testing.T) {
	definition := validDefinition()
	definition.Fields = []schema.FieldDefinition{}
	if err := schema.Validate(definition); err != nil {
		t.Fatalf("empty base table bootstrap rejected: %v", err)
	}

	definition.Kind = schema.TableKindView
	definition.View = &schema.ViewSpec{SourceTableID: "source_orders"}
	err := schema.Validate(definition)
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.table.fields_required" {
		t.Fatalf("empty view validation = %#v, want fields_required", err)
	}
}

func TestRelationDefinitionStrictlyExpressesDirectJunctionAndM2A(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		relation schema.RelationSpec
		wantCode string
	}{
		{
			name: "junction",
			relation: schema.RelationSpec{
				Mode: "junction", TargetTableID: "products",
				Cardinality: "many", DeletePolicy: "cascade",
				JunctionTableID:       ptr("order_products"),
				JunctionSourceFieldID: "order_id",
				JunctionTargetFieldID: "product_id",
			},
		},
		{
			name: "m2a",
			relation: schema.RelationSpec{
				Mode: "m2a", TargetTableID: "products",
				Cardinality: "many", DeletePolicy: "cascade",
				JunctionTableID:              ptr("order_content"),
				JunctionSourceFieldID:        "order_id",
				JunctionTargetFieldID:        "target_id",
				JunctionDiscriminatorFieldID: "target_table",
				AllowedTargetTableIDs:        []string{"products", "services"},
			},
		},
		{
			name: "m2a requires allowlist",
			relation: schema.RelationSpec{
				Mode: "m2a", TargetTableID: "products",
				Cardinality: "many", DeletePolicy: "cascade",
				JunctionTableID:              ptr("order_content"),
				JunctionSourceFieldID:        "order_id",
				JunctionTargetFieldID:        "target_id",
				JunctionDiscriminatorFieldID: "target_table",
			},
			wantCode: "schema.field.invalid_relation",
		},
		{
			name: "junction rejects discriminator",
			relation: schema.RelationSpec{
				Mode: "junction", TargetTableID: "products",
				Cardinality: "many", DeletePolicy: "cascade",
				JunctionTableID:              ptr("order_products"),
				JunctionSourceFieldID:        "order_id",
				JunctionTargetFieldID:        "product_id",
				JunctionDiscriminatorFieldID: "target_table",
			},
			wantCode: "schema.field.invalid_relation",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			definition := validDefinition()
			relationField := schema.FieldDefinition{
				FieldID: "items", PhysicalName: "items", DisplayName: "Items",
				Kind: schema.FieldKindRelation, DataType: schema.DataTypeRelation,
				StorageType: schema.StorageRelation, Nullable: true, ReadOnly: true,
				Constraints: []schema.FieldConstraint{},
				Editor:      schema.EditorDefinition{Kind: "relation", Config: map[string]any{}},
				Relation:    &testCase.relation,
			}
			definition.Fields = append(definition.Fields, relationField)
			err := schema.Validate(definition)
			if testCase.wantCode == "" {
				if err != nil {
					t.Fatalf("Validate() = %#v", err)
				}
				raw, marshalErr := json.Marshal(relationField.Relation)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				var roundTripped schema.RelationSpec
				if unmarshalErr := json.Unmarshal(raw, &roundTripped); unmarshalErr != nil {
					t.Fatal(unmarshalErr)
				}
				if !reflect.DeepEqual(roundTripped, testCase.relation) {
					t.Fatalf("relation round trip = %#v", roundTripped)
				}
				return
			}
			var productErr *schema.ProductError
			if !errors.As(err, &productErr) || productErr.Code != testCase.wantCode {
				t.Fatalf("Validate() = %#v, want %s", err, testCase.wantCode)
			}
		})
	}
}

func ptr(value string) *string {
	return &value
}

func TestValidateRejectsExactDecimalUntilRoundTripPOCExists(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*schema.FieldDefinition)
	}{
		{"exactDecimal", func(field *schema.FieldDefinition) {
			field.DataType, field.StorageType = schema.DataTypeDecimal, schema.StorageNumber
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition()
			test.mutate(&definition.Fields[0])
			err := schema.Validate(definition)
			var productErr *schema.ProductError
			if !errors.As(err, &productErr) || productErr.Code != "schema.constraint.unsupported" {
				t.Fatalf("Validate() = %#v, want schema.constraint.unsupported", err)
			}
		})
	}
}

func TestValidateAcceptsProductEnforcedDefaultsJSONSchemaAndRestrict(t *testing.T) {
	definition := validDefinition()
	definition.Fields[0].DefaultValue = "new"
	definition.Fields[0].Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintDefault, Value: "new",
	}}

	jsonField := schema.FieldDefinition{
		FieldID: "metadata", PhysicalName: "metadata", DisplayName: "Metadata",
		Kind: schema.FieldKindScalar, DataType: schema.DataTypeJSON,
		StorageType: schema.StorageJSON, Nullable: false,
		DefaultValue: map[string]any{"source": "manual"},
		Constraints: []schema.FieldConstraint{{
			Kind: schema.ConstraintJSONSchema,
			Schema: map[string]any{
				"type":     "object",
				"required": []any{"source"},
				"properties": map[string]any{
					"source": map[string]any{"type": "string"},
				},
			},
		}},
		Editor: schema.EditorDefinition{Kind: "json", Config: map[string]any{}},
	}
	relationField := schema.FieldDefinition{
		FieldID: "owner", PhysicalName: "owner", DisplayName: "Owner",
		Kind: schema.FieldKindRelation, DataType: schema.DataTypeRelation,
		StorageType: schema.StorageRelation, Nullable: true,
		Constraints: []schema.FieldConstraint{{
			Kind: schema.ConstraintRelation, TargetTableID: "users",
			Cardinality: "one", DeletePolicy: "restrict",
		}},
		Editor: schema.EditorDefinition{Kind: "relation", Config: map[string]any{}},
		Relation: &schema.RelationSpec{
			TargetTableID: "users", Cardinality: "one", DeletePolicy: "restrict",
		},
	}
	definition.Fields = append(definition.Fields, jsonField, relationField)

	if err := schema.Validate(definition); err != nil {
		t.Fatalf("Validate() = %#v, want product-enforced schema accepted", err)
	}
}

func TestValidateRejectsInvalidSemanticDefaultsAtStableFieldPaths(t *testing.T) {
	cases := []struct {
		name       string
		dataType   schema.DataType
		storage    schema.StorageType
		value      any
		constraint *schema.FieldConstraint
		wantPath   string
	}{
		{
			name: "time", dataType: schema.DataTypeTime, storage: schema.StorageText,
			value: "24:00:00", wantPath: "fields[0].defaultValue",
		},
		{
			name: "uuid", dataType: schema.DataTypeUUID, storage: schema.StorageText,
			value: "not-a-uuid", wantPath: "fields[0].defaultValue",
		},
		{
			name: "list", dataType: schema.DataTypeList, storage: schema.StorageJSON,
			value: map[string]any{"not": "an array"}, wantPath: "fields[0].defaultValue",
		},
		{
			name: "json schema instance", dataType: schema.DataTypeJSON, storage: schema.StorageJSON,
			value: map[string]any{"count": "wrong"},
			constraint: &schema.FieldConstraint{
				Kind: schema.ConstraintJSONSchema,
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"count": map[string]any{"type": "integer"},
					},
				},
			},
			wantPath: "fields[0].defaultValue",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			definition := validDefinition()
			field := &definition.Fields[0]
			field.DataType, field.StorageType = testCase.dataType, testCase.storage
			field.DefaultValue = testCase.value
			field.Constraints = []schema.FieldConstraint{}
			if testCase.constraint != nil {
				field.Constraints = append(field.Constraints, *testCase.constraint)
			}
			err := schema.Validate(definition)
			var productErr *schema.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "schema.field.invalid_default" ||
				productErr.Path != testCase.wantPath {
				t.Fatalf("Validate() = %#v, want schema.field.invalid_default at %s", err, testCase.wantPath)
			}
		})
	}
}

func TestValidateRejectsMalformedJSONSchemaAtConstraintPath(t *testing.T) {
	definition := validDefinition()
	field := &definition.Fields[0]
	field.DataType, field.StorageType = schema.DataTypeJSON, schema.StorageJSON
	field.Constraints = []schema.FieldConstraint{{
		Kind:   schema.ConstraintJSONSchema,
		Schema: map[string]any{"type": "definitely-not-a-json-schema-type"},
	}}

	err := schema.Validate(definition)
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.field.invalid_constraint" ||
		productErr.Path != "fields[0].constraints[0].schema" {
		t.Fatalf("Validate() = %#v, want invalid JSON Schema at exact path", err)
	}
}

func TestRevisionFormatParsesStably(t *testing.T) {
	for _, revision := range []int64{0, 1, 7, 10000} {
		wire := schema.FormatSchemaRevision(revision)
		parsed, err := schema.ParseSchemaRevision(wire)
		if err != nil || parsed != revision {
			t.Fatalf("roundtrip %d via %q = %d, %v", revision, wire, parsed, err)
		}
	}
}

func TestCompileManyRelationUsesPocketBaseMultiValueStorage(t *testing.T) {
	definition := schema.FieldDefinition{
		FieldID: "lines_id", PhysicalName: "lines", DisplayName: "Lines",
		Kind: schema.FieldKindRelation, DataType: schema.DataTypeRelation,
		StorageType: schema.StorageRelation, Nullable: true,
		Constraints: []schema.FieldConstraint{{
			Kind: schema.ConstraintRelation, TargetTableID: "lines_table",
			Cardinality: "many", DeletePolicy: "setNull",
		}},
		Editor: schema.EditorDefinition{Kind: "relation", Config: map[string]any{}},
		Relation: &schema.RelationSpec{
			TargetTableID: "lines_table", Cardinality: "many", DeletePolicy: "setNull",
		},
	}
	compiled, err := schema.CompileField(definition, func(string) (string, error) {
		return "lines_collection", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	relation, ok := compiled.(*core.RelationField)
	if !ok || !relation.IsMultiple() || relation.MaxSelect != int(1<<53-1) {
		t.Fatalf("compiled many relation = %#v", compiled)
	}
}

func TestValidateReportsEmptyFormulaAtTheSourcePath(t *testing.T) {
	definition := validDefinition()
	definition.Fields[0] = schema.FieldDefinition{
		FieldID: "total", PhysicalName: "total", DisplayName: "Total",
		Kind: schema.FieldKindFormula, DataType: schema.DataTypeFormula,
		StorageType: schema.StorageNumber, Nullable: true, ReadOnly: true,
		Constraints: []schema.FieldConstraint{},
		Editor:      schema.EditorDefinition{Kind: "formula", Config: map[string]any{}},
		Formula: &schema.FormulaSpec{
			Language: "cel-v1", Source: "", ResultType: schema.DataTypeFloat,
			Version: 1, Status: "ready",
		},
	}

	err := schema.Validate(definition)
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) {
		t.Fatalf("Validate() = %#v, want ProductError", err)
	}
	if productErr.Code != "schema.field.invalid_formula" ||
		productErr.Path != "fields[0].formula.source" {
		t.Fatalf("Validate() = %#v, want fields[0].formula.source", productErr)
	}
}

func TestAutoDateRolesValidateAndCompileToPocketBaseTruthTable(t *testing.T) {
	for _, test := range []struct {
		name     string
		role     schema.AutoDateRole
		onCreate bool
		onUpdate bool
	}{
		{
			name: "created at", role: schema.AutoDateRoleCreatedAt,
			onCreate: true, onUpdate: false,
		},
		{
			name: "updated at", role: schema.AutoDateRoleUpdatedAt,
			onCreate: true, onUpdate: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := autoDateField("timestamp", test.role)
			definition := validDefinition()
			definition.Fields = []schema.FieldDefinition{field}
			if err := schema.Validate(definition); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
			compiled, err := schema.CompileField(field, nil)
			if err != nil {
				t.Fatalf("CompileField() = %v", err)
			}
			actual, ok := compiled.(*core.AutodateField)
			if !ok {
				t.Fatalf("CompileField() type = %T", compiled)
			}
			if actual.OnCreate != test.onCreate ||
				actual.OnUpdate != test.onUpdate ||
				actual.System {
				t.Fatalf(
					"compiled autoDate = onCreate:%t onUpdate:%t system:%t",
					actual.OnCreate, actual.OnUpdate, actual.System,
				)
			}
		})
	}
}

func TestAutoDateValidationReportsStableErrors(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*schema.TableDefinition)
		wantCode string
	}{
		{
			name: "missing role",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields = []schema.FieldDefinition{autoDateField("created", "")}
				definition.Fields[0].AutoDate = nil
			},
			wantCode: "schema.field.autodate_role_required",
		},
		{
			name: "unknown role",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields = []schema.FieldDefinition{autoDateField("created", "later")}
			},
			wantCode: "schema.field.autodate_role_invalid",
		},
		{
			name: "duplicate role",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields = []schema.FieldDefinition{
					autoDateField("created", schema.AutoDateRoleCreatedAt),
					autoDateField("also_created", schema.AutoDateRoleCreatedAt),
				}
			},
			wantCode: "schema.field.autodate_role_duplicate",
		},
		{
			name: "config on another field",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields[0].AutoDate = &schema.AutoDateSpec{
					Role: schema.AutoDateRoleCreatedAt,
				}
			},
			wantCode: "schema.field.autodate_config_forbidden",
		},
		{
			name: "nullable",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields = []schema.FieldDefinition{
					autoDateField("created", schema.AutoDateRoleCreatedAt),
				}
				definition.Fields[0].Nullable = true
			},
			wantCode: "schema.field.autodate_nullable_forbidden",
		},
		{
			name: "constraints",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields = []schema.FieldDefinition{
					autoDateField("created", schema.AutoDateRoleCreatedAt),
				}
				definition.Fields[0].Constraints = []schema.FieldConstraint{{
					Kind: schema.ConstraintRequired, Value: true,
				}}
			},
			wantCode: "schema.field.autodate_constraints_forbidden",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition()
			test.mutate(&definition)
			err := schema.Validate(definition)
			var productErr *schema.ProductError
			if !errors.As(err, &productErr) || productErr.Code != test.wantCode {
				t.Fatalf("Validate() = %#v, want %s", err, test.wantCode)
			}
		})
	}
}

func TestValidateRejectsMisappliedDuplicateAndBrokenIndexConstraints(t *testing.T) {
	one, five := 1.0, 5.0
	cases := []struct {
		name     string
		mutate   func(*schema.TableDefinition)
		wantCode string
	}{
		{
			name: "range on text",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields[0].Constraints = []schema.FieldConstraint{{
					Kind: schema.ConstraintRange, Min: &one, Max: &five,
				}}
			},
			wantCode: "schema.field.invalid_constraint",
		},
		{
			name: "pattern on number",
			mutate: func(definition *schema.TableDefinition) {
				field := &definition.Fields[0]
				field.DataType, field.StorageType = schema.DataTypeInteger, schema.StorageNumber
				field.Constraints = []schema.FieldConstraint{{
					Kind: schema.ConstraintPattern, Pattern: "^[0-9]+$",
				}}
			},
			wantCode: "schema.field.invalid_constraint",
		},
		{
			name: "duplicate unique",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields[0].Constraints = []schema.FieldConstraint{
					{Kind: schema.ConstraintUnique, Value: false},
					{Kind: schema.ConstraintUnique, Value: true},
				}
			},
			wantCode: "schema.field.duplicate_constraint",
		},
		{
			name: "field index unknown id",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields[0].Constraints = []schema.FieldConstraint{{
					Kind: schema.ConstraintIndex, FieldIDs: []string{"missing"},
				}}
			},
			wantCode: "schema.index.unknown_field",
		},
		{
			name: "field index duplicate id",
			mutate: func(definition *schema.TableDefinition) {
				definition.Fields[0].Constraints = []schema.FieldConstraint{{
					Kind: schema.ConstraintIndex, FieldIDs: []string{"title", "title"},
				}}
			},
			wantCode: "schema.index.duplicate_field",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition()
			test.mutate(&definition)
			err := schema.Validate(definition)
			var productErr *schema.ProductError
			if !errors.As(err, &productErr) || productErr.Code != test.wantCode {
				t.Fatalf("Validate() = %#v, want %s", err, test.wantCode)
			}
		})
	}
}

func validDefinition() schema.TableDefinition {
	return schema.TableDefinition{
		ContractVersion: schema.ContractVersion,
		TableID:         "orders", PhysicalName: "orders", DisplayName: "Orders",
		Kind: schema.TableKindBase, SchemaRevision: schema.FormatSchemaRevision(0),
		ArchivePolicy: schema.ArchivePolicy{Mode: schema.ArchiveModeNone},
		Fields: []schema.FieldDefinition{{
			FieldID: "title", PhysicalName: "title", DisplayName: "Title",
			Kind: schema.FieldKindScalar, DataType: schema.DataTypeShortText,
			StorageType: schema.StorageText, Nullable: true,
			Constraints: []schema.FieldConstraint{},
			Editor:      schema.EditorDefinition{Kind: "text", Config: map[string]any{}},
		}},
		Indexes: []schema.IndexDefinition{},
	}
}

func autoDateField(
	name string,
	role schema.AutoDateRole,
) schema.FieldDefinition {
	return schema.FieldDefinition{
		FieldID: name, PhysicalName: name, DisplayName: name,
		Kind: schema.FieldKindSystem, DataType: schema.DataTypeAutoDate,
		StorageType: schema.StorageAutodate, Nullable: false, ReadOnly: true,
		Constraints: []schema.FieldConstraint{},
		Editor:      schema.EditorDefinition{Kind: "readonly", Config: map[string]any{}},
		AutoDate:    &schema.AutoDateSpec{Role: role},
	}
}
