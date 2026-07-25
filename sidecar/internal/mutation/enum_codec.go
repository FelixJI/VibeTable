package mutation

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func encodeFieldStorageValue(
	record *core.Record,
	field schema.FieldDefinition,
	value any,
) (any, error) {
	if field.DataType != schema.DataTypeSelect &&
		field.DataType != schema.DataTypeMultiSelect {
		return value, nil
	}
	pbField, ok := record.Collection().Fields.GetByName(
		field.PhysicalName,
	).(*core.SelectField)
	if !ok {
		return nil, fmt.Errorf("compiled select field %q is unavailable", field.PhysicalName)
	}
	return schema.EncodeSelectValueForStorage(field, value, pbField.Values)
}

func fieldByPhysicalName(
	definition schema.TableDefinition,
	name string,
) (schema.FieldDefinition, bool) {
	for _, field := range definition.Fields {
		if field.PhysicalName == name {
			return field, true
		}
	}
	return schema.FieldDefinition{}, false
}
