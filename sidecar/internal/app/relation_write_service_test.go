package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type relationWriteFixture struct {
	pb         *pocketbase.PocketBase
	service    *relation.Service
	kernel     *mutation.Kernel
	table      schemaexecution.Table
	relationID string
	field      string
	label      string
}

func newRelationWriteFixture(t *testing.T, cardinality string) relationWriteFixture {
	t.Helper()
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{DisplayName: "关系写入", OperationID: "relation-write-table", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "标题", "write-label")
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	field := applySchemaProductField(t, pb, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "关联", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: cardinality, DeletePolicy: "setNull", DisplayField: label.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "反向", ReciprocalCardinality: "many", SourceDisplayFieldID: label.FieldID},
	}, "write-relation")
	definition, err := schemaexecution.Describe(context.Background(), pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"writesource0001", "writetarget0001", "writetarget0002"} {
		r := core.NewRecord(collection)
		r.Id = id
		r.Set(label.Definition.Identity.PhysicalName, id)
		if label.Definition.Value.Presence.Mode == v2.PresenceCompanion {
			r.Set(label.Definition.Value.Presence.PhysicalName, true)
		}
		if err := pb.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(pb, mutation.MetadataSchemaSource{})
	return relationWriteFixture{pb: pb, service: relation.New(pb, query.NewPort(pb, source), kernel), kernel: kernel, table: definition, relationID: table.TableID + "." + field.FieldID, field: field.Definition.Identity.PhysicalName, label: label.Definition.Identity.PhysicalName}
}

func (f relationWriteFixture) delta(key string) relation.DeltaRequest {
	return relation.DeltaRequest{RelationID: f.relationID, SourceRecordID: "writesource0001", SchemaRevision: f.table.Snapshot.SchemaRevision,
		Adds: []relation.TargetRef{{TableID: f.table.Snapshot.TableID, RecordID: "writetarget0001", Label: "A"}}, Removes: []relation.TargetRef{},
		RequestID: key, IdempotencyKey: key, Actor: mutation.Actor{Type: "user", ID: "local-user"}}
}

