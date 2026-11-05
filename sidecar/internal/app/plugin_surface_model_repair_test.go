package app

import (
	"context"
	"errors"
	"github.com/pocketbase/dbx"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

func TestPluginModelRepairCanonicalCompareAndHistory(t *testing.T) {
	pb, workspace, input, domain, first, field := existingModelMapping(t)
	run := func(method string, request any) any {
		t.Helper()
		result, err := modelCall(t, pb, workspace, input, domain, method, request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	receipt := func(id string) map[string]string { return map[string]string{"requestId": id} }
	run("model.commit", receipt(first.RequestID))
	alternative := createSchemaProductField(t, pb, field.TableID, v2.LogicalText, "替代名称", "model-repair-alternative")
	propose := func(id string) modelPreview {
		return run("model.preview", modelPreviewRequest{Mode: "repair", Mappings: map[string]pluginmodel.Binding{"items": {TableID: field.TableID, Fields: map[string]string{"name": id}}}}).(modelPreview)
	}
	// Simulate legacy stale permissions. Ordinary data authority must refuse to issue grants.
	schema, err := schemaexecution.Describe(context.Background(), pb, field.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(schema.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	physical := collection.Fields.GetByName(field.Definition.Identity.PhysicalName)
	physical.SetHidden(true)
	if err := pb.Save(collection); err != nil {
		t.Fatal(err)
	}
	observation := run("model.read", map[string]string{}).(pluginmodel.State)
	if observation.Status != "stale" || len(observation.Diagnostics) != 1 || observation.Diagnostics[0].Path != "items.name" || observation.Diagnostics[0].Code != "plugin_model_field_access_denied" {
		t.Fatalf("stale: %+v", observation)
	}
	if _, err := surfaceInstallation(context.Background(), pb, workspace, input); err == nil {
		t.Fatal("stale model conferred data grants")
	}
	a, b := propose(alternative.FieldID), propose(alternative.FieldID)
	if len(a.Changes) != 1 || a.Changes[0].OldField != field.FieldID || a.Changes[0].NewField != alternative.FieldID || a.Status != "pending" {
		t.Fatalf("repair preview: %+v", a)
	}
	if got := run("model.read", map[string]string{}).(pluginmodel.State); got.Bindings["items"].Fields["name"] != field.FieldID {
		t.Fatal("preview switched authority")
	}
	done := run("model.commit", receipt(a.RequestID)).(pluginmodel.State)
	if done.Bindings["items"].Fields["name"] != alternative.FieldID || done.Status != "completed" {
		t.Fatalf("repair: %+v", done)
	}
	if replay := run("model.commit", receipt(a.RequestID)).(pluginmodel.State); replay.RequestID != a.RequestID {
		t.Fatal("retry changed repair")
	}
	if _, err := modelCall(t, pb, workspace, input, domain, "model.commit", receipt(b.RequestID)); err == nil {
		t.Fatal("superseded plan accepted")
	}
	original := run("model.status", receipt(first.RequestID)).(pluginmodel.State)
	if original.Bindings["items"].Fields["name"] != field.FieldID {
		t.Fatal("old binding history was rewritten")
	}
	third := createSchemaProductField(t, pb, field.TableID, v2.LogicalText, "第三名称", "model-repair-third")
	pending := propose(third.FieldID)
	row := core.NewRecord(collection)
	if err := pb.Save(row); err != nil {
		t.Fatal(err)
	}
	if _, err := modelCall(t, pb, workspace, input, domain, "model.commit", receipt(pending.RequestID)); err == nil {
		t.Fatal("row inserted after preview was ignored")
	}
	if blocked := propose(third.FieldID); blocked.Status != "blocked" || blocked.Diagnostics[0].Code != "plugin_model_history_present" {
		t.Fatalf("history: %+v", blocked)
	}
}

func TestPluginModelPhysicalBindingDrift(t *testing.T) {
	for _, kind := range []string{"provider", "type", "precision", "relation-target", "relation-cardinality", "collection"} {
		t.Run(kind, func(t *testing.T) {
			pb, workspace, input, domain := modelFixture(t)
			called := func(method string, params any) any {
				t.Helper()
				r, err := modelCall(t, pb, workspace, input, domain, method, params)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			p := called("model.preview", modelPreviewRequest{Mode: "create"}).(modelPreview)
			state := called("model.commit", map[string]string{"requestId": p.RequestID}).(pluginmodel.State)
			entity, key, code := "assets", "quantity", "plugin_model_precision_loss"
			if kind == "relation-target" || kind == "relation-cardinality" {
				entity, key, code = "people", "tickets", "plugin_model_relation_mismatch"
			}
			b := state.Bindings[entity]
			table, err := schemaexecution.Describe(t.Context(), pb, b.TableID)
			if err != nil {
				t.Fatal(err)
			}
			col, err := pb.FindCollectionByNameOrId(table.CollectionID)
			if err != nil {
				t.Fatal(err)
			}
			def, _ := table.Field(b.Fields[key])
			provider := col.Fields.GetById(def.Identity.ProviderFieldID)
			// Simulate an independently edited legacy provider schema while the v2 declaration stays intact.
			switch kind {
			case "provider":
				provider.SetId("replaced-provider")
				code = "plugin_model_field_missing"
			case "type":
				col.Fields.RemoveById(provider.GetId())
				col.Fields.Add(&core.TextField{Id: provider.GetId(), Name: provider.GetName()})
				code = "plugin_model_type_mismatch"
			case "precision":
				provider.(*core.NumberField).OnlyInt = false
			case "relation-target":
				provider.(*core.RelationField).CollectionId = col.Id
			case "relation-cardinality":
				provider.(*core.RelationField).MaxSelect = 1
			}
			path := entity + "." + key
			if kind == "collection" {
				_, err = pb.DB().NewQuery("DELETE FROM _collections WHERE id={:id}").Bind(dbx.Params{"id": col.Id}).Execute()
				path, code = entity, "plugin_model_table_missing"
			} else {
				fields, encodeErr := col.Fields.Value()
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				_, err = pb.DB().NewQuery("UPDATE _collections SET fields={:fields} WHERE id={:id}").Bind(dbx.Params{"id": col.Id, "fields": fields}).Execute()
			}
			if err != nil {
				t.Fatal(err)
			}
			got := called("model.read", map[string]string{}).(pluginmodel.State)
			if got.Status != "stale" || !slices.ContainsFunc(got.Diagnostics, func(d v2.Diagnostic) bool { return d.Path == path && d.Code == code }) {
				t.Fatalf("stale %s %+v", path, got.Diagnostics)
			}
			if kind == "collection" {
				catalog := called("model.catalog", map[string]string{}).(map[string]any)
				blocked := catalog["diagnostics"].([]v2.Diagnostic)
				if catalog["status"] != "blocked" || len(blocked) != 1 || blocked[0].Path != entity || blocked[0].Code != "plugin_model_history_unknown" ||
					catalog["bindings"].(map[string]pluginmodel.Binding)[entity].TableID != b.TableID {
					t.Fatalf("native blocked catalog: %+v", catalog)
				}
			}
			if _, err := surfaceInstallation(t.Context(), pb, workspace, input); err == nil {
				t.Fatal("drifted provider conferred grants")
			}
			if _, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]string{"requestId": p.RequestID}); err == nil {
				t.Fatal("stale completion replayed")
			}
		})
	}
}

func TestPluginModelRelationMetadataDrift(t *testing.T) {
	for _, drift := range []string{"pair", "delete-policy", "mirror"} {
		t.Run(drift, func(t *testing.T) {
			pb, workspace, input, domain := modelFixture(t)
			call := func(method string, req any) any {
				t.Helper()
				res, err := modelCall(t, pb, workspace, input, domain, method, req)
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			p := call("model.preview", modelPreviewRequest{Mode: "create"}).(modelPreview)
			s := call("model.commit", map[string]string{"requestId": p.RequestID}).(pluginmodel.State)
			b := s.Bindings["people"]
			params := dbx.Params{"table": b.TableID, "field": b.Fields["tickets"]}
			sql := "DELETE FROM vibetable_relations WHERE source_table_id={:table} AND source_field_id={:field}"
			if drift != "mirror" {
				column, path, value := "pair_id", "$.relation.pairId", "relp_different"
				if drift == "delete-policy" {
					column, path, value = "delete_policy", "$.relation.deletePolicy", "setNull"
				}
				params["path"], params["value"] = path, value
				_, err := pb.DB().NewQuery("UPDATE vibetable_fields SET definition_v2_json=json_set(definition_v2_json,{:path},{:value}) WHERE table_id={:table} AND field_id={:field}").Bind(params).Execute()
				if err != nil {
					t.Fatal(err)
				}
				sql = "UPDATE vibetable_relations SET " + column + "={:value} WHERE source_table_id={:table} AND source_field_id={:field}"
			}
			if _, err := pb.DB().NewQuery(sql).Bind(params).Execute(); err != nil {
				t.Fatal(err)
			}
			read := call("model.read", map[string]string{}).(pluginmodel.State)
			for _, path := range []string{"people.tickets", "tickets.person"} {
				if read.Status != "stale" || !slices.ContainsFunc(read.Diagnostics, func(d v2.Diagnostic) bool { return d.Path == path && d.Code == "plugin_model_relation_mismatch" }) {
					t.Fatalf("drift %s: %+v", drift, read)
				}
			}
			if _, err := surfaceInstallation(t.Context(), pb, workspace, input); err == nil {
				t.Fatal("invalid pair granted data access")
			}
		})
	}
}

func TestPluginModelRepairCommittedHookErrorRecoversNextWrite(t *testing.T) {
	runtime, ledger, _, _, _, pb := historyRestoreProductFixture(t)
	_, workspace, input, domain := modelFixtureOn(t, pb)
	call := func(method string, params any) any {
		t.Helper()
		r, err := modelCall(t, pb, workspace, input, domain, method, params)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	request := func(id string) map[string]string { return map[string]string{"requestId": id} }
	seed := call("model.preview", modelPreviewRequest{Mode: "create"}).(modelPreview)
	current := call("model.commit", request(seed.RequestID)).(pluginmodel.State)
	b := current.Bindings["assets"]
	alternate := createSchemaProductField(t, pb, b.TableID, v2.LogicalText, "替代名称", "repair-hook-target")
	propose := func(field string) modelPreview {
		next := cloneModelState(current)
		next.Bindings["assets"].Fields["name"] = field
		return call("model.preview", modelPreviewRequest{Mode: "repair", Mappings: next.Bindings}).(modelPreview)
	}
	plan := propose(alternate.FieldID)
	if plan.Status != "pending" {
		t.Fatal(plan.Diagnostics)
	}
	domain.gates = []businessWriteGate{runtime.CoordinateBusinessWrite}
	path := filepath.Join(filepath.Dir(pb.DataDir()), "coordination", "write-coordinator.db")
	revision := func() uint64 {
		t.Helper()
		r, err := writecoordinator.ReadPersistentMutationRevision(t.Context(), path, workspace)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	before := revision()
	injected := errors.New("injected after successful PB commit")
	hook := pb.OnRecordAfterCreateSuccess("vibetable_plugin_records")
	hookID := hook.BindFunc(func(e *core.RecordEvent) error {
		if e.Record.GetString("kind") == "audit" {
			return injected
		}
		return e.Next()
	})
	_, err := modelCall(t, pb, workspace, input, domain, "model.commit", request(plan.RequestID))
	hook.Unbind(hookID)
	if !errors.Is(err, writecoordinator.ErrExternalCommitted) || !errors.Is(err, injected) {
		t.Fatalf("commit classification: %v", err)
	}
	if got := call("model.read", map[string]string{}).(pluginmodel.State); got.RequestID != plan.RequestID || got.Bindings["assets"].Fields["name"] != alternate.FieldID {
		t.Fatal("PB commit was lost")
	}
	if err := runtime.CoordinateBusinessWrite(t.Context(), "after.repair", "probe", func(context.Context) error { t.Fatal("prepared revision was reused"); return nil }); !errors.Is(err, writecoordinator.ErrRecoveryRequired) {
		t.Fatalf("expected prepared recovery: %v", err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspacev2.Open(t.Context(), workspacev2.Options{App: pb, DataDir: pb.DataDir(), WorkspaceID: workspace, SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Ledger: ledger, DeferBackgroundWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if revision() != before+1 {
		t.Fatal("reopening did not settle the committed repair revision")
	}
	domain.gates = []businessWriteGate{reopened.CoordinateBusinessWrite}
	revert := propose(b.Fields["name"])
	before = revision()
	finished := call("model.commit", request(revert.RequestID)).(pluginmodel.State)
	if finished.Bindings["assets"].Fields["name"] != b.Fields["name"] || revision() != before+1 {
		t.Fatal("next repair failed to advance a fresh revision")
	}
}

func TestPluginModelRepairWaiterSignalsVerifiedReceiptReplay(t *testing.T) {
	pb, workspace, input, domain, first, field := existingModelMapping(t)
	commit := func(owner *schemaFieldChangeDomain, id string) (any, error) {
		return modelCall(t, pb, workspace, input, owner, "model.commit", map[string]string{"requestId": id})
	}
	if _, err := commit(domain, first.RequestID); err != nil {
		t.Fatal(err)
	}
	alt := createSchemaProductField(t, pb, field.TableID, v2.LogicalText, "替代", "repair-replay-alt")
	raw, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "repair", Mappings: map[string]pluginmodel.Binding{"items": {TableID: field.TableID, Fields: map[string]string{"name": alt.FieldID}}}})
	if err != nil {
		t.Fatal(err)
	}
	id := raw.(modelPreview).RequestID
	if err := writecoordinator.EnsurePocketBaseReceiptTable(t.Context(), pb); err != nil {
		t.Fatal(err)
	}
	intent := writecoordinator.WriteIntent{Token: writecoordinator.Token{WorkspaceID: workspace, SessionEpoch: 1, FenceEpoch: 1, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, MutationRevision: 1, AuditSourceEpoch: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	winner := &schemaFieldChangeDomain{gates: []businessWriteGate{func(ctx context.Context, kind, id string, write func(context.Context) error) error {
		bound, err := writecoordinator.WithBusinessIntent(ctx, intent, kind, id)
		if err != nil {
			return err
		}
		return write(bound)
	}}}
	replayed := false
	domain.gates = []businessWriteGate{func(ctx context.Context, kind, id string, write func(context.Context) error) error {
		// The original caller loaded its pending preview before admission. Complete the winner first.
		if _, err := commit(winner, id); err != nil {
			return err
		}
		next := intent
		next.MutationRevision++
		bound, err := writecoordinator.WithBusinessIntent(ctx, next, kind, id)
		if err != nil {
			return err
		}
		err = write(bound)
		if err == writecoordinator.ErrBusinessReplay {
			replayed = true
			return nil
		}
		return err
	}}
	result, err := commit(domain, id)
	if err != nil || !replayed || result.(pluginmodel.State).RequestID != id {
		t.Fatalf("replay=%v result=%+v err=%v", replayed, result, err)
	}
	var count int
	if err := pb.DB().NewQuery("SELECT count(*) FROM workspace_v2_mutation_receipts").Row(&count); err != nil || count != 1 {
		t.Fatalf("receipts=%d err=%v", count, err)
	}
}

func TestPluginModelRepairRegisteredDependencies(t *testing.T) {
	f := newTransactionFixture(t, "integerUnits")
	domain := &schemaFieldChangeDomain{}
	before, err := transactionrules.Load(context.Background(), f.pb)
	if err != nil || len(before) != 1 {
		t.Fatalf("registrations: %v %v", before, err)
	}
	propose := func(key, id string) modelPreview {
		t.Helper()
		next := cloneModelState(f.model)
		next.Bindings["headers"].Fields[key] = id
		result, err := modelCall(t, f.pb, f.workspace, f.input, domain, "model.preview", modelPreviewRequest{Mode: "map", Mappings: next.Bindings})
		if err != nil {
			t.Fatal(err)
		}
		return result.(modelPreview)
	}
	table := f.model.Bindings["headers"].TableID
	code := createSchemaProductField(t, f.pb, table, v2.LogicalText, "替代编号", "model-rule-alternative")
	if p := propose("code", code.FieldID); p.Status != "blocked" || !slices.ContainsFunc(p.Diagnostics, func(d v2.Diagnostic) bool { return d.Code == "plugin_model_rule_protected" && d.Path == "headers.code" }) {
		t.Fatalf("protected: %+v", p)
	}
	quantity := createSchemaProductField(t, f.pb, table, v2.LogicalNumber, "替代数量", "model-rule-unrelated")
	p := propose("quantity", quantity.FieldID)
	if p.Status != "pending" || len(p.Changes) != 1 {
		t.Fatalf("independent field: %+v", p)
	}
	if _, err := modelCall(t, f.pb, f.workspace, f.input, domain, "model.commit", map[string]string{"requestId": p.RequestID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call("transaction.configure", map[string]any{"declaration": f.declaration}); err != nil {
		t.Fatal(err)
	}
	after, err := transactionrules.Load(context.Background(), f.pb)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("repair modified immutable rule registration")
	}
}
