package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func existingModelMapping(t *testing.T) (*pocketbase.PocketBase, string, pluginSurfaceAPIRequest, *schemaFieldChangeDomain, modelPreview, v2.ApplyReceipt) {
	t.Helper()
	pb, workspace, input, domain := modelFixture(t)
	input = changeInstalledModel(t, pb, workspace, input, func(m *pluginmodel.Declaration) {
		m.Version++
		m.Entities = []pluginmodel.Entity{{Key: "items", Label: "逻辑项目", Operations: []string{"read", "query"}, Fields: []pluginmodel.Field{{Key: "name", Label: "逻辑名称", Type: v2.LogicalText}}}}
	})
	table, err := domain.CreateTable(context.Background(), v2.TableCreateIntent{DisplayName: "已有异名表", OperationID: "existing-model-map", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	field := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "已有异名字段", "existing-model-field")
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "map", Mappings: map[string]pluginmodel.Binding{"items": {TableID: table.TableID, Fields: map[string]string{"name": field.FieldID}}}})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	if len(preview.Diagnostics) != 0 || preview.Total != 0 {
		t.Fatalf("mapping preview: %+v", preview)
	}
	return pb, workspace, input, domain, preview, field
}

func requireModelError(t *testing.T, err error, code string) {
	t.Helper()
	var product *schemaerror.ProductError
	var fieldError *fieldchange.ProductError
	if !(errors.As(err, &product) && product.Code == code) && !(errors.As(err, &fieldError) && fieldError.Code == code) {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestPluginModelMappingFencesCopyingMigrationAndSwitchRechecksBinding(t *testing.T) {
	for _, atComplete := range []bool{false, true} {
		name := "accept"
		if atComplete {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			pb, workspace, input, domain, preview, field := existingModelMapping(t)
			schema, err := schemaexecution.Describe(context.Background(), pb, field.TableID)
			if err != nil {
				t.Fatal(err)
			}
			collection, err := pb.FindCollectionByNameOrId(schema.PhysicalName)
			if err != nil {
				t.Fatal(err)
			}
			row := core.NewRecord(collection)
			row.Set(field.Definition.Identity.PhysicalName, "12")
			row.Set(field.Definition.Value.Presence.PhysicalName, true)
			if err := pb.Save(row); err != nil {
				t.Fatal(err)
			}
			defaults, err := v2.RecommendedDefaults(v2.LogicalNumber)
			if err != nil {
				t.Fatal(err)
			}
			conversion, err := domain.Plan(context.Background(), v2.FieldChangeIntent{Action: v2.ActionConvert, TableID: field.TableID, FieldID: field.FieldID, ExpectedSchemaRev: field.SchemaRevision, ConversionRule: "block", Actor: v2.Actor{ID: "local-user", Kind: "user"}, Draft: &v2.FieldDraft{DisplayName: field.Definition.DisplayName, LogicalType: v2.LogicalNumber, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display}})
			if err != nil || !conversion.CanApply || !conversion.CreatesMigration {
				t.Fatalf("conversion: %+v %v", conversion, err)
			}
			copying, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var releaseOnce sync.Once
			var running sync.WaitGroup
			migration := fieldchange.NewMigrationService(pb, fieldchange.NewPocketBasePlanStore(pb), fieldchange.WithMigrationFaultInjector(func(phase v2.MigrationPhase) error {
				if phase == v2.MigrationCopying {
					close(copying)
					<-release
				}
				return nil
			}))
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				running.Wait()
				migration.Shutdown()
			})
			start := func() error {
				job, err := migration.Enqueue(context.Background(), pb, conversion, "model-map-migration")
				if err != nil {
					return err
				}
				running.Add(1)
				go func() { defer running.Done(); finished <- migration.Run(context.Background(), job) }()
				select {
				case <-copying:
					return nil
				case err := <-finished:
					return err
				case <-time.After(5 * time.Second):
					return errors.New("copying barrier deadline")
				}
			}
			if atComplete {
				// The conversion was planned before binding. Enqueue it after accept,
				// before completion, to exercise both the final fence and switch guard.
				domain.gates = []businessWriteGate{func(ctx context.Context, kind, id string, write func(context.Context) error) error {
					if kind == "plugin.model.complete" {
						if err := start(); err != nil {
							return err
						}
					}
					return write(ctx)
				}}
			} else if err := start(); err != nil {
				t.Fatal(err)
			}
			_, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
			requireModelError(t, err, "field.migration.write_locked")
			installed, err := surfaceInstallation(context.Background(), pb, workspace, input)
			if err != nil || len(installed.ModelGrants) != 0 {
				t.Fatalf("locked mapping granted: %+v %v", installed.ModelGrants, err)
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-finished:
				if atComplete {
					requireModelError(t, err, "plugin.model.core_protected")
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("migration did not settle")
			}
			actual, err := domain.catalog.Field(context.Background(), field.TableID, field.FieldID)
			if err != nil {
				t.Fatal(err)
			}
			if atComplete && actual.LogicalType != v2.LogicalText {
				t.Fatal("queued migration changed a newly bound core field")
			}
			if !atComplete && actual.LogicalType != v2.LogicalNumber {
				t.Fatal("unbound ordinary migration was blocked")
			}
		})
	}
}

