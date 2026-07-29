package fieldvalue_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestPresenceTruthTableDistinguishesMissingFromExplicitZeroValues(t *testing.T) {
	t.Parallel()
	kernel := fieldvalue.New()
	tests := []struct {
		name       string
		logical    v2.LogicalType
		value      any
		wantStored any
	}{
		{name: "number zero", logical: v2.LogicalNumber, value: json.Number("0"), wantStored: float64(0)},
		{name: "false", logical: v2.LogicalBool, value: false, wantStored: false},
		{
			name: "null island", logical: v2.LogicalGeoPoint,
			value:      map[string]any{"lat": 0, "lon": 0},
			wantStored: map[string]any{"lat": float64(0), "lon": float64(0)},
		},
		{name: "empty text", logical: v2.LogicalText, value: "", wantStored: ""},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := definitionFor(test.logical)
			missing, err := kernel.NormalizeWrite(
				context.Background(), definition, fieldvalue.Insert,
				fieldvalue.Input{Supplied: true, Value: nil},
			)
			if err != nil {
				t.Fatal(err)
			}
			explicit, err := kernel.NormalizeWrite(
				context.Background(), definition, fieldvalue.Insert,
				fieldvalue.Input{Supplied: true, Value: test.value},
			)
			if err != nil {
				t.Fatal(err)
			}
			if missing.Present || !explicit.Present ||
				missing.PhysicalValues[definition.Value.Presence.PhysicalName] != false ||
				explicit.PhysicalValues[definition.Value.Presence.PhysicalName] != true {
				t.Fatalf("missing=%#v explicit=%#v", missing, explicit)
			}
			if !jsonEqual(explicit.ProductValue, test.wantStored) {
				t.Fatalf("product value = %#v, want %#v", explicit.ProductValue, test.wantStored)
			}
		})
	}
}

func TestDefaultsApplyOnlyToUnsuppliedInsert(t *testing.T) {
	t.Parallel()
	kernel := fieldvalue.New()
	definition := definitionFor(v2.LogicalNumber)
	definition.Value.Default.Enabled = true
	definition.Value.Default.Value = json.Number("7")
	definition.Value.Default.Source = v2.DefaultUser

	insert, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert, fieldvalue.Input{},
	)
	if err != nil || insert.ProductValue != float64(7) {
		t.Fatalf("unsupplied insert = %#v, %v", insert, err)
	}
	explicitBlank, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: true, Value: nil},
	)
	if err != nil || explicitBlank.ProductValue != nil || explicitBlank.Present {
		t.Fatalf("explicit blank = %#v, %v", explicitBlank, err)
	}
	update, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Update, fieldvalue.Input{},
	)
	if err != nil || update.Write {
		t.Fatalf("unsupplied update = %#v, %v", update, err)
	}
}

func TestDynamicDefaultUsesInjectedClockOnce(t *testing.T) {
	t.Parallel()
	count := 0
	kernel := fieldvalue.New(fieldvalue.WithClock(func() time.Time {
		count++
		return time.Date(2026, 7, 28, 9, 10, 11, 0, time.FixedZone("CST", 8*60*60))
	}))
	definition := definitionFor(v2.LogicalDateTime)
	definition.Value.Default.Enabled = true
	definition.Value.Default.Value = map[string]any{"kind": "now"}
	result, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert, fieldvalue.Input{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || result.ProductValue != "2026-07-28T01:10:11Z" {
		t.Fatalf("dynamic default = %#v, clock count %d", result.ProductValue, count)
	}
}

func TestRequiredUsesProductPresenceNotPocketBaseZeroRules(t *testing.T) {
	t.Parallel()
	kernel := fieldvalue.New()
	definition := definitionFor(v2.LogicalBool)
	definition.Value.Required = true
	result, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: true, Value: false},
	)
	if err != nil || !result.Present {
		t.Fatalf("explicit false rejected: %#v, %v", result, err)
	}
	_, err = kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: true, Value: nil},
	)
	var productErr *fieldvalue.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "field.value.required" {
		t.Fatalf("missing required error = %#v", err)
	}
}

