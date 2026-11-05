package app

import (
	"context"
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
