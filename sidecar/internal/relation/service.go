package relation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	lookupcalc "github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

type MutationKernel interface {
	Preview(context.Context, mutation.Request) (mutation.PreviewResult, error)
	Apply(context.Context, mutation.Request) (mutation.Receipt, error)
}

type Service struct {
	app     core.App
	queries query.QueryPort
	kernel  MutationKernel
}

func New(app core.App, queries query.QueryPort, kernel MutationKernel) *Service {
	return &Service{app: app, queries: queries, kernel: kernel}
}

func (service *Service) Describe(
	ctx context.Context,
	tableID string,
) (CatalogResult, error) {
	if tableID == "" {
		return CatalogResult{}, relationError(
			"relation.request.invalid", "tableId is required",
		)
	}
	definition, err := schemaapi.New(service.app).Describe(ctx, tableID)
	if err != nil {
		return CatalogResult{}, err
	}
	result := CatalogResult{
		TableID: tableID, SchemaRevision: definition.SchemaRevision,
		LookupMaxDepth: schema.MaxLookupPathDepth,
		Relations:      []Descriptor{}, Lookups: []LookupDescriptor{},
	}
	lookupRevisions := map[string]int{}
	lookupRecords, lookupErr := service.app.FindRecordsByFilter(
		"vibetable_lookups", "table_id={:table}", "", 0, 0,
		dbx.Params{"table": tableID},
	)
	if lookupErr != nil {
		return CatalogResult{}, relationError(
			"lookup.storage_failed", "lookup metadata could not be read",
		)
	}
	for _, record := range lookupRecords {
		lookupRevisions[record.GetString("lookup_id")] = record.GetInt("revision")
	}
	for _, field := range definition.Fields {
		if field.Kind == schema.FieldKindRelation && field.Relation != nil {
			descriptor := descriptorFrom(tableID+"."+field.FieldID, tableID, field)
			if descriptor.Mode == "direct" {
				target := definition
				if descriptor.TargetTableID != definition.TableID {
					target, err = schemaapi.New(service.app).Describe(ctx, descriptor.TargetTableID)
					if err != nil {
						return CatalogResult{}, err
					}
				}
				descriptor.QuickCreateEligible, descriptor.QuickCreateReason =
					quickCreateEligibility(target)
			}
			result.Relations = append(
				result.Relations,
				descriptor,
			)
		}
		if field.Kind == schema.FieldKindLookup && field.Lookup != nil {
			path, resultMany, pathErr := service.describeLookupPath(
				ctx, definition, *field.Lookup,
			)
			if pathErr != nil {
				return CatalogResult{}, pathErr
			}
			lookupID := tableID + "." + field.FieldID
			revision := lookupRevisions[lookupID]
			if revision < 1 {
				revision = 1
			}
			result.Lookups = append(result.Lookups, LookupDescriptor{
				LookupID: lookupID,
				TableID:  tableID, FieldID: field.FieldID,
				PhysicalName: field.PhysicalName, DisplayName: field.DisplayName,
				RelationFieldID:   field.Lookup.RelationFieldID,
				Path:              path,
				TargetFieldID:     field.Lookup.TargetFieldID,
				JunctionFieldID:   field.Lookup.JunctionFieldID,
				TargetFieldIDs:    field.Lookup.TargetFieldIDs,
				Aggregate:         field.Lookup.Aggregate,
				ResultCardinality: map[bool]string{true: "many", false: "one"}[resultMany],
				OutputStorage:     field.StorageType, Revision: revision,
			})
		}
	}
	return result, nil
}

func (service *Service) describeLookupPath(
	ctx context.Context,
	source schema.TableDefinition,
	spec schema.LookupSpec,
) ([]LookupPathDescriptor, bool, error) {
	current := source
	result := make([]LookupPathDescriptor, 0, len(spec.EffectivePath()))
	resultMany := false
	for _, step := range spec.EffectivePath() {
		relationField, found := relationFieldByID(current, step.RelationFieldID)
		if !found || relationField.Relation == nil {
			return nil, false, relationError(
				"lookup.schema_invalid",
				"lookup path relation metadata is unavailable",
			)
		}
		if relationField.Relation.Cardinality == "many" ||
			relationField.Relation.EffectiveMode() != "direct" {
			resultMany = true
		}
		result = append(result, LookupPathDescriptor{
			RelationID:    current.TableID + "." + step.RelationFieldID,
			M2ACollection: step.M2ACollection,
		})
		targetTableID := relationField.Relation.TargetTableID
		if step.M2ACollection != "" {
			targetTableID = step.M2ACollection
		}
		target, err := schemaapi.New(service.app).Describe(ctx, targetTableID)
		if err != nil {
			return nil, false, err
		}
		current = target
	}
	return result, resultMany, nil
}

func relationFieldByID(
	definition schema.TableDefinition,
	fieldID string,
) (schema.FieldDefinition, bool) {
	for _, field := range definition.Fields {
		if field.FieldID == fieldID {
			return field, true
		}
	}
	return schema.FieldDefinition{}, false
}

