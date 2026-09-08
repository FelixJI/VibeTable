package queryschema

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestNewDerivesStableOpaqueDatabaseIdentity(t *testing.T) {
	first, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.databaseID) != 64 {
		t.Fatalf("database id length = %d", len(first.databaseID))
	}
	if first.databaseID == second.databaseID {
		t.Fatal("different data directories produced the same database id")
	}
}

func TestQueryFieldTypeMapsSchemaV2LogicalTypes(t *testing.T) {
	cases := []struct {
		name  string
		field v2.FieldDefinition
		want  query.FieldType
	}{
		{name: "text", field: v2Field("text", v2.LogicalText), want: query.FieldTypeText},
		{name: "number", field: v2Field("number", v2.LogicalNumber), want: query.FieldTypeNumber},
		{name: "bool", field: v2Field("bool", v2.LogicalBool), want: query.FieldTypeBool},
		{name: "dateTime", field: v2Field("date", v2.LogicalDateTime), want: query.FieldTypeDate},
		{name: "autoDate", field: v2Field("auto", v2.LogicalAutoDate), want: query.FieldTypeDate},
		{
			name: "many relation",
			field: v2.FieldDefinition{
				Identity:    v2.FieldIdentity{PhysicalName: "f_relation"},
				LogicalType: v2.LogicalRelation,
				Relation:    &v2.RelationSpec{Cardinality: "many"},
			},
			want: query.FieldTypeMultiRelation,
		},
		{name: "json", field: v2Field("json", v2.LogicalJSON), want: query.FieldTypeJSON},
		{
			name: "formula result",
			field: v2.FieldDefinition{
				Identity:    v2.FieldIdentity{PhysicalName: "f_formula"},
				LogicalType: v2.LogicalFormula,
				Formula:     &v2.FormulaSpec{ResultType: v2.LogicalNumber},
			},
			want: query.FieldTypeNumber,
		},
		{
			name: "lookup envelope",
			field: v2.FieldDefinition{
				Identity:    v2.FieldIdentity{PhysicalName: "f_lookup"},
				LogicalType: v2.LogicalLookup,
				Lookup:      &v2.LookupSpec{TargetFieldID: "fld_target"},
			},
			want: query.FieldTypeJSON,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := queryFieldType(test.field)
			if err != nil || got != test.want {
				t.Fatalf("queryFieldType(%#v) = %q, %v; want %q", test.field, got, err, test.want)
			}
		})
	}
}

func TestDescribeFieldExposesTerminalFormulaStateAndDiagnostic(t *testing.T) {
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_formula", PhysicalName: "f_total"},
		LogicalType: v2.LogicalFormula,
		Formula:     &v2.FormulaSpec{ResultType: v2.LogicalNumber},
	}
	for _, test := range []struct {
		status string
		code   string
	}{
		{status: "failed", code: "calculation.failed"},
		{status: "cancelled", code: "calculation.cancelled"},
	} {
		t.Run(test.status, func(t *testing.T) {
			descriptor, err := (&Source{}).describeField(
				context.Background(),
				nil,
				schemaexecution.Table{
					Snapshot: v2.SchemaSnapshot{Fields: []v2.FieldDefinition{field}},
					FormulaRuntime: map[string]schemaexecution.FormulaRuntime{
						"fld_formula": {Status: test.status, Version: 1},
					},
				},
				field,
			)
			if err != nil {
				t.Fatal(err)
			}
			if descriptor.ComputedReady || descriptor.ComputedStatus != test.status ||
				descriptor.ComputedError == nil || descriptor.ComputedError.Code != test.code {
				t.Fatalf("%s formula descriptor = %#v", test.status, descriptor)
			}
		})
	}
}

func TestEnumDescriptorUsesActiveStableOptionIdentities(t *testing.T) {
	descriptor := enumDescriptor(v2.FieldDefinition{
		LogicalType: v2.LogicalSelect,
		Select: &v2.SelectSpec{Options: []v2.SelectOption{
			{OptionID: "opt_active", State: v2.OptionActive},
			{OptionID: "opt_retired", State: v2.OptionRetired},
		}},
	})
	if descriptor.Multiple || len(descriptor.Options) != 1 ||
		descriptor.Options[0].Value != "opt_active" ||
		descriptor.Options[0].StorageValue != "opt_active" {
		t.Fatalf("enum descriptor = %#v", descriptor)
	}
}

func TestMapSchemaErrorPreservesTableNotFoundContract(t *testing.T) {
	err := mapSchemaError(errors.Join(errors.New("describe table"), schemaexecution.ErrTableNotFound))
	var productErr *query.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "query.table.not_found" {
		t.Fatalf("mapped error = %#v", err)
	}
}

func v2Field(name string, logicalType v2.LogicalType) v2.FieldDefinition {
	return v2.FieldDefinition{
		Identity:    v2.FieldIdentity{PhysicalName: "f_" + name},
		LogicalType: logicalType,
	}
}
