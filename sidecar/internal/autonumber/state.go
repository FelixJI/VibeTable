// Package autonumber owns persisted allocator state inside the caller's business transaction.
package autonumber

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

// Assign numbers a brand-new record. createdAt must be the same instant the
// caller uses for its audit envelope: business tables carry no created
// column, so the audit ledger is the authoritative creation record the
// number's date segment must match.
func (field *Field) Assign(record *core.Record, createdAt time.Time) error {
	if _, ok := record.Collection().Fields.GetByName(field.Definition.Identity.PhysicalName).(*core.TextField); !ok {
		return fmt.Errorf("auto_number.state_invalid: physical text field is missing")
	}
	if field.highWater >= v2.MaxAutoNumber {
		return fmt.Errorf("auto_number.exhausted: no safe sequence remains")
	}
	field.highWater++
	record.Set(field.Definition.Identity.PhysicalName, v2.AutoNumberValue(*field.Definition.AutoNumber, field.highWater, createdAt))
	field.dirty = true
	return nil
}

// CreationTimes returns each record's current-incarnation creation instant:
// the latest operation='insert' audit envelope in stable ledger order
// (data_revision/sequence descending). Row-history restore re-inserts the same
// record id with a fresh number, so the newest insert — never MIN wall time,
// never the first insert — is the authoritative creation moment.
func CreationTimes(ctx context.Context, app core.App, tableID string) (map[string]time.Time, error) {
	rows := []struct {
		RecordID   string `db:"record_id"`
		OccurredAt string `db:"occurred_at"`
	}{}
	if err := app.DB().NewQuery(
		"SELECT record_id, occurred_at FROM (" +
			"SELECT record_id, occurred_at, ROW_NUMBER() OVER (PARTITION BY record_id ORDER BY data_revision DESC, sequence DESC) AS rn " +
			"FROM vibetable_audit_events WHERE table_id={:table} AND operation='insert'" +
			") WHERE rn=1",
	).WithContext(ctx).Bind(dbx.Params{"table": tableID}).All(&rows); err != nil {
		return nil, err
	}
	result := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		parsed, err := types.ParseDateTime(row.OccurredAt)
		if err != nil || parsed.IsZero() {
			continue
		}
		result[row.RecordID] = parsed.Time()
	}
	return result, nil
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

// Reformat rewrites every stored value from the frozen before spec to the
// after spec inside the caller's field-change transaction. Sequences and
// the allocator high-water mark never change. New values are duplicate-
// checked before any write; changed rows are first parked on placeholders
// that are verified absent from both value sets, so a transient value can
// never collide under a unique index. Any failure rolls the change back.
func Reformat(ctx context.Context, app core.App, tableID string, collection *core.Collection, before, after v2.FieldDefinition) ([]ReformatChange, error) {
	if before.Identity.PhysicalName != after.Identity.PhysicalName ||
		before.AutoNumber == nil || after.AutoNumber == nil {
		return nil, fmt.Errorf("auto_number.state_invalid: reformat requires one stable physical field")
	}
	records, err := app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		return nil, err
	}
	physical := after.Identity.PhysicalName
	rewrites := make([]reformatRewrite, 0, len(records))
	finalValues := make(map[string]struct{}, len(records))
	existing := make(map[string]struct{}, len(records))
	var creations map[string]time.Time
	if v2.AutoNumberDateEnabled(*after.AutoNumber) {
		creations, err = CreationTimes(ctx, app, tableID)
		if err != nil {
			return nil, err
		}
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stored := record.GetString(physical)
		sequence, err := v2.ParseAutoNumberValue(*before.AutoNumber, stored)
		if err != nil {
			return nil, fmt.Errorf("auto_number.reformat_invalid: %s: %w", record.Id, err)
		}
		var createdAt time.Time
		if creations != nil {
			var ok bool
			if createdAt, ok = creations[record.Id]; !ok {
				// Never synthesize a 00010101 segment: rows written through the
				// product boundary always have a creation audit envelope, so a
				// missing one must fail the whole change closed.
				return nil, fmt.Errorf("auto_number.reformat_invalid: %s: record has no creation audit envelope", record.Id)
			}
		}
		value := v2.AutoNumberValue(*after.AutoNumber, sequence, createdAt)
		if _, duplicate := finalValues[value]; duplicate {
			return nil, fmt.Errorf("auto_number.reformat_conflict: %s", value)
		}
		finalValues[value] = struct{}{}
		existing[stored] = struct{}{}
		if value != stored {
			rewrites = append(rewrites, reformatRewrite{record: record, before: record.Clone(), value: value})
		}
	}
	if len(rewrites) == 0 {
		return nil, nil
	}
	parked := parkReformatValues(rewrites, finalValues, existing)
	for _, pending := range rewrites {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pending.record.Set(physical, parked[pending.record.Id])
		if err := app.Save(pending.record); err != nil {
			return nil, fmt.Errorf("auto_number.reformat_parking_failed: %w", err)
		}
	}
	for _, pending := range rewrites {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pending.record.Set(physical, pending.value)
		if err := app.Save(pending.record); err != nil {
			return nil, err
		}
	}
	changes := make([]ReformatChange, 0, len(rewrites))
	for _, pending := range rewrites {
		changes = append(changes, ReformatChange{Before: pending.before, After: pending.record})
	}
	return changes, nil
}

// ReformatChange keeps the original business image, never a parked value.
type ReformatChange struct {
	Before *core.Record
	After  *core.Record
}

// reformatRewrite is one row whose stored value must change.
type reformatRewrite struct {
	record *core.Record
	before *core.Record
	value  string
}

// parkReformatValues allocates a unique placeholder per changed row using a
// deterministic marker that grows until it is free of every stored and final
// value. Record ids are unique, so placeholders cannot collide with each
// other; the marker growth only guards the pathological case of a stored
// value literally equal to marker+id.
func parkReformatValues(
	rewrites []reformatRewrite,
	finalValues map[string]struct{},
	existing map[string]struct{},
) map[string]string {
	parked := make(map[string]string, len(rewrites))
	marker := "vtrefmt"
	for _, pending := range rewrites {
		candidate := marker + pending.record.Id
		for growth := 0; ; growth++ {
			_, clashFinal := finalValues[candidate]
			_, clashExisting := existing[candidate]
			if !clashFinal && !clashExisting {
				break
			}
			if growth >= 128 {
				// Unreachable for real number values (bounded length); keep a
				// hard stop instead of an endless loop.
				candidate = pending.record.Id
				break
			}
			marker += "x"
			candidate = marker + pending.record.Id
		}
		parked[pending.record.Id] = candidate
	}
	return parked
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
	var creations map[string]time.Time
	if v2.AutoNumberDateEnabled(*definition.AutoNumber) {
		creations, err = CreationTimes(ctx, app, tableID)
		if err != nil {
			return err
		}
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		createdAt := time.Time{}
		if creations != nil {
			var ok bool
			if createdAt, ok = creations[record.Id]; !ok {
				return fmt.Errorf("auto_number.reformat_invalid: %s: record has no creation audit envelope", record.Id)
			}
		}
		if err := field.Assign(record, createdAt); err != nil {
			return err
		}
		if err := app.Save(record); err != nil {
			return err
		}
	}
	return Save(app, []*Field{field})
}
