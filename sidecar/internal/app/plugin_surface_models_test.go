package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func modelFixture(t *testing.T) (*pocketbase.PocketBase, string, pluginSurfaceAPIRequest, *schemaFieldChangeDomain) {
	t.Helper()
	pb := schemaProductStore(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	model := pluginmodel.Declaration{Contract: pluginmodel.Contract, ModelID: "synthetic", Version: 1, Entities: []pluginmodel.Entity{
		{Key: "people", Label: "合成人员", Operations: []string{"read", "query", "create", "update"}, Fields: []pluginmodel.Field{{Key: "name", Label: "姓名", Type: v2.LogicalText}, {Key: "tickets", Label: "工单", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "tickets", Field: "person", Cardinality: "many"}}}},
		{Key: "tickets", Label: "合成工单", Operations: []string{"read", "query", "create"}, Fields: []pluginmodel.Field{{Key: "title", Label: "主题", Type: v2.LogicalText}, {Key: "person", Label: "人员", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "people", Field: "tickets", Cardinality: "one"}}}},
		{Key: "assets", Label: "合成资产", Operations: []string{"read", "query"}, Fields: []pluginmodel.Field{{Key: "name", Label: "名称", Type: v2.LogicalText}, {Key: "quantity", Label: "数量", Type: v2.LogicalNumber, Precision: "integer"}}}}}
	store := pluginstore.New(pb, workspace)
	plugin := "com.example.model"
	manifest := map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": plugin, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{}, "compatibility": map[string]any{"pluginApi": "3.x"}, "permissions": map[string]any{"model": model}, "actions": []any{map[string]any{"actionId": "model", "risk": "write", "mode": "local", "invocation": "manual", "presentation": "application", "entry": "index.html"}}, "ui": map[string]any{}}
	raw, _ := json.Marshal(map[string]any{"projectKey": store.ProjectKey(), "pluginId": plugin, "version": "1.0.0", "packageHash": "sha256:" + strings.Repeat("d", 64), "sourceType": "package", "sourceLocation": "host-managed", "manifest": manifest, "schemas": map[string]any{}, "status": "enabled", "revision": 1, "sourceChanged": false, "blockingReasons": []any{}})
	if _, err := store.SaveInstallation(context.Background(), raw, nil); err != nil {
		t.Fatal(err)
	}
	input := pluginSurfaceAPIRequest{InstallationID: installationRecordID(t, store, plugin), InstallationRevision: 1, ProjectKey: store.ProjectKey(), PluginID: plugin, PackageHash: strings.Repeat("d", 64), ActionID: "model", Entry: "index.html", RequestID: "host-request", SessionEpoch: 1}
	catalog := fieldchange.NewCatalog(pb)
	plans := fieldchange.NewPocketBasePlanStore(pb)
	planner := fieldchange.NewPlanner(catalog, catalog, plans, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(pb, plans)
	schema, err := schemacore.New(catalog, planner, executor)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	return pb, workspace, input, &schemaFieldChangeDomain{core: schema, tables: tables, catalog: catalog}
}
func modelCall(t *testing.T, pb core.App, workspace string, input pluginSurfaceAPIRequest, domain *schemaFieldChangeDomain, method string, request any) (any, error) {
	t.Helper()
	input.Method = method
	input.Request, _ = json.Marshal(request)
	return executeSurfaceModel(context.Background(), pb, workspace, input, domain)
}

