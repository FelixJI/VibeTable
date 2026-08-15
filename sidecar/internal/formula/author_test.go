package formula

import (
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestCanonicalizeDisplaySourceUsesPermanentNamesAndRelationAggregates(t *testing.T) {
	lines := relationField("lines_id", "f_lines", "line_items")
	lines.DisplayName = "明细"
	shipping := scalarField("shipping_id", "f_shipping", numberType)
	shipping.DisplayName = "运费"
	amount := scalarField("amount_id", "f_amount", numberType)
	amount.DisplayName = "金额"
	definition := formulaTable(lines, shipping)
	target := formulaTable(amount)
	target.Snapshot.TableID = "line_items"

	canonical, formulaErr := CanonicalizeExecutionDisplaySource(
		definition,
		map[string]schemaexecution.Table{"f_lines": target},
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
	left := scalarField("left_id", "f_left", numberType)
	right := scalarField("right_id", "f_right", numberType)
	left.DisplayName = "金额"
	right.DisplayName = "金额"

	_, formulaErr := CanonicalizeExecutionDisplaySource(
		formulaTable(left, right), nil, `{金额} * 2.0`,
	)
	assertFormulaCode(t, formulaErr, "formula.dependency")
}
