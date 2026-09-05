package fieldchange

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/fieldprojection"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type Catalog struct {
	app core.App
}

type FormulaDraftInspection struct {
	CanonicalSource        string         `json:"canonicalSource"`
	ResultType             v2.LogicalType `json:"resultType"`
	Dependencies           []string       `json:"dependencies"`
	RelationAggregatePaths []string       `json:"relationAggregatePaths"`
}

func NewCatalog(app core.App) *Catalog {
	return &Catalog{app: app}
}

// AuthorFormulaDocument resolves editor references against active catalog IDs.
// A non-nil result with an error is diagnostic-only and must not be submitted.
func (catalog *Catalog) AuthorFormulaDocument(
	ctx context.Context,
	tableID string,
	document workbench.FormulaAuthorDocument,
) (*formula.AuthorResult, error) {
	current, targets, err := catalog.formulaAuthorSchemas(ctx, tableID)
	if err != nil {
		return nil, err
	}
	result, authorErr := formula.AuthorV2Document(current, targets, document)
	if authorErr != nil {
		return result, authorErr
	}
	return result, nil
}

// RestoreFormulaDocument derives display text from persisted canonical source;
// it never rewrites the stored expression when a field is renamed or deleted.
func (catalog *Catalog) RestoreFormulaDocument(
	ctx context.Context,
	tableID string,
	canonicalSource string,
	documentRevision int64,
) (*formula.AuthorResult, error) {
	current, targets, err := catalog.formulaAuthorSchemas(ctx, tableID)
	if err != nil {
		return nil, err
	}
	result, authorErr := formula.RestoreV2AuthorDocument(
		current, targets, canonicalSource, documentRevision,
	)
	if authorErr != nil {
		return result, authorErr
	}
	return result, nil
}

func (catalog *Catalog) InspectFormulaDraft(
	ctx context.Context,
	tableID string,
	displaySource string,
) (FormulaDraftInspection, error) {
	draft := &v2.FieldDefinition{
		Identity: v2.FieldIdentity{
			FieldID: "fld_formula_preview", PhysicalName: "f_formula_preview",
			ProviderFieldID: "pb_formula_preview",
		},
		DisplayName: "Formula preview", LogicalType: v2.LogicalFormula,
		Formula: &v2.FormulaSpec{Language: "cel-v1", Source: displaySource},
	}
	if err := catalog.NormalizeDefinition(ctx, tableID, draft); err != nil {
		return FormulaDraftInspection{}, err
	}
	definition, err := catalog.formulaDefinition(ctx, tableID)
	if err != nil {
		return FormulaDraftInspection{}, err
	}
	upsertFormulaField(&definition, *draft)
	plan, formulaErr := formula.NewCompiler(formula.DefaultLimits()).CompileV2Table(definition)
	if formulaErr != nil {
		return FormulaDraftInspection{}, formulaErr
	}
	for _, compiled := range plan.Formulas {
		if compiled.FieldID == draft.Identity.FieldID {
			return FormulaDraftInspection{
				CanonicalSource: compiled.CanonicalSource,
				ResultType:      draft.Formula.ResultType,
				Dependencies:    append([]string(nil), compiled.Dependencies...),
				RelationAggregatePaths: append(
					[]string(nil), compiled.RelationAggregatePaths...,
				),
			}, nil
		}
	}
	return FormulaDraftInspection{}, productError(
		"formula.runtime", "displaySource", "formula preview plan is unavailable", nil,
	)
}

