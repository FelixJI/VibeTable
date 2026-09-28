package queryschema

import (
	"context"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// TestLookupResultFieldTypeMapsAggregations pins the public query typing:
// counts and numeric summaries are number/one values (numeric filters, sorts
// and group metrics apply), distinct keeps the JSON collection, and the
// legacy values shape is unchanged.
func TestLookupResultFieldTypeMapsAggregations(t *testing.T) {
	t.Parallel()
	table := schemaexecution.Table{Snapshot: v2.SchemaSnapshot{TableID: "tbl_current0001", Fields: []v2.FieldDefinition{
		{Identity: v2.FieldIdentity{FieldID: "fld_amount0001", PhysicalName: "f_amount0001"}, LogicalType: v2.LogicalNumber},
	}}}
	cases := []struct {
		name string
		spec v2.LookupSpec
		want query.FieldType
	}{
		{
			name: "sum over path",
			spec: v2.LookupSpec{TargetFieldID: "fld_amount0001", Aggregation: v2.LookupAggregationSum},
			want: query.FieldTypeNumber,
		},
		{
			name: "countRecords over condition",
			spec: v2.LookupSpec{TargetFieldID: "fld_amount0001", Aggregation: v2.LookupAggregationCountRecords, Condition: &v2.LookupCondition{SourceTableID: "tbl_source0001"}},
			want: query.FieldTypeNumber,
		},
		{
			name: "legacy condition distinct",
			spec: v2.LookupSpec{TargetFieldID: "fld_amount0001", Condition: &v2.LookupCondition{SourceTableID: "tbl_source0001", Distinct: true}},
			want: query.FieldTypeJSON,
		},
		{
			name: "explicit values keeps legacy typing",
			spec: v2.LookupSpec{TargetFieldID: "fld_amount0001", Aggregation: v2.LookupAggregationValues},
			want: query.FieldTypeNumber,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, err := (&Source{}).lookupResultFieldType(context.Background(), nil, table, testCase.spec)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("lookupResultFieldType(%s) = %q, want %q", testCase.spec.Aggregation, got, testCase.want)
			}
		})
	}
}
