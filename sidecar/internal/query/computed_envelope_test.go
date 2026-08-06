package query

import (
	"strings"
	"testing"
)

func TestComputedEnvelopeDecodesReadyValuesAndHidesPendingValues(t *testing.T) {
	readyText := FieldDescriptor{
		PhysicalName: "computed", Type: FieldTypeText,
		ComputedEnvelope: true, ComputedReady: true,
	}
	if got := decodeFieldValue(`"READY"`, readyText); got != "READY" {
		t.Fatalf("decoded ready computed text = %#v", got)
	}
	readyNumber := readyText
	readyNumber.Type = FieldTypeNumber
	if got := decodeFieldValue("12.5", readyNumber); got != 12.5 {
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
		Fields: map[string]FieldDescriptor{
			"id": {PhysicalName: "id", Type: FieldTypeText},
			"total": {
				PhysicalName: "f_total", Type: FieldTypeNumber,
				ComputedEnvelope: true, ComputedReady: true,
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
	if !strings.Contains(compiled.SQL, `json_extract("f_total", '$') >`) {
		t.Fatalf("computed predicate did not unwrap JSON storage: %s", compiled.SQL)
	}
	descriptor.Fields["total"] = FieldDescriptor{
		PhysicalName: "f_total", Type: FieldTypeNumber,
		ComputedEnvelope: true, ComputedReady: false,
	}
	compiled, err = Compile(descriptor, TableQuery{Filters: []FilterExpression{{
		Field: "total", Operator: OperatorGreater, Value: 10,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "NULL >") {
		t.Fatalf("pending computed predicate did not become NULL: %s", compiled.SQL)
	}
}

func TestRelationPathUsesDecodedComputedTargetAndHidesPendingTarget(t *testing.T) {
	descriptor := TableDescriptor{
		DatabaseID: "db", TableID: "orders", PhysicalName: "orders",
		PrimaryKey: "id", SchemaRevision: "schema_1", DataRevision: 1,
		Fields: map[string]FieldDescriptor{
			"id": {PhysicalName: "id", Type: FieldTypeText},
			"customer": {
				PhysicalName: "f_customer", Type: FieldTypeRelation,
				Relation: &RelationDescriptor{
					TableName: "customers", PrimaryKey: "id",
					Fields: map[string]FieldDescriptor{
						"score": {
							PhysicalName: "f_score", Type: FieldTypeNumber,
							ComputedEnvelope: true, ComputedReady: true,
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
	if !strings.Contains(compiled.SQL, `SELECT json_extract(r0."f_score", '$')`) {
		t.Fatalf("relation target did not unwrap computed storage: %s", compiled.SQL)
	}

	target := descriptor.Fields["customer"]
	target.Relation.Fields["score"] = FieldDescriptor{
		PhysicalName: "f_score", Type: FieldTypeNumber,
		ComputedEnvelope: true, ComputedReady: false,
	}
	descriptor.Fields["customer"] = target
	compiled, err = Compile(descriptor, query)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, `SELECT NULL FROM "customers"`) {
		t.Fatalf("pending relation target did not become NULL: %s", compiled.SQL)
	}
}
