package relatedcomputation

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// InputRevisionField records the last business revision relevant to one existing
// dependency edge. It is not a row, schema, or realtime revision.
const InputRevisionField = "input_revision"

// ChangedInputs operates on authoritative before/after images. Membership comes
// from each operation, since a mixed batch's aggregate event says only update.
func ChangedInputs(fields []v2.FieldDefinition, before, after map[string]any, operation string) map[string]bool {
	membership := operation == "insert" || operation == "delete" || operation == "archive" || operation == "restore"
	changed := map[string]bool{"__path__": membership}
	for _, field := range fields {
		name := field.Identity.PhysicalName
		if membership || !reflect.DeepEqual(ProjectStored(before[name]), ProjectStored(after[name])) {
			changed[field.Identity.FieldID] = true
		}
	}
	return changed
}

// InputAffected keeps relation-hop membership separate from whole-table
// membership. A note edit cannot change either kind of path.
func InputAffected(edge *core.Record, changed map[string]bool) bool {
	target := edge.GetString("target_field_id")
	if changed[target] {
		return true
	}
	if target != "__path__" || edge.GetString("relation_field_id") == "" {
		return false
	}
	var path []v2.LookupPathStep
	raw, err := json.Marshal(edge.GetRaw("path_json"))
	if err != nil || json.Unmarshal(raw, &path) != nil {
		return true
	} // Existing invalid-path handling fails closed in the runner.
	for _, step := range path {
		if changed[step.RelationFieldID] {
			return true
		}
	}
	return false
}

func TableInputRevision(ctx context.Context, app core.App, tableID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	record, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID})
	if err != nil {
		return 0, err
	}
	return businessInputRevision(tableID, record)
}

// AdvanceInputRevisions runs in the same transaction as the business write,
// before audit redaction. Derived refreshes never call this function.
func AdvanceInputRevisions(ctx context.Context, app core.App, tableID string, fields []v2.FieldDefinition, before, after map[string]any, operation string, nextDataRevision int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	edges, err := app.FindRecordsByFilter("vibetable_computation_dependencies", "target_table_id={:table}", "", 0, 0, dbx.Params{"table": tableID})
	if err != nil || len(edges) == 0 {
		return err
	}
	if edges[0].Collection().Fields.GetByName(InputRevisionField) == nil {
		return nil // A pre-migration reader keeps the original table watermark.
	}
	metadata, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID})
	if err != nil {
		return err
	}
	business, err := businessInputRevision(tableID, metadata)
	if err != nil {
		return err
	}
	data, _ := storedTableCounter(metadata.GetRaw("data_revision"))
	next := business + nextDataRevision - data
	if next < business || next >= 1<<53 {
		return fmt.Errorf("dependency table %s has an invalid next input revision", tableID)
	}
	changed := ChangedInputs(fields, before, after, operation)
	for _, edge := range edges {
		if !InputAffected(edge, changed) {
			continue
		}
		previous, valid := storedTableCounter(edge.GetRaw(InputRevisionField))
		if !valid || previous > next {
			return fmt.Errorf("dependency input revision is invalid")
		}
		if previous != next {
			edge.Set(InputRevisionField, next)
			if err := app.Save(edge); err != nil {
				return err
			}
		}
	}
	return nil
}
