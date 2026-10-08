package relation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	lookupcalc "github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
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
	definition, err := schemaexecution.Describe(ctx, service.app, tableID)
	if err != nil {
		return CatalogResult{}, err
	}
	result := CatalogResult{
		TableID: tableID, SchemaRevision: definition.Snapshot.SchemaRevision,
		LookupMaxDepth: lookupMaxDepth(definition),
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
	for _, field := range definition.Snapshot.Fields {
		if field.LogicalType == v2.LogicalRelation && field.Relation != nil {
			descriptor := descriptorFrom(tableID+"."+field.Identity.FieldID, tableID, field)
			target := definition
			if descriptor.TargetTableID != definition.Snapshot.TableID {
				target, err = schemaexecution.Describe(ctx, service.app, descriptor.TargetTableID)
				if err != nil {
					return CatalogResult{}, err
				}
			}
			descriptor.QuickCreateEligible, descriptor.QuickCreateReason =
				quickCreateEligibility(target)
			descriptor.DisplayFieldID = field.Relation.DisplayField
			descriptor.DisplayFieldInfo = service.displayFieldInfoByID(ctx, target, field.Relation.DisplayField)
			descriptor.FallbackDisplayFieldInfo = service.fallbackDisplayFieldInfoFor(ctx, target)
			result.Relations = append(
				result.Relations,
				descriptor,
			)
		}
		if field.LogicalType == v2.LogicalLookup && field.Lookup != nil {
			path, resultMany, outputType, pathErr := service.describeLookupPath(
				ctx, definition, *field.Lookup,
			)
			if pathErr != nil {
				return CatalogResult{}, pathErr
			}
			lookupID := tableID + "." + field.Identity.FieldID
			revision := lookupRevisions[lookupID]
			if revision < 1 {
				revision = 1
			}
			relationFieldID := ""
			if len(field.Lookup.Path) > 0 {
				relationFieldID = field.Lookup.Path[0].RelationFieldID
			}
			result.Lookups = append(result.Lookups, LookupDescriptor{
				Condition:   field.Lookup.Condition,
				Aggregation: field.Lookup.Aggregation,
				LookupID:    lookupID,
				TableID:     tableID, FieldID: field.Identity.FieldID,
				PhysicalName: field.Identity.PhysicalName, DisplayName: field.DisplayName,
				RelationFieldID:   relationFieldID,
				Path:              path,
				TargetFieldID:     field.Lookup.TargetFieldID,
				ResultCardinality: map[bool]string{true: "many", false: "one"}[resultMany],
				OutputStorage:     lookupOutputStorage(outputType), Revision: revision,
			})
		}
	}
	return result, nil
}

func (service *Service) describeLookupPath(
	ctx context.Context,
	source schemaexecution.Table,
	spec v2.LookupSpec,
) ([]LookupPathDescriptor, bool, lookupOutputType, error) {
	// Numeric aggregations materialize number/one values; distinct keeps the
	// collection shape regardless of the source path cardinality.
	aggregationNumeric := v2.LookupAggregationNumeric(v2.ResolvedLookupAggregation(spec))
	// Counts and numeric summaries are decimals regardless of the source
	// field's own storage — countRecords over text and average over an
	// integer both produce the CEL-double-compatible decimal output.
	numericOutput := lookupOutputType{logicalType: v2.LogicalNumber}
	if spec.Condition != nil {
		target, err := schemaexecution.Describe(ctx, service.app, spec.Condition.SourceTableID)
		if err != nil {
			return nil, false, lookupOutputType{}, err
		}
		field, found := target.Field(spec.TargetFieldID)
		if !found {
			return nil, false, lookupOutputType{}, relationError("lookup.value.source_missing", "lookup result field is unavailable")
		}
		if aggregationNumeric {
			return []LookupPathDescriptor{}, false, numericOutput, nil
		}
		return []LookupPathDescriptor{}, true, outputTypeFor(field), nil
	}
	current := source
	result := make([]LookupPathDescriptor, 0, len(spec.Path))
	resultMany := false
	for _, step := range spec.Path {
		relationField, found := relationFieldByID(current, step.RelationFieldID)
		if !found || relationField.Relation == nil {
			return nil, false, lookupOutputType{}, relationError(
				"lookup.schema_invalid",
				"lookup path relation metadata is unavailable",
			)
		}
		if relationField.Relation.Cardinality == "many" {
			resultMany = true
		}
		result = append(result, LookupPathDescriptor{
			RelationID: current.Snapshot.TableID + "." + step.RelationFieldID,
		})
		targetTableID := relationField.Relation.TargetTableID
		target, err := schemaexecution.Describe(ctx, service.app, targetTableID)
		if err != nil {
			return nil, false, lookupOutputType{}, err
		}
		current = target
	}
	targetField, found := current.Field(spec.TargetFieldID)
	if !found {
		return nil, false, lookupOutputType{}, relationError(
			"lookup.schema_invalid", "lookup target field is unavailable",
		)
	}
	if aggregationNumeric {
		return result, false, numericOutput, nil
	}
	if v2.ResolvedLookupAggregation(spec) == v2.LookupAggregationDistinct {
		return result, true, outputTypeFor(targetField), nil
	}
	return result, resultMany, outputTypeFor(targetField), nil
}