func (catalog *Catalog) NormalizeDefinition(
	ctx context.Context,
	tableID string,
	definition *v2.FieldDefinition,
) error {
	if definition == nil || definition.LogicalType != v2.LogicalFormula ||
		definition.Formula == nil {
		return nil
	}
	current, targets, err := catalog.formulaAuthorSchemas(ctx, tableID)
	if err != nil {
		return err
	}
	canonical, formulaErr := formula.CanonicalizeV2DisplaySource(
		current, targets, definition.Formula.Source,
	)
	if formulaErr != nil {
		return formulaErr
	}
	withoutCurrent := current
	withoutCurrent.Fields = append([]v2.FieldDefinition(nil), current.Fields...)
	removeFormulaField(&withoutCurrent, definition.Identity.FieldID)
	resultType, onlyInt, formulaErr := formula.NewCompiler(formula.DefaultLimits()).InferV2Source(
		withoutCurrent, canonical,
	)
	if formulaErr != nil {
		return formulaErr
	}
	definition.Formula.Source = canonical
	definition.Formula.ResultType = resultType
	definition.Storage.Options.OnlyInt = onlyInt
	candidate := withoutCurrent
	upsertFormulaField(&candidate, *definition)
	plan, formulaErr := formula.NewCompiler(formula.DefaultLimits()).CompileV2Table(candidate)
	if formulaErr != nil {
		return formulaErr
	}
	for _, compiled := range plan.Formulas {
		if compiled.FieldID != definition.Identity.FieldID {
			continue
		}
		for _, reference := range compiled.ReferencePaths {
			if err := validateAuthoredRelationReference(
				current, targets, reference, false,
			); err != nil {
				return err
			}
		}
		for _, reference := range compiled.RelationAggregatePaths {
			if err := validateAuthoredRelationReference(
				current, targets, reference, true,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// formulaAuthorSchemas supplies the same active field identities to draft
// normalization and structured author documents. Targets are keyed by the
// source relation's permanent physical name, as required by the formula owner.
func (catalog *Catalog) formulaAuthorSchemas(
	ctx context.Context,
	tableID string,
) (formula.V2Table, map[string]formula.V2Table, error) {
	current, err := catalog.formulaDefinition(ctx, tableID)
	if err != nil {
		return formula.V2Table{}, nil, err
	}
	targets := make(map[string]formula.V2Table)
	loaded := map[string]formula.V2Table{tableID: current}
	for _, field := range current.Fields {
		if field.LogicalType != v2.LogicalRelation || field.Relation == nil {
			continue
		}
		targetID := field.Relation.TargetTableID
		target, exists := loaded[targetID]
		if !exists {
			target, err = catalog.formulaDefinition(ctx, targetID)
			if err != nil {
				return formula.V2Table{}, nil, err
			}
			loaded[targetID] = target
		}
		targets[field.Identity.PhysicalName] = target
	}
	return current, targets, nil
}

func (catalog *Catalog) formulaDefinition(
	ctx context.Context,
	tableID string,
) (formula.V2Table, error) {
	fields, err := catalog.Fields(ctx, tableID, false)
	if err != nil {
		return formula.V2Table{}, err
	}
	return formula.V2Table{TableID: tableID, Fields: fields}, nil
}

func validateAuthoredRelationReference(
	definition formula.V2Table,
	targets map[string]formula.V2Table,
	reference string,
	aggregate bool,
) error {
	parts := strings.Split(reference, ".")
	if len(parts) != 2 {
		return productError(
			"formula.dependency", "draft.formula.source",
			"formula relation reference must select one target field",
			map[string]any{"reference": reference},
		)
	}
	var relation *v2.FieldDefinition
	for index := range definition.Fields {
		if definition.Fields[index].Identity.PhysicalName == parts[0] {
			relation = &definition.Fields[index]
			break
		}
	}
	if relation == nil || relation.LogicalType != v2.LogicalRelation || relation.Relation == nil {
		return productError(
			"formula.dependency", "draft.formula.source",
			"formula relation field is unavailable", map[string]any{"reference": reference},
		)
	}
	if !aggregate && relation.Relation.Cardinality != "one" {
		return productError(
			"schema.formula.relation_cardinality", "draft.formula.source",
			"many relations require an aggregate formula function",
			map[string]any{"reference": reference},
		)
	}
	target := targets[relation.Identity.PhysicalName]
	for _, field := range target.Fields {
		if field.Identity.PhysicalName == parts[1] &&
			field.LogicalType != v2.LogicalRelation {
			return nil
		}
	}
	return productError(
		"schema.formula.target_field_not_found", "draft.formula.source",
		"formula relation target field was not found",
		map[string]any{"reference": reference},
	)
}

func upsertFormulaField(table *formula.V2Table, field v2.FieldDefinition) {
	for index := range table.Fields {
		if table.Fields[index].Identity.FieldID == field.Identity.FieldID {
			table.Fields[index] = field
			return
		}
	}
	table.Fields = append(table.Fields, field)
}

func removeFormulaField(table *formula.V2Table, fieldID string) {
	filtered := table.Fields[:0]
	for _, field := range table.Fields {
		if field.Identity.FieldID != fieldID {
			filtered = append(filtered, field)
		}
	}
	table.Fields = filtered
}

func (catalog *Catalog) Revisions(
	ctx context.Context,
	tableID string,
) (Revisions, error) {
	if err := ctx.Err(); err != nil {
		return Revisions{}, err
	}
	record, err := catalog.tableRecord(catalog.app, tableID)
	if err != nil {
		return Revisions{}, err
	}
	schemaRevision, err := storedInteger(record.GetRaw("schema_revision"))
	if err != nil || schemaRevision < 0 {
		return Revisions{}, fmt.Errorf("invalid stored schema revision")
	}
	dataRevision, err := storedInteger(record.GetRaw("data_revision"))
	if err != nil || dataRevision < 0 {
		return Revisions{}, fmt.Errorf("invalid stored data revision")
	}
	return Revisions{
		Schema: v2.FormatSchemaRevision(schemaRevision),
		Data:   dataRevision,
	}, nil
}

func (catalog *Catalog) TableDisplayName(
	ctx context.Context,
	tableID string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	record, err := catalog.tableRecord(catalog.app, tableID)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(record.GetString("display_name"))
	if name == "" {
		return "", fmt.Errorf("stored table display name is blank")
	}
	return name, nil
}

func (catalog *Catalog) Field(
	ctx context.Context,
	tableID string,
	fieldID string,
) (*v2.FieldDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, err := catalog.app.FindFirstRecordByFilter(
		"vibetable_fields",
		"table_id={:table} && field_id={:field}",
		dbx.Params{"table": tableID, "field": fieldID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFieldNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load field metadata: %w", err)
	}
	return decodeDefinitionRecord(record)
}

func (catalog *Catalog) Fields(
	ctx context.Context,
	tableID string,
	includeRetired bool,
) ([]v2.FieldDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filter := "table_id={:table}"
	params := dbx.Params{"table": tableID}
	if !includeRetired {
		filter += " && lifecycle_state!='retired'"
	}
	records, err := catalog.app.FindRecordsByFilter(
		"vibetable_fields", filter, "id", 0, 0, params,
	)
	if err != nil {
		return nil, fmt.Errorf("list field metadata: %w", err)
	}
	result := make([]v2.FieldDefinition, 0, len(records))
	for _, record := range records {
		if record.GetRaw("definition_v2_json") == nil {
			continue
		}
		definition, err := decodeDefinitionRecord(record)
		if err != nil {
			return nil, err
		}
		result = append(result, *definition)
	}
	return result, nil
}

func (catalog *Catalog) Check(
	ctx context.Context,
	intent v2.FieldChangeIntent,
	before *v2.FieldDefinition,
	after *v2.FieldDefinition,
	classes []v2.ChangeClass,
) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error) {
	impact := v2.Impact{
		Failures: []v2.FailureSample{}, Dependencies: []v2.DependencyRef{},
	}
	if after != nil && after.Relation != nil {
		diagnostics, err := catalog.checkRelationTarget(ctx, *after)
		if err != nil {
			return impact, nil, nil, err
		}
		if len(diagnostics) != 0 {
			return impact, []v2.Diagnostic{}, diagnostics, nil
		}
	}
	if after != nil && after.Lookup != nil {
		diagnostics, err := catalog.checkLookupTargets(ctx, intent.TableID, *after)
		if err != nil {
			return impact, nil, nil, err
		}
		if len(diagnostics) != 0 {
			return impact, []v2.Diagnostic{}, diagnostics, nil
		}
	}
	if before != nil &&
		(intent.Action == v2.ActionRetire ||
			intent.Action == v2.ActionPurge) {
		return catalog.checkLifecycleDependencies(ctx, intent, *before, impact)
	}
	if before != nil && after != nil && relationCascadeIntroduced(before, after) {
		return catalog.checkCascadeImpact(ctx, intent, *before, *after, impact)
	}
	if before == nil || after == nil ||
		(!containsClass(classes, v2.ClassConstraint) &&
			!containsClass(classes, v2.ClassMigration)) {
		return impact, []v2.Diagnostic{}, []v2.Diagnostic{}, nil
	}
	// Table identity is not present on a field definition. Resolve the owning
	// table through the stable field metadata pair.
	fieldMeta, err := catalog.app.FindFirstRecordByFilter(
		"vibetable_fields",
		"field_id={:field}",
		dbx.Params{"field": before.Identity.FieldID},
	)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("resolve preflight field owner: %w", err)
	}
	tableID := fieldMeta.GetString("table_id")
	tableRecord, err := catalog.tableRecord(catalog.app, tableID)
	if err != nil {
		return impact, nil, nil, err
	}
	collection, err := catalog.app.FindCollectionByNameOrId(
		tableRecord.GetString("collection_id"),
	)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("load preflight collection: %w", err)
	}
	records, err := catalog.app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("scan preflight records: %w", err)
	}
	impact.Records = int64(len(records))
	projection := fieldprojection.Descriptor{Definition: *before}
	valueKernel := fieldvalue.New()
	unique := map[string]string{}
	diagnostics := []v2.Diagnostic{}
	for _, record := range records {
		physical := map[string]any{
			before.Identity.PhysicalName: record.GetRaw(before.Identity.PhysicalName),
		}
		if before.Value.Presence.Mode == v2.PresenceCompanion {
			physical[before.Value.Presence.PhysicalName] =
				record.GetBool(before.Value.Presence.PhysicalName)
		}
		productValue := projection.ProductValue(physical)
		if productValue == nil {
			impact.Missing++
		}
		writeValue := productValue
		supplied := true
		if containsClass(classes, v2.ClassMigration) {
			writeValue, supplied, err = convertMigrationValue(
				*before, *after, productValue, intent.ConversionRule,
			)
			if err != nil {
				appendFailure(&impact, record.Id, err.Error())
				continue
			}
		}
		result, validationErr := valueKernel.NormalizeWrite(
			ctx, *after, fieldvalue.Update,
			fieldvalue.Input{Supplied: supplied, Value: writeValue},
		)
		if validationErr != nil {
			appendFailure(&impact, record.Id, validationErr.Error())
			continue
		}
		if after.Constraints.Unique.Enabled && result.Present {
			key, participates, keyErr := (fieldprojection.Descriptor{
				Definition: *after,
			}).UniqueKey(result.PhysicalValues)
			if keyErr != nil {
				return impact, nil, nil, keyErr
			}
			if participates {
				if previous, duplicate := unique[key]; duplicate {
					appendFailure(
						&impact, record.Id,
						fmt.Sprintf("duplicate value also used by %s", previous),
					)
				} else {
					unique[key] = record.Id
				}
			}
		}
	}
	if len(impact.Failures) != 0 {
		diagnostics = append(diagnostics, v2.Diagnostic{
			Code:    "field.constraint.existing_data_invalid",
			Path:    "draft.constraints",
			Message: "existing records do not satisfy the requested field settings",
			Details: map[string]any{
				"failed": len(impact.Failures), "scanned": impact.Records,
			},
		})
	}
	return impact, []v2.Diagnostic{}, diagnostics, nil
}

func (catalog *Catalog) checkLookupTargets(
	ctx context.Context,
	tableID string,
	definition v2.FieldDefinition,
) ([]v2.Diagnostic, error) {
	currentTableID := tableID
	for index, step := range definition.Lookup.Path {
		fields, err := catalog.Fields(ctx, currentTableID, false)
		if err != nil {
			return nil, err
		}
		var relation *v2.FieldDefinition
		for fieldIndex := range fields {
			if fields[fieldIndex].Identity.FieldID == step.RelationFieldID {
				relation = &fields[fieldIndex]
				break
			}
		}
		if relation == nil || relation.LogicalType != v2.LogicalRelation ||
			relation.Relation == nil {
			return []v2.Diagnostic{{
				Code:    "field.lookup.relation_invalid",
				Path:    fmt.Sprintf("draft.lookup.path[%d].relationFieldId", index),
				Message: "lookup path step must reference an active direct relation",
				Details: map[string]any{"tableId": currentTableID},
			}}, nil
		}
		currentTableID = relation.Relation.TargetTableID
	}
	targetFields, err := catalog.Fields(ctx, currentTableID, false)
	if err != nil {
		return nil, err
	}
	for _, target := range targetFields {
		if target.Identity.FieldID == definition.Lookup.TargetFieldID &&
			target.LogicalType != v2.LogicalRelation {
			return []v2.Diagnostic{}, nil
		}
	}
	return []v2.Diagnostic{{
		Code: "field.lookup.target_invalid", Path: "draft.lookup.targetFieldId",
		Message: "lookup target must be an active non-relation field at the end of the path",
		Details: map[string]any{"targetTableId": currentTableID},
	}}, nil
}

func (catalog *Catalog) checkCascadeImpact(
	ctx context.Context,
	intent v2.FieldChangeIntent,
	before v2.FieldDefinition,
	after v2.FieldDefinition,
	impact v2.Impact,
) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error) {
	withDependencies, _, lifecycleDiagnostics, err :=
		catalog.checkLifecycleDependencies(ctx, intent, before, impact)
	if err != nil {
		return impact, nil, nil, err
	}
	tableRecord, err := catalog.tableRecord(catalog.app, intent.TableID)
	if err != nil {
		return impact, nil, nil, err
	}
	collection, err := catalog.app.FindCollectionByNameOrId(
		tableRecord.GetString("collection_id"),
	)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("load cascade source collection: %w", err)
	}
	records, err := catalog.app.FindRecordsByFilter(collection, "", "id", 0, 0)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("scan cascade source records: %w", err)
	}
	var referencing int64
	projection := fieldprojection.Descriptor{Definition: before}
	for _, record := range records {
		physical := map[string]any{
			before.Identity.PhysicalName: record.GetRaw(before.Identity.PhysicalName),
		}
		if before.Value.Presence.Mode == v2.PresenceCompanion {
			physical[before.Value.Presence.PhysicalName] =
				record.GetBool(before.Value.Presence.PhysicalName)
		}
		if projection.ProductValue(physical) != nil {
			referencing++
		}
	}
	withDependencies.Records = referencing
	warnings := []v2.Diagnostic{{
		Code:    "field.relation.cascade_impact",
		Path:    "draft.relation.deletePolicy",
		Message: "deleting a target record will delete source records that reference it",
		Details: map[string]any{
			"direction":     "targetToSource",
			"sourceTableId": intent.TableID,
			"targetTableId": after.Relation.TargetTableID,
			"records":       referencing,
		},
	}}
	errorsFound := []v2.Diagnostic{}
	for _, diagnostic := range lifecycleDiagnostics {
		if diagnostic.Code == "field.purge.migration_active" {
			errorsFound = append(errorsFound, diagnostic)
		} else {
			warnings = append(warnings, diagnostic)
		}
	}
	return withDependencies, warnings, errorsFound, nil
}