func (service *Service) SearchTargets(
	ctx context.Context,
	request SearchRequest,
) (SearchResult, error) {
	resolved, err := service.resolve(ctx, request.RelationID)
	if err != nil {
		return SearchResult{}, err
	}
	if request.Offset < 0 || request.Limit < 1 || request.Limit > 100 {
		return SearchResult{}, relationError(
			"relation.request.invalid",
			"relation search paging is invalid",
		)
	}
	targetTableID := resolved.descriptor.TargetTableID
	if resolved.descriptor.Mode == "m2a" {
		targetTableID = request.TargetTableID
		if !contains(resolved.descriptor.AllowedTargetTableIDs, targetTableID) {
			return SearchResult{}, relationError(
				"relation.target_invalid",
				"m2a target table is not allowed",
			)
		}
	} else if request.TargetTableID != "" &&
		request.TargetTableID != targetTableID {
		return SearchResult{}, relationError(
			"relation.target_invalid",
			"target table does not match the relation",
		)
	}
	page, err := service.queries.QueryPage(
		ctx,
		targetTableID,
		query.TableQuery{
			Keyword: request.Query,
			Offset:  request.Offset,
			Limit:   request.Limit,
			Filters: []query.FilterExpression{},
			Sorts: []query.SortCondition{{
				Field: "id", Direction: query.SortAscending,
			}},
		},
	)
	if err != nil {
		return SearchResult{}, err
	}
	target, err := schemaapi.New(service.app).Describe(
		ctx, targetTableID,
	)
	if err != nil {
		return SearchResult{}, err
	}
	labelField := targetLabelField(target)
	secondaryField := targetSecondaryField(target, labelField)
	items := make([]TargetRef, 0, len(page.Rows))
	for _, row := range page.Rows {
		recordID := fmt.Sprint(row["id"])
		label := recordID
		if labelField != "" && row[labelField] != nil &&
			fmt.Sprint(row[labelField]) != "" {
			label = fmt.Sprint(row[labelField])
		}
		secondaryLabel := ""
		if secondaryField != "" && row[secondaryField] != nil {
			secondaryLabel = strings.TrimSpace(fmt.Sprint(row[secondaryField]))
		}
		items = append(items, TargetRef{
			TableID:        targetTableID,
			RecordID:       recordID,
			Label:          label,
			SecondaryLabel: secondaryLabel,
			JunctionValues: map[string]any{},
		})
	}
	return SearchResult{
		Items: items, Total: page.FilteredRows, Snapshot: page.Snapshot,
	}, nil
}

func (service *Service) CreateTarget(
	ctx context.Context,
	request CreateTargetRequest,
) (CreateTargetResult, error) {
	resolved, err := service.resolve(ctx, request.RelationID)
	if err != nil {
		return CreateTargetResult{}, err
	}
	label := strings.TrimSpace(request.Label)
	if resolved.descriptor.Mode != "direct" ||
		request.RequestID == "" || request.IdempotencyKey == "" ||
		request.Actor.Type == "" || request.Actor.ID == "" {
		return CreateTargetResult{}, relationError(
			"relation.request.invalid",
			"direct relation target creation request is incomplete",
		)
	}
	targetTableID := resolved.descriptor.TargetTableID
	if request.TargetTableID != "" && request.TargetTableID != targetTableID {
		return CreateTargetResult{}, relationError(
			"relation.target_invalid",
			"target table does not match the relation",
		)
	}
	target, err := schemaapi.New(service.app).Describe(ctx, targetTableID)
	if err != nil {
		return CreateTargetResult{}, err
	}
	labelPhysicalName := targetLabelField(target)
	if labelPhysicalName == "" {
		return CreateTargetResult{}, relationError(
			"relation.target_create_unavailable",
			"target table has no writable display field",
		)
	}
	values := map[string]any{}
	if len(request.Values) == 0 {
		if label == "" {
			return CreateTargetResult{}, relationError(
				"relation.request.invalid", "target label is required",
			)
		}
		if eligible, reason := quickCreateEligibility(target); !eligible {
			return CreateTargetResult{}, relationError(
				"relation.target_create_requires_full_editor", reason,
			)
		}
		values[labelPhysicalName] = label
	} else {
		allowed := map[string]schema.FieldDefinition{}
		for _, field := range target.Fields {
			if field.ReadOnly || field.Kind == schema.FieldKindFormula ||
				field.Kind == schema.FieldKindLookup || field.Kind == schema.FieldKindSystem {
				continue
			}
			allowed[field.PhysicalName] = field
		}
		for physicalName, value := range request.Values {
			if _, ok := allowed[physicalName]; !ok {
				return CreateTargetResult{}, relationError(
					"relation.target_create_field_invalid",
					"full target creation contains an unknown or read-only field",
				)
			}
			values[physicalName] = value
		}
		label = strings.TrimSpace(fmt.Sprint(values[labelPhysicalName]))
		if label == "" {
			return CreateTargetResult{}, relationError(
				"relation.target_create_field_invalid",
				"full target creation must include the primary display field",
			)
		}
	}
	receipt, err := service.kernel.Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       request.RequestID,
		IdempotencyKey:  request.IdempotencyKey,
		TableID:         target.TableID,
		SchemaRevision:  target.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind:   mutation.OperationInsert,
			Values: values,
		}},
		Actor: request.Actor,
	})
	if err != nil {
		return CreateTargetResult{}, err
	}
	if receipt.Status != mutation.StatusApplied || len(receipt.AffectedRows) != 1 {
		return CreateTargetResult{}, relationError(
			"relation.target_create_pending",
			"target record creation has not committed",
		)
	}
	recordID := receipt.AffectedRows[0].RecordID
	rows, err := service.queries.ReadRows(ctx, target.TableID, []string{recordID})
	if err != nil || len(rows) != 1 {
		return CreateTargetResult{}, relationError(
			"relation.storage_failed",
			"created target record could not be read",
		)
	}
	canonicalLabel := label
	if value := rows[0][labelPhysicalName]; value != nil && fmt.Sprint(value) != "" {
		canonicalLabel = fmt.Sprint(value)
	}
	return CreateTargetResult{
		Target: TargetRef{
			TableID: target.TableID, RecordID: recordID,
			Label: canonicalLabel, JunctionValues: map[string]any{},
		},
		Receipt: receipt,
	}, nil
}

