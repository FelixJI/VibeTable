package v2_test

import (
	"context"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestAutoNumberDateAndOrderContract(t *testing.T) {
	// 23:30 in UTC+8 is the previous UTC day: the segment is UTC, not local.
	when := time.Date(2026, 10, 25, 23, 30, 0, 0, time.FixedZone("+08", 8*3600))
	for _, test := range []struct {
		name string
		spec v2.AutoNumberSpec
		want string
	}{
		{
			name: "default order with date",
			spec: v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 4, DateFormat: v2.AutoNumberDateFormat},
			want: "HT-202610250001",
		},
		{
			name: "number first",
			spec: v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 4, DateFormat: v2.AutoNumberDateFormat, Order: []string{"number", "prefix", "date"}},
			want: "0001HT-20261025",
		},
		{
			name: "date first without prefix",
			spec: v2.AutoNumberSpec{Start: 7, Width: 2, DateFormat: v2.AutoNumberDateFormat, Order: []string{"date", "number", "prefix"}},
			want: "2026102507",
		},
	} {
		if err := v2.ValidateAutoNumberSpec(&test.spec); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		sequence := test.spec.Start
		if got := v2.AutoNumberValue(test.spec, sequence, when); got != test.want {
			t.Fatalf("%s: value = %s, want %s", test.name, got, test.want)
		}
		parsed, err := v2.ParseAutoNumberValue(test.spec, test.want)
		if err != nil || parsed != sequence {
			t.Fatalf("%s: parse %s = %d, %v", test.name, test.want, parsed, err)
		}
		if segment := v2.AutoNumberDateSegment(test.spec, test.want); segment != "20261025" {
			t.Fatalf("%s: date segment = %q", test.name, segment)
		}
	}
	legacy := v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 6}
	legacyValue := v2.AutoNumberValue(legacy, 12, when)
	if legacyValue != "HT-000012" {
		t.Fatalf("legacy format changed: %s", legacyValue)
	}
	if parsed, err := v2.ParseAutoNumberValue(legacy, legacyValue); err != nil || parsed != 12 {
		t.Fatalf("legacy parse = %d, %v", parsed, err)
	}
	for _, bad := range []v2.AutoNumberSpec{
		{Prefix: "HT-", Start: 1, Width: 4, DateFormat: "ddMMyyyy"},
		{Prefix: "HT-", Start: 1, Width: 4, Order: []string{"prefix", "date"}},
		{Prefix: "HT-", Start: 1, Width: 4, Order: []string{"prefix", "date", "date", "number"}},
		{Prefix: "HT-", Start: 1, Width: 4, Order: []string{"prefix", "date", "other"}},
	} {
		if err := v2.ValidateAutoNumberSpec(&bad); err == nil {
			t.Fatalf("invalid order/dateFormat accepted: %#v", bad)
		}
	}
	dated := v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 4, DateFormat: v2.AutoNumberDateFormat}
	for _, bad := range []string{
		"HT-202613010001",  // month 13 is not a legal date
		"HT-202610250001x", // trailing digit joins the number segment
		"XX-202610250001",  // wrong prefix
	} {
		if _, err := v2.ParseAutoNumberValue(dated, bad); err == nil {
			t.Fatalf("invalid value accepted: %s", bad)
		}
	}
}

func TestAutoNumberFixedStringContract(t *testing.T) {
	spec := v2.AutoNumberSpec{Prefix: "HT-", Start: 1, Width: 6}
	if err := v2.ValidateAutoNumberSpec(&spec); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 25, 23, 30, 0, 0, time.FixedZone("+08", 8*3600))
	for _, n := range []int64{1, 2, 1000000, v2.MaxAutoNumber} {
		value := v2.AutoNumberValue(spec, n, when)
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
