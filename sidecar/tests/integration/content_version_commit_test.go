package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
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

// Keep the real audit service behind the same narrow history port as the runtime.
type namedRevisionHistory struct{ *audit.Service }

func (h namedRevisionHistory) ReadBusinessHistory(ctx context.Context, p audit.ReadParams) (audit.Page, error) {
	return h.ReadChangeSets(ctx, p)
}
func (h namedRevisionHistory) PreviewBusinessHistoryRestore(ctx context.Context, p audit.PreviewParams) (audit.Preview, error) {
	return h.PreviewRestore(ctx, p)
}

func TestContentVersionComparisonMatchesRestoredManyRelation(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	targets := createV2IntegrationTable(t, ctx, app, "Version targets", "version_targets")
	targetName := createV2IntegrationField(t, ctx, app, targets.TableID, fieldDraftForIntegration(t, v2.LogicalText, "Name"), "version_target_name")
	source := createV2IntegrationTable(t, ctx, app, "Version source", "version_source")
	title := createV2IntegrationField(t, ctx, app, source.TableID, fieldDraftForIntegration(t, v2.LogicalText, "Title"), "version_source_title")
	related := createV2IntegrationRelation(t, ctx, app, source.TableID, title.FieldID, targets.TableID, targetName.FieldID, "Related", "Sources", "many", "version_relation")
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	service, err := audit.New(app, kernel)
	if err != nil {
		t.Fatal(err)
	}
	owner := metadata.NewContentVersions(app, namedRevisionHistory{service}, service)
	apply := func(key, tableID, recordID string, kind mutation.OperationKind, values map[string]any) {
		t.Helper()
		described, err := schemaexecution.Describe(ctx, app, tableID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = kernel.Apply(ctx, mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: key, IdempotencyKey: key, TableID: tableID, SchemaRevision: described.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: kind, RecordID: &recordID, Values: values}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"versiontarget01", "versiontarget02", "versiontarget03"}
	for _, id := range ids {
		apply("insert-"+id, targets.TableID, id, mutation.OperationInsert, map[string]any{targetName.Definition.Identity.PhysicalName: id})
	}
	field := related.Definition.Identity.PhysicalName
	rowID := "versionsource01"
	apply("insert-source", source.TableID, rowID, mutation.OperationInsert, map[string]any{field: ids[:2]})
	created, err := owner.Write(ctx, "version.create", metadata.VersionParams{Collection: source.TableID, ItemID: rowID, Key: "before", OperationID: "name-relation"})
	if err != nil {
		t.Fatal(err)
	}
	entry := created.(map[string]any)
	versionID := entry["id"].(string)
	apply("update-source", source.TableID, rowID, mutation.OperationUpdate, map[string]any{field: ids[1:]})
	compared, err := owner.Compare(ctx, metadata.VersionParams{Collection: source.TableID, ItemID: rowID, VersionID: versionID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(compared)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		MainHash        string
		VersionRevision string
		Differences     map[string]struct {
			Main    any
			Version any
		}
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	difference := result.Differences[field]
	if difference.Main != strings.Join(ids[1:], ", ") || difference.Version != strings.Join(ids[:2], ", ") {
		t.Fatalf("named comparison hides actual relation sets: %s", encoded)
	}
	_, err = owner.Write(ctx, "version.promote", metadata.VersionParams{Collection: source.TableID, ItemID: rowID, VersionID: versionID, ExpectedRevision: result.VersionRevision, MainHash: result.MainHash, OperationID: "restore-relation"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(source.PhysicalName, rowID)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.GetStringSlice(field); !reflect.DeepEqual(got, ids[:2]) {
		t.Fatalf("restored relation %v differs from displayed %v", got, difference.Version)
	}
}
