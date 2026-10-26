package schemacore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/autonumber"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func tableCreatePlanID(intent v2.TableCreateIntent) string {
	if intent.SourceTableID != "" {
		plan := "table-duplicate:" + intent.SourceTableID + ":" + intent.OperationID
		if !copyScopeEnabled(intent.CopyRecords) || !copyScopeEnabled(intent.CopyViews) {
			plan += fmt.Sprintf(":records=%t:views=%t", copyScopeEnabled(intent.CopyRecords), copyScopeEnabled(intent.CopyViews))
		}
		return plan
	}
	return "table-create:" + intent.OperationID
}

func copyScopeEnabled(value *bool) bool { return value == nil || *value }

func (lifecycle *TableLifecycle) validateCreateReplay(app core.App, intent v2.TableCreateIntent) error {
	audit, err := app.FindFirstRecordByFilter("vibetable_schema_audit", "operation_id={:operation}", dbx.Params{"operation": intent.OperationID})
	if err != nil {
		return fmt.Errorf("find table create replay binding: %w", err)
	}
	if audit.GetString("action") != "table.create" || audit.GetString("plan_id") != tableCreatePlanID(intent) {
		return &schemaerror.ProductError{Code: "schema.table.operation_conflict", Path: "operationId", Message: "operationId was already used for another table intent"}
	}
	return nil
}

func duplicateUnsupported(message string) error {
	return &schemaerror.ProductError{Code: "schema.table.duplicate_unsupported", Path: "sourceTableId", Message: message}
}

// All schema, row and view writes use the table lifecycle's transaction. File
// resources have a separate authority and are rejected before any copy commits.
func duplicateTableContents(ctx context.Context, app core.App, intent v2.TableCreateIntent, target *core.Record, collection *core.Collection, now time.Time) error {
	sourceID := intent.SourceTableID
	copyRecords, copyViews := copyScopeEnabled(intent.CopyRecords), copyScopeEnabled(intent.CopyViews)
	source, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": sourceID})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("find table duplicate source: %w", err)
		}
		return &schemaerror.ProductError{Code: "schema.table.not_found", Path: "sourceTableId", Message: "source table was not found"}
	}
	if source.GetString("kind") != "base" {
		return duplicateUnsupported("Only base tables can be duplicated.")
	}
	definitions, err := fieldchange.NewCatalog(app).Fields(ctx, sourceID, true)
	if err != nil {
		return err
	}
	metadata, err := app.FindRecordsByFilter("vibetable_fields", "table_id={:table}", "id", 0, 0, dbx.Params{"table": sourceID})
	if err != nil {
		return err
	}
	if len(metadata) != len(definitions) {
		return duplicateUnsupported("Legacy field metadata must be migrated before duplication.")
	}
	allocator := v2.NewIdentityAllocator(nil)
	fieldIDs := map[string]string{}
	for i := range definitions {
		field := &definitions[i]
		if (copyRecords && (field.LogicalType == v2.LogicalFile || field.LogicalType == v2.LogicalAutoNumber)) || field.Lookup != nil {
			return duplicateUnsupported("Tables with managed attachments, automatic numbers or lookup fields cannot yet be duplicated.")
		}
		if field.Relation != nil && field.Relation.PairID != "" && field.Relation.TargetTableID != sourceID {
			return duplicateUnsupported("Tables with reciprocal relations to other tables cannot yet be duplicated.")
		}
		if field.Relation != nil && field.Relation.TargetTableID == sourceID && field.Value.Default.Enabled {
			return duplicateUnsupported("Self relations with default records cannot yet be duplicated.")
		}
		if field.Formula != nil {
			// TABLE binds a collection identity in source text. Keep the original
			// source intact until an AST-aware copy seam can rewrite that binding.
			if formula.HasCollectionSyntax(field.Formula.Source) {
				return duplicateUnsupported("Collection formulas cannot yet be duplicated.")
			}
			if copyRecords {
				runtime, err := app.FindFirstRecordByFilter("vibetable_formulas", "table_id={:table} && field_id={:field}", dbx.Params{"table": sourceID, "field": field.Identity.FieldID})
				if err != nil || runtime.GetString("status") != "ready" {
					return duplicateUnsupported("Wait for formula computation to finish before duplicating this table.")
				}
			}
		}
		identity, err := allocator.AllocateField(ctx)
		if err != nil {
			return err
		}
		fieldIDs[field.Identity.FieldID] = identity.FieldID
		// Physical names are collection-scoped and also appear in formula text.
		identity.PhysicalName = field.Identity.PhysicalName
		field.Identity = identity
		if field.Value.Presence.Mode == v2.PresenceCompanion {
			field.Value.Presence.ProviderFieldID, err = allocator.AllocateProviderField(ctx)
			if err != nil {
				return err
			}
		}
	}
	targetID := target.GetString("table_id")
	pairs := map[string]string{}
	for i := range definitions {
		field := &definitions[i]
		if relation := field.Relation; relation != nil && relation.TargetTableID == sourceID {
			relation.TargetTableID = targetID
			relation.DisplayField = fieldIDs[relation.DisplayField]
			if relation.PairID != "" {
				if pairs[relation.PairID] == "" {
					pairs[relation.PairID], err = allocator.AllocateRelationPair(ctx)
					if err != nil {
						return err
					}
				}
				relation.PairID = pairs[relation.PairID]
				relation.ReciprocalFieldID = fieldIDs[relation.ReciprocalFieldID]
			}
		}
		if err := fieldchange.InstallCopiedField(app, collection, targetID, *field); err != nil {
			return err
		}
		if field.AutoNumber != nil {
			metadata, err := app.FindFirstRecordByFilter("vibetable_fields", "table_id={:table} && field_id={:field}", dbx.Params{"table": targetID, "field": field.Identity.FieldID})
			if err != nil {
				return err
			}
			highWater := field.AutoNumber.Start - 1
			raw, err := json.Marshal(autonumber.State{HighWater: &highWater})
			if err != nil {
				return err
			}
			metadata.Set(autonumber.StateColumn, types.JSONRaw(raw))
			if err := app.Save(metadata); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"archive_policy", "primary_display_field_id", "view_v2_json"} {
		if name == "primary_display_field_id" {
			target.Set(name, fieldIDs[source.GetString(name)])
			continue
		}
		if name == "view_v2_json" {
			if copyViews {
				target.Set(name, source.GetRaw(name))
			}
			continue
		}
		var policy v2.ArchivePolicy
		if err := json.Unmarshal([]byte(source.GetString(name)), &policy); err != nil {
			return err
		}
		if policy.FieldID != nil {
			id := fieldIDs[*policy.FieldID]
			policy.FieldID = &id
		}
		raw, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		target.Set(name, string(raw))
	}
	if err := app.Save(target); err != nil {
		return err
	}
	if err := schemaapi.New(app).SyncComputedMetadataForTable(ctx, targetID); err != nil {
		return err
	}
	if err := duplicateRows(ctx, app, source, targetID, collection, definitions, copyRecords); err != nil {
		return err
	}
	if err := mutation.AuditCopiedRows(ctx, app, targetID, intent.OperationID, intent.Actor, now); err != nil {
		return err
	}
	if err := materializeCopiedFormulas(ctx, app, targetID, collection); err != nil {
		return err
	}
	return duplicateViews(ctx, app, sourceID, targetID, fieldIDs, copyRecords, copyViews)
}

