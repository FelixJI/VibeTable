package v2

import (
	"encoding/json"
	"fmt"
	"math"
)

// ValidateLookupConditionShape checks direct Go callers as well as decoded wire
// requests. Cross-table field types and IDs are resolved by the catalog.
func ValidateLookupConditionShape(spec LookupSpec) error {
	if err := ValidateLookupAggregationShape(spec); err != nil {
		return err
	}
	fail := func(path, message string) error {
		return &ProductError{Code: "lookup.condition.invalid", Path: path, Message: message}
	}
	condition := spec.Condition
	if condition == nil {
		return nil
	}
	if len(spec.Path) != 0 || condition.SourceTableID == "" || spec.TargetFieldID == "" {
		return fail("lookup.condition", "condition requires a source table, target field, and an empty relation path")
	}
	if condition.Match != "all" && condition.Match != "any" {
		return fail("lookup.condition.match", "match must be all or any")
	}
	if len(condition.Rules) == 0 || len(condition.Rules) > 50 {
		return fail("lookup.condition.rules", "condition requires 1–50 rules")
	}
	for index, rule := range condition.Rules {
		path := fmt.Sprintf("lookup.condition.rules[%d]", index)
		if rule.SourceFieldID == "" {
			return fail(path+".sourceFieldId", "source field is required")
		}
		switch rule.Operator {
		case "is_null", "is_not_null":
			if rule.Operand != nil {
				return fail(path+".operand", "empty comparison takes no operand")
			}
			continue
		case "eq", "ne", "gt", "gte", "lt", "lte", "contains":
		default:
			return fail(path+".operator", "unsupported condition operator")
		}
		if rule.Operand == nil {
			return fail(path+".operand", "comparison operand is required")
		}
		operand := rule.Operand
		switch operand.Kind {
		case "field":
			if operand.FieldID == "" || operand.Value != nil {
				return fail(path+".operand", "current field requires only a field ID")
			}
		case "constant":
			if operand.FieldID != "" || operand.Value == nil {
				return fail(path+".operand", "constant requires only a typed value")
			}
			switch value := operand.Value.(type) {
			case string, bool:
			default:
				number, ok, err := rangeNumber(value)
				if !ok || err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
					return fail(path+".operand.value", "constant must be finite text, number, or boolean")
				}
			}
		default:
			return fail(path+".operand.kind", "operand must be field or constant")
		}
	}
	return nil
}

func (operand *LookupOperand) UnmarshalJSON(raw []byte) error {
	type wire LookupOperand
	var decoded wire
	if err := StrictDecode(raw, &decoded); err != nil {
		return err
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(raw, &properties); err != nil {
		return err
	}
	key := "value"
	if decoded.Kind == "field" {
		key = "fieldId"
	}
	if _, exists := properties[key]; !exists || len(properties) != 2 {
		return fmt.Errorf("lookup operand requires only kind and %s", key)
	}
	*operand = LookupOperand(decoded)
	return nil
}

func (rule *LookupConditionRule) UnmarshalJSON(raw []byte) error {
	type wire LookupConditionRule
	var decoded wire
	if err := StrictDecode(raw, &decoded); err != nil {
		return err
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(raw, &properties); err != nil {
		return err
	}
	expected := 3
	if decoded.Operator == "is_null" || decoded.Operator == "is_not_null" {
		expected = 2
	}
	if len(properties) != expected || (expected == 3 && decoded.Operand == nil) {
		return fmt.Errorf("lookup condition rule has invalid operand properties")
	}
	*rule = LookupConditionRule(decoded)
	return nil
}
