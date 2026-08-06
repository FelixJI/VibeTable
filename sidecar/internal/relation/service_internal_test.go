package relation

import (
	"encoding/json"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestDescriptorFromSerializesEmptyAllowlistAsArray(t *testing.T) {
	field := schema.FieldDefinition{
		FieldID:      "customer",
		PhysicalName: "customer_record",
		Relation: &schema.RelationSpec{
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
	allowed, ok := decoded["allowedTargetTableIds"].([]any)
	if !ok {
		t.Fatalf("allowedTargetTableIds = %#v, want JSON array", decoded["allowedTargetTableIds"])
	}
	if len(allowed) != 0 {
		t.Fatalf("allowedTargetTableIds = %#v, want empty array", allowed)
	}
}

func TestQuickCreateEligibilityRequiresOnlyTheWritableDisplayField(t *testing.T) {
	display := schema.FieldDefinition{
		FieldID: "name_id", PhysicalName: "name", DisplayName: "名称",
		Kind: schema.FieldKindScalar, DataType: schema.DataTypeShortText,
		StorageType: schema.StorageText, Nullable: false,
	}
	definition := schema.TableDefinition{
		PrimaryDisplayFieldID: display.FieldID,
		Fields:                []schema.FieldDefinition{display},
	}
	if eligible, reason := quickCreateEligibility(definition); !eligible || reason != "" {
		t.Fatalf("single-label eligibility = %v, %q", eligible, reason)
	}

	required := display
	required.FieldID = "region_id"
	required.PhysicalName = "region"
	required.DisplayName = "区域"
	definition.Fields = append(definition.Fields, required)
	if eligible, reason := quickCreateEligibility(definition); eligible || reason == "" {
		t.Fatalf("required-field eligibility = %v, %q", eligible, reason)
	}

	required.DefaultValue = "华东"
	definition.Fields[1] = required
	if eligible, reason := quickCreateEligibility(definition); !eligible || reason != "" {
		t.Fatalf("defaulted-field eligibility = %v, %q", eligible, reason)
	}
}