func relationFieldByID(
	definition schemaexecution.Table,
	fieldID string,
) (v2.FieldDefinition, bool) {
	for _, field := range definition.Snapshot.Fields {
		if field.Identity.FieldID == fieldID {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
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
	if request.TargetTableID != "" &&
		request.TargetTableID != targetTableID {
		return SearchResult{}, relationError(
			"relation.target_invalid",
			"target table does not match the relation",
		)
	}
	// Direct ID refresh reuses the same projection and paging budget as the
	// keyword search: one batched IN query, never per-record reads.
	filters := []query.FilterExpression{}
	if len(request.TargetItemIDs) > 0 {
		values := make([]any, 0, len(request.TargetItemIDs))
		for _, id := range request.TargetItemIDs {
			values = append(values, id)
		}
		filters = append(filters, query.FilterExpression{
			Field: "id", Operator: query.OperatorIn, Value: values, Logic: query.LogicAnd,
		})
	}
	page, err := service.queries.QueryPage(
		ctx,
		targetTableID,
		query.TableQuery{
			Keyword: request.Query,
			Offset:  request.Offset,
			Limit:   request.Limit,
			Filters: filters,
			Sorts: []query.SortCondition{{
				Field: "id", Direction: query.SortAscending,
			}},
		},
	)
	if err != nil {
		return SearchResult{}, err
	}
	target, err := schemaexecution.Describe(ctx, service.app, targetTableID)
	if err != nil {
		return SearchResult{}, err
	}
	projection := resolveTargetDisplay(resolved.field, target)
	items := make([]TargetRef, 0, len(page.Rows))
	for _, row := range page.Rows {
		recordID := fmt.Sprint(row["id"])
		items = append(items, projection.projectTargetRef(targetTableID, recordID, row))
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
	if request.RequestID == "" || request.IdempotencyKey == "" ||
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
	target, err := schemaexecution.Describe(ctx, service.app, targetTableID)
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
		allowed := map[string]v2.FieldDefinition{}
		for _, field := range target.Snapshot.Fields {
			if fieldReadOnly(field) {
				continue
			}
			allowed[field.Identity.PhysicalName] = field
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
	receipt, err := service.kernel.Apply(mutation.WithBusinessReplay(ctx, "relation.create-target"), mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       request.RequestID,
		IdempotencyKey:  request.IdempotencyKey,
		TableID:         target.Snapshot.TableID,
		SchemaRevision:  target.Snapshot.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind:   mutation.OperationInsert,
			Values: values,
		}},
		Actor: request.Actor,
	})
	if err == writecoordinator.ErrBusinessReplay {
		return CreateTargetResult{}, relationError("relation.target_create_pending", "target record creation has not committed")
	}
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
	rows, err := service.queries.ReadRows(ctx, target.Snapshot.TableID, []string{recordID})
	if err != nil || len(rows) != 1 {
		return CreateTargetResult{}, relationError(
			"relation.storage_failed",
			"created target record could not be read",
		)
	}
	// Business writes keep the global primary display field semantics; only
	// the returned display follows this relation's own display projection.
	projection := resolveTargetDisplay(resolved.field, target)
	return CreateTargetResult{
		Target:  projection.projectTargetRef(target.Snapshot.TableID, recordID, rows[0]),
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
		mutation.WithBusinessReplay(ctx, "relation.apply-delta"), service.deltaMutation(request, resolved, result),
	)
	if err != nil && err != writecoordinator.ErrBusinessReplay {
		return DeltaResult{}, err
	}
	return DeltaResult{Current: result, Receipt: receipt}, err
}

func (service *Service) QueryLookups(
	ctx context.Context,
	request LookupQueryRequest,
) (LookupQueryResult, error) {
	ctx = formula.EnsureEvaluationTime(ctx)
	definition, err := schemaexecution.Describe(ctx, service.app, request.TableID)
	if err != nil {
		return LookupQueryResult{}, err
	}
	if definition.Snapshot.SchemaRevision != request.SchemaRevision {
		return LookupQueryResult{}, relationError(
			"lookup.schema_revision_conflict",
			"lookup schema revision does not match",
		)
	}
	view, err := service.queries.ExecuteViewQuery(ctx, request.TableID, query.ViewQuery{
		Query: request.Query, Groups: request.Groups, GroupLimit: request.GroupLimit,
	})
	if err != nil {
		return LookupQueryResult{}, err
	}
	page, err := service.attachLookupCells(ctx, definition, view.Page, nil)
	if err != nil {
		return LookupQueryResult{}, err
	}
	return LookupQueryResult{
		Page: page, GroupRows: view.GroupRows,
		GroupOffset: view.GroupOffset, GroupLimit: view.GroupLimit,
		HasMoreGroups: view.HasMoreGroups,
	}, nil
}

func (service *Service) LookupValuePage(
	ctx context.Context,
	request LookupValuePageRequest,
) (lookupcalc.CellValue, error) {
	definition, err := schemaexecution.Describe(ctx, service.app, request.TableID)
	if err != nil {
		return lookupcalc.CellValue{}, err
	}
	if definition.Snapshot.SchemaRevision != request.SchemaRevision {
		return lookupcalc.CellValue{}, relationError(
			"lookup.schema_revision_conflict", "lookup schema revision does not match",
		)
	}
	lookupField, found := definition.Field(request.FieldID)
	if !found || lookupField.Lookup == nil || request.SourceRecordID == "" {
		return lookupcalc.CellValue{}, relationError(
			"lookup.request.invalid", "lookup value page target is invalid",
		)
	}
	collection, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}",
		dbx.Params{"table": definition.Snapshot.TableID},
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
	definition schemaexecution.Table,
	page query.Page,
	selected map[string]bool,
) (query.Page, error) {
	if err := ctx.Err(); err != nil {
		return query.Page{}, err
	}
	if len(page.Rows) == 0 {
		return page, nil
	}
	meta, err := service.app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}",
		dbx.Params{"table": definition.Snapshot.TableID},
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
	recordIDs := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		recordID, ok := row["id"].(string)
		if !ok || recordID == "" {
			return query.Page{}, relationError(
				"lookup.storage_failed", "lookup source row has no id",
			)
		}
		recordIDs = append(recordIDs, recordID)
	}
	records := make([]*core.Record, 0, len(recordIDs))
	for start := 0; start < len(recordIDs); start += 256 {
		if err := ctx.Err(); err != nil {
			return query.Page{}, err
		}
		batch, findErr := service.app.FindRecordsByIds(
			collection, recordIDs[start:min(start+256, len(recordIDs))],
			func(statement *dbx.SelectQuery) error {
				statement.WithContext(ctx)
				return nil
			},
		)
		if findErr != nil {
			if err := ctx.Err(); err != nil {
				return query.Page{}, err
			}
			return query.Page{}, relationError(
				"lookup.storage_failed", "lookup source row could not be read",
			)
		}
		records = append(records, batch...)
	}
	values, calculateErr := lookupcalc.NewCalculator().CalculateCellsBatch(
		ctx, service.app, definition, records, selected,
	)
	if calculateErr != nil {
		return query.Page{}, calculateErr
	}
	for _, row := range page.Rows {
		cells, found := values[row["id"].(string)]
		if !found {
			return query.Page{}, relationError(
				"lookup.storage_failed", "lookup source row could not be read",
			)
		}
		for _, field := range definition.Snapshot.Fields {
			if field.Lookup == nil ||
				(selected != nil && !selected[field.Identity.FieldID]) {
				continue
			}
			row[field.Identity.PhysicalName] = cells[field.Identity.PhysicalName]
		}
	}
	return page, nil
}

