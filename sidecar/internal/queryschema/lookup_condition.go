package queryschema

import (
	"context"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type LookupConditionPlan struct {
	Spec       v2.LookupSpec
	Source     schemaexecution.Table
	Target     schemaexecution.Table
	Field      v2.FieldDefinition
	Descriptor query.TableDescriptor
}

// ValidateLookupCondition is shared by schema apply, field planning and draft preview.
func ValidateLookupCondition(ctx context.Context, app core.App, source schemaexecution.Table, spec v2.LookupSpec) error {
	_, err := PrepareLookupCondition(ctx, app, source, spec)
	return err
}

func PrepareLookupCondition(ctx context.Context, app core.App, source schemaexecution.Table, spec v2.LookupSpec) (LookupConditionPlan, error) {
	plan := LookupConditionPlan{Spec: spec, Source: source}
	if err := v2.ValidateLookupConditionShape(spec); err != nil {
		return plan, err
	}
	if spec.Condition == nil {
		return plan, conditionError("lookup.condition.invalid", "condition is required")
	}
	target, err := schemaexecution.Describe(ctx, app, spec.Condition.SourceTableID)
	if err != nil {
		return plan, conditionError("lookup.value.source_missing", "lookup source table is unavailable")
	}
	plan.Target = target
	field, ok := target.Field(spec.TargetFieldID)
	if !ok || field.LogicalType == v2.LogicalRelation || field.LogicalType == v2.LogicalLookup || field.LogicalType == v2.LogicalFormula {
		return plan, conditionError("lookup.value.source_missing", "lookup result must reference an active local source field")
	}
	plan.Field = field
	if v2.LookupAggregationRequiresNumericSource(v2.ResolvedLookupAggregation(spec)) &&
		!v2.LookupFieldTargetNumeric(field) {
		return plan, &query.ProductError{
			Code: "lookup.aggregation.target_not_numeric", Path: "lookup.aggregation",
			Message: "numeric lookup aggregation requires a number source field",
		}
	}
	sourceAdapter, err := New(app.DataDir())
	if err != nil {
		return plan, err
	}
	descriptor, err := sourceAdapter.DescribeQueryTable(ctx, app, target.Snapshot.TableID)
	if err != nil {
		return plan, err
	}
	plan.Descriptor = descriptor
	for _, rule := range spec.Condition.Rules {
		sourceField, found := target.Field(rule.SourceFieldID)
		if !found || conditionType(sourceField) == "" {
			return plan, conditionError("lookup.condition.field_invalid", "condition source field is unavailable or unsupported")
		}
		if !conditionOperatorAllowed(sourceField, rule.Operator) {
			return plan, conditionError("lookup.condition.operator_invalid", "operator is not supported for the source field")
		}
		var value any
		operator := query.Operator(rule.Operator)
		if rule.Operand != nil {
			value = rule.Operand.Value
			if rule.Operand.Kind == "field" {
				currentField, found := source.Field(rule.Operand.FieldID)
				if !found || conditionType(currentField) != conditionType(sourceField) {
					return plan, conditionError("lookup.condition.type_mismatch", "current field and source field types must match")
				}
				value = sampleConditionValue(sourceField)
				if value == nil {
					operator = query.OperatorIsNull
				}
			}
		}
		_, err = query.Compile(descriptor, query.TableQuery{Filters: []query.FilterExpression{{
			Field: sourceField.Identity.PhysicalName, Operator: operator, Value: conditionFilterValue(sourceField, value),
		}}})
		if err != nil {
			return plan, err
		}
	}
	return plan, nil
}

func conditionType(field v2.FieldDefinition) string {
	switch field.LogicalType {
	case v2.LogicalAutoNumber, v2.LogicalText, v2.LogicalEditor, v2.LogicalEmail, v2.LogicalURL:
		return "text"
	case v2.LogicalNumber:
		return "number"
	case v2.LogicalBool:
		return "bool"
	case v2.LogicalDate:
		return "date"
	case v2.LogicalDateTime, v2.LogicalAutoDate:
		return "dateTime"
	case v2.LogicalSelect:
		return "select"
	default:
		return ""
	}
}

func conditionOperatorAllowed(field v2.FieldDefinition, operator string) bool {
	switch operator {
	case "eq", "ne", "is_null", "is_not_null":
		return conditionType(field) != ""
	case "contains":
		return conditionType(field) == "text"
	case "gt", "gte", "lt", "lte":
		return conditionType(field) == "number" || conditionType(field) == "date" || conditionType(field) == "dateTime"
	default:
		return false
	}
}

func sampleConditionValue(field v2.FieldDefinition) any {
	switch conditionType(field) {
	case "text":
		return ""
	case "number":
		return float64(0)
	case "bool":
		return false
	case "date", "dateTime":
		return "2000-01-01T00:00:00Z"
	case "select":
		if field.Select != nil {
			for _, option := range field.Select.Options {
				if option.State == v2.OptionActive {
					return option.OptionID
				}
			}
		}
	}
	return nil
}

func mappedSelectValue(current, source v2.FieldDefinition, value any) any {
	if current.Select == nil || source.Select == nil {
		return nil
	}
	label := ""
	found := false
	for _, option := range current.Select.Options {
		if option.OptionID == value && option.State == v2.OptionActive {
			label, found = option.Label, true
			break
		}
	}
	if !found {
		return nil
	}
	for _, option := range source.Select.Options {
		if option.Label == label && option.State == v2.OptionActive {
			return option.OptionID
		}
	}
	return nil
}

func (plan LookupConditionPlan) Filters(record *core.Record) []query.FilterExpression {
	filters := make([]query.FilterExpression, 0, len(plan.Spec.Condition.Rules))
	for _, rule := range plan.Spec.Condition.Rules {
		sourceField, _ := plan.Target.Field(rule.SourceFieldID)
		filter := query.FilterExpression{Field: sourceField.Identity.PhysicalName, Operator: query.Operator(rule.Operator), Logic: query.LogicAnd}
		if plan.Spec.Condition.Match == "any" {
			filter.Logic = query.LogicOr
		}
		if operand := rule.Operand; operand != nil {
			filter.Value = operand.Value
			if operand.Kind == "field" {
				current, _ := plan.Source.Field(operand.FieldID)
				filter.Value = productrow.Project([]v2.FieldDefinition{current}, record)[current.Identity.PhysicalName]
				if filter.Value != nil && current.LogicalType == v2.LogicalSelect {
					filter.Value = mappedSelectValue(current, sourceField, filter.Value)
					if filter.Value == nil && rule.Operator == "ne" {
						filter.Operator = query.OperatorIsNotNull
						filters = append(filters, filter)
						continue
					}
				}
				// SQL null comparisons are unknown, never truthy. ANY ignores that
				// branch; ALL has no possible match. Empty strings/zero/false remain values.
				if filter.Value == nil {
					if plan.Spec.Condition.Match == "all" {
						return nil
					}
					continue
				}
			}
		}
		filter.Value = conditionFilterValue(sourceField, filter.Value)
		filters = append(filters, filter)
	}
	if len(filters) == 0 {
		return nil
	}
	return filters
}

func conditionError(code, message string) *query.ProductError {
	return &query.ProductError{Code: code, Path: "lookup.condition", Message: message}
}

func conditionFilterValue(field v2.FieldDefinition, value any) any {
	if field.LogicalType == v2.LogicalDate {
		if text, ok := value.(string); ok {
			if date, err := time.Parse(time.DateOnly, text); err == nil {
				return date.Format(time.RFC3339)
			}
		}
	}
	return value
}