func (service *Service) PreviewDelta(
	ctx context.Context,
	request DeltaRequest,
) (DeltaPreview, error) {
	resolved, current, result, err := service.prepareDelta(ctx, request)
	if err != nil {
		return DeltaPreview{}, err
	}
	mutationRequest := service.deltaMutation(request, resolved, result)
	if _, err := service.kernel.Preview(ctx, mutationRequest); err != nil {
		return DeltaPreview{}, err
	}
	return DeltaPreview{
		RelationID:     request.RelationID,
		SourceRecordID: request.SourceRecordID,
		Current:        current, Result: result,
		Adds: len(request.Adds), Removes: len(request.Removes),
		CanApply: true,
	}, nil
}

func (service *Service) ApplyDelta(
	ctx context.Context,
	request DeltaRequest,
) (DeltaResult, error) {
	resolved, _, result, err := service.prepareDelta(ctx, request)
	if err != nil {
		return DeltaResult{}, err
	}
	receipt, err := service.kernel.Apply(
		ctx, service.deltaMutation(request, resolved, result),
	)
	if err != nil {
		return DeltaResult{}, err
	}
	return DeltaResult{Current: result, Receipt: receipt}, nil
}

func (service *Service) QueryLookups(
	ctx context.Context,
	request LookupQueryRequest,
) (query.Page, error) {
	definition, err := schemaapi.New(service.app).Describe(ctx, request.TableID)
	if err != nil {
		return query.Page{}, err
	}
	if definition.SchemaRevision != request.SchemaRevision {
		return query.Page{}, relationError(
			"lookup.schema_revision_conflict",
			"lookup schema revision does not match",
		)
	}
	page, err := service.queries.QueryPage(ctx, request.TableID, request.Query)
	if err != nil {
		return query.Page{}, err
	}
	return service.attachLookupCells(ctx, definition, page, nil)
}

func (service *Service) PreviewLookups(
	ctx context.Context,
	request LookupPreviewRequest,
) (query.Page, error) {
	currentRevision, err := schemaapi.New(service.app).GetRevision(
		ctx, request.Definition.TableID,
	)
	if err != nil {
		return query.Page{}, err
	}
	if _, err := schemaapi.New(service.app).ValidateChange(
		ctx,
		schemaapi.Change{
			Definition: request.Definition, ExpectedRevision: currentRevision,
		},
	); err != nil {
		return query.Page{}, err
	}
	selected := make(map[string]schema.FieldDefinition, len(request.FieldIDs))
	for _, field := range request.Definition.Fields {
		if field.Kind != schema.FieldKindLookup || field.Lookup == nil {
			continue
		}
		for _, fieldID := range request.FieldIDs {
			if field.FieldID == fieldID {
				selected[fieldID] = field
			}
		}
	}
	if len(selected) != len(request.FieldIDs) {
		return query.Page{}, relationError(
			"lookup.request.invalid", "preview fieldIds contain an unknown Lookup",
		)
	}
	page, err := service.queries.QueryPage(
		ctx, request.Definition.TableID, request.Query,
	)
	if err != nil {
		return query.Page{}, err
	}
	selectedIDs := make(map[string]bool, len(selected))
	for fieldID := range selected {
		selectedIDs[fieldID] = true
	}
	return service.attachLookupCells(ctx, request.Definition, page, selectedIDs)
}