func (catalog *Catalog) checkLifecycleDependencies(
	ctx context.Context,
	intent v2.FieldChangeIntent,
	before v2.FieldDefinition,
	impact v2.Impact,
) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error) {
	tableRecord, err := catalog.tableRecord(catalog.app, intent.TableID)
	if err != nil {
		return impact, nil, nil, err
	}
	collection, err := catalog.app.FindCollectionByNameOrId(
		tableRecord.GetString("collection_id"),
	)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("load lifecycle collection: %w", err)
	}
	impact.Records, err = catalog.app.CountRecords(collection)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("count lifecycle records: %w", err)
	}

	formulaDependencies, err := catalog.app.FindRecordsByFilter(
		"vibetable_formula_dependencies",
		"(target_table_id={:table} && target_field_id={:field}) || "+
			"(source_table_id={:table} && relation_field_id={:field})",
		"id",
		0,
		0,
		dbx.Params{"table": intent.TableID, "field": before.Identity.FieldID},
	)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("load formula dependencies: %w", err)
	}
	for _, dependency := range formulaDependencies {
		impact.Dependencies = append(impact.Dependencies, v2.DependencyRef{
			Kind: "formula",
			ID:   dependency.GetString("formula_field_id"),
			Name: dependency.GetString("formula_field_id"),
		})
	}

	lookups, err := catalog.app.FindRecordsByFilter(
		"vibetable_lookups",
		"",
		"id",
		0,
		0,
	)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("load lookup dependencies: %w", err)
	}
	for _, lookup := range lookups {
		depends, decodeErr := lookupMetadataDependsOnField(
			lookup.GetString("path_json"), before.Identity.FieldID,
		)
		if decodeErr != nil {
			return impact, nil, nil, fmt.Errorf(
				"decode lookup dependency %s: %w",
				lookup.GetString("lookup_id"), decodeErr,
			)
		}
		if !depends {
			continue
		}
		if lookup.GetString("table_id") == intent.TableID &&
			lookup.GetString("field_id") == before.Identity.FieldID {
			continue
		}
		impact.Dependencies = append(impact.Dependencies, v2.DependencyRef{
			Kind: "lookup",
			ID:   lookup.GetString("lookup_id"),
			Name: lookup.GetString("lookup_id"),
		})
	}

	fields, err := catalog.app.FindRecordsByFilter(
		"vibetable_fields",
		"lifecycle_state='active'",
		"id",
		0,
		0,
	)
	if err != nil {
		return impact, nil, nil, fmt.Errorf(
			"load relation display-field dependencies: %w", err,
		)
	}
	for _, record := range fields {
		definition, decodeErr := decodeDefinitionRecord(record)
		if decodeErr != nil {
			return impact, nil, nil, decodeErr
		}
		if definition.Relation == nil ||
			definition.Relation.TargetTableID != intent.TableID ||
			definition.Relation.DisplayField != before.Identity.FieldID ||
			definition.Identity.FieldID == before.Identity.FieldID {
			continue
		}
		impact.Dependencies = append(impact.Dependencies, v2.DependencyRef{
			Kind: "relationDisplayField",
			ID:   definition.Identity.FieldID,
			Name: definition.DisplayName,
		})
	}

	jobs, err := catalog.app.FindRecordsByFilter(
		"vibetable_jobs",
		"job_type='field_migration' && field_id={:field} && "+
			"(state='queued' || state='running' || state='cancelling')",
		"id",
		0,
		0,
		dbx.Params{"field": before.Identity.FieldID},
	)
	if err != nil {
		return impact, nil, nil, fmt.Errorf("load active field migrations: %w", err)
	}
	diagnostics := []v2.Diagnostic{}
	if len(impact.Dependencies) != 0 {
		diagnostics = append(diagnostics, v2.Diagnostic{
			Code:    lifecycleDependencyCode(before),
			Path:    "fieldId",
			Message: "field dependencies must be resolved before this lifecycle change",
			Details: map[string]any{"count": len(impact.Dependencies)},
		})
	}
	if len(jobs) != 0 {
		diagnostics = append(diagnostics, v2.Diagnostic{
			Code:    "field.purge.migration_active",
			Path:    "fieldId",
			Message: "an active field migration blocks this lifecycle change",
			Details: map[string]any{"count": len(jobs)},
		})
	}
	return impact, []v2.Diagnostic{}, diagnostics, nil
}

