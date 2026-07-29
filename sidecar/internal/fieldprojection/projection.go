package fieldprojection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type Descriptor struct {
	Definition v2.FieldDefinition
}

func (descriptor Descriptor) ProductValue(physical map[string]any) any {
	definition := descriptor.Definition
	if definition.Value.Presence.Mode == v2.PresenceCompanion {
		present, _ := physical[definition.Value.Presence.PhysicalName].(bool)
		if !present {
			return nil
		}
	}
	value, exists := physical[definition.Identity.PhysicalName]
	if !exists || value == nil {
		return nil
	}
	if text, ok := value.(string); ok && text == "" {
		switch definition.LogicalType {
		case v2.LogicalMultiSelect, v2.LogicalFile:
			// PocketBase exposes an explicitly present empty multi-value field
			// as an empty string. Keep the product contract typed as a
			// collection; presence still distinguishes this from missing.
			return []any{}
		}
		if definition.LogicalType == v2.LogicalRelation &&
			definition.Relation != nil &&
			definition.Relation.Cardinality == "many" {
			return []any{}
		}
	}
	switch typed := value.(type) {
	case pbtypes.DateTime:
		if definition.LogicalType == v2.LogicalDate {
			return typed.Time().Format("2006-01-02")
		}
		if definition.LogicalType == v2.LogicalDateTime ||
			definition.LogicalType == v2.LogicalAutoDate {
			return typed.Time().UTC().Format(time.RFC3339Nano)
		}
	}
	return clone(value)
}

func (descriptor Descriptor) Blank(physical map[string]any) bool {
	return descriptor.ProductValue(physical) == nil
}

func (descriptor Descriptor) UniqueKey(physical map[string]any) (string, bool, error) {
	value := descriptor.ProductValue(physical)
	if value == nil {
		return "", false, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", false, fmt.Errorf("encode unique field value: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), true, nil
}

func ProductRow(
	definitions []v2.FieldDefinition,
	physical map[string]any,
) map[string]any {
	result := make(map[string]any, len(definitions))
	for _, definition := range definitions {
		if definition.Lifecycle.State != v2.LifecycleActive {
			continue
		}
		result[definition.Identity.FieldID] = (Descriptor{Definition: definition}).ProductValue(physical)
	}
	return result
}

func clone(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}
