package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestNativePackageAdditiveCapacityUpgrade(t *testing.T) {
	ctx := context.Background()
	runtime, _, _, _, _, pb := historyRestoreProductFixture(t)
	_, workspace, input, domain := modelFixtureOn(t, pb)
	store := pluginstore.New(pb, workspace)
	raw, err := store.GetInstallation(ctx, input.ProjectKey, input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Manifest map[string]any `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	manifest := original.Manifest
	input.PluginID = "com.example.model-capacity"
	input.InstallationID = ""
	manifest["pluginId"] = input.PluginID
	model := pluginmodel.Declaration{Contract: pluginmodel.Contract, ModelID: "capacity"}
	declaration := transactionrules.Declaration{Contract: transactionrules.Contract, ModelID: model.ModelID}
	f := &transactionFixture{pb: pb, workspace: workspace, domain: domain, kernel: mutation.New(pb, mutation.MetadataSchemaSource{}), epoch: 1}
	encode := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	configure := func(version, size int) pluginmodel.State {
		t.Helper()
		model.Version, declaration.Version = version, version
		for i := len(model.Entities); i < size; i++ {
			key := fmt.Sprintf("entity_%d", i)
			fields := []pluginmodel.Field{{Key: "code", Label: "代码", Type: v2.LogicalText}}
			if i == 0 {
				fields = append(fields, pluginmodel.Field{Key: "raw", Label: "原始数量", Type: v2.LogicalNumber, Precision: "integer"})
			}
			model.Entities = append(model.Entities, pluginmodel.Entity{Key: key, Label: key, Operations: []string{"read", "query", "create", "update"}, Fields: fields})
			for j := 0; j < 4; j++ {
				declaration.Rules = append(declaration.Rules, transactionrules.Rule{ID: fmt.Sprintf("u_%d_%d", i, j), Kind: "unique", Entity: key, Fields: []string{"code"}})
			}
		}
		if version == 3 {
			// The 40-entity model carries exactly the 192-rule declaration ceiling.
			for j := 0; j < 32; j++ {
				declaration.Rules = append(declaration.Rules, transactionrules.Rule{ID: fmt.Sprintf("x_%d", j), Kind: "unique", Entity: "entity_0", Fields: []string{"code"}})
			}
		}
		if version == 2 {
			model.Entities[0].Fields = append(model.Entities[0].Fields, pluginmodel.Field{Key: "note", Label: "新说明", Type: v2.LogicalText}, pluginmodel.Field{Key: "owner", Label: "归属", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "entity_16", Field: "members", Cardinality: "one"}})
			model.Entities[16].Fields = append(model.Entities[16].Fields, pluginmodel.Field{Key: "members", Label: "成员", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "entity_0", Field: "owner", Cardinality: "many"}})
		}
		manifest["version"] = fmt.Sprintf("%d.0.0", version)
		manifest["permissions"] = map[string]any{"model": model, "transactions": declaration}
		input.PackageHash = strings.Repeat(fmt.Sprint(version), 64)
		packageHash := "sha256:" + input.PackageHash
		plan := map[string]any{"planId": fmt.Sprint(version), "projectRevision": "synthetic", "projectKey": input.ProjectKey, "sourceType": "package", "sourceLocation": "host-managed", "packageHash": packageHash, "manifest": manifest, "schemas": map[string]any{}}
		revision := map[string]any{"projectKey": input.ProjectKey, "pluginId": input.PluginID, "version": manifest["version"], "packageHash": packageHash, "localPath": "synthetic.vtplugin", "manifest": manifest, "state": "current"}
		var snapshot json.RawMessage
		var err error
		if version == 1 {
			if _, err = store.CommitInstall(ctx, encode(plan), encode(revision)); err != nil {
				t.Fatal(err)
			}
			snapshot, err = store.SetEnabled(ctx, input.ProjectKey, input.PluginID, true)
		} else {
			plan["targetInstallationId"], plan["targetRevision"] = input.InstallationID, input.InstallationRevision
			permissions := manifest["permissions"].(map[string]any)
			reject := func() {
				t.Helper()
				if _, err := store.CommitUpgrade(ctx, encode(plan), encode(revision)); err == nil {
					t.Fatal("over-budget/incompatible upgrade accepted")
				}
			}
			tooMany := model
			extra := model.Entities[0]
			extra.Key, extra.Label = "extra", "Extra"
			tooMany.Entities = append(append([]pluginmodel.Entity{}, model.Entities...), extra)
			if len(tooMany.Entities) > 40 {
				permissions["model"] = tooMany
				reject()
			}
			permissions["model"] = model
			invalid := declaration
			rule := declaration.Rules[0]
			rule.ID = "extra"
			invalid.Rules = append(append([]transactionrules.Rule{}, declaration.Rules...), rule)
			if len(invalid.Rules) > transactionrules.MaxRules {
				permissions["transactions"] = invalid
				reject()
			}
			invalid.Rules = []transactionrules.Rule{{ID: "path", Kind: "sameTarget", Entity: "entity_0", PathMatch: transactionrules.PathMatch{Left: []transactionrules.RelationStep{{Field: "code", Target: "entity_1"}}, Right: []transactionrules.RelationStep{{Field: "code", Target: "entity_1"}}}}}
			permissions["transactions"] = invalid
			reject()
			permissions["transactions"] = declaration
			snapshot, err = store.CommitUpgrade(ctx, encode(plan), encode(revision))
		}
		if err != nil {
			t.Fatal(err)
		}
		var installed struct {
			ID       string `json:"installationId"`
			Revision int64  `json:"revision"`
		}
		if err := json.Unmarshal(snapshot, &installed); err != nil {
			t.Fatal(err)
		}
		if version > 1 && input.InstallationID != installed.ID {
			t.Fatal("upgrade replaced installation")
		}
		input.InstallationID, input.InstallationRevision = installed.ID, installed.Revision
		f.input = input
		candidate, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
		if err != nil {
			t.Fatal(err)
		}
		planState := candidate.(modelPreview)
		if len(planState.Diagnostics) != 0 {
			t.Fatal(planState.Diagnostics)
		}
		if version == 2 && len(planState.Steps) != 34 {
			t.Fatalf("upgrade steps %d want only 16 new tables + 17 scalar fields + one reciprocal pair", len(planState.Steps))
		}
		if version == 2 {
			domain.gates = []businessWriteGate{runtime.CoordinateBusinessWrite}
			before, err := schemaapi.New(pb).List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			progress, err := pluginmodel.States(ctx, pb, input.ProjectKey, input.PluginID)
			if err != nil {
				t.Fatal(err)
			}
			creates := 0
			hook := pb.OnRecordCreate("vibetable_tables").BindFunc(func(event *core.RecordEvent) error {
				creates++
				if creates == 2 {
					return errors.New("second table creation interrupted")
				}
				return event.Next()
			})
			_, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": planState.RequestID})
			pb.OnRecordCreate("vibetable_tables").Unbind(hook)
			if proof, proofErr := writecoordinator.HasPocketBaseReceiptIdentity(ctx, pb, workspace, "plugin.model.upgrade", planState.RequestID); proof || proofErr != nil {
				t.Fatalf("receipt survived failed upgrade: %v %v", proof, proofErr)
			}
			if err == nil || creates != 2 {
				t.Fatalf("upgrade interruption did not reach the second physical table: %d %v", creates, err)
			}
			after, err := schemaapi.New(pb).List(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed upgrade changed public catalog: %d -> %d: %v", len(before), len(after), err)
			}
			afterProgress, err := pluginmodel.States(ctx, pb, input.ProjectKey, input.PluginID)
			if err != nil || !reflect.DeepEqual(progress, afterProgress) {
				t.Fatalf("failed upgrade changed durable bindings: %v", err)
			}
		}
		done, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": planState.RequestID})
		if err != nil {
			t.Fatal(err)
		}
		if version == 2 {
			if proof, proofErr := writecoordinator.HasPocketBaseReceiptIdentity(ctx, pb, workspace, "plugin.model.upgrade", planState.RequestID); !proof || proofErr != nil {
				t.Fatalf("upgrade lacks durable receipt: %v %v", proof, proofErr)
			}
		}
		if _, err := f.call("transaction.configure", map[string]any{"declaration": declaration}); err != nil {
			t.Fatal(err)
		}
		return done.(pluginmodel.State)
	}
	old := configure(1, 16)
	b := old.Bindings["entity_0"]
	ext := createSchemaProductField(t, pb, b.TableID, v2.LogicalText, "用户扩展", "capacity-extension")
	table, err := schemaexecution.Describe(ctx, pb, b.TableID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := table.Field(b.Fields["code"])
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	draft := v2.FieldDraft{DisplayName: "自定义代码", Help: code.Help, LogicalType: code.LogicalType, Value: code.Value, Constraints: code.Constraints, Storage: code.Storage, Display: code.Display}
	rename, err := domain.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: b.TableID, FieldID: b.Fields["code"], ExpectedSchemaRev: table.Snapshot.SchemaRevision, Draft: &draft, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.Apply(ctx, v2.ApplyRequest{PlanID: rename.PlanID, PlanHash: rename.PlanHash, OperationID: "capacity-rename", Actor: actor, Confirmations: []string{}}); err != nil {
		t.Fatal(err)
	}
	table, err = schemaexecution.Describe(ctx, pb, b.TableID)
	if err != nil {
		t.Fatal(err)
	}
	req := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "seed", IdempotencyKey: "seed", TableID: b.TableID, SchemaRevision: table.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{b.Fields["code"]: "unique", ext.FieldID: "keep"}}}}
	if _, err := f.kernel.Apply(ctx, req); err != nil {
		t.Fatal(err)
	}
	legacyPlan := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "capacity", Operations: []transactionOperation{{Entity: "entity_0", Kind: "create", TempID: "lost", Values: map[string]any{"code": "lost-ack"}}}}
	applied := f.submit(t, legacyPlan)
	legacyPlan.Operations[0].Values["code"] = "pending"
	pending, err := f.call("transaction.preview", map[string]any{"plan": legacyPlan})
	if err != nil {
		t.Fatal(err)
	}
	next := configure(2, 32)
	final := configure(3, 40)
	recovered, err := f.call("transaction.status", map[string]string{"requestId": applied.RequestID})
	if err != nil || !reflect.DeepEqual(applied, recovered) {
		t.Fatalf("legacy applied receipt: %v %v", recovered, err)
	}
	if _, err := f.call("transaction.commit", map[string]any{"requestId": pending.(transactionPreview).RequestID}); err == nil || !strings.Contains(err.Error(), "transaction.plan_stale") {
		t.Fatalf("legacy prepared request: %v", err)
	}
	for entity, binding := range old.Bindings {
		newBinding := next.Bindings[entity]
		if binding.TableID != newBinding.TableID {
			t.Fatal("table replaced", entity)
		}
		for key, id := range binding.Fields {
			if newBinding.Fields[key] != id {
				t.Fatal("field replaced", entity, key)
			}
		}
	}
	for entity, binding := range next.Bindings {
		finalBinding := final.Bindings[entity]
		if binding.TableID != finalBinding.TableID {
			t.Fatal("table replaced at 40 entities", entity)
		}
		for key, id := range binding.Fields {
			if finalBinding.Fields[key] != id {
				t.Fatal("field replaced at 40 entities", entity, key)
			}
		}
	}
	table, err = schemaexecution.Describe(ctx, pb, b.TableID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ = table.Field(b.Fields["code"])
	rawField, _ := table.Field(b.Fields["raw"])
	row, err := pb.FindFirstRecordByData(table.PhysicalName, code.Identity.PhysicalName, "unique")
	if err != nil {
		t.Fatal(err)
	}
	values := mutation.ProductRow(pb, table, row)
	if code.DisplayName != "自定义代码" || values[rawField.Identity.PhysicalName] != nil || values[ext.Definition.Identity.PhysicalName] != "keep" {
		t.Fatalf("upgrade corrupted legacy values: %v %s", values, code.DisplayName)
	}
	registered, err := transactionrules.Load(ctx, pb)
	if err != nil || len(registered) != 3 || len(registered[0].Declaration.Rules) != 64 || len(registered[1].Declaration.Rules) != 128 || len(registered[2].Declaration.Rules) != 192 {
		t.Fatalf("registration history: %d %v", len(registered), err)
	}
	req.SchemaRevision = table.Snapshot.SchemaRevision
	req.RequestID, req.IdempotencyKey = "duplicate", "duplicate"
	if _, err := f.kernel.Apply(ctx, req); err == nil || !strings.Contains(err.Error(), "unique_violation") {
		t.Fatalf("lost old rule: %v", err)
	}
	f.input = changeInstalledModel(t, pb, workspace, input, func(m *pluginmodel.Declaration) {
		m.Version++
		m.Entities[0].Fields = m.Entities[0].Fields[1:] // The field used by the old business request is no longer granted.
	})
	if _, err := f.call("transaction.status", map[string]string{"requestId": applied.RequestID}); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
		t.Fatalf("revoked field exposed old receipt: %v", err)
	}
}

// A blank workspace installs the full 40-entity/192-rule ceiling in one package
// and registers the complete declaration; over-capacity packages fail install.
func TestNativePackageFreshInstallAtCapacityCeiling(t *testing.T) {
	ctx := context.Background()
	_, _, _, _, _, pb := historyRestoreProductFixture(t)
	_, workspace, input, domain := modelFixtureOn(t, pb)
	store := pluginstore.New(pb, workspace)
	raw, err := store.GetInstallation(ctx, input.ProjectKey, input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Manifest map[string]any `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	input.PluginID = "com.example.model-ceiling"
	input.InstallationID = ""
	manifest := original.Manifest
	manifest["pluginId"] = input.PluginID
	model := pluginmodel.Declaration{Contract: pluginmodel.Contract, ModelID: "ceiling", Version: 1}
	declaration := transactionrules.Declaration{Contract: transactionrules.Contract, ModelID: model.ModelID, Version: 1}
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("entity_%d", i)
		model.Entities = append(model.Entities, pluginmodel.Entity{Key: key, Label: key, Operations: []string{"read", "query", "create", "update"}, Fields: []pluginmodel.Field{{Key: "code", Label: "代码", Type: v2.LogicalText}}})
		for j := 0; j < 4; j++ {
			declaration.Rules = append(declaration.Rules, transactionrules.Rule{ID: fmt.Sprintf("u_%d_%d", i, j), Kind: "unique", Entity: key, Fields: []string{"code"}})
		}
	}
	for j := 0; j < 32; j++ {
		declaration.Rules = append(declaration.Rules, transactionrules.Rule{ID: fmt.Sprintf("x_%d", j), Kind: "unique", Entity: "entity_0", Fields: []string{"code"}})
	}
	if len(model.Entities) != 40 || len(declaration.Rules) != 192 {
		t.Fatalf("fixture shape: %d entities %d rules", len(model.Entities), len(declaration.Rules))
	}
	manifest["permissions"] = map[string]any{"model": model, "transactions": declaration}
	input.PackageHash = strings.Repeat("9", 64)
	packageHash := "sha256:" + input.PackageHash
	plan := map[string]any{"planId": "ceiling", "projectRevision": "synthetic", "projectKey": input.ProjectKey, "sourceType": "package", "sourceLocation": "host-managed", "packageHash": packageHash, "manifest": manifest, "schemas": map[string]any{}}
	revision := map[string]any{"projectKey": input.ProjectKey, "pluginId": input.PluginID, "version": manifest["version"], "packageHash": packageHash, "localPath": "synthetic.vtplugin", "manifest": manifest, "state": "current"}
	encode := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	over := manifest
	oversized := model
	extra := model.Entities[0]
	extra.Key, extra.Label = "extra", "Extra"
	oversized.Entities = append(append([]pluginmodel.Entity{}, model.Entities...), extra)
	over["permissions"] = map[string]any{"model": oversized, "transactions": declaration}
	if _, err := store.CommitInstall(ctx, encode(over), encode(revision)); err == nil {
		t.Fatal("41-entity package installed")
	}
	overRules := declaration
	overRules.Rules = append(append([]transactionrules.Rule{}, declaration.Rules...), transactionrules.Rule{ID: "over", Kind: "unique", Entity: "entity_0", Fields: []string{"code"}})
	over["permissions"] = map[string]any{"model": model, "transactions": overRules}
	if _, err := store.CommitInstall(ctx, encode(over), encode(revision)); err == nil {
		t.Fatal("193-rule package installed")
	}
	manifest["permissions"] = map[string]any{"model": model, "transactions": declaration}
	if _, err := store.CommitInstall(ctx, encode(plan), encode(revision)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetEnabled(ctx, input.ProjectKey, input.PluginID, true); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetInstallation(ctx, input.ProjectKey, input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var installed struct {
		ID       string `json:"installationId"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal(current, &installed); err != nil {
		t.Fatal(err)
	}
	input.InstallationID, input.InstallationRevision = installed.ID, installed.Revision
	f := &transactionFixture{pb: pb, workspace: workspace, input: input, domain: domain, kernel: mutation.New(pb, mutation.MetadataSchemaSource{}), epoch: 1}
	candidate, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if state := candidate.(modelPreview); len(state.Diagnostics) != 0 {
		t.Fatal(state.Diagnostics)
	}
	if _, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": candidate.(modelPreview).RequestID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call("transaction.configure", map[string]any{"declaration": declaration}); err != nil {
		t.Fatal(err)
	}
	registered, err := transactionrules.Load(ctx, pb)
	if err != nil || len(registered) != 1 || len(registered[0].Declaration.Rules) != 192 {
		t.Fatalf("fresh ceiling registration: %d %v", len(registered), err)
	}
}
