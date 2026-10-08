package v2_test

import (
	"context"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"testing"
)

func TestAutoNumberFixedStringContract(t *testing.T) {
	spec := v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 6}
	if err := v2.ValidateAutoNumberSpec(&spec); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{1, 2, 1000000, v2.MaxAutoNumber} {
		value := v2.AutoNumberValue(spec, n)
		parsed, err := v2.ParseAutoNumberValue(spec, value)
		if err != nil || parsed != n {
			t.Fatalf("roundtrip %s: %d %v", value, parsed, err)
		}
	}
	for _, bad := range []v2.AutoNumberSpec{{Start: 0, Width: 6}, {Start: 1, Width: 17}, {Start: 1, Width: 0}, {Start: v2.MaxAutoNumber + 1, Width: 6}, {Prefix: "bad\n", Start: 1, Width: 6}} {
		if v2.ValidateAutoNumberSpec(&bad) == nil {
			t.Fatalf("invalid config accepted: %#v", bad)
		}
	}
	recommended, _ := v2.RecommendedDefaults(v2.LogicalAutoNumber)
	definition := v2.FieldDefinition{Contract: v2.Contract, Identity: v2.FieldIdentity{FieldID: "fld_number0001", PhysicalName: "f_number0001", ProviderFieldID: "pb_number0001"}, DisplayName: "合同编号", LogicalType: v2.LogicalAutoNumber, Lifecycle: v2.Lifecycle{State: v2.LifecycleActive}, Value: recommended.Value, Constraints: recommended.Constraints, Storage: recommended.Storage, Display: recommended.Display, AutoNumber: &spec}
	if err := v2.Validate(definition); err != nil {
		t.Fatal(err)
	}
	if _, err := fieldvalue.New().NormalizeWrite(context.Background(), definition, fieldvalue.Insert, fieldvalue.Input{Supplied: true, Value: "HT-000001"}); err == nil {
		t.Fatal("explicit number write accepted")
	}
	if _, err := fieldvalue.New().NormalizeWrite(context.Background(), definition, fieldvalue.Insert, fieldvalue.Input{}); err != nil {
		t.Fatal(err)
	}
}