func lookupMetadataDependsOnField(pathJSON string, fieldID string) (bool, error) {
	var metadata struct {
		RelationFieldID string `json:"relationFieldId"`
		TargetFieldID   string `json:"targetFieldId"`
		Path            []struct {
			RelationFieldID string `json:"relationFieldId"`
		} `json:"path"`
	}
	if err := json.Unmarshal([]byte(pathJSON), &metadata); err != nil {
		return false, err
	}
	if metadata.RelationFieldID == fieldID || metadata.TargetFieldID == fieldID {
		return true, nil
	}
	for _, step := range metadata.Path {
		if step.RelationFieldID == fieldID {
			return true, nil
		}
	}
	return false, nil
}

func lifecycleDependencyCode(before v2.FieldDefinition) string {
	if before.Relation != nil {
		return "relation.delete.dependency_blocked"
	}
	return "field.lifecycle.dependencies"
}

func (catalog *Catalog) checkRelationTarget(
	ctx context.Context,
	definition v2.FieldDefinition,
) ([]v2.Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := catalog.tableRecord(
		catalog.app, definition.Relation.TargetTableID,
	); err != nil {
		return []v2.Diagnostic{{
			Code:    "field.relation.target_missing",
			Path:    "draft.relation.targetTableId",
			Message: "relation target table does not exist",
		}}, nil
	}
	display, err := catalog.Field(
		ctx,
		definition.Relation.TargetTableID,
		definition.Relation.DisplayField,
	)
	if errors.Is(err, ErrFieldNotFound) {
		return []v2.Diagnostic{{
			Code:    "field.relation.display_field_missing",
			Path:    "draft.relation.displayFieldId",
			Message: "relation display field does not exist",
		}}, nil
	}
	if err != nil {
		return nil, err
	}
	if display.Lifecycle.State != v2.LifecycleActive {
		return []v2.Diagnostic{{
			Code:    "field.relation.display_field_retired",
			Path:    "draft.relation.displayFieldId",
			Message: "relation display field is retired",
		}}, nil
	}
	return []v2.Diagnostic{}, nil
}

