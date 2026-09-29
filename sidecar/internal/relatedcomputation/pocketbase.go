package relatedcomputation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// ClockRevisionField retains the storage name for derived-cache transactions on the
// vibetable_tables metadata; the freshness contract subtracts it from
// data_revision to track business inputs. Absent (older metadata) means zero.
const ClockRevisionField = "clock_revision"

// ExpectationFor derives the version that a computed cell must carry at the
// instant it is read or written. Dependency revisions are deliberately read
// from PocketBase inside the caller's transaction.
func ExpectationFor(
	ctx context.Context,
	app core.App,
	tableID string,
	fields []v2.FieldDefinition,
	fieldID string,
	sourceRevision int64,
) (Expectation, error) {
	if err := ctx.Err(); err != nil {
		return Expectation{}, err
	}
	field, ok := computedField(fields, fieldID)
	if !ok {
		return Expectation{}, fmt.Errorf("computed field %q is unavailable", fieldID)
	}
	version, err := definitionVersion(app, tableID, field)
	if err != nil {
		return Expectation{}, err
	}
	tables, err := dependencyTables(ctx, app, tableID, fields, field)
	if err != nil {
		return Expectation{}, err
	}
	revisions := make(map[string]int64, len(tables))
	for _, tableID := range tables {
		record, findErr := app.FindFirstRecordByFilter(
			"vibetable_tables",
			"table_id={:table}",
			dbx.Params{"table": tableID},
		)
		if findErr != nil {
			return Expectation{}, fmt.Errorf("load computed dependency %s: %w", tableID, findErr)
		}
		// The dependency watermark tracks business inputs only: derived
		// cache transactions advance clock_revision alongside data_revision,
		// and a missing counter on older metadata reads as zero.
		business, revisionErr := businessInputRevision(tableID, record)
		if revisionErr != nil {
			return Expectation{}, revisionErr
		}
		revisions[tableID] = business
	}
	references, err := ClockReferencesFor(ctx, app, tableID, fields, fieldID)
	if err != nil {
		return Expectation{}, err
	}
	watermark := Watermark(revisions)
	if period := formula.ClockSignature(ctx, references); period != "" {
		watermark += "|" + period
	}
	return Expectation{
		DefinitionVersion: version, SourceDataRevision: sourceRevision,
		DependencyWatermark: watermark,
	}, nil
}

// businessInputRevision derives the business-input revision of one table:
// data_revision minus its derived clock_revision. Validation fails
// closed on malformed counters instead of blessing a stale-looking watermark.
func businessInputRevision(tableID string, record *core.Record) (int64, error) {
	data, dataOK := storedTableCounter(record.GetRaw("data_revision"))
	if !dataOK || data < 0 {
		return 0, fmt.Errorf("dependency table %s has an invalid data revision", tableID)
	}
	clock := int64(0)
	if raw := record.GetRaw(ClockRevisionField); raw != nil {
		counter, clockOK := storedTableCounter(raw)
		// A non-zero counter can only come from the migrated optional field;
		// anything else indicates corrupt metadata and must not read as zero.
		if !clockOK || counter < 0 || counter > data {
			return 0, fmt.Errorf("dependency table %s has an invalid clock revision", tableID)
		}
		if counter != 0 && record.Collection().Fields.GetByName(ClockRevisionField) == nil {
			return 0, fmt.Errorf("dependency table %s counts clock revisions without the field", tableID)
		}
		clock = counter
	}
	return data - clock, nil
}

// PocketBase NumberField normalizes stored values to float64. Accept only
// exact non-negative counters within the existing safe-integer contract.
func storedTableCounter(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || number < 0 || number >= 1<<53 {
		return 0, false
	}
	return int64(number), true
}

func WrapValues(
	ctx context.Context,
	app core.App,
	tableID string,
	fields []v2.FieldDefinition,
	sourceRevision int64,
	values map[string]any,
) (map[string]any, error) {
	result := make(map[string]any, len(values))
	for name, value := range values {
		field, ok := computedField(fields, name)
		if !ok {
			return nil, fmt.Errorf("computed field %q is unavailable", name)
		}
		expectation, err := ExpectationFor(
			ctx, app, tableID, fields, field.Identity.FieldID, sourceRevision,
		)
		if err != nil {
			return nil, err
		}
		envelope := Ready(value, CellVersion{
			DefinitionVersion:   expectation.DefinitionVersion,
			SourceDataRevision:  expectation.SourceDataRevision,
			DependencyWatermark: expectation.DependencyWatermark,
		})
		raw, err := json.Marshal(envelope)
		if err != nil {
			return nil, fmt.Errorf("encode computed field %q: %w", field.Identity.PhysicalName, err)
		}
		var stored map[string]any
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil, fmt.Errorf("normalize computed field %q: %w", field.Identity.PhysicalName, err)
		}
		result[field.Identity.PhysicalName] = stored
	}
	return result, nil
}

func ProjectStored(value any) any {
	if envelope, ok := Decode(value); ok {
		return envelope.Value
	}
	return value
}