func (service *Service) LookupValuePage(
	ctx context.Context,
	request LookupValuePageRequest,
) (lookupcalc.CellValue, error) {
	definition, err := schemaapi.New(service.app).Describe(ctx, request.TableID)
	if err != nil {
		return lookupcalc.CellValue{}, err
	}
	if definition.SchemaRevision != request.SchemaRevision {
		return lookupcalc.CellValue{}, relationError(
			"lookup.schema_revision_conflict", "lookup schema revision does not match",
		)
	}
	var lookupField schema.FieldDefinition
	found := false
	for _, field := range definition.Fields {
		if field.FieldID == request.FieldID &&
			field.Kind == schema.FieldKindLookup && field.Lookup != nil {
			lookupField = field
			found = true
			break
		}
	}
	if !found || request.SourceRecordID == "" {
		return lookupcalc.CellValue{}, relationError(
			"lookup.request.invalid", "lookup value page target is invalid",
		)
	}
	collection, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return lookupcalc.CellValue{}, relationError(
			"lookup.storage_failed", "lookup source storage is unavailable",
		)
	}
	sourceCollection, err := service.app.FindCollectionByNameOrId(
		collection.GetString("collection_id"),
	)
	if err != nil {
		return lookupcalc.CellValue{}, relationError(
			"lookup.storage_failed", "lookup source storage is unavailable",
		)
	}
	record, err := service.app.FindRecordById(sourceCollection, request.SourceRecordID)
	if err != nil {
		return lookupcalc.CellValue{}, relationError(
			"lookup.storage_failed", "lookup source record is unavailable",
		)
	}
	return lookupcalc.NewCalculator().CalculateFieldPage(
		ctx, service.app, definition, record, lookupField, request.Offset, request.Limit,
	)
}

func (service *Service) attachLookupCells(
	ctx context.Context,
	definition schema.TableDefinition,
	page query.Page,
	selected map[string]bool,
) (query.Page, error) {
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}",
		dbx.Params{"table": definition.TableID},
	)
	if err != nil {
		return query.Page{}, relationError(
			"lookup.storage_failed", "lookup source storage is unavailable",
		)
	}
	collection, err := service.app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return query.Page{}, relationError(
			"lookup.storage_failed", "lookup source storage is unavailable",
		)
	}
	calculator := lookupcalc.NewCalculator()
	for _, row := range page.Rows {
		recordID, ok := row["id"].(string)
		if !ok || recordID == "" {
			return query.Page{}, relationError(
				"lookup.storage_failed", "lookup source row has no id",
			)
		}
		record, findErr := service.app.FindRecordById(collection, recordID)
		if findErr != nil {
			return query.Page{}, relationError(
				"lookup.storage_failed", "lookup source row could not be read",
			)
		}
		values, calculateErr := calculator.CalculateCells(
			ctx, service.app, definition, record,
		)
		if calculateErr != nil {
			return query.Page{}, calculateErr
		}
		for _, field := range definition.Fields {
			if field.Kind != schema.FieldKindLookup || field.Lookup == nil ||
				(selected != nil && !selected[field.FieldID]) {
				continue
			}
			row[field.PhysicalName] = values[field.PhysicalName]
		}
	}
	return page, nil
}

type resolvedRelation struct {
	definition schema.TableDefinition
	field      schema.FieldDefinition
	descriptor Descriptor
	junction   *schema.TableDefinition
}

func (service *Service) resolve(
	ctx context.Context,
	relationID string,
) (resolvedRelation, error) {
	if relationID == "" {
		return resolvedRelation{}, relationError(
			"relation.request.invalid", "relationId is required",
		)
	}
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_relations",
		"relation_id={:relation}",
		dbx.Params{"relation": relationID},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return resolvedRelation{}, relationError(
				"relation.not_found", "relation was not found",
			)
		}
		return resolvedRelation{}, relationError(
			"relation.storage_failed", "relation metadata could not be read",
		)
	}
	definition, err := schemaapi.New(service.app).Describe(
		ctx, meta.GetString("source_table_id"),
	)
	if err != nil {
		return resolvedRelation{}, err
	}
	for _, field := range definition.Fields {
		if field.FieldID == meta.GetString("source_field_id") &&
			field.Kind == schema.FieldKindRelation &&
			field.Relation != nil {
			resolved := resolvedRelation{
				definition: definition,
				field:      field,
				descriptor: descriptorFrom(
					relationID, definition.TableID, field,
				),
			}
			if field.Relation.EffectiveMode() != "direct" {
				junction, junctionErr := schemaapi.New(service.app).Describe(
					ctx, *field.Relation.JunctionTableID,
				)
				if junctionErr != nil {
					return resolvedRelation{}, junctionErr
				}
				resolved.junction = &junction
			}
			return resolved, nil
		}
	}
	return resolvedRelation{}, relationError(
		"relation.schema_invalid",
		"relation field is unavailable in the current schema",
	)
}