func TestPluginModelCreateIdentityGrantsAndNativeProtection(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	if len(preview.Diagnostics) > 0 {
		t.Fatalf("preview: %+v", preview.Diagnostics)
	}
	tables, err := schemaapi.New(pb).List(context.Background())
	if err != nil || len(tables) != 0 {
		t.Fatalf("preview wrote schema: %d %v", len(tables), err)
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	state := result.(pluginmodel.State)
	if state.Status != "completed" || state.Completed != state.Total {
		t.Fatalf("state: %+v", state)
	}
	people := state.Bindings["people"]
	installed, err := surfaceInstallation(context.Background(), pb, workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveModelGrants(context.Background(), pb, input.ProjectKey, input.PluginID, &installed.pluginInstallation); err != nil {
		t.Fatal(err)
	}
	grant := installed.grant(people.TableID, "")
	if grant == nil || pluginHas(grant.Fields, "*") || len(grant.Fields) != 3 {
		t.Fatalf("grant: %+v", grant)
	}
	userField := createSchemaProductField(t, pb, people.TableID, v2.LogicalText, "用户备注", "model-user-extension")
	if pluginHas(grant.Fields, userField.FieldID) {
		t.Fatal("user extension implicitly authorized")
	}
	snapshot, err := schemaexecution.Describe(context.Background(), pb, people.TableID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := domain.catalog.Field(context.Background(), people.TableID, people.Fields["name"])
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{DisplayName: "用户重命名", Help: before.Help, LogicalType: before.LogicalType, Value: before.Value, Constraints: before.Constraints, Storage: before.Storage, Display: before.Display}
	rename, err := domain.Plan(context.Background(), v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: people.TableID, FieldID: people.Fields["name"], ExpectedSchemaRev: snapshot.Snapshot.SchemaRevision, Draft: &draft, Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = domain.Apply(context.Background(), v2.ApplyRequest{PlanID: rename.PlanID, PlanHash: rename.PlanHash, OperationID: "model-rename", Actor: rename.Intent.Actor, Confirmations: []string{}}); err != nil {
		t.Fatal(err)
	}
	installed, err = surfaceInstallation(context.Background(), pb, workspace, input)
	if err != nil || installed.grant(people.TableID, "") == nil {
		t.Fatalf("rename broke logical binding: %v", err)
	}
	snapshot, err = schemaexecution.Describe(context.Background(), pb, people.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schemaapi.New(pb).DeleteTable(context.Background(), people.TableID, mustModelRevision(t, snapshot.Snapshot.SchemaRevision)); err == nil {
		t.Fatal("bound table deleted")
	}
	if _, err := domain.Plan(context.Background(), v2.FieldChangeIntent{Action: v2.ActionRetire, TableID: people.TableID, FieldID: people.Fields["name"], ExpectedSchemaRev: snapshot.Snapshot.SchemaRevision, Actor: v2.Actor{ID: "local-user", Kind: "user"}}); err == nil {
		t.Fatal("core field retired")
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if result.(modelPreview).Bindings["people"].TableID != people.TableID {
		t.Fatal("repeat changed identity")
	}
	foreign := input
	foreign.PluginID = "com.example.foreign"
	if _, err = modelCall(t, pb, workspace, foreign, domain, "model.read", map[string]any{}); err == nil {
		t.Fatal("foreign plugin read binding")
	}
	if _, err = modelCall(t, pb, "22222222-2222-4222-8222-222222222222", input, domain, "model.read", map[string]any{}); err == nil {
		t.Fatal("foreign workspace read binding")
	}
	if _, err := pluginstore.New(pb, workspace).SetEnabled(context.Background(), input.ProjectKey, input.PluginID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = modelCall(t, pb, workspace, input, domain, "model.read", map[string]any{}); err == nil {
		t.Fatal("disabled handle remained live")
	}
	if err := pluginmodel.ProtectTable(context.Background(), pb, people.TableID); err == nil {
		t.Fatal("disable removed model protection")
	}
}
func mustModelRevision(t *testing.T, revision string) int64 {
	t.Helper()
	n, err := v2.ParseSchemaRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPluginModelMappingDiagnosticsAndStalePreview(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "map"})
	if err != nil {
		t.Fatal(err)
	}
	if result.(modelPreview).Status != "blocked" || len(result.(modelPreview).Diagnostics) < 3 {
		t.Fatal("missing mappings accepted")
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	input.InstallationRevision++
	if _, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID}); err == nil {
		t.Fatal("stale installation accepted")
	}
	tables, _ := schemaapi.New(pb).List(context.Background())
	if len(tables) != 0 {
		t.Fatal("stale preview wrote tables")
	}
}

func TestPluginModelPartialResumeUsesOriginalIdentities(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	failOnce := true
	domain.gates = []businessWriteGate{func(ctx context.Context, kind, id string, write func(context.Context) error) error {
		if kind == "field.change.apply" && failOnce {
			failOnce = false
			return errors.New("synthetic interrupted field commit")
		}
		return write(ctx)
	}}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err == nil {
		t.Fatal("interruption was swallowed")
	}
	partial := result.(pluginmodel.State)
	if partial.Status != "partial" || partial.Completed != 3 {
		t.Fatalf("inaccurate partial: %+v", partial)
	}
	installed, err := surfaceInstallation(context.Background(), pb, workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed.ModelGrants) != 0 {
		t.Fatal("partial model granted data access")
	}
	observed, err := modelCall(t, pb, workspace, input, domain, "model.status", map[string]any{"requestId": preview.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	if observed.(pluginmodel.State).Completed != 3 {
		t.Fatal("durable status lost completed range")
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if result.(modelPreview).RequestID != preview.RequestID {
		t.Fatal("resume fabricated another plan")
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	complete := result.(pluginmodel.State)
	for key, b := range partial.Bindings {
		if complete.Bindings[key].TableID != b.TableID {
			t.Fatal("resume replaced table")
		}
	}
	tables, _ := schemaapi.New(pb).List(context.Background())
	if len(tables) != 3 || complete.Status != "completed" {
		t.Fatal("resume fabricated blank tables")
	}
}

func changeInstalledModel(t *testing.T, pb core.App, workspace string, input pluginSurfaceAPIRequest, change func(*pluginmodel.Declaration)) pluginSurfaceAPIRequest {
	t.Helper()
	store := pluginstore.New(pb, workspace)
	raw, err := store.GetInstallation(context.Background(), input.ProjectKey, input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	var manifest map[string]json.RawMessage
	_ = json.Unmarshal(object["manifest"], &manifest)
	var permissions map[string]json.RawMessage
	_ = json.Unmarshal(manifest["permissions"], &permissions)
	var model pluginmodel.Declaration
	_ = json.Unmarshal(permissions["model"], &model)
	change(&model)
	permissions["model"], _ = json.Marshal(model)
	manifest["permissions"], _ = json.Marshal(permissions)
	object["manifest"], _ = json.Marshal(manifest)
	previous := input.InstallationRevision
	input.InstallationRevision++
	object["revision"], _ = json.Marshal(input.InstallationRevision)
	raw, _ = json.Marshal(object)
	if _, err := store.SaveInstallation(context.Background(), raw, &previous); err != nil {
		t.Fatal(err)
	}
	return input
}

func TestPluginModelUpgradePreservesValuesAndRejectsPrecisionLoss(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": result.(modelPreview).RequestID})
	if err != nil {
		t.Fatal(err)
	}
	initial := result.(pluginmodel.State)
	assets := initial.Bindings["assets"]
	extra := createSchemaProductField(t, pb, assets.TableID, v2.LogicalText, "用户扩展", "model-user-value")
	schema, err := schemaexecution.Describe(context.Background(), pb, assets.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(schema.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Set(extra.Definition.Identity.PhysicalName, "必须保留原值")
	if err = pb.Save(row); err != nil {
		t.Fatal(err)
	}
	input = changeInstalledModel(t, pb, workspace, input, func(m *pluginmodel.Declaration) { m.Version = 2; m.Entities[2].Fields[1].Precision = "float64" })
	result, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := result.(modelPreview)
	if blocked.Status != "blocked" || len(blocked.Diagnostics) != 1 || blocked.Diagnostics[0].Code != "plugin_model_precision_loss" || blocked.Diagnostics[0].Path != "assets.quantity" {
		t.Fatalf("precision diagnostic: %+v", blocked.Diagnostics)
	}
	input = changeInstalledModel(t, pb, workspace, input, func(m *pluginmodel.Declaration) {
		m.Version = 3
		m.Entities[2].Fields[1].Precision = "integer"
		m.Entities[2].Fields = append(m.Entities[2].Fields, pluginmodel.Field{Key: "checked", Label: "已核对", Type: v2.LogicalBool})
	})
	installed, err := surfaceInstallation(context.Background(), pb, workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed.ModelGrants) != 0 {
		t.Fatal("upgrade implicitly granted fields")
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	upgrade := result.(modelPreview)
	if len(upgrade.Diagnostics) > 0 || len(upgrade.Steps) != 1 {
		t.Fatalf("upgrade: %+v", upgrade)
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": upgrade.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	done := result.(pluginmodel.State)
	if done.Bindings["assets"].Fields["quantity"] != assets.Fields["quantity"] || done.Bindings["assets"].Fields["checked"] == "" {
		t.Fatal("upgrade lost canonical identity")
	}
	saved, err := pb.FindRecordById(collection.Id, row.Id)
	if err != nil || saved.GetString(extra.Definition.Identity.PhysicalName) != "必须保留原值" {
		t.Fatal("upgrade overwrote user extension value")
	}
	installed, err = surfaceInstallation(context.Background(), pb, workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	grant := installed.grant(assets.TableID, "")
	if grant == nil || pluginHas(grant.Fields, extra.FieldID) {
		t.Fatal("upgrade granted extension field")
	}
}
