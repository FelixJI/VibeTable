package mutation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

var errPlanInspected = errors.New("mutation plan inspected without persistence")

// InspectPlan reuses the write session on an in-memory record overlay. It does
// not invoke PocketBase writes, file hooks, business receipts or publishers.
// Business rules and computed values remain authoritative at actual commit.
func (kernel *Kernel) InspectPlan(ctx context.Context, requests []Request, authorize func(context.Context, core.App, string, string) error) (PlanReceipt, error) {
	if err := kernel.coordinator.acquire(ctx); err != nil {
		return PlanReceipt{}, err
	}
	defer kernel.coordinator.release()
	copy := *kernel
	copy.coordinator = &mutationCoordinator{gate: make(chan struct{}, 1)}
	copy.formulas, copy.attachments, copy.invalidator, copy.publisher = nil, nil, nil, nil
	copy.fault = func(string) error { return nil }
	var result PlanReceipt
	err := kernel.app.RunInTransaction(func(tx core.App) error {
		copy.app = &planOverlay{App: tx, records: map[RowKey]*core.Record{}}
		_, err := copy.ApplyPlan(context.WithValue(ctx, inspectPlanKey{}, true), requests, authorize, func(_ core.App, receipt PlanReceipt) error {
			result = receipt
			return errPlanInspected
		})
		return err
	})
	if !errors.Is(err, errPlanInspected) {
		return PlanReceipt{}, err
	}
	return result, nil
}

type planOverlay struct {
	core.App
	records map[RowKey]*core.Record
}

func (overlay *planOverlay) RunInTransaction(fn func(core.App) error) error { return fn(overlay) }

func (overlay *planOverlay) Save(model core.Model) error {
	record, ok := model.(*core.Record)
	if !ok {
		return errors.New("plan inspection accepts only record writes")
	}
	overlay.records[RowKey{record.Collection().Id, record.Id}] = record
	return nil
}

func (overlay *planOverlay) Delete(model core.Model) error {
	record, ok := model.(*core.Record)
	if !ok {
		return errors.New("plan inspection accepts only record deletes")
	}
	overlay.records[RowKey{record.Collection().Id, record.Id}] = nil
	return nil
}

func (overlay *planOverlay) FindRecordById(collection any, id string, filters ...func(*dbx.SelectQuery) error) (*core.Record, error) {
	var schema *core.Collection
	var err error
	switch value := collection.(type) {
	case *core.Collection:
		schema = value
	case string:
		schema, err = overlay.App.FindCollectionByNameOrId(value)
	default:
		return nil, errors.New("unsupported plan inspection collection")
	}
	if err != nil {
		return nil, err
	}
	key := RowKey{schema.Id, id}
	if record, found := overlay.records[key]; found {
		if record == nil {
			return nil, sql.ErrNoRows
		}
		return record, nil
	}
	record, err := overlay.App.FindRecordById(collection, id, filters...)
	if err == nil {
		overlay.records[key] = record
	}
	return record, err
}
