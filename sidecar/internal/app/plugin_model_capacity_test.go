package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

func TestNativePackageAdditiveCapacityUpgrade(t *testing.T) {
	ctx := context.Background()
	pb, workspace, input, domain := modelFixture(t)
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
		if version == 2 {
			model.Entities[0].Fields = append(model.Entities[0].Fields, pluginmodel.Field{Key: "note", Label: "新说明", Type: v2.LogicalText})
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
			permissions["model"] = tooMany
			reject()
			permissions["model"] = model
			invalid := declaration
			rule := declaration.Rules[0]
			rule.ID = "extra"
			invalid.Rules = append(append([]transactionrules.Rule{}, declaration.Rules...), rule)
			permissions["transactions"] = invalid
			reject()
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
		if version == 2 && len(planState.Steps) != 33 {
			t.Fatalf("upgrade steps %d want only 16 new tables + 17 fields", len(planState.Steps))
		}
		done, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": planState.RequestID})
		if err != nil {
			t.Fatal(err)
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
	next := configure(2, 32)
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
	table, err = schemaexecution.Describe(ctx, pb, b.TableID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ = table.Field(b.Fields["code"])
	rawField, _ := table.Field(b.Fields["raw"])
	rows, err := pb.FindAllRecords(table.PhysicalName)
	if err != nil || len(rows) != 1 {
		t.Fatalf("old row lost: %d %v", len(rows), err)
	}
	values := mutation.ProductRow(pb, table, rows[0])
	if code.DisplayName != "自定义代码" || values[rawField.Identity.PhysicalName] != nil || values[ext.Definition.Identity.PhysicalName] != "keep" {
		t.Fatalf("upgrade corrupted legacy values: %v %s", values, code.DisplayName)
	}
	registered, err := transactionrules.Load(ctx, pb)
	if err != nil || len(registered) != 2 || len(registered[0].Declaration.Rules) != 64 || len(registered[1].Declaration.Rules) != 128 {
		t.Fatalf("registration history: %d %v", len(registered), err)
	}
	req.SchemaRevision = table.Snapshot.SchemaRevision
	req.RequestID, req.IdempotencyKey = "duplicate", "duplicate"
	if _, err := f.kernel.Apply(ctx, req); err == nil || !strings.Contains(err.Error(), "unique_violation") {
		t.Fatalf("lost old rule: %v", err)
	}
}