func (service *Service) prepareDelta(
	ctx context.Context,
	request DeltaRequest,
) (resolvedRelation, []TargetRef, []TargetRef, error) {
	resolved, err := service.resolve(ctx, request.RelationID)
	if err != nil {
		return resolvedRelation{}, nil, nil, err
	}
	if request.SourceRecordID == "" ||
		request.SchemaRevision != resolved.definition.SchemaRevision ||
		request.RequestID == "" || request.IdempotencyKey == "" ||
		request.Actor.Type == "" || request.Actor.ID == "" {
		return resolvedRelation{}, nil, nil, relationError(
			"relation.request.invalid",
			"relation delta request is incomplete or stale",
		)
	}
	if resolved.descriptor.Mode != "direct" {
		current, result, junctionErr := service.prepareJunctionDelta(
			ctx, resolved, request,
		)
		return resolved, current, result, junctionErr
	}
	if resolved.descriptor.Cardinality != "many" {
		if len(request.Adds) > 1 || len(request.Removes) > 1 {
			return resolvedRelation{}, nil, nil, relationError(
				"relation.cardinality",
				"single relation accepts at most one add and remove",
			)
		}
	}
	rows, err := service.queries.ReadRows(
		ctx, resolved.definition.TableID,
		[]string{request.SourceRecordID},
	)
	if err != nil {
		return resolvedRelation{}, nil, nil, err
	}
	if len(rows) != 1 {
		return resolvedRelation{}, nil, nil, relationError(
			"relation.source_not_found", "source record was not found",
		)
	}
	currentIDs := relationIDs(rows[0][resolved.field.PhysicalName])
	currentSet := make(map[string]TargetRef, len(currentIDs))
	for _, recordID := range currentIDs {
		currentSet[recordID] = TargetRef{
			TableID:  resolved.descriptor.TargetTableID,
			RecordID: recordID,
			Label:    recordID,
		}
	}
	for _, remove := range request.Removes {
		if remove.TableID != resolved.descriptor.TargetTableID {
			return resolvedRelation{}, nil, nil, relationError(
				"relation.target_invalid",
				"remove target belongs to another table",
			)
		}
		if _, exists := currentSet[remove.RecordID]; !exists {
			return resolvedRelation{}, nil, nil, relationError(
				"relation.target_not_linked",
				"remove target is not linked",
			)
		}
		delete(currentSet, remove.RecordID)
	}
	for _, add := range request.Adds {
		if add.TableID != resolved.descriptor.TargetTableID ||
			add.RecordID == "" {
			return resolvedRelation{}, nil, nil, relationError(
				"relation.target_invalid",
				"add target belongs to another table",
			)
		}
		if _, duplicate := currentSet[add.RecordID]; duplicate {
			return resolvedRelation{}, nil, nil, relationError(
				"relation.target_duplicate",
				"add target is already linked",
			)
		}
		currentSet[add.RecordID] = add
	}
	current := refsFromIDs(
		resolved.descriptor.TargetTableID, currentIDs,
	)
	result := make([]TargetRef, 0, len(currentSet))
	for _, item := range currentSet {
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].RecordID < result[right].RecordID
	})
	if resolved.descriptor.Cardinality == "one" && len(result) > 1 {
		return resolvedRelation{}, nil, nil, relationError(
			"relation.cardinality",
			"single relation accepts at most one target",
		)
	}
	return resolved, current, result, nil
}

func (service *Service) deltaMutation(
	request DeltaRequest,
	resolved resolvedRelation,
	result []TargetRef,
) mutation.Request {
	if resolved.descriptor.Mode != "direct" {
		return service.junctionMutation(request, resolved)
	}
	ids := make([]string, 0, len(result))
	for _, item := range result {
		ids = append(ids, item.RecordID)
	}
	var value any = ids
	if resolved.descriptor.Cardinality == "one" {
		value = nil
		if len(ids) == 1 {
			value = ids[0]
		}
	}
	recordID := request.SourceRecordID
	return mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       request.RequestID,
		IdempotencyKey:  request.IdempotencyKey,
		TableID:         resolved.definition.TableID,
		SchemaRevision:  request.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind:     mutation.OperationUpdate,
			RecordID: &recordID,
			Values:   map[string]any{resolved.field.PhysicalName: value},
		}},
		Actor:          request.Actor,
		ExpectedDigest: request.ExpectedDigest,
	}
}

func descriptorFrom(
	relationID string,
	tableID string,
	field schema.FieldDefinition,
) Descriptor {
	relation := field.Relation
	return Descriptor{
		RelationID: relationID, SourceTableID: tableID,
		SourceFieldID: field.FieldID, PhysicalName: field.PhysicalName,
		Mode:          relation.EffectiveMode(),
		TargetTableID: relation.TargetTableID,
		Cardinality:   relation.Cardinality, DeletePolicy: relation.DeletePolicy,
		JunctionTableID:              relation.JunctionTableID,
		JunctionSourceFieldID:        relation.JunctionSourceFieldID,
		JunctionTargetFieldID:        relation.JunctionTargetFieldID,
		JunctionDiscriminatorFieldID: relation.JunctionDiscriminatorFieldID,
		AllowedTargetTableIDs: append(
			[]string{}, relation.AllowedTargetTableIDs...,
		),
		PairID:            relation.PairID,
		ReciprocalFieldID: relation.ReciprocalFieldID,
	}
}

