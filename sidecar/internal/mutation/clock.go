package mutation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

// RecalculateClock refreshes derived caches without editing business inputs or
// creating row history. Only the internal job path can call this method; the
// public mutation endpoint always uses Apply. The existing workspace write gate
// still binds every transaction to its recovery receipt and audit ledger.
func (kernel *Kernel) RecalculateClock(ctx context.Context, request Request) (Receipt, error) {
	if err := validateRequestShape(request); err != nil {
		return Receipt{}, err
	}
	if formula.EvaluationFields(ctx) == nil || kernel.formulas == nil {
		return Receipt{}, fmt.Errorf("clock materialization context is unavailable")
	}
	for _, operation := range request.Operations {
		if operation.Kind != OperationUpdate || operation.RecordID == nil || len(operation.Values) != 0 || len(operation.RawValues) != 0 {
			return Receipt{}, fmt.Errorf("clock materialization accepts only existing records without input changes")
		}
	}
	if err := kernel.coordinator.acquire(ctx); err != nil {
		return Receipt{}, err
	}
	ctx = relatedcomputation.WithClockCache(formula.EnsureEvaluationTime(ctx))
	var eventIDs []string
	receipt := Receipt{ContractVersion: ContractVersion, Status: StatusApplied, ComputedFields: map[string]map[string]any{}}
	err := kernel.app.RunInTransaction(func(app core.App) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		preview, err := kernel.preview(ctx, app, request)
		if err != nil {
			return err
		}
		definition := preview.Definition

		meta, collection, err := loadTableMetadata(app, request.TableID)
		if err != nil {
			return err
		}
		revision, err := storedNonNegativeInteger(meta.GetRaw("data_revision"), "mutation.metadata.invalid_data_revision")
		if err != nil {
			return err
		}
		clockRevision, err := storedNonNegativeInteger(meta.GetRaw(relatedcomputation.ClockRevisionField), "mutation.metadata.invalid_clock_revision")
		if err != nil {
			return err
		}
		if clockRevision > revision {
			return fmt.Errorf("clock revision exceeds data revision")
		}
		if revision >= maxSafeCounter {
			return fmt.Errorf("clock data revision exhausted")
		}
		var changed []string
		for _, operation := range request.Operations {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := app.FindRecordById(collection, *operation.RecordID)
			if err != nil {
				return err
			}
			values, err := kernel.formulas.Calculate(ctx, app, definition, record)
			if err != nil {
				return err
			}
			if len(values) == 0 {
				continue
			}
			values, err = normalizeComputedFields(definition, values)
			if err != nil {
				return err
			}
			stored, err := relatedcomputation.WrapValues(ctx, app, request.TableID,
				definition.Snapshot.Fields, int64(record.GetInt(relatedcomputation.RowRevisionField)), values)
			if err != nil {
				return err
			}
			// Update only the schema-declared computed JSON columns. A normal
			// record Save would also advance PocketBase auto-date inputs.
			columns := dbx.Params{}
			for name, value := range stored {
				raw, err := json.Marshal(value)
				if err != nil {
					return err
				}
				columns[name] = string(raw)
			}
			if _, err := app.DB().Update(collection.Name, columns, dbx.HashExp{"id": record.Id}).WithContext(ctx).Execute(); err != nil {
				return err
			}
			changed = append(changed, record.Id)
			receipt.ComputedFields[record.Id] = values
		}
		if len(changed) > 0 {
			meta.Set("data_revision", revision+1)
			meta.Set(relatedcomputation.ClockRevisionField, clockRevision+1)
			if err := app.Save(meta); err != nil {
				return err
			}
			event := DataChangedEvent{
				ContractVersion: ContractVersion, Topic: "data.changed", EventID: kernel.newID("event"),
				Sequence: revision + 1, OccurredAt: formula.EvaluationTime(ctx).Format(time.RFC3339Nano),
				SchemaRevision: request.SchemaRevision, DataRevision: formatRevision("data", revision+1),
				TableID: request.TableID, RecordIDs: changed, Operation: DataChangeUpdate,
			}
			// A nil changeSetId identifies a derived-cache refresh: no user
			// edit or row-history entry exists for the clock moving forward.
			if err := saveOutbox(app, event); err != nil {
				return err
			}
			if kernel.invalidator != nil {
				if _, err := kernel.invalidator.EnqueueInvalidations(ctx, app, event); err != nil {
					return err
				}
			}
			eventIDs = []string{event.EventID}
			receipt.EmittedEvents = eventIDs
			receipt.NewRevision = &event.DataRevision
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return writecoordinator.PersistPocketBaseReceipt(ctx, app, kernel.now())
	})
	err = writecoordinator.ClassifyPocketBaseTransactionError(ctx, kernel.app, err)
	kernel.coordinator.release()
	if err != nil {
		return Receipt{}, err
	}
	if kernel.publisher != nil && len(eventIDs) > 0 {
		if err := kernel.publishCommitted(ctx, eventIDs); err != nil {
			receipt.Warnings = append(receipt.Warnings, ProductError{
				ContractVersion: ContractVersion, Code: "mutation.realtime.publish_pending",
				Message: "computed refresh remains in the durable outbox", Retryable: true,
			})
		}
	}
	return receipt, nil
}
