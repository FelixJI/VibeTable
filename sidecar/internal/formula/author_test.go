package formula

import (
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestCanonicalizeDisplaySourceUsesPermanentNamesAndRelationAggregates(t *testing.T) {
	lines := scalarField("lines_id", "f_lines", schema.DataTypeRelation)
	lines.DisplayName = "明细"
	lines.Kind = schema.FieldKindRelation
	lines.Relation = &schema.RelationSpec{
		TargetTableID: "line_items", Cardinality: "many", DeletePolicy: "setNull",
	}
	shipping := scalarField("shipping_id", "f_shipping", schema.DataTypeFloat)
	shipping.DisplayName = "运费"
	amount := scalarField("amount_id", "f_amount", schema.DataTypeFloat)
	amount.DisplayName = "金额"
	definition := formulaTable(lines, shipping)
	target := formulaTable(amount)
	target.TableID = "line_items"

	canonical, formulaErr := CanonicalizeDisplaySource(
		definition,
		map[string]schema.TableDefinition{"f_lines": target},
		`SUM({明细}.{金额}) + {运费}`,
	)
	if formulaErr != nil {
		t.Fatal(formulaErr)
	}
	if canonical != `relationSum(f_lines, "f_amount") + f_shipping` {
		t.Fatalf("canonical source = %q", canonical)
	}
}

func TestCanonicalizeDisplaySourceRejectsAmbiguousDisplayName(t *testing.T) {
	left := scalarField("left_id", "f_left", schema.DataTypeFloat)
	right := scalarField("right_id", "f_right", schema.DataTypeFloat)
	left.DisplayName = "金额"
	right.DisplayName = "金额"

	_, formulaErr := CanonicalizeDisplaySource(
		formulaTable(left, right), nil, `{金额} * 2.0`,
	)
	assertFormulaCode(t, formulaErr, "formula.dependency")
}