func (service *Service) prepareJunctionDelta(
	ctx context.Context,
	resolved resolvedRelation,
	request DeltaRequest,
) ([]TargetRef, []TargetRef, error) {
	if resolved.junction == nil {
		return nil, nil, relationError(
			"relation.schema_invalid", "junction schema is unavailable",
		)
	}
	rows, err := service.junctionRefs(ctx, resolved, request.SourceRecordID)
	if err != nil {
		return nil, nil, err
	}
	byJunction := make(map[string]TargetRef, len(rows))
	byTarget := make(map[string]string, len(rows))
	for _, item := range rows {
		byJunction[item.JunctionID] = item
		byTarget[targetKey(item)] = item.JunctionID
	}
	result := append([]TargetRef(nil), rows...)
	for _, remove := range request.Removes {
		junctionID := remove.JunctionID
		if junctionID == "" {
			junctionID = byTarget[targetKey(remove)]
		}
		if _, exists := byJunction[junctionID]; !exists {
			return nil, nil, relationError(
				"relation.target_not_linked", "remove target is not linked",
			)
		}
		delete(byJunction, junctionID)
	}
	for _, update := range request.Updates {
		item, exists := byJunction[update.JunctionID]
		if !exists {
			return nil, nil, relationError(
				"relation.junction_not_found", "junction row was not found",
			)
		}
		values, validateErr := junctionContextValues(
			*resolved.junction, resolved.descriptor, update.Values,
		)
		if validateErr != nil {
			return nil, nil, validateErr
		}
		item.JunctionValues = merge(item.JunctionValues, values)
		byJunction[update.JunctionID] = item
	}
	for _, add := range request.Adds {
		if err := service.validateTarget(ctx, resolved.descriptor, add); err != nil {
			return nil, nil, err
		}
		if _, duplicate := byTarget[targetKey(add)]; duplicate {
			// Preserve the original delta shape so an exact idempotent retry
			// reaches MutationKernel replay before insert validation.
			continue
		}
		values, validateErr := junctionContextValues(
			*resolved.junction, resolved.descriptor, add.JunctionValues,
		)
		if validateErr != nil {
			return nil, nil, validateErr
		}
		add.JunctionID = stableJunctionID(
			request.RelationID, request.SourceRecordID, add,
		)
		add.JunctionValues = values
		byJunction[add.JunctionID] = add
		byTarget[targetKey(add)] = add.JunctionID
	}
	result = result[:0]
	for _, item := range byJunction {
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].JunctionID < result[right].JunctionID
	})
	return rows, result, nil
}

func (service *Service) junctionRefs(
	ctx context.Context,
	resolved resolvedRelation,
	sourceRecordID string,
) ([]TargetRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	junction := *resolved.junction
	source, _ := schemaField(junction, resolved.descriptor.JunctionSourceFieldID)
	target, _ := schemaField(junction, resolved.descriptor.JunctionTargetFieldID)
	discriminator, _ := schemaField(
		junction, resolved.descriptor.JunctionDiscriminatorFieldID,
	)
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}",
		dbx.Params{"table": junction.TableID},
	)
	if err != nil {
		return nil, relationError(
			"relation.storage_failed", "junction storage is unavailable",
		)
	}
	collection, err := service.app.FindCollectionByNameOrId(
		meta.GetString("collection_id"),
	)
	if err != nil {
		return nil, relationError(
			"relation.storage_failed", "junction storage is unavailable",
		)
	}
	records, err := service.app.FindRecordsByFilter(
		collection,
		source.PhysicalName+"={:source}",
		"+id",
		maxRelationRows+1,
		0,
		dbx.Params{"source": sourceRecordID},
	)
	if err != nil || len(records) > maxRelationRows {
		return nil, relationError(
			"relation.storage_failed", "junction rows could not be read",
		)
	}
	result := make([]TargetRef, 0, len(records))
	for _, record := range records {
		tableID := resolved.descriptor.TargetTableID
		if resolved.descriptor.Mode == "m2a" {
			tableID = record.GetString(discriminator.PhysicalName)
			if !contains(
				resolved.descriptor.AllowedTargetTableIDs, tableID,
			) {
				continue
			}
		} else if record.GetString(target.PhysicalName) == "" {
			continue
		}
		values := map[string]any{}
		for _, field := range junction.Fields {
			if field.FieldID == resolved.descriptor.JunctionSourceFieldID ||
				field.FieldID == resolved.descriptor.JunctionTargetFieldID ||
				field.FieldID == resolved.descriptor.JunctionDiscriminatorFieldID ||
				field.ReadOnly {
				continue
			}
			value := record.GetRaw(field.PhysicalName)
			if field.DataType == schema.DataTypeSelect ||
				field.DataType == schema.DataTypeMultiSelect {
				value = schema.DecodeSelectValueFromStorage(field, value)
			}
			values[field.PhysicalName] = value
		}
		revision, revisionErr := relationRowRevision(
			ctx, service.app, junction.TableID, record.Id,
		)
		if revisionErr != nil {
			return nil, revisionErr
		}
		result = append(result, TargetRef{
			TableID: tableID, RecordID: record.GetString(target.PhysicalName),
			Label:            record.GetString(target.PhysicalName),
			JunctionID:       record.Id,
			JunctionRevision: fmt.Sprintf("row_%04d", revision),
			JunctionValues:   values,
		})
	}
	return result, nil
}

