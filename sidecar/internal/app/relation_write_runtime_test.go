package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type relationWriteFixture struct {
	workspaceMutationReplayFixture
	params        map[string]any
	registrations map[string]productrpc.Registration
}

func newRelationWriteFixture(t *testing.T, cardinality string, options ...mutation.Option) relationWriteFixture {
	f := newWorkspaceMutationReplayFixture(t)
	ctx := context.Background()
	var label v2.FieldDefinition
	for _, field := range f.definition.Snapshot.Fields {
		if field.Identity.PhysicalName == f.field {
			label = field
		}
	}
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	related := applySchemaProductField(t, f.pb, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: f.definition.Snapshot.TableID,
		Draft: &v2.FieldDraft{DisplayName: "单值关系", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: f.definition.Snapshot.TableID, Cardinality: cardinality, DeletePolicy: "setNull", DisplayField: label.Identity.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "反向关系", ReciprocalCardinality: "many", SourceDisplayFieldID: label.Identity.FieldID},
	}, "write-single-field")
	definition, err := schemaexecution.Describe(ctx, f.pb, f.definition.Snapshot.TableID)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	collection, err := f.pb.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	for _, id := range []string{"writesource0001", "writetarget0001", "writetarget0002"} {
		row := core.NewRecord(collection)
		row.Id = id
		row.Set(f.field, id)
		if label.Value.Presence.Mode == v2.PresenceCompanion {
			row.Set(label.Value.Presence.PhysicalName, true)
		}
		if err := f.pb.Save(row); err != nil {
			t.Fatalf("%#v", err)
		}
	}
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatalf("%#v", err)
	}
	queries := query.NewPort(f.pb, source)
	service := relation.New(f.pb, queries, mutation.New(f.pb, mutation.MetadataSchemaSource{}, options...))
	registrations := map[string]productrpc.Registration{}
	for _, reg := range relationWriteRegistrations(service, queries, f.runtime.CoordinateBusinessWrite) {
		registrations[reg.Method] = reg
	}
	params := map[string]any{"relationId": definition.Snapshot.TableID + "." + related.FieldID, "sourceItemId": "writesource0001", "target": map[string]any{"collection": definition.Snapshot.TableID, "itemId": "writetarget0001", "label": "Target"}, "expectedSchemaRevision": definition.Snapshot.SchemaRevision, "idempotencyKey": "single-replay"}
	return relationWriteFixture{f, params, registrations}
}

func (f relationWriteFixture) invoke(t *testing.T, ctx context.Context, method string, params map[string]any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	reg := f.registrations[method]
	if err := reg.ValidateParams(raw); err != nil {
		t.Fatal(err)
	}
	return reg.Handler(ctx, raw)
}

func TestRelationWriteSingleReplayDoesNotCommitAgain(t *testing.T) {
	f := newRelationWriteFixture(t, "one")
	ctx := context.Background()
	registration := f.registrations["relation.updateSingle"]
	raw, _ := json.Marshal(f.params)
	before := f.state(t)
	first, err := registration.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	if first == nil {
		t.Fatal("missing committed result")
	}
	committed := f.state(t)
	if committed.revision != before.revision+1 || committed.proofs != before.proofs+1 {
		t.Fatalf("first commit did not write exactly once: before=%+v after=%+v", before, committed)
	}
	replay, err := registration.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	if replay == nil {
		t.Fatal("missing replay result")
	}
	if after := f.state(t); !reflect.DeepEqual(committed, after) {
		t.Fatalf("replay changed authority: before revision=%d proofs=%d after revision=%d proofs=%d", committed.revision, committed.proofs, after.revision, after.proofs)
	}
}

