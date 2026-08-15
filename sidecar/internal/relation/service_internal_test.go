package relation

import (
	"encoding/json"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestDescriptorFromOmitsRemovedRelationModes(t *testing.T) {
	field := v2.FieldDefinition{
		Identity: v2.FieldIdentity{
			FieldID: "customer", PhysicalName: "customer_record",
		},
		LogicalType: v2.LogicalRelation,
		Relation: &v2.RelationSpec{
			TargetTableID: "customers",
			Cardinality:   "one",
			DeletePolicy:  "setNull",
		},
	}

	descriptor := descriptorFrom("orders.customer", "orders", field)
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatalf("Marshal(descriptor): %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal(descriptor): %v", err)
	}
	for _, removed := range []string{"mode", "junctionTableId", "allowedTargetTableIds"} {
		if _, exists := decoded[removed]; exists {
			t.Fatalf("descriptor still exposes removed field %q: %#v", removed, decoded)
		}
	}
}

func TestQuickCreateEligibilityRequiresOnlyTheWritableDisplayField(t *testing.T) {
	display := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "name_id", PhysicalName: "name"},
		DisplayName: "名称", LogicalType: v2.LogicalText,
		Value: v2.ValueSpec{Required: true},
	}
	definition := schemaexecution.Table{
		PrimaryDisplayFieldID: display.Identity.FieldID,
		Snapshot:              v2.SchemaSnapshot{Fields: []v2.FieldDefinition{display}},
	}
	if eligible, reason := quickCreateEligibility(definition); !eligible || reason != "" {
		t.Fatalf("single-label eligibility = %v, %q", eligible, reason)
	}

	required := display
	required.Identity.FieldID = "region_id"
	required.Identity.PhysicalName = "region"
	required.DisplayName = "区域"
	definition.Snapshot.Fields = append(definition.Snapshot.Fields, required)
	if eligible, reason := quickCreateEligibility(definition); eligible || reason == "" {
		t.Fatalf("required-field eligibility = %v, %q", eligible, reason)
	}

	required.Value.Default = v2.DefaultSpec{Enabled: true, Value: "华东"}
	definition.Snapshot.Fields[1] = required
	if eligible, reason := quickCreateEligibility(definition); !eligible || reason != "" {
		t.Fatalf("defaulted-field eligibility = %v, %q", eligible, reason)
	}
}

func TestLookupOutputStorageAdaptsV2TypeAtWireSeam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		field  v2.FieldDefinition
		output string
	}{
		{
			name: "integer number",
			field: v2.FieldDefinition{
				LogicalType: v2.LogicalNumber,
				Storage:     v2.StorageSpec{Options: v2.StorageOptions{OnlyInt: true}},
			},
			output: "integer",
		},
		{
			name: "decimal formula",
			field: v2.FieldDefinition{
				LogicalType: v2.LogicalFormula,
				Formula:     &v2.FormulaSpec{ResultType: v2.LogicalNumber},
			},
			output: "decimal",
		},
		{
			name: "date time",
			field: v2.FieldDefinition{
				LogicalType: v2.LogicalDateTime,
			},
			output: "datetime",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if output := lookupOutputStorage(outputTypeFor(test.field)); output != test.output {
				t.Fatalf("lookup output storage = %q, want %q", output, test.output)
			}
		})
	}
}