const maxRelationRows = 1000

func (service *Service) junctionMutation(
	request DeltaRequest,
	resolved resolvedRelation,
) mutation.Request {
	junction := *resolved.junction
	source, _ := schemaField(junction, resolved.descriptor.JunctionSourceFieldID)
	target, _ := schemaField(junction, resolved.descriptor.JunctionTargetFieldID)
	discriminator, _ := schemaField(
		junction, resolved.descriptor.JunctionDiscriminatorFieldID,
	)
	operations := make([]mutation.Operation, 0,
		len(request.Adds)+len(request.Updates)+len(request.Removes))
	for _, add := range request.Adds {
		recordID := stableJunctionID(
			request.RelationID, request.SourceRecordID, add,
		)
		values := map[string]any{
			source.PhysicalName: request.SourceRecordID,
			target.PhysicalName: add.RecordID,
		}
		if resolved.descriptor.Mode == "m2a" {
			values[discriminator.PhysicalName] = add.TableID
		}
		values = merge(values, add.JunctionValues)
		operations = append(operations, mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID, Values: values,
		})
	}
	for _, update := range request.Updates {
		recordID := update.JunctionID
		operations = append(operations, mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values:           update.Values,
			ExpectedRevision: update.ExpectedRevision,
			ExpectedDigest:   update.ExpectedDigest,
		})
	}
	for _, remove := range request.Removes {
		recordID := remove.JunctionID
		if recordID == "" {
			recordID = stableJunctionID(
				request.RelationID, request.SourceRecordID, remove,
			)
		}
		expected := nullable(remove.JunctionRevision)
		operations = append(operations, mutation.Operation{
			Kind: mutation.OperationDelete, RecordID: &recordID,
			ExpectedRevision: expected,
		})
	}
	return mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       request.RequestID, IdempotencyKey: request.IdempotencyKey,
		TableID: junction.TableID, SchemaRevision: junction.SchemaRevision,
		Operations: operations, Actor: request.Actor,
	}
}

func (service *Service) validateTarget(
	ctx context.Context,
	descriptor Descriptor,
	target TargetRef,
) error {
	if target.RecordID == "" ||
		(descriptor.Mode == "m2a" &&
			!contains(descriptor.AllowedTargetTableIDs, target.TableID)) ||
		(descriptor.Mode != "m2a" &&
			target.TableID != descriptor.TargetTableID) {
		return relationError(
			"relation.target_invalid", "relation target is not allowed",
		)
	}
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}",
		dbx.Params{"table": target.TableID},
	)
	if err != nil {
		return relationError(
			"relation.target_invalid", "relation target table was not found",
		)
	}
	collection, err := service.app.FindCollectionByNameOrId(
		meta.GetString("collection_id"),
	)
	if err != nil {
		return relationError(
			"relation.storage_failed", "relation target storage is unavailable",
		)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := service.app.FindRecordById(collection, target.RecordID); err != nil {
		return relationError(
			"relation.target_not_found", "relation target record was not found",
		)
	}
	return nil
}

func junctionContextValues(
	junction schema.TableDefinition,
	descriptor Descriptor,
	values map[string]any,
) (map[string]any, error) {
	result := make(map[string]any, len(values))
	for key, value := range values {
		field, ok := schemaFieldByAlias(junction, key)
		if !ok || field.ReadOnly ||
			field.FieldID == descriptor.JunctionSourceFieldID ||
			field.FieldID == descriptor.JunctionTargetFieldID ||
			field.FieldID == descriptor.JunctionDiscriminatorFieldID ||
			field.Kind != schema.FieldKindScalar {
			return nil, relationError(
				"relation.junction_value_invalid",
				"junction context field is not writable",
			)
		}
		result[field.PhysicalName] = value
	}
	return result, nil
}

func schemaField(
	definition schema.TableDefinition,
	fieldID string,
) (schema.FieldDefinition, bool) {
	for _, field := range definition.Fields {
		if field.FieldID == fieldID {
			return field, true
		}
	}
	return schema.FieldDefinition{}, false
}