func duplicateRows(ctx context.Context, app core.App, source *core.Record, targetID string, target *core.Collection, fields []v2.FieldDefinition, copyRecords bool) error {
	old, err := app.FindCollectionByNameOrId(source.GetString("collection_id"))
	if err != nil {
		return err
	}
	for _, field := range old.Fields {
		if target.Fields.GetByName(field.GetName()) == nil {
			return duplicateUnsupported("Unmanaged provider fields must be migrated before duplication.")
		}
	}
	if !copyRecords {
		return nil
	}
	rows, err := app.FindAllRecords(old)
	if err != nil {
		return err
	}
	ids := map[string]string{}
	for _, row := range rows {
		ids[row.Id] = security.RandomString(15)
	}
	columns, expressions := []string{"`id`"}, []string{"{:id}"}
	for _, field := range target.Fields {
		if field.GetName() == "id" {
			continue
		}
		columns = append(columns, "`"+field.GetName()+"`")
		expressions = append(expressions, "`"+field.GetName()+"`")
		if field.GetName() == relatedcomputation.RowRevisionField {
			expressions[len(expressions)-1] = "1"
		}
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		values := append([]string(nil), expressions...)
		params := dbx.Params{"id": ids[row.Id], "source": row.Id}
		for _, field := range fields {
			if field.Relation == nil || field.Relation.TargetTableID != targetID {
				continue
			}
			linked := row.GetStringSlice(field.Identity.PhysicalName)
			for i, id := range linked {
				if ids[id] == "" {
					return duplicateUnsupported("A self relation references a missing source row.")
				}
				linked[i] = ids[id]
			}
			raw, err := json.Marshal(linked)
			if err != nil {
				return err
			}
			name := field.Identity.PhysicalName
			params[name] = string(raw)
			if field.Relation.Cardinality == "one" {
				params[name] = ""
				if len(linked) > 0 {
					params[name] = linked[0]
				}
			}
			for i, column := range columns {
				if column == "`"+name+"`" {
					values[i] = "{:" + name + "}"
				}
			}
		}
		query := "INSERT INTO `" + target.Name + "` (" + strings.Join(columns, ",") + ") SELECT " + strings.Join(values, ",") + " FROM `" + old.Name + "` WHERE id={:source}"
		if _, err := app.DB().NewQuery(query).Bind(params).Execute(); err != nil {
			return err
		}
	}
	if len(rows) > 0 {
		_, err = app.DB().NewQuery("UPDATE vibetable_tables SET data_revision=1 WHERE table_id={:table}").Bind(dbx.Params{"table": targetID}).Execute()
	}
	return err
}