func definitionVersion(
	app core.App,
	tableID string,
	field v2.FieldDefinition,
) (int, error) {
	if field.Formula != nil {
		record, err := app.FindFirstRecordByFilter(
			"vibetable_formulas",
			"table_id={:table} && field_id={:field}",
			dbx.Params{"table": tableID, "field": field.Identity.FieldID},
		)
		if err != nil {
			return 0, err
		}
		version := record.GetInt("version")
		if version < 1 {
			return 0, errors.New("computed formula version is invalid")
		}
		return version, nil
	}
	if field.Lookup == nil {
		return 0, errors.New("field is not computed")
	}
	record, err := app.FindFirstRecordByFilter(
		"vibetable_lookups",
		"table_id={:table} && field_id={:field}",
		dbx.Params{"table": tableID, "field": field.Identity.FieldID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	version := record.GetInt("revision")
	if version < 1 {
		return 0, errors.New("computed lookup version is invalid")
	}
	return version, nil
}

func dependencyTables(ctx context.Context, app core.App, tableID string, fields []v2.FieldDefinition, field v2.FieldDefinition) ([]string, error) {
	cache, ok := ctx.Value(clockCacheKey{}).(*clockCache)
	if !ok {
		cache = newClockCache()
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, found := computedField(fields, field.Identity.FieldID); !found {
		fields = append(append([]v2.FieldDefinition(nil), fields...), field)
	}
	cache.fields[tableID] = fields
	key := clockFieldKey{tableID, field.Identity.FieldID}
	if _, err := cache.visit(ctx, app, key, map[clockFieldKey]bool{}); err != nil {
		return nil, err
	}
	return cache.tables[key], nil
}

func directDependencyTables(
	ctx context.Context,
	app core.App,
	tableID string,
	fields []v2.FieldDefinition,
	field v2.FieldDefinition,
) ([]string, error) {
	if field.Formula != nil {
		collection, predicate := "vibetable_formula_dependencies", "source_table_id={:table} && formula_field_id={:field}"
		if field.Formula.Language == "cel-v2" {
			collection, predicate = "vibetable_computation_dependencies", "source_table_id={:table} && computed_field_id={:field} && computed_kind='formula'"
		}
		records, err := app.FindRecordsByFilter(
			collection,
			predicate,
			"+target_table_id",
			0,
			0,
			dbx.Params{"table": tableID, "field": field.Identity.FieldID},
		)
		if err != nil {
			return nil, err
		}
		result := make([]string, 0, len(records))
		seen := map[string]struct{}{}
		for _, record := range records {
			targetTableID := record.GetString("target_table_id")
			if targetTableID == "" || (targetTableID == tableID && field.Formula.Language != "cel-v2") {
				continue
			}
			if _, exists := seen[targetTableID]; !exists {
				seen[targetTableID] = struct{}{}
				result = append(result, targetTableID)
			}
		}
		return result, nil
	}
	if field.Lookup == nil {
		return nil, nil
	}
	if field.Lookup.Condition != nil {
		return []string{field.Lookup.Condition.SourceTableID}, nil
	}
	currentFields := fields
	result := make([]string, 0, len(field.Lookup.Path))
	for index, step := range field.Lookup.Path {
		relation, ok := relationField(currentFields, step.RelationFieldID)
		if !ok || relation.Relation == nil {
			return nil, errors.New("computed lookup relation is unavailable")
		}
		targetID := relation.Relation.TargetTableID
		result = append(result, targetID)
		if index+1 < len(field.Lookup.Path) {
			next, err := describeTableFields(ctx, app, targetID)
			if err != nil {
				return nil, err
			}
			currentFields = next
		}
	}
	return result, nil
}

func describeTableFields(
	ctx context.Context,
	app core.App,
	tableID string,
) ([]v2.FieldDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_fields",
		"table_id={:table} && schema_model_version=2 && lifecycle_state='active'",
		"id", 0, 0, dbx.Params{"table": tableID},
	)
	if err != nil {
		return nil, err
	}
	result := make([]v2.FieldDefinition, 0, len(records))
	for _, record := range records {
		raw, marshalErr := json.Marshal(record.GetRaw("definition_v2_json"))
		if marshalErr != nil {
			return nil, marshalErr
		}
		var definition v2.FieldDefinition
		if decodeErr := v2.StrictDecode(raw, &definition); decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, definition)
	}
	return result, nil
}

func computedField(
	fields []v2.FieldDefinition,
	name string,
) (v2.FieldDefinition, bool) {
	for _, field := range fields {
		if (field.LogicalType == v2.LogicalFormula || field.LogicalType == v2.LogicalLookup) &&
			(field.Identity.PhysicalName == name || field.Identity.FieldID == name) {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
}

func relationField(
	fields []v2.FieldDefinition,
	fieldID string,
) (v2.FieldDefinition, bool) {
	for _, field := range fields {
		if field.Identity.FieldID == fieldID && field.Relation != nil {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
}