func schemaFieldByAlias(
	definition schema.TableDefinition,
	key string,
) (schema.FieldDefinition, bool) {
	for _, field := range definition.Fields {
		if field.FieldID == key || field.PhysicalName == key {
			return field, true
		}
	}
	return schema.FieldDefinition{}, false
}

func stableJunctionID(
	relationID string,
	sourceRecordID string,
	target TargetRef,
) string {
	digest := sha256.Sum256([]byte(
		relationID + "\x00" + sourceRecordID + "\x00" +
			target.TableID + "\x00" + target.RecordID,
	))
	return hex.EncodeToString(digest[:])[:15]
}

func targetKey(target TargetRef) string {
	return target.TableID + "\x00" + target.RecordID
}

func merge(left map[string]any, right map[string]any) map[string]any {
	result := make(map[string]any, len(left)+len(right))
	for key, value := range left {
		result[key] = value
	}
	for key, value := range right {
		result[key] = value
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func relationRowRevision(
	ctx context.Context,
	app core.App,
	tableID string,
	recordID string,
) (int64, error) {
	var count int64
	err := app.ConcurrentDB().NewQuery(`
		SELECT COUNT(DISTINCT change_set_id)
		FROM vibetable_audit_events
		WHERE table_id = {:table} AND record_id = {:record}
	`).WithContext(ctx).Bind(dbx.Params{
		"table": tableID, "record": recordID,
	}).Row(&count)
	if err != nil {
		return 0, relationError(
			"relation.storage_failed", "junction revision could not be read",
		)
	}
	return count, nil
}

func targetLabelField(definition schema.TableDefinition) string {
	if definition.PrimaryDisplayFieldID != "" {
		for _, field := range definition.Fields {
			if field.FieldID == definition.PrimaryDisplayFieldID {
				return field.PhysicalName
			}
		}
	}
	for _, field := range definition.Fields {
		if field.ReadOnly || field.Kind != schema.FieldKindScalar {
			continue
		}
		switch field.DataType {
		case schema.DataTypeShortText, schema.DataTypeLongText,
			schema.DataTypeEmail, schema.DataTypeUUID:
			return field.PhysicalName
		}
	}
	return ""
}

func targetSecondaryField(definition schema.TableDefinition, labelPhysicalName string) string {
	for _, preferredType := range []schema.DataType{
		schema.DataTypeShortText, schema.DataTypeLongText, schema.DataTypeEmail,
		schema.DataTypeURL, schema.DataTypeSelect, schema.DataTypeInteger,
		schema.DataTypeDecimal, schema.DataTypeDate, schema.DataTypeDateTime,
	} {
		for _, field := range definition.Fields {
			if field.PhysicalName == labelPhysicalName ||
				field.Kind != schema.FieldKindScalar || field.DataType != preferredType {
				continue
			}
			return field.PhysicalName
		}
	}
	return ""
}

func quickCreateEligibility(definition schema.TableDefinition) (bool, string) {
	labelPhysicalName := targetLabelField(definition)
	if labelPhysicalName == "" {
		return false, "目标表没有可写的主显示字段"
	}
	for _, field := range definition.Fields {
		if field.PhysicalName == labelPhysicalName || field.ReadOnly ||
			field.Kind == schema.FieldKindFormula || field.Kind == schema.FieldKindLookup ||
			field.Kind == schema.FieldKindSystem || !fieldRequiresValue(field) ||
			hasFieldDefault(field) {
			continue
		}
		return false, fmt.Sprintf("目标表字段“%s”必须在完整记录编辑器中填写", field.DisplayName)
	}
	return true, ""
}

func fieldRequiresValue(field schema.FieldDefinition) bool {
	if !field.Nullable {
		return true
	}
	for _, constraint := range field.Constraints {
		if constraint.Kind == schema.ConstraintRequired {
			value, _ := constraint.Value.(bool)
			if value {
				return true
			}
		}
		if constraint.Kind == schema.ConstraintEnum && constraint.MinSelected == 1 {
			return true
		}
	}
	return false
}

func hasFieldDefault(field schema.FieldDefinition) bool {
	if field.DefaultValue != nil {
		return true
	}
	for _, constraint := range field.Constraints {
		if constraint.Kind == schema.ConstraintDefault && constraint.Value != nil {
			return true
		}
	}
	return false
}

func relationIDs(value any) []string {
	switch typed := value.(type) {
	case nil:
		return []string{}
	case string:
		if typed == "" {
			return []string{}
		}
		return []string{typed}
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return []string{}
	}
}

func refsFromIDs(tableID string, ids []string) []TargetRef {
	result := make([]TargetRef, 0, len(ids))
	for _, recordID := range ids {
		result = append(result, TargetRef{
			TableID: tableID, RecordID: recordID, Label: recordID,
		})
	}
	return result
}

func relationError(code, message string) *mutation.ProductError {
	return &mutation.ProductError{
		ContractVersion: mutation.ContractVersion,
		Code:            code, Message: message, Details: map[string]any{},
	}
}
