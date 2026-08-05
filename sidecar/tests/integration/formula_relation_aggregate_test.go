package integration_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestFormulaRelationAggregatesMoreThanTenThousandRecords(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)
	target, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"formula_scale_lines", "formula_scale_lines",
			[]schema.FieldDefinition{
				field("amount_id", "amount", schema.FieldKindScalar, schema.DataTypeFloat),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	relation := field("lines_id", "lines", schema.FieldKindRelation, schema.DataTypeRelation)
	relation.Relation = &schema.RelationSpec{
		TargetTableID: target.TableID, Cardinality: "many", DeletePolicy: "setNull",
	}
	relation.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRelation, TargetTableID: target.TableID,
		Cardinality: "many", DeletePolicy: "setNull",
	}}
	source, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"formula_scale_orders", "formula_scale_orders",
			[]schema.FieldDefinition{
				relation,
				formulaField(
					"sum_id", "line_sum", schema.DataTypeFloat,
					`relationSum(lines, "amount")`,
				),
				formulaField(
					"count_id", "line_count", schema.DataTypeInteger,
					"relationCount(lines)",
				),
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 10001
		)
		INSERT INTO formula_scale_lines (id, amount)
		SELECT printf('rel%012d', value), value FROM sequence
	`).WithContext(ctx).Execute(); err != nil {
		t.Fatalf("seed 10001 relation targets: %v", err)
	}
	var storedSum, storedMin, storedMax float64
	if err := app.DB().NewQuery(
		"SELECT SUM(amount), MIN(amount), MAX(amount) FROM formula_scale_lines",
	).Row(&storedSum, &storedMin, &storedMax); err != nil {
		t.Fatal(err)
	}
	if storedSum != 50_015_001 || storedMin != 1 || storedMax != 10_001 {
		t.Fatalf("seeded values = sum %v min %v max %v", storedSum, storedMin, storedMax)
	}
	recordIDs := make([]string, 10_001)
	for index := range recordIDs {
		recordIDs[index] = fmt.Sprintf("rel%012d", index+1)
	}
	collection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("lines", recordIDs)

	calculated, err := formula.NewCalculator(nil).Calculate(ctx, app, source, record)
	if err != nil {
		t.Fatalf("calculate 10001 relation aggregates: %v", err)
	}
	if calculated["line_sum"] != 50_015_001.0 {
		t.Fatalf("line_sum = %#v", calculated["line_sum"])
	}
	if calculated["line_count"] != int64(10_001) {
		t.Fatalf("line_count = %#v", calculated["line_count"])
	}
}
