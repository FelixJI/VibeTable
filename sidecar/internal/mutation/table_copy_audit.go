package mutation

import (
	"context"
	"time"

	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// AuditCopiedRows records independent insert history in the enclosing table
// copy transaction, including the ordinary durable audit outbox projection.
func AuditCopiedRows(ctx context.Context, app core.App, tableID, operationID string, actor v2.Actor, now time.Time) error {
	definition, err := schemaexecution.Describe(ctx, app, tableID)
	if err != nil {
		return err
	}
	rows, err := app.FindRecordsByFilter(definition.PhysicalName, "", "id", 0, 0)
	if err != nil {
		return err
	}
	actorType := actor.Kind
	if actorType == "host" {
		actorType = "system"
	}
	request := Request{ContractVersion: ContractVersion, RequestID: "table-copy:" + operationID,
		IdempotencyKey: operationID, TableID: tableID, SchemaRevision: definition.Snapshot.SchemaRevision,
		Actor: Actor{Type: actorType, ID: actor.ID}}
	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := saveAudit(ctx, app, "table-copy:"+operationID, i+1, request, definition, row.Id, OperationInsert, nil, ProductRow(app, definition, row), 1, now); err != nil {
			return err
		}
	}
	return nil
}
