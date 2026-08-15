package schemaexecution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const (
	tablesCollectionName   = "vibetable_tables"
	formulasCollectionName = "vibetable_formulas"
)

// ErrTableNotFound is returned when a schema execution snapshot cannot be
// resolved for the requested stable table identity.
var ErrTableNotFound = errors.New("schema execution table not found")

// ErrRetiredFieldNotFound is returned when attachment/history recovery asks
// for a field that is not an authoritative retired Schema V2 definition for
// the requested table.
var ErrRetiredFieldNotFound = errors.New("schema execution retired field not found")

// FormulaRuntime contains execution-only state that does not belong in the
// persisted schema-v2 field definition.
type FormulaRuntime struct {
	Version int
	Status  string
}

// Table is the execution projection of a schema-v2 snapshot. It deliberately
// keeps PocketBase bindings and computed-field runtime state outside the
// schema contract instead of reconstructing the retired legacy schema model.
type Table struct {
	Snapshot              v2.SchemaSnapshot
	PhysicalName          string
	Kind                  string
	ViewSourceTableID     string
	PrimaryDisplayFieldID string
	ArchivePolicy         v2.ArchivePolicy
	FormulaRuntime        map[string]FormulaRuntime
}

// Field returns an active field by stable field ID or PocketBase physical name.
func (table Table) Field(identity string) (v2.FieldDefinition, bool) {
	for _, field := range table.Snapshot.Fields {
		if field.Identity.FieldID == identity || field.Identity.PhysicalName == identity {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
}

// RetiredField loads one retired Schema V2 definition by stable identity.
// Retired fields stay outside Table.Snapshot so normal execution can never
// accidentally write them; recovery modules opt into this narrow seam.
func RetiredField(
	ctx context.Context,
	app core.App,
	tableID string,
	fieldID string,
) (v2.FieldDefinition, error) {
	if err := ctx.Err(); err != nil {
		return v2.FieldDefinition{}, err
	}
	record, err := app.FindFirstRecordByFilter(
		"vibetable_fields",
		"table_id={:table} && field_id={:field} && lifecycle_state='retired'",
		dbx.Params{"table": tableID, "field": fieldID},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return v2.FieldDefinition{}, fmt.Errorf(
				"%w: %s/%s", ErrRetiredFieldNotFound, tableID, fieldID,
			)
		}
		return v2.FieldDefinition{}, fmt.Errorf("read retired field: %w", err)
	}
	definition, err := decodeField(record)
	if err != nil {
		return v2.FieldDefinition{}, fmt.Errorf("read retired field: %w", err)
	}
	return definition, nil
}

// Describe loads one revision-consistent schema-v2 execution projection.
func Describe(ctx context.Context, app core.App, tableID string) (Table, error) {
	if err := ctx.Err(); err != nil {
		return Table{}, err
	}

	initial, err := app.FindFirstRecordByFilter(
		tablesCollectionName,
		"table_id = {:tableID}",
		dbx.Params{"tableID": tableID},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Table{}, fmt.Errorf("%w: %s", ErrTableNotFound, tableID)
		}
		return Table{}, fmt.Errorf("read table execution binding: %w", err)
	}

	initialSchemaRevision := initial.GetInt("schema_revision")
	initialDataRevision := initial.GetInt("data_revision")
	fieldRecords, err := app.FindRecordsByFilter(
		"vibetable_fields",
		"table_id={:table} && lifecycle_state!='retired'",
		"id",
		0,
		0,
		dbx.Params{"table": tableID},
	)
	if err != nil {
		return Table{}, fmt.Errorf("list table fields: %w", err)
	}
	fields := make([]v2.FieldDefinition, 0, len(fieldRecords))
	for _, record := range fieldRecords {
		field, decodeErr := decodeField(record)
		if decodeErr != nil {
			return Table{}, fmt.Errorf("read table fields: %w", decodeErr)
		}
		fields = append(fields, field)
	}

	physicalName := initial.GetString("physical_name")
	collectionID := initial.GetString("collection_id")
	collection, err := app.FindCollectionByNameOrId(collectionID)
	if err != nil {
		return Table{}, fmt.Errorf("read table collection: %w", err)
	}
	physicalOrder := make(map[string]int, len(collection.Fields))
	for index, field := range collection.Fields {
		physicalOrder[field.GetName()] = index
	}
	sort.SliceStable(fields, func(left, right int) bool {
		leftIndex, leftFound := physicalOrder[fields[left].Identity.PhysicalName]
		rightIndex, rightFound := physicalOrder[fields[right].Identity.PhysicalName]
		if leftFound != rightFound {
			return leftFound
		}
		if leftIndex != rightIndex {
			return leftIndex < rightIndex
		}
		return fields[left].Identity.FieldID < fields[right].Identity.FieldID
	})

	capabilities := make([]v2.Capability, 0, len(v2.LogicalTypes))
	for _, logicalType := range v2.LogicalTypes {
		capability, capabilityErr := v2.CapabilityFor(logicalType)
		if capabilityErr != nil {
			return Table{}, fmt.Errorf("describe %s capability: %w", logicalType, capabilityErr)
		}
		capabilities = append(capabilities, capability)
	}
	archivePolicy := v2.ArchivePolicy{}
	if raw := initial.GetString("archive_policy"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &archivePolicy); err != nil {
			return Table{}, fmt.Errorf("decode archive policy: %w", err)
		}
	}
	kind := initial.GetString("kind")
	if kind == "" {
		kind = "base"
	}
	if archivePolicy.Mode == "" {
		archivePolicy.Mode = "none"
	}
	snapshot := v2.SchemaSnapshot{
		Contract:       v2.Contract,
		TableID:        tableID,
		DisplayName:    initial.GetString("display_name"),
		Kind:           kind,
		SchemaRevision: v2.FormatSchemaRevision(int64(initialSchemaRevision)),
		DataRevision:   int64(initialDataRevision),
		ArchivePolicy:  archivePolicy,
		Capabilities:   capabilities,
		Fields:         fields,
	}
	viewSourceTableID := ""
	if initial.GetString("kind") == "view" {
		raw, marshalErr := json.Marshal(initial.GetRaw("view_v2_json"))
		if marshalErr != nil || string(raw) == "null" {
			return Table{}, errors.New("schema view metadata is missing")
		}
		var view struct {
			SourceTableID string `json:"sourceTableId"`
		}
		if err := json.Unmarshal(raw, &view); err != nil || view.SourceTableID == "" {
			return Table{}, errors.New("schema view metadata is invalid")
		}
		viewSourceTableID = view.SourceTableID
	}

	formulaRuntime := make(map[string]FormulaRuntime)
	formulaRecords, err := app.FindRecordsByFilter(
		formulasCollectionName,
		"table_id = {:tableID}",
		"",
		0,
		0,
		dbx.Params{"tableID": tableID},
	)
	if err != nil {
		return Table{}, fmt.Errorf("read formula runtime: %w", err)
	}
	for _, record := range formulaRecords {
		formulaRuntime[record.GetString("field_id")] = FormulaRuntime{
			Version: record.GetInt("version"),
			Status:  record.GetString("status"),
		}
	}

	current, err := app.FindFirstRecordByFilter(
		tablesCollectionName,
		"table_id = {:tableID}",
		dbx.Params{"tableID": tableID},
	)
	if err != nil {
		return Table{}, fmt.Errorf("re-read table execution binding: %w", err)
	}
	if current.GetInt("schema_revision") != initialSchemaRevision ||
		current.GetInt("data_revision") != initialDataRevision ||
		snapshot.SchemaRevision != fmt.Sprintf("schema_%04d", initialSchemaRevision) ||
		snapshot.DataRevision != int64(initialDataRevision) {
		return Table{}, fmt.Errorf(
			"schema.execution_revision_conflict: table %s changed while loading execution snapshot",
			tableID,
		)
	}

	return Table{
		Snapshot:              snapshot,
		PhysicalName:          physicalName,
		Kind:                  kind,
		ViewSourceTableID:     viewSourceTableID,
		PrimaryDisplayFieldID: initial.GetString("primary_display_field_id"),
		ArchivePolicy:         archivePolicy,
		FormulaRuntime:        formulaRuntime,
	}, nil
}

func decodeField(record *core.Record) (v2.FieldDefinition, error) {
	raw, err := json.Marshal(record.GetRaw("definition_v2_json"))
	if err != nil || string(raw) == "null" {
		return v2.FieldDefinition{}, errors.New("schema V2 field definition is missing")
	}
	var definition v2.FieldDefinition
	if err := v2.StrictDecode(raw, &definition); err != nil {
		return v2.FieldDefinition{}, fmt.Errorf("decode Schema V2 field: %w", err)
	}
	if err := v2.Validate(definition); err != nil {
		return v2.FieldDefinition{}, fmt.Errorf("validate Schema V2 field: %w", err)
	}
	if definition.Identity.FieldID != record.GetString("field_id") ||
		definition.Identity.PhysicalName != record.GetString("physical_name") ||
		string(definition.Lifecycle.State) != record.GetString("lifecycle_state") {
		return v2.FieldDefinition{}, errors.New("stored Schema V2 field identity mismatch")
	}
	return definition, nil
}
