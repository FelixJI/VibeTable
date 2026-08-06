package queryschema

import (
	"context"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
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

func TestQueryFieldTypeMapsProductStorageWithoutProviderNames(t *testing.T) {
	cases := []struct {
		field schema.FieldDefinition
		want  query.FieldType
	}{
		{schema.FieldDefinition{DataType: schema.DataTypeShortText}, query.FieldTypeText},
		{schema.FieldDefinition{DataType: schema.DataTypeInteger}, query.FieldTypeNumber},
		{schema.FieldDefinition{DataType: schema.DataTypeBoolean}, query.FieldTypeBool},
		{schema.FieldDefinition{DataType: schema.DataTypeDateTime}, query.FieldTypeDate},
		{
			schema.FieldDefinition{
				DataType: schema.DataTypeAutoDate,
				AutoDate: &schema.AutoDateSpec{Role: schema.AutoDateRoleUpdatedAt},
			},
			query.FieldTypeDate,
		},
		{
			schema.FieldDefinition{
				DataType: schema.DataTypeRelation,
				Relation: &schema.RelationSpec{Cardinality: "many"},
			},
			query.FieldTypeMultiRelation,
		},
		{schema.FieldDefinition{DataType: schema.DataTypeJSON}, query.FieldTypeJSON},
		{
			schema.FieldDefinition{
				DataType: schema.DataTypeFormula,
				Formula:  &schema.FormulaSpec{ResultType: schema.DataTypeFloat},
			},
			query.FieldTypeNumber,
		},
		{
			schema.FieldDefinition{
				DataType:    schema.DataTypeLookup,
				StorageType: schema.StorageDate,
			},
			query.FieldTypeDate,
		},
	}
	for _, test := range cases {
		got, err := queryFieldType(test.field)
		if err != nil || got != test.want {
			t.Fatalf("queryFieldType(%#v) = %q, %v; want %q", test.field, got, err, test.want)
		}
	}
}

func TestDescribeFieldExposesCancelledFormulaDiagnostic(t *testing.T) {
	descriptor, err := (&Source{}).describeField(
		context.Background(),
		nil,
		schema.FieldDefinition{
			PhysicalName: "f_total",
			Kind:         schema.FieldKindFormula,
			DataType:     schema.DataTypeFormula,
			Formula: &schema.FormulaSpec{
				ResultType: schema.DataTypeFloat,
				Status:     "cancelled",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.ComputedReady || descriptor.ComputedStatus != "error" ||
		descriptor.ComputedError == nil ||
		descriptor.ComputedError.Code != "calculation.cancelled" {
		t.Fatalf("cancelled formula descriptor = %#v", descriptor)
	}
}
