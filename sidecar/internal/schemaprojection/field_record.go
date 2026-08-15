package schemaprojection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// PopulateFieldRecord writes query-friendly columns and the authoritative
// closed V2 document. Callers save the supplied PocketBase record atomically.
func PopulateFieldRecord(record *core.Record, tableID string, definition v2.FieldDefinition) error {
	raw, err := json.Marshal(definition)
	if err != nil {
		return fmt.Errorf("encode canonical field definition: %w", err)
	}
	sum := sha256.Sum256(raw)
	identity, _ := json.Marshal(definition.Identity)
	value, _ := json.Marshal(definition.Value)
	constraints, _ := json.Marshal(definition.Constraints)
	storage, _ := json.Marshal(definition.Storage)
	display, _ := json.Marshal(definition.Display)
	record.Set("table_id", tableID)
	record.Set("field_id", definition.Identity.FieldID)
	record.Set("physical_name", definition.Identity.PhysicalName)
	record.Set("display_name", definition.DisplayName)
	record.Set("kind", storedFieldKind(definition.LogicalType))
	record.Set("data_type", string(definition.LogicalType))
	record.Set("storage_type", string(definition.Storage.Kind))
	record.Set("constraints_json", types.JSONRaw(constraints))
	record.Set("editor_json", types.JSONRaw(display))
	record.Set("schema_model_version", 2)
	record.Set("lifecycle_state", definition.Lifecycle.State)
	record.Set("retired_at", "")
	record.Set("identity_json", types.JSONRaw(identity))
	record.Set("value_semantics_json", types.JSONRaw(value))
	record.Set("constraints_v2_json", types.JSONRaw(constraints))
	record.Set("storage_v2_json", types.JSONRaw(storage))
	record.Set("display_v2_json", types.JSONRaw(display))
	record.Set("recommended_defaults_version", definition.Value.Default.DefaultsVersion)
	record.Set("definition_hash", hex.EncodeToString(sum[:]))
	record.Set("definition_v2_json", types.JSONRaw(raw))
	return nil
}

func storedFieldKind(logicalType v2.LogicalType) string {
	switch logicalType {
	case v2.LogicalRelation:
		return "relation"
	case v2.LogicalFile:
		return "attachment"
	case v2.LogicalFormula:
		return "formula"
	case v2.LogicalLookup:
		return "lookup"
	default:
		return "scalar"
	}
}