func materializeCopiedFormulas(ctx context.Context, app core.App, tableID string, collection *core.Collection) error {
	definition, err := schemaapi.New(app).Describe(ctx, tableID)
	if err != nil {
		return err
	}
	formulaFields := false
	for _, field := range definition.Snapshot.Fields {
		formulaFields = formulaFields || field.Formula != nil
	}
	if !formulaFields {
		return nil
	}
	ctx = formula.WithSourceRecalculation(formula.EnsureEvaluationTime(ctx))
	calculator := computed.New(formula.NewCalculator(formula.CompilerFor(app)))
	rows, err := app.FindAllRecords(collection)
	if err != nil {
		return err
	}
	for _, row := range rows {
		values, err := calculator.Calculate(ctx, app, definition, row)
		if err != nil {
			return err
		}
		wrapped, err := relatedcomputation.WrapValues(ctx, app, tableID, definition.Snapshot.Fields, 1, values)
		if err != nil {
			return err
		}
		updates := dbx.Params{}
		for name, value := range wrapped {
			raw, err := json.Marshal(value)
			if err != nil {
				return err
			}
			updates[name] = string(raw)
		}
		if len(updates) > 0 {
			if _, err := app.DB().Update(collection.Name, updates, dbx.HashExp{"id": row.Id}).Execute(); err != nil {
				return err
			}
		}
	}
	_, err = app.DB().NewQuery("UPDATE vibetable_formulas SET status='ready' WHERE table_id={:table}").Bind(dbx.Params{"table": tableID}).Execute()
	return err
}

func duplicateViews(ctx context.Context, app core.App, source, target string, fieldIDs map[string]string, copyRecords, copyViews bool) error {
	for _, name := range []string{"vibetable_presets", "vibetable_content_profiles", "vibetable_record_document_links"} {
		if (name == "vibetable_presets" && !copyViews) || (name == "vibetable_record_document_links" && !copyRecords) {
			continue
		}
		records, err := app.FindAllRecords(name)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			var payload map[string]any
			raw, err := json.Marshal(record.GetRaw("payload_json"))
			if err != nil {
				return err
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&payload); err != nil {
				return err
			}
			if payload["scope"] != source && payload["tableId"] != source {
				continue
			}
			if name == "vibetable_record_document_links" {
				return duplicateUnsupported("Tables with linked file documents cannot yet be duplicated.")
			}
			if payload["scope"] == source {
				payload["scope"] = target
			}
			if payload["tableId"] == source {
				payload["tableId"] = target
			}
			remapViewFields(payload, fieldIDs)
			raw, err = json.Marshal(payload)
			if err != nil {
				return err
			}
			copy := core.NewRecord(record.Collection())
			logicalID := "copy_" + security.RandomString(20)
			if name == "vibetable_content_profiles" {
				logicalID = target
			}
			copy.Set("logical_id", logicalID)
			copy.Set("payload_json", types.JSONRaw(raw))
			if err := app.Save(copy); err != nil {
				return err
			}
		}
	}
	return nil
}

func remapViewFields(value any, fields map[string]string) {
	switch node := value.(type) {
	case map[string]any:
		for key, item := range node {
			switch key {
			case "field", "fieldId", "fieldRef", "dateField", "endDateField", "titleField", "groupField", "coverField", "titleFieldId", "bodyFieldId", "summaryFieldId":
				if text, ok := item.(string); ok && fields[text] != "" {
					node[key] = fields[text]
				}
			case "visibleFields", "searchableFieldIds":
				if values, ok := item.([]any); ok {
					for i, value := range values {
						if text, ok := value.(string); ok && fields[text] != "" {
							values[i] = fields[text]
						}
					}
				}
			case "view", "filters", "sorts", "groups", "summaries", "columns":
				remapViewFields(item, fields)
			}
		}
	case []any:
		for _, item := range node {
			remapViewFields(item, fields)
		}
	}
}
