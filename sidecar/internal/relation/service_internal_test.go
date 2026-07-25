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