type resolvedRelation struct {
	definition schemaexecution.Table
	field      v2.FieldDefinition
	descriptor Descriptor
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
	definition, err := schemaexecution.Describe(ctx, service.app, meta.GetString("source_table_id"))
	if err != nil {
		return resolvedRelation{}, err
	}
	for _, field := range definition.Snapshot.Fields {
		if field.Identity.FieldID == meta.GetString("source_field_id") &&
			field.LogicalType == v2.LogicalRelation &&
			field.Relation != nil {
			resolved := resolvedRelation{
				definition: definition,
				field:      field,
				descriptor: descriptorFrom(
					relationID, definition.Snapshot.TableID, field,
				),
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
		request.SchemaRevision != resolved.definition.Snapshot.SchemaRevision ||
		request.RequestID == "" || request.IdempotencyKey == "" ||
		request.Actor.Type == "" || request.Actor.ID == "" {
		return resolvedRelation{}, nil, nil, relationError(
			"relation.request.invalid",
			"relation delta request is incomplete or stale",
		)
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
		ctx, resolved.definition.Snapshot.TableID,
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
	currentIDs := relationIDs(rows[0][resolved.field.Identity.PhysicalName])
	currentSet := make(map[string]struct{}, len(currentIDs))
	for _, recordID := range currentIDs {
		currentSet[recordID] = struct{}{}
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
		currentSet[add.RecordID] = struct{}{}
	}
	target, err := schemaexecution.Describe(
		ctx, service.app, resolved.descriptor.TargetTableID,
	)
	if err != nil {
		return resolvedRelation{}, nil, nil, err
	}
	// Labels are display-only metadata resolved through the shared projection;
	// relation IDs, digests and mutation values stay authoritative. The batch
	// covers the union of current and post-delta targets so added targets get
	// projected labels too, not just pre-existing links.
	projection := resolveTargetDisplay(resolved.field, target)
	resultIDs := make([]string, 0, len(currentSet))
	for recordID := range currentSet {
		resultIDs = append(resultIDs, recordID)
	}
	sort.Strings(resultIDs)
	unionIDs := append([]string(nil), currentIDs...)
	unionIDs = append(unionIDs, resultIDs...)
	rowsByID, err := service.readTargetRows(
		ctx, resolved.descriptor.TargetTableID, unionIDs,
	)
	if err != nil {
		return resolvedRelation{}, nil, nil, err
	}
	current := make([]TargetRef, 0, len(currentIDs))
	for _, recordID := range currentIDs {
		current = append(current, projection.projectTargetRef(
			resolved.descriptor.TargetTableID, recordID, rowsByID[recordID],
		))
	}
	result := make([]TargetRef, 0, len(resultIDs))
	for _, recordID := range resultIDs {
		result = append(result, projection.projectTargetRef(
			resolved.descriptor.TargetTableID, recordID, rowsByID[recordID],
		))
	}
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
		TableID:         resolved.definition.Snapshot.TableID,
		SchemaRevision:  request.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind:     mutation.OperationUpdate,
			RecordID: &recordID,
			Values:   map[string]any{resolved.field.Identity.PhysicalName: value},
		}},
		Actor:          request.Actor,
		ExpectedDigest: request.ExpectedDigest,
	}
}

func descriptorFrom(
	relationID string,
	tableID string,
	field v2.FieldDefinition,
) Descriptor {
	relation := field.Relation
	return Descriptor{
		RelationID: relationID, SourceTableID: tableID,
		SourceFieldID: field.Identity.FieldID, PhysicalName: field.Identity.PhysicalName,
		TargetTableID: relation.TargetTableID,
		Cardinality:   relation.Cardinality, DeletePolicy: relation.DeletePolicy,
		PairID:            relation.PairID,
		ReciprocalFieldID: relation.ReciprocalFieldID,
	}
}

func targetLabelField(definition schemaexecution.Table) string {
	if definition.PrimaryDisplayFieldID != "" {
		for _, field := range definition.Snapshot.Fields {
			if field.Identity.FieldID == definition.PrimaryDisplayFieldID {
				return field.Identity.PhysicalName
			}
		}
	}
	for _, field := range definition.Snapshot.Fields {
		if fieldReadOnly(field) {
			continue
		}
		switch field.LogicalType {
		case v2.LogicalText, v2.LogicalEditor, v2.LogicalEmail:
			return field.Identity.PhysicalName
		}
	}
	return ""
}

func targetSecondaryField(definition schemaexecution.Table, labelPhysicalName string) string {
	for _, preferredType := range []v2.LogicalType{
		v2.LogicalText, v2.LogicalEditor, v2.LogicalEmail,
		v2.LogicalURL, v2.LogicalSelect, v2.LogicalNumber,
		v2.LogicalDate, v2.LogicalDateTime,
	} {
		for _, field := range definition.Snapshot.Fields {
			if field.Identity.PhysicalName == labelPhysicalName ||
				fieldReadOnly(field) || field.LogicalType != preferredType {
				continue
			}
			return field.Identity.PhysicalName
		}
	}
	return ""
}

func quickCreateEligibility(definition schemaexecution.Table) (bool, string) {
	labelPhysicalName := targetLabelField(definition)
	if labelPhysicalName == "" {
		return false, "目标表没有可写的主显示字段"
	}
	for _, field := range definition.Snapshot.Fields {
		if field.Identity.PhysicalName == labelPhysicalName || fieldReadOnly(field) ||
			!fieldRequiresValue(field) ||
			hasFieldDefault(field) {
			continue
		}
		return false, fmt.Sprintf("目标表字段“%s”必须在完整记录编辑器中填写", field.DisplayName)
	}
	return true, ""
}

func fieldRequiresValue(field v2.FieldDefinition) bool {
	return field.Value.Required || field.Constraints.Selection.Min > 0
}

func hasFieldDefault(field v2.FieldDefinition) bool {
	return field.Value.Default.Enabled
}

func fieldReadOnly(field v2.FieldDefinition) bool {
	return field.LogicalType == v2.LogicalAutoDate ||
		field.LogicalType == v2.LogicalFormula ||
		field.LogicalType == v2.LogicalLookup
}

func lookupMaxDepth(definition schemaexecution.Table) int {
	for _, capability := range definition.Snapshot.Capabilities {
		if capability.LogicalType == v2.LogicalLookup {
			return capability.LookupMaxDepth
		}
	}
	return 0
}

// lookupOutputType is relation's compact execution type. The public catalog
// still exposes the historical outputStorage string, converted once at its
// JSON-facing seam instead of rebuilding a legacy schema field.
type lookupOutputType struct {
	logicalType v2.LogicalType
	onlyInt     bool
}

func outputTypeFor(field v2.FieldDefinition) lookupOutputType {
	logicalType := field.LogicalType
	if logicalType == v2.LogicalFormula && field.Formula != nil {
		logicalType = field.Formula.ResultType
	}
	return lookupOutputType{
		logicalType: logicalType,
		onlyInt:     field.Storage.Options.OnlyInt,
	}
}

func lookupOutputStorage(output lookupOutputType) string {
	switch output.logicalType {
	case v2.LogicalText, v2.LogicalEditor, v2.LogicalEmail,
		v2.LogicalURL, v2.LogicalSelect, v2.LogicalMultiSelect,
		v2.LogicalRelation, v2.LogicalFile:
		return "text"
	case v2.LogicalNumber:
		if output.onlyInt {
			return "integer"
		}
		return "decimal"
	case v2.LogicalBool:
		return "boolean"
	case v2.LogicalDate:
		return "date"
	case v2.LogicalDateTime, v2.LogicalAutoDate:
		return "datetime"
	case v2.LogicalTime:
		return "time"
	default:
		return "json"
	}
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

// readTargetRows batches target reads for label projection; it never issues
// per-record queries.
func (service *Service) readTargetRows(
	ctx context.Context,
	tableID string,
	ids []string,
) (map[string]map[string]any, error) {
	result := make(map[string]map[string]any, len(ids))
	for start := 0; start < len(ids); start += 200 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch := ids[start:min(start+200, len(ids))]
		rows, err := service.queries.ReadRows(ctx, tableID, batch)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id, ok := row["id"].(string); ok {
				result[id] = row
			}
		}
	}
	return result, nil
}

func relationError(code, message string) *mutation.ProductError {
	return &mutation.ProductError{
		ContractVersion: mutation.ContractVersion,
		Code:            code, Message: message, Details: map[string]any{},
	}
}