func TestPluginModelPartialRevisionChangeReconfirmsOnlyRemainingObjects(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	count := 0
	domain.gates = []businessWriteGate{func(ctx context.Context, kind, id string, write func(context.Context) error) error {
		if kind == "schema.table.create" {
			count++
			if count == 2 {
				return errors.New("interrupt after first table")
			}
		}
		return write(ctx)
	}}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err == nil || result.(pluginmodel.State).Completed != 1 {
		t.Fatalf("partial result: %+v %v", result, err)
	}
	store := pluginstore.New(pb, workspace)
	if _, err := store.SetEnabled(context.Background(), input.ProjectKey, input.PluginID, false); err != nil {
		t.Fatal(err)
	}
	raw, err := store.SetEnabled(context.Background(), input.ProjectKey, input.PluginID, true)
	if err != nil {
		t.Fatal(err)
	}
	var enabled struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(raw, &enabled); err != nil {
		t.Fatal(err)
	}
	input.InstallationRevision = enabled.Revision
	domain.gates = nil
	result, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	renewed := result.(modelPreview)
	if len(renewed.Diagnostics) != 0 || renewed.RequestID == preview.RequestID || renewed.Bindings["people"].TableID != preview.Bindings["people"].TableID {
		t.Fatalf("renewed plan: %+v", renewed)
	}
	tables, _ := schemaapi.New(pb).List(context.Background())
	if len(tables) != 1 {
		t.Fatal("preview created remaining tables without confirmation")
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": renewed.RequestID})
	if err != nil || result.(pluginmodel.State).Status != "completed" {
		t.Fatalf("resume: %+v %v", result, err)
	}
	tables, _ = schemaapi.New(pb).List(context.Background())
	if len(tables) != 3 {
		t.Fatalf("recovery produced %d tables", len(tables))
	}
	// Simulate damaged persisted metadata, bypassing the normal protected native
	// deletion API. Recovery must diagnose loss, never fabricate a replacement.
	table, err := pb.FindFirstRecordByData("vibetable_tables", "table_id", renewed.Bindings["people"].TableID)
	if err != nil {
		t.Fatal(err)
	}
	if err = pb.Delete(table); err != nil {
		t.Fatal(err)
	}
	input = changeInstalledModel(t, pb, workspace, input, func(m *pluginmodel.Declaration) {})
	result, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := result.(modelPreview)
	if blocked.Status != "blocked" {
		t.Fatal("missing persisted table was recreated")
	}
	found := false
	for _, diagnostic := range blocked.Diagnostics {
		if diagnostic.Code == "plugin_model_table_missing" && diagnostic.Path == "people" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing table diagnosis: %+v", blocked.Diagnostics)
	}
	for _, step := range blocked.Steps {
		if step.Kind == "table" && step.Entity == "people" {
			t.Fatal("planned replacement of previously persisted table")
		}
	}
}

func TestPluginModelFailureWaiterCannotDowngradeConcurrentCompletion(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	preview := result.(modelPreview)
	waiting, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var failed atomic.Bool
	domain.gates = []businessWriteGate{func(ctx context.Context, kind, id string, write func(context.Context) error) error {
		if kind == "field.change.apply" && failed.CompareAndSwap(false, true) {
			return errors.New("first caller interrupted")
		}
		if kind == "plugin.model.failure" {
			close(waiting)
			<-release
		}
		return write(ctx)
	}}
	type completion struct {
		state any
		err   error
	}
	first := make(chan completion, 1)
	go func() {
		result, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
		first <- completion{result, err}
	}()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("failure writer did not reach gate")
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err != nil || result.(pluginmodel.State).Status != "completed" {
		t.Fatalf("second caller: %+v %v", result, err)
	}
	once.Do(func() { close(release) })
	select {
	case reply := <-first:
		if reply.err != nil || reply.state.(pluginmodel.State).Status != "completed" {
			t.Fatalf("first caller did not recover completion: %+v %v", reply.state, reply.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failure writer did not settle")
	}
	result, err = modelCall(t, pb, workspace, input, domain, "model.status", map[string]any{"requestId": preview.RequestID})
	if err != nil || result.(pluginmodel.State).Status != "completed" {
		t.Fatalf("public status downgraded: %+v %v", result, err)
	}
	states, err := pluginmodel.States(context.Background(), pb, input.ProjectKey, input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	completed, highWater := false, 0
	for _, state := range states {
		if state.RequestID != preview.RequestID {
			continue
		}
		if state.Completed < highWater || completed && state.Status != "completed" {
			t.Fatalf("durable state regressed: %+v", state)
		}
		highWater = state.Completed
		completed = state.Status == "completed"
	}
}

func TestPluginModelMappedIdentityAndProtectionSurviveUninstall(t *testing.T) {
	pb, workspace, input, domain, preview, field := existingModelMapping(t)
	result, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	done := result.(pluginmodel.State)
	if done.Status != "completed" || done.Bindings["items"].Fields["name"] != field.FieldID || done.Bindings["items"].TableID != field.TableID {
		t.Fatalf("mapped identity: %+v", done)
	}
	if removed, err := pluginstore.New(pb, workspace).DeleteInstallation(context.Background(), input.ProjectKey, input.PluginID, input.InstallationID, input.InstallationRevision, true); err != nil || !removed {
		t.Fatalf("uninstall: %v %v", removed, err)
	}
	_, err = schemaapi.New(pb).DeleteTable(context.Background(), field.TableID, mustModelRevision(t, field.SchemaRevision))
	requireModelError(t, err, "plugin.model.core_protected")
	_, err = domain.Plan(context.Background(), v2.FieldChangeIntent{Action: v2.ActionRetire, TableID: field.TableID, FieldID: field.FieldID, ExpectedSchemaRev: field.SchemaRevision, Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	requireModelError(t, err, "plugin.model.core_protected")
	table, err := domain.CreateTable(context.Background(), v2.TableCreateIntent{DisplayName: "普通用户表", OperationID: "ordinary-unbound", Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	ordinary := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "普通用户字段", "ordinary-unbound-field")
	plan, err := domain.Plan(context.Background(), v2.FieldChangeIntent{Action: v2.ActionRetire, TableID: table.TableID, FieldID: ordinary.FieldID, ExpectedSchemaRev: ordinary.SchemaRevision, Actor: v2.Actor{ID: "local-user", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	retired, err := domain.Apply(context.Background(), v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "ordinary-retire", Actor: plan.Intent.Actor, Confirmations: plan.Confirmations})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schemaapi.New(pb).DeleteTable(context.Background(), table.TableID, mustModelRevision(t, retired.SchemaRevision)); err != nil {
		t.Fatalf("ordinary native schema blocked: %v", err)
	}
}
