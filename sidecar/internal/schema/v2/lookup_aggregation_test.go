package v2_test

import (
	"encoding/json"
	"strings"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func lookupAggregationRaw(aggregation string) []byte {
	raw := `{"path":[],"targetFieldId":"fld_target00001"`
	if aggregation != "" {
		raw += `,"aggregation":` + aggregation
	}
	return []byte(raw + `}`)
}

// TestLookupAggregationWireAcceptsClosedSetAndRejectsContraband pins the raw
// JSON contract for the top-level aggregation: the closed enumeration decodes,
// unknown values and the legacy aggregate attribute stay rejected, and an
// explicit aggregation must not combine with condition.distinct=true.
func TestLookupAggregationWireAcceptsClosedSetAndRejectsContraband(t *testing.T) {
	t.Parallel()
	for _, aggregation := range []string{
		v2.LookupAggregationValues, v2.LookupAggregationDistinct,
		v2.LookupAggregationCountRecords, v2.LookupAggregationCountNonEmpty,
		v2.LookupAggregationCountDistinct, v2.LookupAggregationSum,
		v2.LookupAggregationAverage, v2.LookupAggregationMin,
		v2.LookupAggregationMax,
	} {
		var spec v2.LookupSpec
		if err := json.Unmarshal(lookupAggregationRaw(`"`+aggregation+`"`), &spec); err != nil {
			t.Fatalf("valid aggregation %q rejected: %v", aggregation, err)
		}
		if spec.Aggregation != aggregation {
			t.Fatalf("aggregation drifted: %q", spec.Aggregation)
		}
	}

	var invalid v2.LookupSpec
	if err := json.Unmarshal(lookupAggregationRaw(`"median"`), &invalid); err == nil {
		t.Fatal("unknown aggregation was accepted")
	}
	if err := v2.ValidateLookupAggregationShape(v2.LookupSpec{Aggregation: "count_all"}); err == nil {
		t.Fatal("direct Go caller bypassed the enumeration")
	}
	// The historical aggregate attribute stays removed; strict decode rejects it.
	var legacy v2.LookupSpec
	if err := v2.StrictDecode(
		[]byte(`{"path":[],"targetFieldId":"fld_target00001","aggregate":"sum"}`), &legacy,
	); err == nil {
		t.Fatal("removed Lookup aggregate field was accepted")
	}

	condition := `{"sourceTableId":"tbl_source00001","match":"all","rules":[{"sourceFieldId":"fld_source00001","operator":"is_not_null"}],"distinct":true}`
	var combined v2.LookupSpec
	err := json.Unmarshal([]byte(
		`{"path":[],"targetFieldId":"fld_target00001","aggregation":"sum","condition":`+condition+`}`,
	), &combined)
	if err == nil {
		t.Fatal("explicit aggregation combined with condition distinct")
	}
	if !strings.Contains(err.Error(), "condition distinct") {
		t.Fatalf("rejection reason = %q", err.Error())
	}
	// distinct=false stays a legal companion: the legacy flag is simply absent.
	combined = v2.LookupSpec{}
	loose := strings.Replace(condition, `"distinct":true`, `"distinct":false`, 1)
	if err := json.Unmarshal([]byte(
		`{"path":[],"targetFieldId":"fld_target00001","aggregation":"countRecords","condition":`+loose+`}`,
	), &combined); err != nil || combined.Aggregation != "countRecords" {
		t.Fatalf("aggregation with distinct=false rejected: %+v, %v", combined, err)
	}
}

// TestResolvedLookupAggregationMapsLegacyModes pins mode resolution: absent
// aggregation keeps values, the legacy condition.distinct flag maps to
// distinct, and an explicit aggregation wins.
func TestResolvedLookupAggregationMapsLegacyModes(t *testing.T) {
	t.Parallel()
	path := v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: "fld_rel000001"}}, TargetFieldID: "fld_target00001"}
	if mode := v2.ResolvedLookupAggregation(path); mode != v2.LookupAggregationValues {
		t.Fatalf("path default = %q", mode)
	}
	legacy := v2.LookupSpec{TargetFieldID: "fld_target00001", Condition: &v2.LookupCondition{Distinct: true}}
	if mode := v2.ResolvedLookupAggregation(legacy); mode != v2.LookupAggregationDistinct {
		t.Fatalf("legacy distinct = %q", mode)
	}
	explicit := v2.LookupSpec{TargetFieldID: "fld_target00001", Aggregation: v2.LookupAggregationCountRecords}
	if mode := v2.ResolvedLookupAggregation(explicit); mode != v2.LookupAggregationCountRecords {
		t.Fatalf("explicit mode = %q", mode)
	}
}

// TestLookupAggregationClassification pins the shared classification the query,
// formula and catalog layers rely on: numeric outputs, numeric-source
// requirements, and which modes need the complete matched set.
func TestLookupAggregationClassification(t *testing.T) {
	t.Parallel()
	numeric := map[string]bool{
		v2.LookupAggregationValues: false, v2.LookupAggregationDistinct: false,
		v2.LookupAggregationCountRecords: true, v2.LookupAggregationCountNonEmpty: true,
		v2.LookupAggregationCountDistinct: true, v2.LookupAggregationSum: true,
		v2.LookupAggregationAverage: true, v2.LookupAggregationMin: true,
		v2.LookupAggregationMax: true,
	}
	numericSource := map[string]bool{
		v2.LookupAggregationSum: true, v2.LookupAggregationAverage: true,
		v2.LookupAggregationMin: true, v2.LookupAggregationMax: true,
	}
	for mode, wantNumeric := range numeric {
		if got := v2.LookupAggregationNumeric(mode); got != wantNumeric {
			t.Fatalf("LookupAggregationNumeric(%q) = %v", mode, got)
		}
		wantSource := numericSource[mode]
		if got := v2.LookupAggregationRequiresNumericSource(mode); got != wantSource {
			t.Fatalf("LookupAggregationRequiresNumericSource(%q) = %v", mode, got)
		}
	}
	if !v2.LookupFieldTargetNumeric(v2.FieldDefinition{LogicalType: v2.LogicalNumber}) {
		t.Fatal("number target must be numeric")
	}
	if !v2.LookupFieldTargetNumeric(v2.FieldDefinition{
		LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{ResultType: v2.LogicalNumber},
	}) {
		t.Fatal("numeric formula target must be numeric")
	}
	if v2.LookupFieldTargetNumeric(v2.FieldDefinition{LogicalType: v2.LogicalText}) {
		t.Fatal("text target must not be numeric")
	}
}
