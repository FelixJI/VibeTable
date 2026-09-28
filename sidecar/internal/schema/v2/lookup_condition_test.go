package v2_test

import (
	"encoding/json"
	"strings"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// lookupConditionRaw builds the exact wire shape a client would send for a
// conditional Lookup field. The condition fragment is embedded verbatim so each
// case controls every JSON property, including smuggled nulls.
func lookupConditionRaw(condition string) []byte {
	return []byte(`{"path":[],"targetFieldId":"fld_target00001","condition":` + condition + `}`)
}

func lookupConditionRules(rules string) string {
	return `{"sourceTableId":"tbl_source00001","match":"all","rules":[` + rules + `],"distinct":false}`
}

// TestLookupConditionWireRejectsSmuggledNullsAndEmptyContraband pins the raw
// JSON contract: explicit condition:null, operand:null on empty-judgment
// operators, value:null smuggled into a field operand, and fieldId:"" smuggled
// into a constant operand must all fail decode. A condition that is merely
// absent stays a legal unconditional Lookup.
func TestLookupConditionWireRejectsSmuggledNullsAndEmptyContraband(t *testing.T) {
	t.Parallel()
	rejected := []struct {
		name      string
		condition string
		needle    string
	}{
		{
			name:      "explicit null condition",
			condition: `null`,
			needle:    "lookup condition cannot be null",
		},
		{
			name:      "empty comparison carries null operand",
			condition: lookupConditionRules(`{"sourceFieldId":"fld_source00001","operator":"is_null","operand":null}`),
			needle:    "invalid operand properties",
		},
		{
			name:      "field operand smuggles null value",
			condition: lookupConditionRules(`{"sourceFieldId":"fld_source00001","operator":"eq","operand":{"kind":"field","fieldId":"fld_current0001","value":null}}`),
			needle:    "lookup operand requires only kind and fieldId",
		},
		{
			name:      "constant operand smuggles empty fieldId",
			condition: lookupConditionRules(`{"sourceFieldId":"fld_source00001","operator":"eq","operand":{"kind":"constant","value":true,"fieldId":""}}`),
			needle:    "lookup operand requires only kind and value",
		},
	}
	for _, testCase := range rejected {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var spec v2.LookupSpec
			err := json.Unmarshal(lookupConditionRaw(testCase.condition), &spec)
			if err == nil {
				t.Fatalf("smuggled shape was accepted: %+v", spec)
			}
			if !strings.Contains(err.Error(), testCase.needle) {
				t.Fatalf("rejection reason = %q, want %q", err.Error(), testCase.needle)
			}
		})
	}

	var unconditional v2.LookupSpec
	if err := json.Unmarshal([]byte(`{"path":[],"targetFieldId":"fld_target00001"}`), &unconditional); err != nil || unconditional.Condition != nil {
		t.Fatalf("absent condition must stay a valid unconditional lookup: %+v, %v", unconditional, err)
	}
}

// TestLookupConditionWireKeepsFalsyConstants guards the opposite direction:
// empty-string, false, and zero constants are real typed values and must
// survive strict decode plus shape validation without being mistaken for the
// null/empty smuggling rejected above.
func TestLookupConditionWireKeepsFalsyConstants(t *testing.T) {
	t.Parallel()
	accepted := []struct {
		name      string
		condition string
		check     func(*testing.T, v2.LookupConditionRule)
	}{
		{
			name:      "empty string constant",
			condition: lookupConditionRules(`{"sourceFieldId":"fld_source00001","operator":"eq","operand":{"kind":"constant","value":""}}`),
			check: func(t *testing.T, rule v2.LookupConditionRule) {
				if rule.Operand.Value != "" {
					t.Fatalf("empty string drifted to %#v", rule.Operand.Value)
				}
			},
		},
		{
			name:      "false constant",
			condition: lookupConditionRules(`{"sourceFieldId":"fld_source00001","operator":"eq","operand":{"kind":"constant","value":false}}`),
			check: func(t *testing.T, rule v2.LookupConditionRule) {
				if value, ok := rule.Operand.Value.(bool); !ok || value {
					t.Fatalf("false drifted to %#v", rule.Operand.Value)
				}
			},
		},
		{
			name:      "zero constant",
			condition: lookupConditionRules(`{"sourceFieldId":"fld_source00001","operator":"eq","operand":{"kind":"constant","value":0}}`),
			check: func(t *testing.T, rule v2.LookupConditionRule) {
				if number, ok := rule.Operand.Value.(json.Number); !ok || number.String() != "0" {
					t.Fatalf("zero drifted to %#v", rule.Operand.Value)
				}
			},
		},
		{
			name:      "field operand without value",
			condition: lookupConditionRules(`{"sourceFieldId":"fld_source00001","operator":"eq","operand":{"kind":"field","fieldId":"fld_current0001"}}`),
			check: func(t *testing.T, rule v2.LookupConditionRule) {
				if rule.Operand.FieldID != "fld_current0001" || rule.Operand.Value != nil {
					t.Fatalf("field operand drifted: %+v", rule.Operand)
				}
			},
		},
		{
			name:      "empty comparison without operand",
			condition: lookupConditionRules(`{"sourceFieldId":"fld_source00001","operator":"is_not_null"}`),
			check: func(t *testing.T, rule v2.LookupConditionRule) {
				if rule.Operand != nil {
					t.Fatalf("empty comparison gained an operand: %+v", rule.Operand)
				}
			},
		},
	}
	for _, testCase := range accepted {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var spec v2.LookupSpec
			if err := json.Unmarshal(lookupConditionRaw(testCase.condition), &spec); err != nil {
				t.Fatalf("valid falsy constant was rejected: %v", err)
			}
			if err := v2.ValidateLookupConditionShape(spec); err != nil {
				t.Fatalf("valid falsy constant failed shape validation: %v", err)
			}
			testCase.check(t, spec.Condition.Rules[0])
		})
	}
}
