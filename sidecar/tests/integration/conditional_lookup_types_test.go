package integration_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestConditionalLookupTypedPredicatesRespectNullAndDateStorage(t *testing.T) {
	cases := []struct {
		name             string
		kind             v2.LogicalType
		equal, different any
	}{
		{"text", v2.LogicalText, "中文A", "中文a"},
		{"empty", v2.LogicalText, "", "other"},
		{"zero", v2.LogicalNumber, float64(0), float64(1)},
		{"false", v2.LogicalBool, false, true},
		{"date", v2.LogicalDate, "2026-09-28", "2026-09-29"},
		{"instant", v2.LogicalDateTime, "2026-09-28T08:00:00+08:00", "2026-09-28T01:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := bootstrapApp(t, queryTempDir(t))
			defer resetApp(t, app)
			ctx := context.Background()
			current := createV2IntegrationTable(t, ctx, app, "当前", "typed-current")
			source := createV2IntegrationTable(t, ctx, app, "来源", "typed-source")
			left := createV2IntegrationField(t, ctx, app, current.TableID, fieldDraftForIntegration(t, tc.kind, "当前值"), "typed-left")
			right := createV2IntegrationField(t, ctx, app, source.TableID, fieldDraftForIntegration(t, tc.kind, "来源值"), "typed-right")
			currentTable, _ := schemaexecution.Describe(ctx, app, current.TableID)
			currentCollection, _ := app.FindCollectionByNameOrId(current.PhysicalName)
			sourceCollection, _ := app.FindCollectionByNameOrId(source.PhysicalName)
			row := core.NewRecord(currentCollection)
			row.Set(left.Definition.Identity.PhysicalName, tc.equal)
			row.Set(left.Definition.Value.Presence.PhysicalName, true)
			if err := app.Save(row); err != nil {
				t.Fatal(err)
			}
			for index, value := range []any{tc.equal, tc.different, nil} {
				target := core.NewRecord(sourceCollection)
				target.Id = fmt.Sprintf("typed%010d", index)
				if value != nil {
					target.Set(right.Definition.Identity.PhysicalName, value)
					target.Set(right.Definition.Value.Presence.PhysicalName, true)
				}
				if err := app.Save(target); err != nil {
					t.Fatal(err)
				}
			}
			condition := &v2.LookupCondition{SourceTableID: source.TableID, Match: "all", Rules: []v2.LookupConditionRule{
				{SourceFieldID: right.FieldID, Operator: "eq", Operand: &v2.LookupOperand{Kind: "field", FieldID: left.FieldID}},
			}}
			field := v2.FieldDefinition{LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{}, TargetFieldID: right.FieldID, Condition: condition}}
			calculate := func(want int) lookup.CellValue {
				cell, err := lookup.NewCalculator().CalculateFieldPage(ctx, app, currentTable, row, field, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				if cell.ProvenanceTotal != want {
					t.Fatalf("want %d matches: %#v", want, cell)
				}
				return cell
			}
			matched := calculate(1)
			condition.Rules[0].Operand = &v2.LookupOperand{Kind: "constant", Value: tc.equal}
			if constant := calculate(1); !reflect.DeepEqual(matched.Value, constant.Value) {
				t.Fatal("constant and field operands diverged")
			}
			if tc.kind == v2.LogicalNumber || tc.kind == v2.LogicalDate || tc.kind == v2.LogicalDateTime {
				condition.Rules[0].Operator = "gte"
				condition.Rules = append(condition.Rules, v2.LookupConditionRule{SourceFieldID: right.FieldID, Operator: "lt", Operand: &v2.LookupOperand{Kind: "constant", Value: tc.different}})
				calculate(1)
				condition.Match = "any"
				calculate(2)
				condition.Match = "all"
				condition.Rules = condition.Rules[:1]
			}
			condition.Rules[0].Operator = "ne"
			calculate(1)
			condition.Match = "any"
			condition.Rules = append(condition.Rules, v2.LookupConditionRule{SourceFieldID: right.FieldID, Operator: "is_null"})
			calculate(2)
			condition.Match = "all"
			calculate(0)
			condition.Rules = condition.Rules[:1]
			condition.Rules[0].Operator = "eq"
			condition.Rules[0].Operand = &v2.LookupOperand{Kind: "field", FieldID: left.FieldID}
			row.Set(left.Definition.Value.Presence.PhysicalName, false)
			calculate(0)
		})
	}
}