func TestRelationWriteSingleRejectionsAndClearAreAtomic(t *testing.T) {
	f := newRelationWriteFixture(t, "one")
	ctx := context.Background()
	if _, err := f.invoke(t, ctx, "relation.updateSingle", f.params); err != nil {
		t.Fatal(err)
	}
	committed := f.state(t)
	variants := []map[string]any{
		{"target": map[string]any{"collection": f.definition.Snapshot.TableID, "itemId": "writetarget0002"}},
		{"target": map[string]any{"collection": "another-table", "itemId": "writetarget0001"}, "idempotencyKey": "wrong-table"},
		{"expectedSchemaRevision": "stale", "idempotencyKey": "stale"},
		{"sourceItemId": "missing-row", "idempotencyKey": "missing"},
	}
	for _, changes := range variants {
		params := map[string]any{}
		for key, value := range f.params {
			params[key] = value
		}
		for key, value := range changes {
			params[key] = value
		}
		if _, err := f.invoke(t, ctx, "relation.updateSingle", params); err == nil {
			t.Fatalf("accepted invalid update: %v", changes)
		}
		if after := f.state(t); !reflect.DeepEqual(committed, after) {
			t.Fatal("rejection changed authority")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.invoke(t, cancelled, "relation.updateSingle", f.params); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if after := f.state(t); !reflect.DeepEqual(committed, after) {
		t.Fatal("cancellation changed authority")
	}
	f.params["target"], f.params["idempotencyKey"] = nil, "clear"
	result, err := f.invoke(t, ctx, "relation.updateSingle", f.params)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["current"] != nil {
		t.Fatal("clear did not project null")
	}
	cleared := f.state(t)
	if _, err := f.invoke(t, ctx, "relation.updateSingle", f.params); err != nil {
		t.Fatal(err)
	}
	if after := f.state(t); !reflect.DeepEqual(cleared, after) {
		t.Fatal("clear replay changed authority")
	}
}

func TestRelationWriteDeltaPreservesRepeatedRemoveFailureWithoutWrites(t *testing.T) {
	f := newRelationWriteFixture(t, "many")
	ctx := context.Background()
	before := f.state(t)
	if _, err := f.invoke(t, ctx, "relation.updateSingle", f.params); err == nil {
		t.Fatal("single writer accepted many cardinality")
	}
	if after := f.state(t); !reflect.DeepEqual(before, after) {
		t.Fatal("cardinality rejection wrote authority")
	}
	delete(f.params, "target")
	target := func(id string) any { return map[string]any{"collection": f.definition.Snapshot.TableID, "itemId": id} }
	f.params["adds"], f.params["removes"] = []any{target("writetarget0001"), target("writetarget0002")}, []any{}
	result, err := f.invoke(t, ctx, "relation.applyDelta", f.params)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.(map[string]any)["current"].([]any)) != 2 {
		t.Fatal("missing links")
	}
	f.params["idempotencyKey"], f.params["adds"], f.params["removes"] = "remove-one", []any{}, []any{target("writetarget0001")}
	result, err = f.invoke(t, ctx, "relation.applyDelta", f.params)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.(map[string]any)["current"].([]any)) != 1 {
		t.Fatal("remove did not commit")
	}
	committed := f.state(t)
	_, err = f.invoke(t, ctx, "relation.applyDelta", f.params)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "relation.target_not_linked" {
		t.Fatalf("old repeated-remove behavior changed: %#v", err)
	}
	if after := f.state(t); !reflect.DeepEqual(committed, after) {
		t.Fatal("repeated remove wrote authority")
	}
}

func TestRelationWriteCreateAndFailedMutationAreAtomic(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		f := newRelationWriteFixture(t, "one")
		params := map[string]any{"relationId": f.params["relationId"], "label": "新目标", "idempotencyKey": "create-target"}
		before := f.state(t)
		result, err := f.invoke(t, context.Background(), "relation.createTarget", params)
		if err != nil {
			t.Fatal(err)
		}
		target := result.(map[string]any)["target"].(map[string]any)
		if target["collection"] != f.definition.Snapshot.TableID || target["label"] != "新目标" || target["itemId"] == "" {
			t.Fatal(target)
		}
		committed := f.state(t)
		if committed.revision != before.revision+1 || committed.proofs != before.proofs+1 {
			t.Fatal("create must commit once")
		}
		_, err = f.invoke(t, context.Background(), "relation.createTarget", params)
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != "relation.target_create_pending" {
			t.Fatalf("old create replay behavior changed: %#v", err)
		}
		if after := f.state(t); !reflect.DeepEqual(committed, after) {
			t.Fatal("create replay wrote authority")
		}
	})
	t.Run("rollback", func(t *testing.T) {
		reached := false
		f := newRelationWriteFixture(t, "one", mutation.WithFaultInjector(func(point string) error {
			if point == "before_commit" {
				reached = true
				return errors.New("injected")
			}
			return nil
		}))
		before := f.state(t)
		if _, err := f.invoke(t, context.Background(), "relation.updateSingle", f.params); err == nil || !reached {
			t.Fatal("fault did not reach transaction boundary")
		}
		if after := f.state(t); !reflect.DeepEqual(before, after) {
			t.Fatal("failure left rows, reciprocal links, audit or receipts")
		}
	})
}
