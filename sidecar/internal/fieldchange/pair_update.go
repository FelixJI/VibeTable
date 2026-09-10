package fieldchange

import (
	"context"
	"fmt"
	"strings"

	"github.com/pocketbase/dbx"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func applyRelationEndpointPatch(
	definition *v2.FieldDefinition,
	patch *v2.RelationPairPatch,
	reciprocal bool,
) error {
	name, cardinality, display := patch.SourceDisplayName, patch.SourceCardinality, patch.SourceDisplayFieldID
	if reciprocal {
		name, cardinality, display = patch.ReciprocalDisplayName, patch.ReciprocalCardinality, patch.ReciprocalDisplayFieldID
	}
	if name != nil {
		definition.DisplayName = strings.TrimSpace(*name)
	}
	if cardinality != nil {
		definition.Relation.Cardinality = *cardinality
	}
	if display != nil {
		definition.Relation.DisplayField = *display
	}
	if patch.DeletePolicy != nil {
		if *patch.DeletePolicy != "setNull" && *patch.DeletePolicy != "restrict" {
			return productError("field.contract.invalid", "relationPairPatch.deletePolicy",
				"pair delete policy must be setNull or restrict", nil)
		}
		definition.Relation.DeletePolicy = *patch.DeletePolicy
	}
	return v2.Validate(*definition)
}

func classifyIntent(intent v2.FieldChangeIntent, before, after *v2.FieldDefinition) []v2.ChangeClass {
	classes := classify(intent.Action, before, after, intent.ConversionRule)
	if intent.RelationPairPatch != nil {
		for index, class := range classes {
			if class == v2.ClassMigration {
				// PocketBase converts relation storage in its schema transaction,
				// preserving the provider identity. Preflight rejects lossy narrowing.
				classes[index] = v2.ClassSchema
			}
		}
	}
	return classes
}

func (catalog *Catalog) checkPairCardinality(
	ctx context.Context,
	tableID string,
	before, after *v2.FieldDefinition,
	impact v2.Impact,
) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error) {
	if before.Relation.Cardinality != "many" || after.Relation.Cardinality != "one" {
		return impact, []v2.Diagnostic{}, []v2.Diagnostic{}, nil
	}
	table, err := catalog.tableRecord(catalog.app, tableID)
	if err != nil {
		return impact, nil, nil, err
	}
	collection, err := catalog.app.FindCollectionByNameOrId(table.GetString("collection_id"))
	if err != nil {
		return impact, nil, nil, err
	}
	const pageSize = 256
	lastID := ""
	for {
		if err := ctx.Err(); err != nil {
			return impact, nil, nil, err
		}
		records, err := catalog.app.FindRecordsByFilter(collection,
			"id>{:last}", "id", pageSize, 0, dbx.Params{"last": lastID})
		if err != nil {
			return impact, nil, nil, fmt.Errorf("scan relation cardinality: %w", err)
		}
		for _, record := range records {
			impact.Records++
			// Read with the old many schema; the new one schema would silently
			// select the last link before we could detect the conflict.
			links := record.GetStringSlice(before.Identity.PhysicalName)
			if len(links) == 0 {
				impact.Missing++
			} else if len(links) > 1 {
				impact.Ambiguous++
				appendFailure(&impact, record.Id, "multiple links prevent many-to-one relation update")
			}
			lastID = record.Id
		}
		if len(records) < pageSize {
			break
		}
	}
	diagnostics := []v2.Diagnostic{}
	if impact.Ambiguous != 0 {
		diagnostics = append(diagnostics, v2.Diagnostic{
			Code: "relation.cardinality.conflict", Path: "relationPairPatch",
			Message: "multiple links prevent many-to-one relation update",
			Details: map[string]any{"tableId": tableID, "fieldId": before.Identity.FieldID,
				"ambiguous": impact.Ambiguous, "scanned": impact.Records},
		})
	}
	return impact, []v2.Diagnostic{}, diagnostics, nil
}