func TestRelationWriteCreateTargetReplay(t *testing.T) {
	f := newRelationWriteFixture(t, "many")
	request := relation.CreateTargetRequest{RelationID: f.relationID, Label: "新目标", RequestID: "create-key", IdempotencyKey: "create-key", Actor: mutation.Actor{Type: "user", ID: "local-user"}}
	first, err := f.service.CreateTarget(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
	replayed, err := f.service.CreateTarget(context.Background(), request)
	if err != nil {
		t.Fatalf("committed create must replay: %v", err)
	}
	if replayed.Receipt.Status != mutation.StatusReplayed || replayed.Target.RecordID != first.Target.RecordID {
		t.Fatal(replayed)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("replay wrote a second row, receipt or audit")
	}
	request.Label = "different"
	_, err = f.service.CreateTarget(context.Background(), request)
	assertRelationWriteCode(t, err, "mutation.idempotency_conflict")
}

func TestRelationWriteDeltaReplayBeforeReadingNewState(t *testing.T) {
	f := newRelationWriteFixture(t, "many")
	request := f.delta("delta-key")
	first, err := f.service.ApplyDelta(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
	replayed, err := f.service.ApplyDelta(context.Background(), request)
	if err != nil {
		t.Fatalf("committed delta must replay before duplicate check: %v", err)
	}
	if replayed.Receipt.Status != mutation.StatusReplayed || *replayed.Receipt.ChangeSetID != *first.Receipt.ChangeSetID {
		t.Fatal(replayed)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("replay wrote authority")
	}
	request.Adds[0].RecordID = "writetarget0002"
	_, err = f.service.ApplyDelta(context.Background(), request)
	assertRelationWriteCode(t, err, "mutation.idempotency_conflict")
}

func assertRelationWriteCode(t *testing.T, err error, code string) {
	t.Helper()
	var product *mutation.ProductError
	if !errors.As(err, &product) || product.Code != code {
		t.Fatalf("error=%v want %s", err, code)
	}
}

func TestRelationWriteSingleSetClearAndSemanticConflict(t *testing.T) {
	f := newRelationWriteFixture(t, "one")
	request := relation.SingleRequest{RelationID: f.relationID, SourceRecordID: "writesource0001", SchemaRevision: f.table.Snapshot.SchemaRevision, Target: &relation.TargetRef{TableID: f.table.Snapshot.TableID, RecordID: "writetarget0001", Label: "A"}, RequestID: "single-key", IdempotencyKey: "single-key", Actor: mutation.Actor{Type: "user", ID: "local-user"}}
	first, err := f.service.UpdateSingle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
	replayed, err := f.service.UpdateSingle(context.Background(), request)
	if err != nil || replayed.Status != mutation.StatusReplayed || *first.ChangeSetID != *replayed.ChangeSetID {
		t.Fatalf("single replay=%+v %v", replayed, err)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("single replay wrote authority")
	}
	request.Target.RecordID = "writetarget0002"
	_, err = f.service.UpdateSingle(context.Background(), request)
	assertRelationWriteCode(t, err, "mutation.idempotency_conflict")
	request.Target = nil
	request.RequestID = "clear-key"
	request.IdempotencyKey = "clear-key"
	cleared, err := f.service.UpdateSingle(context.Background(), request)
	if err != nil || cleared.Status != mutation.StatusApplied {
		t.Fatalf("clear=%+v %v", cleared, err)
	}
	row, err := f.pb.FindRecordById(f.table.PhysicalName, request.SourceRecordID)
	if err != nil || row.GetString(f.field) != "" {
		t.Fatalf("clear row=%v err=%v", row, err)
	}
	before = previewAuthorityState(t, f.pb, f.table.PhysicalName)
	cleared, err = f.service.UpdateSingle(context.Background(), request)
	if err != nil || cleared.Status != mutation.StatusReplayed {
		t.Fatalf("clear replay=%+v %v", cleared, err)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("clear replay wrote authority")
	}
	// The same key cannot be reused by another operation even when its final
	// source cell would be identical.
	delta := f.delta("clear-key")
	delta.Adds = []relation.TargetRef{}
	_, err = f.service.ApplyDelta(context.Background(), delta)
	assertRelationWriteCode(t, err, "mutation.idempotency_conflict")
	_, err = f.service.CreateTarget(context.Background(), relation.CreateTargetRequest{RelationID: f.relationID, Label: "A", RequestID: "clear-key", IdempotencyKey: "clear-key", Actor: request.Actor})
	assertRelationWriteCode(t, err, "mutation.idempotency_conflict")
}

func TestRelationWritePreparedCompilesOnceAndRollsBack(t *testing.T) {
	f := newRelationWriteFixture(t, "many")
	ctx := context.Background()
	id := "writesource0001"
	request := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "prepared-key", IdempotencyKey: "prepared-key", Actor: mutation.Actor{Type: "user", ID: "local-user"}, TableID: f.table.Snapshot.TableID, SchemaRevision: f.table.Snapshot.SchemaRevision, Operations: []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &id, Values: map[string]any{f.label: "compiled"}}}}
	intent := mutation.PreparedIntent{Kind: "relation.test", RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey, Actor: request.Actor, Params: map[string]any{"scope": request.TableID, "schemaRevision": request.SchemaRevision, "desired": "compiled"}}
	calls := 0
	compile := func(tx core.App) (mutation.Request, error) {
		calls++
		if tx == f.pb {
			t.Fatal("compiler received outer app")
		}
		return request, nil
	}
	projectCalls := 0
	project := func(tx core.App, receipt mutation.Receipt) (any, error) {
		projectCalls++
		if tx == f.pb {
			t.Fatal("projector received outer app")
		}
		return map[string]any{"recordId": receipt.AffectedRows[0].RecordID}, nil
	}
	first, err := f.kernel.ApplyPrepared(ctx, intent, compile, project)
	if err != nil {
		t.Fatal(err)
	}
	before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
	replayed, err := f.kernel.ApplyPrepared(ctx, intent, func(core.App) (mutation.Request, error) {
		t.Fatal("replay compiled mutable state")
		return mutation.Request{}, nil
	}, func(core.App, mutation.Receipt) (any, error) {
		t.Fatal("replay projected mutable state")
		return nil, nil
	})
	if err != nil || replayed.Receipt.Status != mutation.StatusReplayed || calls != 1 || projectCalls != 1 || *first.Receipt.ChangeSetID != *replayed.Receipt.ChangeSetID || string(first.Result) != string(replayed.Result) {
		t.Fatalf("replayed=%+v calls=%d err=%v", replayed, calls, err)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("prepared replay wrote authority")
	}
	_, err = f.kernel.Apply(ctx, request)
	assertRelationWriteCode(t, err, "mutation.idempotency_conflict")
	changed := intent
	changed.Actor.ID = "different-actor"
	_, err = f.kernel.ApplyPrepared(ctx, changed, compile, project)
	assertRelationWriteCode(t, err, "mutation.idempotency_conflict")
	changed = intent
	changed.Params = map[string]any{"scope": request.TableID, "schemaRevision": request.SchemaRevision, "desired": "different"}
	_, err = f.kernel.ApplyPrepared(ctx, changed, compile, project)
	assertRelationWriteCode(t, err, "mutation.idempotency_conflict")
	// A compiler error must roll back even writes made before it returned.
	intent.RequestID = "rollback-key"
	intent.IdempotencyKey = "rollback-key"
	forced := errors.New("forced compiler failure")
	_, err = f.kernel.ApplyPrepared(ctx, intent, func(tx core.App) (mutation.Request, error) {
		row, err := tx.FindRecordById(f.table.PhysicalName, id)
		if err != nil {
			return mutation.Request{}, err
		}
		row.Set(f.label, "must roll back")
		if err = tx.Save(row); err != nil {
			return mutation.Request{}, err
		}
		return mutation.Request{}, forced
	}, project)
	if !errors.Is(err, forced) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("failed compiler leaked row or receipt")
	}
	request.RequestID = intent.RequestID
	request.IdempotencyKey = intent.IdempotencyKey
	_, err = f.kernel.ApplyPrepared(ctx, intent, compile, func(core.App, mutation.Receipt) (any, error) { return nil, forced })
	if !errors.Is(err, forced) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("failed projector leaked business rows, audit or receipt")
	}
	// Ordinary Apply keeps its original identity and replay contract.
	request.RequestID = "direct-key"
	request.IdempotencyKey = "direct-key"
	direct, err := f.kernel.Apply(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	before = previewAuthorityState(t, f.pb, f.table.PhysicalName)
	replay, err := f.kernel.Apply(ctx, request)
	if err != nil || replay.Status != mutation.StatusReplayed || *direct.ChangeSetID != *replay.ChangeSetID {
		t.Fatalf("direct replay=%+v err=%v", replay, err)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("direct replay wrote authority")
	}
}

