package mutation

import (
	"context"
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/autonumber"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// ReformatAutoNumber runs only inside the field-change transaction. Its replay
// receipt owns idempotency; only final business images enter history and events.
func (kernel *Kernel) ReformatAutoNumber(
	ctx context.Context, app core.App, tableID string,
	before, after v2.FieldDefinition, actor v2.Actor, operationID string,
) ([]string, error) {
	ctx = relatedcomputation.WithClockCache(formula.EnsureEvaluationTimeAt(ctx, kernel.now()))
	definition, err := kernel.schemas.Describe(ctx, app, tableID)
	if err != nil {
		return nil, err
	}
	for _, field := range definition.Snapshot.Fields {
		if (field.Formula != nil || field.Lookup != nil) && kernel.formulas == nil {
			return nil, fmt.Errorf("auto_number.reformat_unavailable: computed calculator is unavailable")
		}
	}
	if kernel.invalidator == nil {
		dependencies, dependencyErr := app.FindRecordsByFilter("vibetable_computation_dependencies", "target_table_id={:table}", "", 1, 0, dbx.Params{"table": tableID})
		if dependencyErr != nil {
			return nil, dependencyErr
		}
		if len(dependencies) != 0 {
			return nil, fmt.Errorf("auto_number.reformat_unavailable: durable invalidation scheduler is unavailable")
		}
	}
	table, collection, err := loadTableMetadata(app, tableID)
	if err != nil {
		return nil, err
	}
	dataRevision, err := storedNonNegativeInteger(table.GetRaw("data_revision"), "mutation.metadata.invalid_data_revision")
	if err != nil {
		return nil, err
	}
	if dataRevision >= maxSafeCounter {
		return nil, mutationError("mutation.metadata.data_revision_exhausted", nil, "table data revision cannot be incremented", nil, false)
	}
	changes, err := autonumber.Reformat(ctx, app, tableID, collection, before, after)
	if err != nil || len(changes) == 0 {
		return nil, err
	}
	request := Request{ContractVersion: ContractVersion, RequestID: "field-change:" + operationID,
		IdempotencyKey: operationID, TableID: tableID, SchemaRevision: definition.Snapshot.SchemaRevision,
		Actor: Actor{Type: actor.Kind, ID: actor.ID}}
	changeSetID := kernel.newID("changeSet")
	beforeRows := make([]map[string]any, len(changes))
	recordIDs := make([]string, 0, len(changes))
	// Invalidate every rewritten row and dependency before calculating any
	// formula, so no collection expression can read a half-updated input set.
	for index, change := range changes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		beforeRows[index] = ProductRow(app, definition, change.Before)
		revision, err := committedRowRevision(ctx, app, tableID, change.After.Id)
		if err != nil {
			return nil, err
		}
		change.After.Set(relatedcomputation.RowRevisionField, revision+1)
		if err := app.Save(change.After); err != nil {
			return nil, storageSaveFailure(err)
		}
		if err := relatedcomputation.AdvanceInputRevisions(ctx, app, tableID, definition.Snapshot.Fields,
			beforeRows[index], ProductRow(app, definition, change.After), string(OperationUpdate), dataRevision+1); err != nil {
			return nil, err
		}
		recordIDs = append(recordIDs, change.After.Id)
	}
	computedFields := map[string]map[string]any{}
	// All business inputs are final; this trusted schema-owned pass may
	// materialize stale computed dependencies without relaxing public reads.
	calculationCtx := formula.WithSourceRecalculation(ctx)
	for index, change := range changes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, err := kernel.applyOperation(calculationCtx, app, definition, change.After,
			NormalizedOperation{Kind: OperationUpdate}, beforeRows[index],
			int64(change.After.GetInt(relatedcomputation.RowRevisionField)), computedFields, nil, formula.EvaluationTime(ctx))
		if err != nil {
			return nil, err
		}
		if err := kernel.injectFault("after_record"); err != nil {
			return nil, err
		}
		if err := saveAudit(ctx, app, changeSetID, index+1, request, definition,
			change.After.Id, OperationUpdate, beforeRows[index], values, dataRevision+1, formula.EvaluationTime(ctx)); err != nil {
			return nil, err
		}
	}
	table.Set("data_revision", dataRevision+1)
	if err := app.Save(table); err != nil {
		return nil, storageSaveFailure(err)
	}
	event := DataChangedEvent{ContractVersion: ContractVersion, Topic: "data.changed", EventID: kernel.newID("event"),
		Sequence: dataRevision + 1, OccurredAt: formula.EvaluationTime(ctx).UTC().Format(time.RFC3339Nano),
		SchemaRevision: definition.Snapshot.SchemaRevision, DataRevision: formatRevision("data", dataRevision+1),
		ChangeSetID: &changeSetID, TableID: tableID, RecordIDs: recordIDs, Operation: DataChangeUpdate}
	if err := saveOutbox(app, event); err != nil {
		return nil, err
	}
	if kernel.invalidator != nil {
		if _, err := kernel.invalidator.EnqueueInvalidations(ctx, app, event); err != nil {
			return nil, err
		}
	}
	for _, change := range changes {
		if err := refreshComputedVersions(ctx, app, definition, change.After); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []string{event.EventID}, nil
}

func (kernel *Kernel) PublishAutoNumberReformat(ctx context.Context, eventIDs []string) error {
	if kernel.publisher == nil {
		return nil
	}
	return kernel.publishCommitted(ctx, eventIDs)
}
