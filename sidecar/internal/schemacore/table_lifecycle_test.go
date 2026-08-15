package schemacore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestTableLifecycleCreatesOnlyNormalizedV2AuthorityAndAudit(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.TableCreateIntent{
		DisplayName: " 客户订单 ",
		OperationID: "operation-create-table-12345678",
		Actor:       v2.Actor{ID: "local-user", Kind: "user"},
	}
	receipt, err := lifecycle.Create(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Contract != v2.Contract || receipt.DisplayName != "客户订单" ||
		!strings.HasPrefix(receipt.TableID, "tbl_") || receipt.SchemaRevision != "schema_0001" {
		t.Fatalf("receipt = %#v", receipt)
	}
	table, err := app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:table}",
		dbx.Params{"table": receipt.TableID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if table.GetString("display_name") != "客户订单" ||
		table.GetInt("schema_revision") != 1 || table.GetInt("data_revision") != 0 {
		t.Fatalf("table metadata = %#v", table)
	}
	if table.Collection().Fields.GetByName("definition_json") != nil {
		t.Fatal("vibetable_tables still exposes the legacy definition_json authority")
	}
	outbox, err := app.FindFirstRecordByFilter(
		"vibetable_audit_outbox",
		"event_id={:event}",
		dbx.Params{"event": "schema:" + intent.OperationID},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(outbox.GetRaw("payload_json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "definition") || strings.Contains(string(raw), "fields") {
		t.Fatalf("table audit contains schema values instead of safe identity: %s", raw)
	}
	replayed, err := lifecycle.Create(context.Background(), intent)
	if err != nil || replayed != receipt {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	readOnlyReplay, err := lifecycle.Replay(intent)
	if err != nil || readOnlyReplay != receipt {
		t.Fatalf("read-only replay = %#v, %v", readOnlyReplay, err)
	}
	foundReplay, found, err := lifecycle.FindReplay(intent)
	if err != nil || !found || foundReplay != receipt {
		t.Fatalf("found replay = %#v, %t, %v", foundReplay, found, err)
	}
}

func TestTableLifecycleRejectsMissingAppAndInvalidClosedIntent(t *testing.T) {
	if lifecycle, err := NewTableLifecycle(nil); err == nil || lifecycle != nil {
		t.Fatalf("NewTableLifecycle(nil) = %#v, %v", lifecycle, err)
	}
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	lifecycle, err := NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	valid := v2.TableCreateIntent{
		DisplayName: "Orders", OperationID: "operation-1",
		Actor: v2.Actor{ID: "user-1", Kind: "user"},
	}
	for name, intent := range map[string]v2.TableCreateIntent{
		"blank-name":   {DisplayName: " ", OperationID: valid.OperationID, Actor: valid.Actor},
		"long-name":    {DisplayName: strings.Repeat("界", 129), OperationID: valid.OperationID, Actor: valid.Actor},
		"control-name": {DisplayName: "Orders\nHidden", OperationID: valid.OperationID, Actor: valid.Actor},
		"operation":    {DisplayName: valid.DisplayName, Actor: valid.Actor},
		"actor-id":     {DisplayName: valid.DisplayName, OperationID: valid.OperationID, Actor: v2.Actor{Kind: "user"}},
		"actor-kind":   {DisplayName: valid.DisplayName, OperationID: valid.OperationID, Actor: v2.Actor{ID: "user-1"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := lifecycle.Replay(intent); err == nil {
				t.Fatal("invalid intent was accepted")
			}
			if _, err := lifecycle.Create(context.Background(), intent); err == nil {
				t.Fatal("invalid create intent was accepted")
			}
			if _, found, err := lifecycle.FindReplay(intent); err == nil || found {
				t.Fatalf("FindReplay() = found %t, err %v", found, err)
			}
		})
	}
}

func TestTableSettingsValidationFailsClosedBeforeStorage(t *testing.T) {
	t.Parallel()
	actor := v2.Actor{ID: "user-1", Kind: "user"}
	tests := []struct {
		name     string
		intent   v2.TableSettingsIntent
		wantPath string
	}{
		{
			name: "incomplete intent",
			intent: v2.TableSettingsIntent{
				ExpectedSchemaRev: "schema_0001", OperationID: "settings-incomplete",
				Actor: actor,
			},
			wantPath: "",
		},
		{
			name: "malformed revision",
			intent: v2.TableSettingsIntent{
				TableID: "tbl_orders", ExpectedSchemaRev: "revision-one",
				OperationID: "settings-bad-revision", Actor: actor,
			},
			wantPath: "expectedSchemaRevision",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := tableSettingsReceipt(test.intent)
			var productErr *v2.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "schema.table.settings_invalid" ||
				productErr.Path != test.wantPath {
				t.Fatalf("tableSettingsReceipt() error = %#v", err)
			}
		})
	}
}

func TestArchivePolicyValidationRejectsInvalidShapesBeforeFieldLookup(t *testing.T) {
	t.Parallel()
	fieldID := "fld_status"
	archivedValue := "opt_archived"
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateArchivePolicy(cancelled, nil, "tbl_orders", v2.ArchivePolicy{Mode: "none"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled validateArchivePolicy() error = %v", err)
	}

	tests := []struct {
		name     string
		policy   v2.ArchivePolicy
		wantPath string
	}{
		{
			name: "none references field",
			policy: v2.ArchivePolicy{
				Mode: "none", FieldID: &fieldID,
			},
			wantPath: "archivePolicy",
		},
		{
			name:     "unknown mode",
			policy:   v2.ArchivePolicy{Mode: "hidden"},
			wantPath: "archivePolicy.mode",
		},
		{
			name: "status without field",
			policy: v2.ArchivePolicy{
				Mode: "status", ArchivedValue: archivedValue,
			},
			wantPath: "archivePolicy.fieldId",
		},
		{
			name: "deletedAt with blank field",
			policy: v2.ArchivePolicy{
				Mode: "deletedAt", FieldID: new(string),
			},
			wantPath: "archivePolicy.fieldId",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateArchivePolicy(context.Background(), nil, "tbl_orders", test.policy)
			var productErr *v2.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "schema.table.archive_policy_invalid" ||
				productErr.Path != test.wantPath {
				t.Fatalf("validateArchivePolicy() error = %#v", err)
			}
		})
	}
	if err := validateArchivePolicy(
		context.Background(), nil, "tbl_orders", v2.ArchivePolicy{Mode: "none"},
	); err != nil {
		t.Fatalf("valid none policy error = %v", err)
	}
}

func TestTableLifecycleDetectsOperationIdentityConflict(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	first := v2.TableCreateIntent{
		DisplayName: "Orders", OperationID: "operation-conflict",
		Actor: v2.Actor{ID: "user-1", Kind: "user"},
	}
	if _, err := lifecycle.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.DisplayName = "Customers"
	if _, err := lifecycle.Create(context.Background(), changed); err == nil ||
		!strings.Contains(err.Error(), "schema.table.operation_conflict") {
		t.Fatalf("Create(conflict) error = %v", err)
	}
	if _, found, err := lifecycle.FindReplay(changed); err == nil || found {
		t.Fatalf("FindReplay(conflict) = found %t, err %v", found, err)
	}
}

func TestTableLifecycleConfiguresRevisionBoundArchivePolicyAndReplays(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lifecycle, err := NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: "Orders", OperationID: "settings-table-create",
		Actor: v2.Actor{ID: "user-1", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	recommended, err := v2.RecommendedDefaults(v2.LogicalSelect)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{
		DisplayName: "Status", LogicalType: v2.LogicalSelect,
		Value: recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
		Select: &v2.SelectSpec{Options: []v2.SelectOption{
			{Label: "Active", Color: "#16a34a", Order: 10, State: v2.OptionActive},
			{Label: "Archived", Color: "#64748b", Order: 20, State: v2.OptionActive},
		}},
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "user-1", Kind: "user"}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		ExpectedSchemaRev: table.SchemaRevision, Draft: &draft, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	field, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "settings-status-create", Actor: actor,
	})
	if err != nil || field.Definition == nil || field.Definition.Select == nil {
		t.Fatalf("create status field = %#v, %v", field, err)
	}
	archivedOption := field.Definition.Select.Options[1].OptionID
	fieldID := field.FieldID
	intent := v2.TableSettingsIntent{
		TableID: table.TableID, ExpectedSchemaRev: field.SchemaRevision,
		ArchivePolicy: v2.ArchivePolicy{
			Mode: "status", FieldID: &fieldID, ArchivedValue: archivedOption,
		},
		OperationID: "settings-archive-status", Actor: actor,
	}
	receipt, err := lifecycle.Configure(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Contract != v2.Contract || receipt.SchemaRevision != "schema_0003" ||
		receipt.ArchivePolicy.Mode != "status" ||
		receipt.ArchivePolicy.ArchivedValue != archivedOption {
		t.Fatalf("settings receipt = %#v", receipt)
	}
	replayed, err := lifecycle.Configure(ctx, intent)
	if err != nil || replayed != receipt {
		t.Fatalf("settings replay = %#v, %v", replayed, err)
	}
	tableRecord, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": table.TableID},
	)
	if err != nil || tableRecord.GetInt("schema_revision") != 3 ||
		!strings.Contains(tableRecord.GetString("archive_policy"), archivedOption) {
		t.Fatalf("stored table settings = %#v, %v", tableRecord, err)
	}
	stale := intent
	stale.OperationID = "settings-stale"
	if _, err := lifecycle.Configure(ctx, stale); err == nil {
		t.Fatal("stale table settings revision was accepted")
	}
	changed := intent
	changed.ArchivePolicy.ArchivedValue = field.Definition.Select.Options[0].OptionID
	var productErr *v2.ProductError
	if _, err := lifecycle.Configure(ctx, changed); !errors.As(err, &productErr) ||
		productErr.Code != "schema.table.operation_conflict" {
		t.Fatalf("settings operation conflict = %#v", err)
	}
	invalid := intent
	invalid.ExpectedSchemaRev = receipt.SchemaRevision
	invalid.OperationID = "settings-invalid-option"
	invalid.ArchivePolicy.ArchivedValue = "opt_missing"
	if _, err := lifecycle.Configure(ctx, invalid); !errors.As(err, &productErr) ||
		productErr.Code != "schema.table.archive_policy_invalid" {
		t.Fatalf("invalid archived option = %#v", err)
	}
	if tableRecord, err = app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": table.TableID},
	); err != nil || tableRecord.GetInt("schema_revision") != 3 {
		t.Fatalf("rejected settings advanced revision: %#v, %v", tableRecord, err)
	}
}