func TestRelationWriteFailedDeltaLeavesNoReceiptOrAudit(t *testing.T) {
	f := newRelationWriteFixture(t, "many")
	request := f.delta("failed-delta")
	request.Adds[0].RecordID = "missingtarget01"
	before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
	_, err := f.service.ApplyDelta(context.Background(), request)
	assertRelationWriteCode(t, err, "mutation.relation.target_not_found")
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("failed delta wrote row, receipt or audit")
	}
	request.Adds[0].RecordID = "writetarget0001"
	if _, err = f.service.ApplyDelta(context.Background(), request); err != nil {
		t.Fatal("failed attempt reserved idempotency key", err)
	}
}

func TestRelationWriteFirstAttemptChecksSchemaTargetsAndGuard(t *testing.T) {
	f := newRelationWriteFixture(t, "many")
	for _, sample := range []struct {
		name, code string
		change     func(*relation.DeltaRequest)
	}{
		{"schema", "relation.request.invalid", func(r *relation.DeltaRequest) { r.SchemaRevision = "stale-schema" }},
		{"table", "relation.target_invalid", func(r *relation.DeltaRequest) { r.Adds[0].TableID = "another-table" }},
		{"duplicate", "relation.target_duplicate", func(r *relation.DeltaRequest) { r.Adds = append(r.Adds, r.Adds[0]) }},
		{"remove", "relation.target_not_linked", func(r *relation.DeltaRequest) { r.Removes = r.Adds; r.Adds = []relation.TargetRef{} }},
		{"digest", "mutation.digest_conflict", func(r *relation.DeltaRequest) {
			digest := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
			r.ExpectedDigest = &digest
		}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			request := f.delta(sample.name)
			sample.change(&request)
			before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
			_, err := f.service.ApplyDelta(context.Background(), request)
			assertRelationWriteCode(t, err, sample.code)
			if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
				t.Fatal("rejected request changed authority")
			}
		})
	}
}

