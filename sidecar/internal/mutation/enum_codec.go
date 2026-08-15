package mutation

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func encodeFieldStorageValue(
	record *core.Record,
	field v2.FieldDefinition,
	value any,
) (any, error) {
	if field.LogicalType != v2.LogicalSelect && field.LogicalType != v2.LogicalMultiSelect {
		return value, nil
	}
	pbField, ok := record.Collection().Fields.GetByName(
		field.Identity.PhysicalName,
	).(*core.SelectField)
	if !ok {
		return nil, fmt.Errorf("compiled select field %q is unavailable", field.Identity.PhysicalName)
	}
	allowed := make(map[string]struct{}, len(pbField.Values))
	for _, option := range pbField.Values {
		allowed[option] = struct{}{}
	}
	encode := func(candidate string) (string, error) {
		if _, ok := allowed[candidate]; !ok {
			return "", fmt.Errorf("select option is not present in the storage schema")
		}
		return candidate, nil
	}
	if field.LogicalType == v2.LogicalSelect {
		candidate, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("select value must be an option id")
		}
		return encode(candidate)
	}
	values, ok := value.([]string)
	if !ok {
		items, arrayOK := value.([]any)
		if !arrayOK {
			return nil, fmt.Errorf("multiSelect value must be an array")
		}
		values = make([]string, len(items))
		for index, item := range items {
			var itemOK bool
			values[index], itemOK = item.(string)
			if !itemOK {
				return nil, fmt.Errorf("multiSelect value must contain option ids")
			}
		}
	}
	encoded := make([]string, len(values))
	for index, candidate := range values {
		var err error
		encoded[index], err = encode(candidate)
		if err != nil {
			return nil, err
		}
	}
	return encoded, nil
}

func fieldByPhysicalName(
	definition schemaexecution.Table,
	name string,
) (v2.FieldDefinition, bool) {
	for _, field := range definition.Snapshot.Fields {
		if field.Identity.PhysicalName == name {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
}

func decodeSelectValue(field v2.FieldDefinition, value any) any {
	if field.LogicalType != v2.LogicalMultiSelect {
		return value
	}
	if text, ok := value.(string); ok {
		var decoded []string
		if json.Unmarshal([]byte(text), &decoded) == nil {
			return decoded
		}
	}
	return value
}

func productValuesEqual(left, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	if leftErr == nil && rightErr == nil {
		return string(leftRaw) == string(rightRaw)
	}
	return reflect.DeepEqual(left, right)
}
