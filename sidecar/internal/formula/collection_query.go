package formula

import (
	"context"

	"github.com/vibetable/vibetable/sidecar/internal/queryfilter"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

// Query predicates only narrow candidates. The complete CEL predicate still
// evaluates every returned row, including nullable comparison errors.
type collectionQuery struct {
	field    string
	operator queryfilter.Operator
	operand  *CompiledFormula
	logic    queryfilter.Logic
	children []*collectionQuery
}

func (compiler *Compiler) collectionQuery(definition schemaexecution.Table, source v2.SchemaSnapshot, expression *exprpb.Expr) *collectionQuery {
	call := expression.GetCallExpr()
	if call == nil || call.Target != nil || len(call.Args) != 2 {
		return nil
	}
	if call.Function == "_&&_" || call.Function == "_||_" || call.Function == "AND" || call.Function == "OR" {
		left := compiler.collectionQuery(definition, source, call.Args[0])
		right := compiler.collectionQuery(definition, source, call.Args[1])
		if left == nil || right == nil {
			return nil
		}
		logic := queryfilter.LogicAnd
		if call.Function == "_||_" || call.Function == "OR" {
			logic = queryfilter.LogicOr
		}
		return &collectionQuery{logic: logic, children: []*collectionQuery{left, right}}
	}
	operators := map[string]queryfilter.Operator{
		"_==_": queryfilter.OperatorEqual, "_>_": queryfilter.OperatorGreater, "_>=_": queryfilter.OperatorGreaterEq,
		"_<_": queryfilter.OperatorLess, "_<=_": queryfilter.OperatorLessEq,
	}
	op, ok := operators[call.Function]
	if !ok {
		return nil
	}
	selected := call.Args[0].GetSelectExpr()
	if selected == nil || selected.Operand.GetIdentExpr().GetName() != "CurrentValue" {
		return nil
	}
	field, ok := schemaSnapshotField(source, selected.Field)
	if !ok || IsComputedSource(field) {
		return nil
	}
	switch field.LogicalType {
	case v2.LogicalText, v2.LogicalBool:
		if op != queryfilter.OperatorEqual {
			return nil
		}
	case v2.LogicalNumber:
	default:
		// Dates and enum codecs keep their existing CEL semantics until query
		// normalization can prove that narrowing preserves their full precision.
		return nil
	}
	// Only total, row-independent leaves are moved ahead of the source scan.
	// Functions and source-field operands retain lazy CEL evaluation.
	operand := call.Args[1]
	if operand.GetConstExpr() == nil && operand.GetIdentExpr() == nil {
		return nil
	}
	if name := operand.GetIdentExpr().GetName(); name != "" {
		if _, ok := definition.Field(name); !ok {
			return nil
		}
	}
	compiled, err := compiler.collectionScalar(definition, operand, nil)
	if err != nil {
		return nil
	}
	return &collectionQuery{field: field.Identity.PhysicalName, operator: op, operand: compiled}
}

func (node *collectionNode) queryFilters(ctx context.Context, row map[string]any) ([]queryfilter.FilterExpression, *Error) {
	if node.queryPlan == nil {
		return nil, nil
	}
	filter, usable, err := node.queryPlan.evaluate(ctx, row)
	if err != nil || !usable {
		return nil, err
	}
	return []queryfilter.FilterExpression{filter}, nil
}

func (plan *collectionQuery) evaluate(ctx context.Context, row map[string]any) (queryfilter.FilterExpression, bool, *Error) {
	if len(plan.children) > 0 {
		group := queryfilter.FilterExpression{GroupLogic: plan.logic}
		for _, child := range plan.children {
			filter, usable, err := child.evaluate(ctx, row)
			if err != nil || !usable {
				return queryfilter.FilterExpression{}, false, err
			}
			group.Filters = append(group.Filters, filter)
		}
		return group, true, nil
	}
	value, err := plan.operand.evaluate(ctx, row)
	// Missing/null operands must preserve the full predicate's CEL behavior.
	if err != nil {
		if err.Code == "formula.resource_limit" {
			return queryfilter.FilterExpression{}, false, err
		}
		return queryfilter.FilterExpression{}, false, nil
	}
	if value == nil {
		return queryfilter.FilterExpression{}, false, nil
	}
	filter := queryfilter.FilterExpression{Field: plan.field, Operator: plan.operator, Value: value}
	if plan.operator != queryfilter.OperatorEqual {
		filter = queryfilter.FilterExpression{GroupLogic: queryfilter.LogicOr, Filters: []queryfilter.FilterExpression{
			filter, {Field: plan.field, Operator: queryfilter.OperatorIsNull},
		}}
	}
	return filter, true, nil
}

func (node *collectionNode) selectSourceFields() {
	selected := map[string]v2.FieldDefinition{}
	var leaf *collectionNode
	for current := node; current != nil; current = current.child {
		if current.kind == "table" {
			leaf = current
		}
		if current.projection.Identity.FieldID != "" {
			selected[current.projection.Identity.FieldID] = current.projection
		}
		for _, field := range current.inputs {
			selected[field.Identity.FieldID] = field
		}
	}
	if leaf == nil {
		return
	}
	for _, field := range leaf.records.Fields {
		if _, ok := selected[field.Identity.FieldID]; ok {
			leaf.sourceFields = append(leaf.sourceFields, field)
		}
	}
}