func TestRelationWriteReplaySettlesAfterCommittedRowsDisappear(t *testing.T) {
	t.Run("delta source deleted", func(t *testing.T) {
		f := newRelationWriteFixture(t, "many")
		request := f.delta("deleted-source")
		first, err := f.service.ApplyDelta(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		row, err := f.pb.FindRecordById(f.table.PhysicalName, request.SourceRecordID)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.pb.Delete(row); err != nil {
			t.Fatal(err)
		}
		before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
		replayed, err := f.service.ApplyDelta(context.Background(), request)
		if err != nil || replayed.Receipt.Status != mutation.StatusReplayed || !reflect.DeepEqual(first.Current, replayed.Current) {
			t.Fatalf("committed delta could not settle: %+v %v", replayed, err)
		}
		if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
			t.Fatal("deleted source replay wrote authority")
		}
	})
	t.Run("created target deleted", func(t *testing.T) {
		f := newRelationWriteFixture(t, "many")
		request := relation.CreateTargetRequest{RelationID: f.relationID, Label: "original label", RequestID: "deleted-target", IdempotencyKey: "deleted-target", Actor: mutation.Actor{Type: "user", ID: "local-user"}}
		first, err := f.service.CreateTarget(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		row, err := f.pb.FindRecordById(f.table.PhysicalName, first.Target.RecordID)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.pb.Delete(row); err != nil {
			t.Fatal(err)
		}
		before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
		replayed, err := f.service.CreateTarget(context.Background(), request)
		if err != nil || replayed.Receipt.Status != mutation.StatusReplayed || first.Target != replayed.Target {
			t.Fatalf("committed create could not settle: %+v %v", replayed, err)
		}
		if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
			t.Fatal("deleted target replay wrote authority")
		}
	})
	t.Run("relation schema removed", func(t *testing.T) {
		f := newRelationWriteFixture(t, "many")
		request := f.delta("deleted-relation")
		first, err := f.service.ApplyDelta(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := f.pb.FindFirstRecordByFilter("vibetable_relations", "relation_id={:relation}", dbx.Params{"relation": f.relationID})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.pb.Delete(meta); err != nil {
			t.Fatal(err)
		}
		before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
		replayed, err := f.service.ApplyDelta(context.Background(), request)
		if err != nil || replayed.Receipt.Status != mutation.StatusReplayed || !reflect.DeepEqual(first.Current, replayed.Current) {
			t.Fatalf("removed relation replay=%+v %v", replayed, err)
		}
		if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
			t.Fatal("schema removal replay changed authority")
		}
	})
}

func TestRelationWriteCorruptPreparedResultFailsClosed(t *testing.T) {
	for _, result := range []any{nil, map[string]any{"not": "targets"}} {
		t.Run(fmt.Sprint(result), func(t *testing.T) {
			f := newRelationWriteFixture(t, "many")
			request := f.delta("corrupt-result")
			first, err := f.service.ApplyDelta(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := f.pb.FindFirstRecordByFilter("vibetable_idempotency_keys", "key={:key}", dbx.Params{"key": request.IdempotencyKey})
			if err != nil {
				t.Fatal(err)
			}
			envelope := map[string]any{"receipt": first.Receipt}
			if result != nil {
				envelope["result"] = result
			}
			stored.Set("receipt_json", envelope)
			if err = f.pb.Save(stored); err != nil {
				t.Fatal(err)
			}
			before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
			_, err = f.service.ApplyDelta(context.Background(), request)
			if err == nil {
				t.Fatal("corrupt result silently recomputed current projection")
			}
			if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
				t.Fatal("corrupt replay wrote authority")
			}
		})
	}
}

