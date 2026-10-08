// Package autonumber owns persisted allocator state inside the caller's business transaction.
package autonumber

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const StateColumn = "auto_number_state_json"

type State struct {
	HighWater *int64 `json:"highWater"`
}

func DecodeState(raw []byte, spec v2.AutoNumberSpec) (int64, error) {
	var state State
	if err := v2.StrictDecode(raw, &state); err != nil {
		return 0, fmt.Errorf("auto_number.state_invalid: %w", err)
	}
	if state.HighWater == nil || *state.HighWater < spec.Start-1 || *state.HighWater > v2.MaxAutoNumber {
		return 0, fmt.Errorf("auto_number.state_invalid: highWater is missing or outside the safe range")
	}
	return *state.HighWater, nil
}

type Field struct {
	Definition v2.FieldDefinition
	metadata   *core.Record
	highWater  int64
	dirty      bool
}

// Load includes retired fields: hiding a number does not pause its allocation.
func Load(ctx context.Context, app core.App, tableID string) ([]*Field, error) {
	records, err := app.FindRecordsByFilter("vibetable_fields", "table_id={:table}", "id", 0, 0, dbx.Params{"table": tableID})
	if err != nil {
		return nil, err
	}
	fields := make([]*Field, 0, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var discriminator struct {
			LogicalType v2.LogicalType `json:"logicalType"`
		}
		_ = json.Unmarshal([]byte(record.GetString("definition_v2_json")), &discriminator)
		if record.GetString("data_type") != "autoNumber" && discriminator.LogicalType != v2.LogicalAutoNumber {
			continue
		}
		var definition v2.FieldDefinition
		if err := v2.StrictDecode([]byte(record.GetString("definition_v2_json")), &definition); err != nil {
			return nil, fmt.Errorf("auto_number.state_invalid: %w", err)
		}
		if record.GetString("data_type") != "autoNumber" || definition.LogicalType != v2.LogicalAutoNumber || definition.AutoNumber == nil || definition.Identity.FieldID != record.GetString("field_id") || definition.Identity.PhysicalName != record.GetString("physical_name") {
			return nil, fmt.Errorf("auto_number.state_invalid: metadata does not match definition")
		}
		if err := v2.Validate(definition); err != nil {
			return nil, err
		}
		highWater, err := DecodeState([]byte(record.GetString(StateColumn)), *definition.AutoNumber)
		if err != nil {
			return nil, err
		}
		fields = append(fields, &Field{Definition: definition, metadata: record, highWater: highWater})
	}
	return fields, nil
}

func (field *Field) Assign(record *core.Record) error {
	if _, ok := record.Collection().Fields.GetByName(field.Definition.Identity.PhysicalName).(*core.TextField); !ok {
		return fmt.Errorf("auto_number.state_invalid: physical text field is missing")
	}
	if field.highWater >= v2.MaxAutoNumber {
		return fmt.Errorf("auto_number.exhausted: no safe sequence remains")
	}
	field.highWater++
	record.Set(field.Definition.Identity.PhysicalName, v2.AutoNumberValue(*field.Definition.AutoNumber, field.highWater))
	field.dirty = true
	return nil
}

func Save(app core.App, fields []*Field) error {
	for _, field := range fields {
		if !field.dirty {
			continue
		}
		raw, err := json.Marshal(State{HighWater: &field.highWater})
		if err != nil {
			return err
		}
		field.metadata.Set(StateColumn, types.JSONRaw(raw))
		if err := app.Save(field.metadata); err != nil {
			return err
		}
	}
	return nil
}

// Backfill runs only when creating a new field, under the frozen field-change transaction.
func Backfill(ctx context.Context, app core.App, tableID string, collection *core.Collection, definition v2.FieldDefinition) error {
	metadata, err := app.FindFirstRecordByFilter("vibetable_fields", "table_id={:table} && field_id={:field}", dbx.Params{"table": tableID, "field": definition.Identity.FieldID})
	if err != nil {
		return err
	}
	field := &Field{Definition: definition, metadata: metadata, highWater: definition.AutoNumber.Start - 1, dirty: true}
	records, err := app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		return err
	}
	if int64(len(records)) > v2.MaxAutoNumber-field.highWater {
		return fmt.Errorf("auto_number.exhausted: backfill exceeds safe sequence")
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := field.Assign(record); err != nil {
			return err
		}
		if err := app.Save(record); err != nil {
			return err
		}
	}
	return Save(app, []*Field{field})
}