func (catalog *Catalog) tableRecord(app core.App, tableID string) (*core.Record, error) {
	if tableID == "" {
		return nil, errors.New("table id is required")
	}
	record, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("table %q not found", tableID)
	}
	if err != nil {
		return nil, fmt.Errorf("load table metadata: %w", err)
	}
	return record, nil
}

func decodeDefinitionRecord(record *core.Record) (*v2.FieldDefinition, error) {
	raw, err := json.Marshal(record.GetRaw("definition_v2_json"))
	if err != nil {
		return nil, fmt.Errorf("encode stored field definition: %w", err)
	}
	var definition v2.FieldDefinition
	if err := v2.StrictDecode(raw, &definition); err != nil {
		return nil, fmt.Errorf("decode stored field definition: %w", err)
	}
	if err := v2.Validate(definition); err != nil {
		return nil, fmt.Errorf("validate stored field definition: %w", err)
	}
	if definition.Identity.FieldID != record.GetString("field_id") ||
		definition.Identity.PhysicalName != record.GetString("physical_name") {
		return nil, errors.New("stored field definition identity mismatch")
	}
	return &definition, nil
}

func storedInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		if math.Trunc(typed) != typed {
			return 0, errors.New("stored number is fractional")
		}
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	default:
		return 0, errors.New("stored number is missing")
	}
}

func appendFailure(impact *v2.Impact, recordID string, reason string) {
	if len(impact.Failures) >= 20 {
		return
	}
	impact.Failures = append(impact.Failures, v2.FailureSample{
		RecordID: recordID, Reason: reason,
	})
}
