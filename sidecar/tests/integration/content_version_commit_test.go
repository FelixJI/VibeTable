package integration_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type mutationCommitter interface {
	ApplyWithCommit(context.Context, mutation.Request, func(core.App, mutation.Receipt) error) (mutation.Receipt, error)
}

func TestMutationCommitCallbackIsAtomicAndNeverRunsOnReplay(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "Named revision commit", "named_commit_table")
	field := createV2IntegrationField(t, ctx, app, table.TableID, fieldDraftForIntegration(t, v2.LogicalText, "Title"), "named_commit_title")
	description, err := schemaexecution.Describe(ctx, app, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &committedOutboxPublisher{app: app}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithPublisher(publisher))
	committer, ok := any(kernel).(mutationCommitter)
	if !ok {
		t.Fatal("mutation kernel has no atomic result commit seam")
	}
	id := "versionrecord01"
	request := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "version-atomic", IdempotencyKey: "version-atomic", TableID: table.TableID, SchemaRevision: description.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &id, Values: map[string]any{field.Definition.Identity.PhysicalName: "final value"}}}}
	snapshot := func() []int {
		result := []int{}
		for _, name := range []string{description.PhysicalName, "vibetable_audit_events", "vibetable_outbox", "vibetable_idempotency_keys"} {
			rows, err := app.FindAllRecords(name)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, len(rows))
		}
		return result
	}
	before := snapshot()
	failure := errors.New("result receipt write failed")
	calls := 0
	_, err = committer.ApplyWithCommit(ctx, request, func(tx core.App, receipt mutation.Receipt) error {
		calls++
		row, readErr := tx.FindRecordById(description.PhysicalName, id)
		if readErr != nil || row.GetString(field.Definition.Identity.PhysicalName) != "final value" || receipt.ChangeSetID == nil {
			t.Fatalf("callback did not see final transaction: %#v %v", receipt, readErr)
		}
		return failure
	})
	if !errors.Is(err, failure) || calls != 1 || publisher.called || !reflect.DeepEqual(snapshot(), before) {
		t.Fatalf("callback rollback: %v calls=%d published=%v before=%v after=%v", err, calls, publisher.called, before, snapshot())
	}
	cancelled, cancel := context.WithCancel(ctx)
	_, err = committer.ApplyWithCommit(cancelled, request, func(core.App, mutation.Receipt) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || publisher.called || !reflect.DeepEqual(snapshot(), before) {
		t.Fatalf("cancel after result callback escaped transaction: %v %v", err, snapshot())
	}
	receipt, err := committer.ApplyWithCommit(ctx, request, func(core.App, mutation.Receipt) error { calls++; return nil })
	if err != nil || receipt.Status != mutation.StatusApplied || calls != 2 {
		t.Fatalf("commit: %#v %v %d", receipt, err, calls)
	}
	after := snapshot()
	replay, err := committer.ApplyWithCommit(ctx, request, func(core.App, mutation.Receipt) error {
		t.Fatal("replayed mutation must not regenerate an original result")
		return nil
	})
	if err != nil || replay.Status != mutation.StatusReplayed || !reflect.DeepEqual(snapshot(), after) {
		t.Fatalf("replay: %#v %v", replay, err)
	}
}
