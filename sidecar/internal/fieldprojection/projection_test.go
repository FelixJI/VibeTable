package fieldprojection_test

import (
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldprojection"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestProjectionDistinguishesMissingFromEveryExplicitZeroValue(t *testing.T) {
	t.Parallel()
	definition := numberDefinition()
	descriptor := fieldprojection.Descriptor{Definition: definition}
	missing := map[string]any{
		definition.Identity.PhysicalName:       float64(0),
		definition.Value.Presence.PhysicalName: false,
	}
	explicit := map[string]any{
		definition.Identity.PhysicalName:       float64(0),
		definition.Value.Presence.PhysicalName: true,
	}
	if descriptor.ProductValue(missing) != nil || !descriptor.Blank(missing) {
		t.Fatalf("missing projected as %#v", descriptor.ProductValue(missing))
	}
	if descriptor.ProductValue(explicit) != float64(0) || descriptor.Blank(explicit) {
		t.Fatalf("explicit zero projected as %#v", descriptor.ProductValue(explicit))
	}
	if _, participates, err := descriptor.UniqueKey(missing); err != nil || participates {
		t.Fatalf("missing unique key = %v, %v", participates, err)
	}
	if _, participates, err := descriptor.UniqueKey(explicit); err != nil || !participates {
		t.Fatalf("explicit unique key = %v, %v", participates, err)
	}
}

func TestProductRowNeverLeaksProviderOrRetiredFields(t *testing.T) {
	t.Parallel()
	active := numberDefinition()
	retired := numberDefinition()
	retired.Identity.FieldID = "fld_01JRETIRE"
	retired.Identity.PhysicalName = "f_01jretire"
	retired.Identity.ProviderFieldID = "pb_01JRETIRE"
	retired.Value.Presence.PhysicalName = "__vt_has_f_01jretire"
	retired.Lifecycle = v2.Lifecycle{State: v2.LifecycleRetired, RetiredAt: pointer("2026-07-28T01:00:00Z")}
	row := fieldprojection.ProductRow(
		[]v2.FieldDefinition{active, retired},
		map[string]any{
			active.Identity.PhysicalName:        float64(3),
			active.Value.Presence.PhysicalName:  true,
			retired.Identity.PhysicalName:       float64(4),
			retired.Value.Presence.PhysicalName: true,
			active.Identity.ProviderFieldID:     "must-not-leak",
		},
	)
	if len(row) != 1 || row[active.Identity.FieldID] != float64(3) {
		t.Fatalf("product row = %#v", row)
	}
}

func TestProjectionKeepsExplicitEmptyMultiValueTypedAsCollection(t *testing.T) {
	t.Parallel()
	definition := numberDefinition()
	definition.LogicalType = v2.LogicalMultiSelect
	descriptor := fieldprojection.Descriptor{Definition: definition}
	physical := map[string]any{
		definition.Identity.PhysicalName:       "",
		definition.Value.Presence.PhysicalName: true,
	}
	value, ok := descriptor.ProductValue(physical).([]any)
	if !ok || len(value) != 0 || descriptor.Blank(physical) {
		t.Fatalf("explicit empty multi-value projected as %#v", value)
	}
}

func numberDefinition() v2.FieldDefinition {
	recommended, _ := v2.RecommendedDefaults(v2.LogicalNumber)
	recommended.Value.Presence = v2.PresenceSpec{
		Mode: v2.PresenceCompanion, ProviderFieldID: "pb_01JPRESEN",
		PhysicalName: "__vt_has_f_01jfieldx",
	}
	return v2.FieldDefinition{
		Contract: v2.Contract,
		Identity: v2.FieldIdentity{
			FieldID: "fld_01JFIELDX", PhysicalName: "f_01jfieldx", ProviderFieldID: "pb_01JFIELDX",
		},
		DisplayName: "数字", LogicalType: v2.LogicalNumber,
		Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
		Value:     recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
	}
}

func pointer(value string) *string {
	return &value
}
