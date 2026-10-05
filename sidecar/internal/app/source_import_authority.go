package app

import (
	"context"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

// sourceImportAuthority reuses the running application's schema and mutation
// authorities. The migration executor owns which newly created targets it may
// address; this adapter never substitutes a target, revision, actor or key.
type sourceImportAuthority struct {
	schema schemaFieldChangeDomain
	kernel *mutation.Kernel
	gates  []businessWriteGate
}

var _ sourceimport.Authority = (*sourceImportAuthority)(nil)

func newSourceImportAuthority(
	domain schemaFieldChangeDomain,
	kernel *mutation.Kernel,
	gates ...businessWriteGate,
) *sourceImportAuthority {
	return &sourceImportAuthority{schema: domain, kernel: kernel, gates: gates}
}

func (authority *sourceImportAuthority) Describe(
	ctx context.Context,
	tableID string,
) (v2.SchemaSnapshot, error) {
	return authority.schema.core.Describe(ctx, tableID)
}

func (authority *sourceImportAuthority) CreateTable(
	ctx context.Context,
	intent v2.TableCreateIntent,
	commit func(core.App, v2.TableCreateReceipt) error,
) (v2.TableCreateReceipt, error) {
	return authority.schema.CreateTableWithCommit(ctx, intent, func(tx core.App, receipt v2.TableCreateReceipt) error {
		// Preview cannot reserve a display name. Check again inside the create
		// transaction so a competing table leaves this new table rolled back.
		conflicts, err := tx.FindRecordsByFilter("vibetable_tables",
			"display_name={:name} && table_id!={:table}", "", 1, 0,
			dbx.Params{"name": receipt.DisplayName, "table": receipt.TableID})
		if err != nil {
			return err
		}
		if len(conflicts) != 0 {
			return &sourceimport.Error{
				Code: "source_import.target_name", Message: "target table name is already in use; rename and preview again",
			}
		}
		if commit != nil {
			return commit(tx, receipt)
		}
		return nil
	})
}

func (authority *sourceImportAuthority) ChangeField(
	ctx context.Context,
	intent v2.FieldChangeIntent,
	operationID string,
	commit func(core.App, v2.ApplyReceipt) error,
) (v2.ApplyReceipt, error) {
	if operationID == "" || (intent.Action != v2.ActionCreate && intent.Action != v2.ActionUpdate) {
		return v2.ApplyReceipt{}, &sourceimport.Error{
			Code: "source_import.schema.unsupported", Message: "source migration only creates or configures fields with a stable operation identity",
		}
	}
	// Keep the caller's expected revisions. Describing the latest schema here
	// and replacing that fence would silently accept a stale migration plan.
	plan, err := authority.schema.Plan(ctx, intent)
	if err != nil {
		return v2.ApplyReceipt{}, err
	}
	if !plan.CanApply || len(plan.Errors) != 0 {
		return v2.ApplyReceipt{}, &sourceimport.Error{
			Code: "source_import.schema.blocked", Message: "source migration field plan contains blocking diagnostics",
		}
	}
	if plan.CreatesMigration {
		return v2.ApplyReceipt{}, &sourceimport.Error{
			Code: "source_import.schema.migration_required", Message: "source migration cannot complete an asynchronous field migration",
		}
	}
	for _, class := range plan.Classes {
		if class == v2.ClassDanger || class == v2.ClassMigration {
			return v2.ApplyReceipt{}, &sourceimport.Error{
				Code: "source_import.schema.unsupported", Message: "source migration cannot perform dangerous or migrating field changes",
			}
		}
	}
	// Current create and constraint-update plans require no confirmations.
	// In particular, cascade and protection confirmations must never be
	// manufactured from the user's confirmation of a create-only import.
	if len(plan.Confirmations) != 0 {
		return v2.ApplyReceipt{}, &sourceimport.Error{
			Code: "source_import.schema.confirmation_required", Message: "field plan requires a confirmation outside the source migration contract",
		}
	}
	return authority.schema.ApplyWithCommit(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: operationID,
		Actor: intent.Actor, Confirmations: []string{},
	}, commit)
}

func (authority *sourceImportAuthority) Mutate(
	ctx context.Context,
	request mutation.Request,
	commit func(core.App, mutation.Receipt) error,
) (mutation.Receipt, error) {
	var receipt mutation.Receipt
	err := runBusinessWrite(ctx, authority.gates, "mutation.apply", request.IdempotencyKey, func(ctx context.Context) error {
		var err error
		receipt, err = authority.kernel.ApplyWithCommit(ctx, request, commit)
		return err
	})
	return receipt, err
}