func TestSelectAcceptsStableActiveIDsAndRejectsRetiredOptions(t *testing.T) {
	t.Parallel()
	kernel := fieldvalue.New()
	definition := definitionFor(v2.LogicalSelect)
	active := "opt_01JACTIVE1"
	retired := "opt_01JRETIRED"
	definition.Select = &v2.SelectSpec{Options: []v2.SelectOption{
		{OptionID: active, Label: "有效", State: v2.OptionActive},
		{OptionID: retired, Label: "停用", State: v2.OptionRetired},
	}}
	one := 1
	definition.Constraints.Selection.Max = &one
	if _, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: true, Value: active},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: true, Value: retired},
	); err == nil {
		t.Fatal("retired option was accepted for a new write")
	}
}

func TestTemporalRangeIsValidatedByLogicalType(t *testing.T) {
	t.Parallel()
	definition := definitionFor(v2.LogicalDate)
	definition.Constraints.Range.Min = "2026-07-01"
	definition.Constraints.Range.Max = "2026-07-31"
	kernel := fieldvalue.New()

	if _, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: true, Value: "2026-07-14"},
	); err != nil {
		t.Fatalf("date inside range: %v", err)
	}
	if _, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: true, Value: "2026-08-01"},
	); err == nil {
		t.Fatal("date above maximum was accepted")
	}
}

func TestJSONNullIsTheNativeProductBlankAndDefaultStateRemainsExplicit(t *testing.T) {
	t.Parallel()
	definition := definitionFor(v2.LogicalJSON)
	definition.JSON.RootType = "null"
	definition.Value.Default.Enabled = true
	definition.Value.Default.Value = nil
	kernel := fieldvalue.New()

	result, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: false},
	)
	if err != nil || !result.Write || result.Present ||
		result.PhysicalValues[definition.Identity.PhysicalName] != nil {
		t.Fatalf("native JSON null default = %#v, err=%v", result, err)
	}
	if !definition.Value.Default.Enabled {
		t.Fatal("enabled JSON null default collapsed into disabled state")
	}

	definition.Value.Required = true
	if _, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert,
		fieldvalue.Input{Supplied: true, Value: nil},
	); err == nil {
		t.Fatal("required JSON accepted its native blank null")
	}
}

func TestNormalizeRawInputFeedsTheAuthoritativeNumberWritePath(t *testing.T) {
	t.Parallel()
	kernel := fieldvalue.New()
	definition := definitionFor(v2.LogicalNumber)

	input, err := kernel.NormalizeRawInput(
		context.Background(), definition, "$1,234.50",
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := kernel.NormalizeWrite(
		context.Background(), definition, fieldvalue.Insert, input,
	)
	if err != nil || result.ProductValue != float64(1234.5) {
		t.Fatalf("normalized raw number = %#v, err=%v", result, err)
	}
}

func definitionFor(logicalType v2.LogicalType) v2.FieldDefinition {
	recommended, err := v2.RecommendedDefaults(logicalType)
	if err != nil {
		panic(err)
	}
	identity := v2.FieldIdentity{
		FieldID: "fld_01JFIELDX", PhysicalName: "f_01jfieldx", ProviderFieldID: "pb_01JFIELDX",
	}
	recommended.Value.Presence.ProviderFieldID = "pb_01JPRESEN"
	recommended.Value.Presence.PhysicalName = "__vt_has_" + identity.PhysicalName
	definition := v2.FieldDefinition{
		Contract: v2.Contract, Identity: identity, DisplayName: "字段", LogicalType: logicalType,
		Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
		Value:     recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
		File: recommended.File, JSON: recommended.JSON,
	}
	switch logicalType {
	case v2.LogicalRelation:
		definition.Relation = &v2.RelationSpec{
			TargetTableID: "tbl_target", Cardinality: "one", DeletePolicy: "setNull",
		}
	case v2.LogicalSelect, v2.LogicalMultiSelect:
		definition.Select = &v2.SelectSpec{Options: []v2.SelectOption{{
			OptionID: "opt_01JACTIVE1", Label: "有效", State: v2.OptionActive,
		}}}
	}
	return definition
}

func jsonEqual(left, right any) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return string(leftRaw) == string(rightRaw)
}