func TestRelationWriteTransactionReadsRespectArchivePolicy(t *testing.T) {
	for _, cardinality := range []string{"one", "many"} {
		t.Run(cardinality, func(t *testing.T) {
			f := newRelationWriteFixture(t, cardinality)
			ctx := context.Background()
			deletedAt := createSchemaProductField(t, f.pb, f.table.Snapshot.TableID, v2.LogicalDateTime, "Archived at", "relation-archive-field")
			lifecycle, err := schemacore.NewTableLifecycle(f.pb)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := lifecycle.Configure(ctx, v2.TableSettingsIntent{
				TableID: f.table.Snapshot.TableID, ExpectedSchemaRev: deletedAt.SchemaRevision,
				ArchivePolicy: v2.ArchivePolicy{Mode: "deletedAt", FieldID: &deletedAt.FieldID},
				OperationID:   "relation-archive-policy", Actor: v2.Actor{ID: "local-user", Kind: "user"},
			})
			if err != nil {
				t.Fatal(err)
			}
			f.table, err = schemaexecution.Describe(ctx, f.pb, f.table.Snapshot.TableID)
			if err != nil {
				t.Fatal(err)
			}
			actor := mutation.Actor{Type: "user", ID: "local-user"}
			// A successful creation must project its new row through QueryPort
			// before the existing write transaction commits.
			created, err := f.service.CreateTarget(ctx, relation.CreateTargetRequest{RelationID: f.relationID, Label: "visible before commit", RequestID: "archive-active-create", IdempotencyKey: "archive-active-create", Actor: actor})
			if err != nil || created.Target.Label != "visible before commit" || created.Receipt.Status != mutation.StatusApplied {
				t.Fatalf("transaction's new target is not visible: %+v %v", created, err)
			}
			sourceID := "writesource0001"
			if _, err = f.kernel.Apply(ctx, mutation.Request{
				ContractVersion: mutation.ContractVersion, RequestID: "archive-source", IdempotencyKey: "archive-source", Actor: actor,
				TableID: f.table.Snapshot.TableID, SchemaRevision: settings.SchemaRevision,
				Operations: []mutation.Operation{{Kind: mutation.OperationArchive, RecordID: &sourceID}},
			}); err != nil {
				t.Fatal(err)
			}
			before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
			if cardinality == "one" {
				_, err = f.service.UpdateSingle(ctx, relation.SingleRequest{RelationID: f.relationID, SourceRecordID: sourceID, SchemaRevision: settings.SchemaRevision, Target: &relation.TargetRef{TableID: f.table.Snapshot.TableID, RecordID: "writetarget0001", Label: "A"}, RequestID: "archive-write", IdempotencyKey: "archive-write", Actor: actor})
			} else {
				_, err = f.service.ApplyDelta(ctx, f.delta("archive-write"))
			}
			assertRelationWriteCode(t, err, "relation.source_not_found")
			if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
				t.Fatal("archived source rejection changed business, audit or receipts")
			}
			_, err = f.service.CreateTarget(ctx, relation.CreateTargetRequest{
				RelationID: f.relationID, Values: map[string]any{f.label: "archived target", deletedAt.Definition.Identity.PhysicalName: "2026-07-24T10:00:00Z"},
				RequestID: "archive-create-bypass", IdempotencyKey: "archive-create-bypass", Actor: actor,
			})
			assertRelationWriteCode(t, err, "mutation.archive.requires_operation")
			if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
				t.Fatal("archived target creation rejection changed business, audit or receipts")
			}
		})
	}
}
