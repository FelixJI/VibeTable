package app

import (
	"context"
	"fmt"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

// REST and Product share these existing authorities and write admission keys.
// A new transport must not create its own planner, plan store or executor.
type schemaFieldChangeDomain struct {
	fieldSettingsDescribeDomain
	core      *schemacore.Core
	tables    *schemacore.TableLifecycle
	catalog   *fieldchange.Catalog
	migration *fieldchange.MigrationService
	gates     []businessWriteGate
}

func (d schemaFieldChangeDomain) CreateTable(ctx context.Context, intent v2.TableCreateIntent) (v2.TableCreateReceipt, error) {
	if replay, found, err := d.tables.FindReplay(intent); err != nil {
		return v2.TableCreateReceipt{}, err
	} else if found {
		return replay, nil
	}
	var receipt v2.TableCreateReceipt
	err := runIdempotentBusinessWrite(ctx, d.gates, "schema.table.create", intent.OperationID, func(ctx context.Context) error {
		var err error
		receipt, err = d.tables.Create(ctx, intent)
		return err
	})
	if err != nil {
		return receipt, err
	}
	if receipt.TableID == "" {
		return d.tables.Replay(intent)
	}
	return receipt, nil
}

func (d schemaFieldChangeDomain) Plan(ctx context.Context, intent v2.FieldChangeIntent) (v2.FieldChangePlan, error) {
	var plan v2.FieldChangePlan
	err := runBusinessWrite(ctx, d.gates, "field.change.plan", fmt.Sprintf("%s:%s:%s:%s", intent.TableID, intent.FieldID, intent.Action, intent.ExpectedSchemaRev), func(ctx context.Context) error {
		var err error
		plan, err = d.core.Plan(ctx, intent)
		return err
	})
	return plan, err
}

func (d schemaFieldChangeDomain) Apply(ctx context.Context, request v2.ApplyRequest) (v2.ApplyReceipt, error) {
	var receipt v2.ApplyReceipt
	err := runBusinessWrite(ctx, d.gates, "field.change.apply", request.OperationID, func(ctx context.Context) error {
		var err error
		receipt, err = d.core.Apply(ctx, request)
		return err
	})
	return receipt, err
}

type recycledFieldsResult struct {
	Contract string               `json:"contract"`
	Fields   []v2.FieldDefinition `json:"fields"`
}

func (d schemaFieldChangeDomain) RecycledFields(ctx context.Context, tableID string) (recycledFieldsResult, error) {
	fields, err := d.catalog.Fields(ctx, tableID, true)
	if err != nil {
		return recycledFieldsResult{}, err
	}
	retired := make([]v2.FieldDefinition, 0)
	for _, field := range fields {
		if field.Lifecycle.State == v2.LifecycleRetired {
			retired = append(retired, field)
		}
	}
	return recycledFieldsResult{Contract: v2.Contract, Fields: retired}, nil
}

func (d schemaFieldChangeDomain) Status(ctx context.Context, jobID string) (v2.MigrationStatus, error) {
	return d.migration.Status(ctx, jobID)
}

func (d schemaFieldChangeDomain) Cancel(ctx context.Context, jobID string) (v2.MigrationStatus, error) {
	var status v2.MigrationStatus
	err := runBusinessWrite(ctx, d.gates, "field.change.cancel", jobID, func(ctx context.Context) error {
		var err error
		status, err = d.migration.Cancel(ctx, jobID)
		return err
	})
	return status, err
}

func deleteSchemaTable(ctx context.Context, catalog schemaapi.SchemaCatalog, tableID, revision string, gates []businessWriteGate) (schemaapi.DeleteResult, error) {
	expected, err := v2.ParseSchemaRevision(revision)
	if err != nil {
		return schemaapi.DeleteResult{}, &schemaerror.ProductError{Code: "schema.revision.invalid", Path: "expectedRevision", Message: err.Error()}
	}
	var result schemaapi.DeleteResult
	err = runBusinessWrite(ctx, gates, "schema.delete", fmt.Sprintf("%s:%d", tableID, expected), func(ctx context.Context) error {
		var err error
		result, err = catalog.DeleteTable(ctx, tableID, expected)
		return err
	})
	return result, err
}
