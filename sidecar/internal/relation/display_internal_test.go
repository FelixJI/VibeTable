package relation

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestTargetLabelValueKeepsTypedScalarsAndFreshComputedResults(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  any
		valid bool
	}{
		{"text", " Alpha ", "Alpha", true},
		{"blank text", "   ", nil, false},
		{"zero float", float64(0), float64(0), true},
		{"zero int", int64(0), int64(0), true},
		{"false", false, false, true},
		{"json zero", json.Number("0"), json.Number("0"), true},
		{"fresh envelope", map[string]any{"state": "ready", "value": "Fresh"}, "Fresh", true},
		{"ok envelope numeric", map[string]any{"state": "ok", "value": float64(3)}, float64(3), true},
		{"stale envelope", map[string]any{"state": "updating", "value": "STALE"}, nil, false},
		{"failed envelope", map[string]any{"state": "failed", "value": "FAILED"}, nil, false},
		{"envelope object value", map[string]any{"state": "ready", "value": map[string]any{"secret": true}}, nil, false},
		{"plain object", map[string]any{"secret": true}, nil, false},
		{"list", []any{"a"}, nil, false},
		{"nil", nil, nil, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, valid := targetLabelValue(test.value)
			if valid != test.valid || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("targetLabelValue(%#v) = (%#v, %v), want (%#v, %v)", test.value, got, valid, test.want, test.valid)
			}
		})
	}
}

func TestProjectTargetRefResolvesFrozenFallbackChain(t *testing.T) {
	target := schemaexecution.Table{
		PrimaryDisplayFieldID: "fld_code",
		Snapshot: v2.SchemaSnapshot{
			Fields: []v2.FieldDefinition{
				{Identity: v2.FieldIdentity{FieldID: "fld_code", PhysicalName: "f_code"}, LogicalType: v2.LogicalText},
				{Identity: v2.FieldIdentity{FieldID: "fld_name", PhysicalName: "f_name"}, LogicalType: v2.LogicalText},
				{Identity: v2.FieldIdentity{FieldID: "fld_units", PhysicalName: "f_units"}, LogicalType: v2.LogicalNumber},
			},
		},
	}
	field := v2.FieldDefinition{
		LogicalType: v2.LogicalRelation,
		Relation:    &v2.RelationSpec{TargetTableID: "contracts", DisplayField: "fld_name"},
	}
	projection := resolveTargetDisplay(field, target)
	if projection.displayPhysical != "f_name" || projection.primaryPhysical != "f_code" {
		t.Fatalf("projection = %#v", projection)
	}
	cases := []struct {
		name     string
		recordID string
		row      map[string]any
		want     TargetRef
	}{
		{"display wins with primary auxiliary", "rec001", map[string]any{"f_code": "CT-001", "f_name": "城轨一期"}, TargetRef{
			Label: "城轨一期", SecondaryLabel: "CT-001",
			DisplayValue: "城轨一期", SecondaryValue: "CT-001",
		}},
		{"empty display falls back to primary", "rec002", map[string]any{"f_code": "CT-002", "f_name": "  "}, TargetRef{
			Label: "CT-002", SecondaryValue: "CT-002",
		}},
		{"both empty falls back to record ID", "rec003", map[string]any{"f_code": nil, "f_name": ""}, TargetRef{
			Label: "rec003",
		}},
		{"typed zero display value", "rec004", map[string]any{"f_code": "CT-004", "f_name": "名称"}, TargetRef{
			Label: "名称", SecondaryLabel: "CT-004", DisplayValue: "名称", SecondaryValue: "CT-004",
		}},
		{"missing target row keeps record ID", "rec005", nil, TargetRef{Label: "rec005"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := projection.projectTargetRef("contracts", test.recordID, test.row)
			test.want.TableID, test.want.RecordID = "contracts", test.recordID
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("projectTargetRef = %#v, want %#v", got, test.want)
			}
		})
	}
	// Zero and false stay valid typed display values without fallback.
	zeroField := v2.FieldDefinition{
		LogicalType: v2.LogicalRelation,
		Relation:    &v2.RelationSpec{TargetTableID: "contracts", DisplayField: "fld_units"},
	}
	zeroProjection := resolveTargetDisplay(zeroField, target)
	ref := zeroProjection.projectTargetRef("contracts", "rec006", map[string]any{"f_code": "CT-006", "f_units": float64(0)})
	if ref.Label != "0" || ref.DisplayValue != float64(0) || ref.SecondaryLabel != "CT-006" {
		t.Fatalf("numeric zero projection = %#v", ref)
	}
}

// TestDisplayFieldInfoCarriesCanonicalSpecs proves the render info for the
// relation display field and the fallback primary field each carry their own
// canonical DisplaySpec and declared numeric type, so a percentage display
// field falling back to a currency primary never formats with one shared
// numeric authority.
func TestDisplayFieldInfoCarriesCanonicalSpecs(t *testing.T) {
	percent := v2.DisplaySpec{Kind: v2.DisplayNumber, Preset: "percent", DisplayScale: 1, ScaleMode: "fixed", PercentStorage: "percent"}
	currency := v2.DisplaySpec{Kind: v2.DisplayNumber, Preset: "currency", DisplayScale: 2, ScaleMode: "fixed", UseGrouping: true, Currency: "CNY"}
	target := schemaexecution.Table{
		PrimaryDisplayFieldID: "fld_amount",
		Snapshot: v2.SchemaSnapshot{
			Fields: []v2.FieldDefinition{
				{Identity: v2.FieldIdentity{FieldID: "fld_amount", PhysicalName: "f_amount"}, LogicalType: v2.LogicalNumber, Display: currency},
				{Identity: v2.FieldIdentity{FieldID: "fld_rate", PhysicalName: "f_rate"}, LogicalType: v2.LogicalNumber, Display: percent},
			},
		},
	}
	service := &Service{}
	rate := service.displayFieldInfoByID(context.Background(), target, "fld_rate")
	if rate == nil || rate.DataType != "decimal" || rate.Display == nil || rate.Display.Preset != "percent" || rate.Display.PercentStorage != "percent" {
		t.Fatalf("display field info = %#v", rate)
	}
	amount := service.fallbackDisplayFieldInfoFor(context.Background(), target)
	if amount == nil || amount.FieldID != "fld_amount" || amount.DataType != "decimal" || amount.Display == nil || amount.Display.Preset != "currency" || amount.Display.Currency != "CNY" {
		t.Fatalf("fallback field info = %#v", amount)
	}
}

func TestProjectTargetRefKeepsNumericPrimaryForSameAndMissingDisplay(t *testing.T) {
	for _, display := range []string{"amount", ""} {
		projection := targetDisplayProjection{displayPhysical: display, primaryPhysical: "amount"}
		ref := projection.projectTargetRef("contracts", "target", map[string]any{"amount": float64(1982)})
		if ref.Label != "1982" {
			t.Fatalf("display=%q: %#v", display, ref)
		}
		if display == "amount" && ref.DisplayValue != float64(1982) {
			t.Fatalf("same-field display must retain its typed scalar: %#v", ref)
		}
		if display == "" && ref.SecondaryValue != float64(1982) {
			t.Fatalf("missing display must retain typed primary fallback: %#v", ref)
		}
	}
}
