package query

import (
	"strings"
	"testing"
)

func TestComputedEnvelopeDecodesReadyValuesAndHidesPendingValues(t *testing.T) {
	readyText := FieldDescriptor{
		PhysicalName: "computed", Type: FieldTypeText,
		ComputedEnvelope: true, ComputedReady: true,
		ComputedDefinitionVersion:   2,
		ComputedDependencyWatermark: "sha256:dependency",
	}
	readyRaw := `{"state":"ready","value":"READY","version":{"definitionVersion":2,"sourceDataRevision":4,"dependencyWatermark":"sha256:dependency"}}`
	if got := decodeFieldValue(readyRaw, readyText); got != "READY" {
		t.Fatalf("decoded ready computed text = %#v", got)
	}
	readyNumber := readyText
	readyNumber.Type = FieldTypeNumber
	numberRaw := `{"state":"ready","value":12.5,"version":{"definitionVersion":2,"sourceDataRevision":4,"dependencyWatermark":"sha256:dependency"}}`
	if got := decodeFieldValue(numberRaw, readyNumber); got != 12.5 {
		t.Fatalf("decoded ready computed number = %#v", got)
	}
	pending := readyText
	pending.ComputedReady = false
	pending.ComputedStatus = "updating"
	pending.ComputedError = &ComputedDiagnostic{
		Code: "calculation.pending", Path: "fields.computed",
		Message: "formula value is being recalculated", Details: map[string]any{},
	}
	got, ok := decodeFieldValue(`"STALE"`, pending).(map[string]any)
	if !ok || got["state"] != "updating" || got["value"] != nil ||
		got["diagnostic"] != pending.ComputedError {
		t.Fatalf("pending computed envelope = %#v", got)
	}
}

func TestComputedEnvelopeUsesDecodedScalarForPredicates(t *testing.T) {
	descriptor := TableDescriptor{
		DatabaseID: "db", TableID: "orders", PhysicalName: "orders",
		PrimaryKey: "id", SchemaRevision: "schema_1", DataRevision: 1,
		RowRevisionName: "__vt_row_revision",
		Fields: map[string]FieldDescriptor{
			"id": {PhysicalName: "id", Type: FieldTypeText},
			"total": {
				PhysicalName: "f_total", Type: FieldTypeNumber,
				ComputedEnvelope: true, ComputedReady: true,
				ComputedDefinitionVersion:   2,
				ComputedDependencyWatermark: "sha256:dependency",
			},
		},
		PresenceFields: map[string]string{},
	}
	compiled, err := Compile(descriptor, TableQuery{Filters: []FilterExpression{{
		Field: "total", Operator: OperatorGreater, Value: 10,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, `json_extract("f_total", '$.value')`) {
		t.Fatalf("computed predicate did not unwrap JSON storage: %s", compiled.SQL)
	}
	descriptor.Fields["total"] = FieldDescriptor{
		PhysicalName: "f_total", Type: FieldTypeNumber,
		ComputedEnvelope: true, ComputedReady: false,
		ComputedDefinitionVersion:   2,
		ComputedDependencyWatermark: "sha256:dependency",
	}
	compiled, err = Compile(descriptor, TableQuery{Filters: []FilterExpression{{
		Field: "total", Operator: OperatorGreater, Value: 10,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "CASE WHEN 0") ||
		!strings.Contains(compiled.SQL, "ELSE NULL END) >") {
		t.Fatalf("pending computed predicate did not become NULL: %s", compiled.SQL)
	}
}

func TestComputedEnvelopeGroupsOnlyFreshProjectedValues(t *testing.T) {
	descriptor := TableDescriptor{
		DatabaseID: "db", TableID: "orders", PhysicalName: "orders",
		PrimaryKey: "id", SchemaRevision: "schema_1", DataRevision: 1,
		RowRevisionName: "__vt_row_revision",
		Fields: map[string]FieldDescriptor{
			"id": {PhysicalName: "id", Type: FieldTypeText},
			"customer_name": {
				PhysicalName: "f_customer_name", Type: FieldTypeText,
				ComputedEnvelope: true, ComputedReady: true,
				ComputedDefinitionVersion:   2,
				ComputedDependencyWatermark: "sha256:dependency",
			},
		},
		PresenceFields: map[string]string{},
	}
	plan, err := compileViewGroups(
		descriptor,
		TableQuery{Limit: 100},
		[]GroupSpec{{Field: "customer_name"}},
		nil,
		0,
		101,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.sql, `json_extract("f_customer_name", '$.state') = 'ready'`) ||
		!strings.Contains(plan.sql, `json_extract("f_customer_name", '$.value')`) {
		t.Fatalf("computed group did not enforce freshness and project value: %s", plan.sql)
	}
	rows, err := decodeViewGroupRows(
		[]map[string]any{{"group_0": "Ada", "row_count": int64(2)}},
		plan,
	)
	if err != nil || len(rows) != 1 || rows[0].Key[0] != "Ada" {
		t.Fatalf("computed group rows = %#v, err=%v", rows, err)
	}
}

func TestRelationPathUsesDecodedComputedTargetAndHidesPendingTarget(t *testing.T) {
	descriptor := TableDescriptor{
		DatabaseID: "db", TableID: "orders", PhysicalName: "orders",
		PrimaryKey: "id", SchemaRevision: "schema_1", DataRevision: 1,
		RowRevisionName: "__vt_row_revision",
		Fields: map[string]FieldDescriptor{
			"id": {PhysicalName: "id", Type: FieldTypeText},
			"customer": {
				PhysicalName: "f_customer", Type: FieldTypeRelation,
				Relation: &RelationDescriptor{
					TableName: "customers", PrimaryKey: "id",
					RowRevisionName: "__vt_row_revision",
					Fields: map[string]FieldDescriptor{
						"score": {
							PhysicalName: "f_score", Type: FieldTypeNumber,
							ComputedEnvelope: true, ComputedReady: true,
							ComputedDefinitionVersion:   2,
							ComputedDependencyWatermark: "sha256:dependency",
						},
					},
				},
			},
		},
		PresenceFields: map[string]string{},
	}
	query := TableQuery{Filters: []FilterExpression{{
		Field: "customer.score", Operator: OperatorGreater, Value: 10,
	}}}
	compiled, err := Compile(descriptor, query)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, `json_extract(r0."f_score", '$.value')`) {
		t.Fatalf("relation target did not unwrap computed storage: %s", compiled.SQL)
	}

	target := descriptor.Fields["customer"]
	target.Relation.Fields["score"] = FieldDescriptor{
		PhysicalName: "f_score", Type: FieldTypeNumber,
		ComputedEnvelope: true, ComputedReady: false,
		ComputedDefinitionVersion:   2,
		ComputedDependencyWatermark: "sha256:dependency",
	}
	descriptor.Fields["customer"] = target
	compiled, err = Compile(descriptor, query)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, `CASE WHEN 0`) ||
		!strings.Contains(compiled.SQL, `ELSE NULL END) FROM "customers"`) {
		t.Fatalf("pending relation target did not become NULL: %s", compiled.SQL)
	}
}
