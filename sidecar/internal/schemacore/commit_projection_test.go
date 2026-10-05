package schemacore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func commitProjectionApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}

func projectionMarkerKey(operationID string) string {
	return "commit-projection:" + operationID
}

// writeProjectionMarker persists a projection through the app handed to the
// commit callback. When that app is the transaction, the marker commits or
// rolls back together with the business write; when it is not, the marker
// survives a business rollback and the seam is broken.
func writeProjectionMarker(app core.App, operationID string) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_idempotency_keys")
	if err != nil {
		return fmt.Errorf("load projection marker collection: %w", err)
	}
	record := core.NewRecord(collection)
	record.Set("key", projectionMarkerKey(operationID))
	record.Set("request_hash", "commit-projection")
	record.Set("status", "projected")
	record.Set("receipt_json", types.JSONRaw([]byte(`{}`)))
	record.Set("expires_at", time.Now().Add(time.Hour))
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save projection marker: %w", err)
	}
	return nil
}

func TestTableCreateWithCommitProjectsInsideFirstBusinessTransactionOnly(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, err := NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.TableCreateIntent{
		DisplayName: "迁移投影目标", OperationID: "operation-table-commit-projection",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	}
	calls := 0
	var projected v2.TableCreateReceipt
	receipt, err := lifecycle.CreateWithCommit(context.Background(), intent, func(tx core.App, committed v2.TableCreateReceipt) error {
		calls++
		projected = committed
		// The projection runs before the business commit: the uncommitted
		// table metadata is only visible on the transaction's connection.
		if _, findErr := tx.FindFirstRecordByFilter(
			"vibetable_tables", "table_id={:table}",
			dbx.Params{"table": committed.TableID},
		); findErr != nil {
			return fmt.Errorf("projection cannot observe the uncommitted table: %w", findErr)
		}
		return writeProjectionMarker(tx, committed.OperationID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("projection calls = %d, want exactly one first-time call", calls)
	}
	if projected != receipt {
		t.Fatalf("projected receipt %#v differs from returned receipt %#v", projected, receipt)
	}
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys", "key={:key}",
		dbx.Params{"key": projectionMarkerKey(intent.OperationID)},
	); findErr != nil {
		t.Fatalf("projection marker was not committed with the table: %v", findErr)
	}
	// Repeating the same create must replay without re-projecting.
	replayed, err := lifecycle.CreateWithCommit(context.Background(), intent, func(core.App, v2.TableCreateReceipt) error {
		calls++
		return errors.New("replayed create must not re-project")
	})
	if err != nil {
		t.Fatalf("replayed CreateWithCommit() failed: %v", err)
	}
	if replayed != receipt {
		t.Fatalf("replayed receipt %#v differs from committed receipt %#v", replayed, receipt)
	}
	if calls != 1 {
		t.Fatalf("replay re-ran the projection: calls = %d", calls)
	}
}

func TestTableCreateProjectionFailureRollsBackTableMetadataAndAudit(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, err := NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.TableCreateIntent{
		DisplayName: "回滚投影", OperationID: "operation-table-projection-rollback",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	}
	projectionErr := errors.New("table projection failed")
	_, err = lifecycle.CreateWithCommit(context.Background(), intent, func(tx core.App, committed v2.TableCreateReceipt) error {
		// Write through the callback app first: a rollback of the business
		// create must also discard this projection write.
		if err := writeProjectionMarker(tx, committed.OperationID); err != nil {
			return err
		}
		return projectionErr
	})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("CreateWithCommit() error = %v, want %v", err, projectionErr)
	}
	tableID, physicalName := tableIdentities(intent.OperationID)
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("rolled back table metadata is still present: %v", findErr)
	}
	if _, findErr := app.FindCollectionByNameOrId(physicalName); findErr == nil {
		t.Fatal("rolled back table collection is still present")
	}
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_schema_audit", "operation_id={:operation}",
		dbx.Params{"operation": intent.OperationID},
	); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("rolled back schema audit is still present: %v", findErr)
	}
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys", "key={:key}",
		dbx.Params{"key": projectionMarkerKey(intent.OperationID)},
	); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("projection marker committed despite the business rollback: %v", findErr)
	}
	// The failed operation left no partial state: the same intent can create
	// the table again through the unchanged nil-callback path.
	receipt, err := lifecycle.Create(context.Background(), intent)
	if err != nil {
		t.Fatalf("retry after projection rollback failed: %v", err)
	}
	if receipt.TableID != tableID || receipt.SchemaRevision != v2.FormatSchemaRevision(1) {
		t.Fatalf("retry receipt = %#v", receipt)
	}
}

type commitForwardExecutor struct{ executorStub }

func (stub *commitForwardExecutor) ApplyWithCommit(
	_ context.Context,
	request v2.ApplyRequest,
	commit func(core.App, v2.ApplyReceipt) error,
) (v2.ApplyReceipt, error) {
	receipt := v2.ApplyReceipt{Contract: v2.Contract, OperationID: request.OperationID}
	if commit == nil {
		return v2.ApplyReceipt{}, errors.New("commit callback is required")
	}
	if err := commit(nil, receipt); err != nil {
		return v2.ApplyReceipt{}, err
	}
	return receipt, nil
}

func TestCoreApplyWithCommitForwardsProjectionToCommitCapableExecutor(t *testing.T) {
	schemaCore, err := New(sourceStub{}, &plannerStub{}, &commitForwardExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	invoked := false
	receipt, err := schemaCore.ApplyWithCommit(
		context.Background(),
		v2.ApplyRequest{PlanID: "plan-commit", OperationID: "operation-commit"},
		func(core.App, v2.ApplyReceipt) error { invoked = true; return nil },
	)
	if err != nil || !invoked {
		t.Fatalf("ApplyWithCommit() = %#v, %v, callback invoked %t", receipt, err, invoked)
	}
	if receipt.OperationID != "operation-commit" {
		t.Fatalf("forwarded receipt = %#v", receipt)
	}
}

func TestCoreApplyWithCommitFailsExplicitlyWithoutCommitSupport(t *testing.T) {
	schemaCore, err := New(sourceStub{}, &plannerStub{}, &executorStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := v2.ApplyRequest{PlanID: "plan-1", OperationID: "operation-1"}
	if _, err := schemaCore.ApplyWithCommit(
		context.Background(), request,
		func(core.App, v2.ApplyReceipt) error {
			t.Fatal("callback must not run when the executor cannot project in the commit transaction")
			return nil
		},
	); err == nil || !strings.Contains(err.Error(), "does not support commit projection") {
		t.Fatalf("silent callback drop = %v", err)
	}
	// A nil callback keeps the plain Apply behavior on any executor.
	if receipt, err := schemaCore.ApplyWithCommit(context.Background(), request, nil); err != nil ||
		receipt.OperationID != request.OperationID {
		t.Fatalf("nil callback ApplyWithCommit() = %#v, %v", receipt, err)
	}
}
