package schemaexecution_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestDescribeBindsAuthoritativeV2SnapshotToPhysicalTable(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t)
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	tableReceipt, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: "Execution records", OperationID: "execution-table-create",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	createField := func(name, operationID string) v2.ApplyReceipt {
		t.Helper()
		recommended, defaultsErr := v2.RecommendedDefaults(v2.LogicalText)
		if defaultsErr != nil {
			t.Fatal(defaultsErr)
		}
		revisions, revisionErr := catalog.Revisions(ctx, tableReceipt.TableID)
		if revisionErr != nil {
			t.Fatal(revisionErr)
		}
		draft := v2.FieldDraft{
			DisplayName: name, LogicalType: v2.LogicalText,
			Value: recommended.Value, Constraints: recommended.Constraints,
			Storage: recommended.Storage, Display: recommended.Display,
		}
		plan, planErr := planner.Plan(ctx, v2.FieldChangeIntent{
			Action: v2.ActionCreate, TableID: tableReceipt.TableID,
			ExpectedSchemaRev: revisions.Schema, Draft: &draft,
			Actor: v2.Actor{ID: "local-user", Kind: "user"},
		})
		if planErr != nil {
			t.Fatal(planErr)
		}
		receipt, applyErr := executor.Apply(ctx, v2.ApplyRequest{
			PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: operationID,
			Actor: v2.Actor{ID: "local-user", Kind: "user"},
		})
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		return receipt
	}
	first := createField("First", "execution-first-create")
	second := createField("Second", "execution-second-create")

	table, err := schemaexecution.Describe(ctx, app, tableReceipt.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if table.Snapshot.Contract != v2.Contract ||
		table.Snapshot.SchemaRevision != second.SchemaRevision ||
		table.Snapshot.DataRevision != 0 || table.PhysicalName == "" {
		t.Fatalf("execution table = %#v", table)
	}
	if len(table.Snapshot.Fields) != 2 ||
		table.Snapshot.Fields[0].Identity.FieldID != first.FieldID ||
		table.Snapshot.Fields[1].Identity.FieldID != second.FieldID {
		t.Fatalf("physical field order = %#v", table.Snapshot.Fields)
	}
	if field, found := table.Field(second.FieldID); !found ||
		field.Identity.PhysicalName != second.Definition.Identity.PhysicalName {
		t.Fatalf("field lookup = %#v found=%v", field, found)
	}
}

func TestDescribeExposesStableTableNotFoundError(t *testing.T) {
	_, err := schemaexecution.Describe(context.Background(), newTestApp(t), "tbl_missing")
	if !errors.Is(err, schemaexecution.ErrTableNotFound) {
		t.Fatalf("Describe() error = %v; want ErrTableNotFound", err)
	}
}

func TestRetiredFieldLoadsAuthoritativeV2DefinitionWithoutPollutingSnapshot(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t)
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	tableReceipt, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: "Retired field records", OperationID: "retired-table-create",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	recommended, err := v2.RecommendedDefaults(v2.LogicalFile)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{
		DisplayName: "Receipt", LogicalType: v2.LogicalFile,
		Value: recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display, File: recommended.File,
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: tableReceipt.TableID,
		ExpectedSchemaRev: tableReceipt.SchemaRevision, Draft: &draft,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "retired-field-create",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	retirePlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionRetire, TableID: tableReceipt.TableID,
		FieldID: created.FieldID, ExpectedSchemaRev: created.SchemaRevision,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: retirePlan.PlanID, PlanHash: retirePlan.PlanHash,
		OperationID: "retired-field-retire",
		Actor:       v2.Actor{ID: "local-user", Kind: "user"},
	}); err != nil {
		t.Fatal(err)
	}

	table, err := schemaexecution.Describe(ctx, app, tableReceipt.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := table.Field(created.FieldID); found {
		t.Fatal("retired field leaked into active execution snapshot")
	}
	retired, err := schemaexecution.RetiredField(ctx, app, tableReceipt.TableID, created.FieldID)
	if err != nil {
		t.Fatal(err)
	}
	if retired.Identity.FieldID != created.FieldID ||
		retired.LogicalType != v2.LogicalFile ||
		retired.Lifecycle.State != v2.LifecycleRetired {
		t.Fatalf("retired field = %#v", retired)
	}
}

func TestDescribeRejectsNonAuthoritativeStoredFieldDefinitions(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(map[string]any)
		wantDetail string
	}{
		{
			name: "unknown contract member",
			mutate: func(definition map[string]any) {
				definition["unexpected"] = true
			},
			wantDetail: "decode Schema V2 field",
		},
		{
			name: "metadata identity mismatch",
			mutate: func(definition map[string]any) {
				identity := definition["identity"].(map[string]any)
				identity["fieldId"] = "fld_mismatch"
			},
			wantDetail: "identity mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			app := newTestApp(t)
			table := createTestExecutionTable(t, ctx, app, "Strict execution table", "strict-table")
			field := createTestExecutionField(
				t, ctx, app, table.TableID, v2.LogicalText, "Name", "strict-field",
			)
			encoded, err := json.Marshal(field.Definition)
			if err != nil {
				t.Fatal(err)
			}
			var definition map[string]any
			if err := json.Unmarshal(encoded, &definition); err != nil {
				t.Fatal(err)
			}
			test.mutate(definition)
			stored, err := app.FindFirstRecordByFilter(
				"vibetable_fields", "field_id={:field}", dbx.Params{"field": field.FieldID},
			)
			if err != nil {
				t.Fatal(err)
			}
			stored.Set("definition_v2_json", definition)
			if err := app.Save(stored); err != nil {
				t.Fatal(err)
			}

			_, err = schemaexecution.Describe(ctx, app, table.TableID)
			if err == nil || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("Describe() error = %v; want detail %q", err, test.wantDetail)
			}
		})
	}
}

// A schema revision changed inside the caller's transaction between the first
// and last metadata reads must still be rejected: the guard keeps protecting
// execution snapshots regardless of how the reads are bound to a transaction.
// The change is a real UPDATE on the caller's own transaction connection,
// injected right after the initial metadata read has completed (observed via
// the vibetable_fields select).
func TestDescribeRejectsRevisionChangedWhileLoading(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t)
	table := createTestExecutionTable(t, ctx, app, "Conflicting execution table", "conflict-table")

	var txWriter dbx.Builder
	var injected bool
	var injectErr error
	installHook := func(db *dbx.DB) {
		previous := db.QueryLogFunc
		db.QueryLogFunc = func(logCtx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, queryErr error) {
			if txWriter != nil && strings.Contains(statement, "vibetable_fields") && !injected {
				injected = true
				_, injectErr = txWriter.NewQuery(
					"UPDATE vibetable_tables SET schema_revision = schema_revision + 1, data_revision = data_revision + 1 WHERE table_id = {:table}",
				).Bind(dbx.Params{"table": table.TableID}).Execute()
			}
			if previous != nil {
				previous(logCtx, elapsed, statement, rows, queryErr)
			}
		}
		t.Cleanup(func() { db.QueryLogFunc = previous })
	}
	for _, db := range []*dbx.DB{app.ConcurrentDB().(*dbx.DB), app.NonconcurrentDB().(*dbx.DB)} {
		installHook(db)
	}

	err := app.RunInTransaction(func(txApp core.App) error {
		txWriter = txApp.NonconcurrentDB()
		_, err := schemaexecution.Describe(ctx, txApp, table.TableID)
		return err
	})
	if !injected {
		t.Fatal("revision change never executed: no vibetable_fields select observed")
	}
	if injectErr != nil {
		t.Fatalf("in-transaction revision change failed: %v", injectErr)
	}
	if err == nil || !strings.Contains(err.Error(), "schema.execution_revision_conflict") {
		t.Fatalf("Describe() error = %v; want schema.execution_revision_conflict", err)
	}
}

// A legitimate business write that advances data_revision between Describe's
// first and last metadata reads must not surface as a revision conflict: the
// execution snapshot has to stay a coherent read while business data
// revisions advance. The interleave commits a real UPDATE on an independent
// connection (never inside Describe's transaction) right after the initial
// metadata read completed, observed via the vibetable_fields select on both
// database pools so old and new read paths stay comparable. Deterministic
// without sleeps.
func TestDescribeKeepsConsistentSnapshotWhenBusinessWriteAdvancesDataRevision(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t)
	table := createTestExecutionTable(t, ctx, app, "并发业务写入表", "concurrent-business-table")
	writer, err := core.DefaultDBConnect(filepath.Join(app.DataDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("close independent writer: %v", err)
		}
	})

	var injected bool
	var injectErr error
	installHook := func(db *dbx.DB) {
		previous := db.QueryLogFunc
		db.QueryLogFunc = func(logCtx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, queryErr error) {
			if !injected && strings.Contains(statement, "vibetable_fields") {
				injected = true
				_, injectErr = writer.NewQuery(
					"UPDATE vibetable_tables SET data_revision = data_revision + 1 WHERE table_id = {:table}",
				).Bind(dbx.Params{"table": table.TableID}).Execute()
			}
			if previous != nil {
				previous(logCtx, elapsed, statement, rows, queryErr)
			}
		}
		t.Cleanup(func() { db.QueryLogFunc = previous })
	}
	for _, db := range []*dbx.DB{app.ConcurrentDB().(*dbx.DB), app.NonconcurrentDB().(*dbx.DB)} {
		installHook(db)
	}

	execution, err := schemaexecution.Describe(ctx, app, table.TableID)
	if !injected {
		t.Fatal("interleave never executed: no vibetable_fields select observed")
	}
	if injectErr != nil {
		t.Fatalf("interleaved business write failed: %v", injectErr)
	}
	if err != nil {
		t.Fatalf("Describe() rejected a legitimate concurrent business write: %v", err)
	}
	committed, findErr := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}",
		dbx.Params{"table": table.TableID})
	if findErr != nil {
		t.Fatal(findErr)
	}
	// The interleaved write committed on its own connection, and the returned
	// snapshot must be one coherent pre-write state, not a torn mix.
	if execution.Snapshot.DataRevision != 0 || committed.GetInt("data_revision") != 1 {
		t.Fatalf("snapshot data revision = %d, committed = %d; want 0 and 1",
			execution.Snapshot.DataRevision, committed.GetInt("data_revision"))
	}
}

func TestRetiredFieldRejectsActiveAndUnknownFields(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t)
	table := createTestExecutionTable(t, ctx, app, "Active execution table", "active-table")
	active := createTestExecutionField(
		t, ctx, app, table.TableID, v2.LogicalFile, "Receipt", "active-field",
	)

	for _, fieldID := range []string{active.FieldID, "fld_unknown"} {
		_, err := schemaexecution.RetiredField(ctx, app, table.TableID, fieldID)
		if !errors.Is(err, schemaexecution.ErrRetiredFieldNotFound) {
			t.Fatalf("RetiredField(%q) error = %v; want ErrRetiredFieldNotFound", fieldID, err)
		}
	}
}

func TestExecutionReadsHonorCanceledContextBeforeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app := newTestApp(t)

	if _, err := schemaexecution.Describe(ctx, app, "tbl_unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Describe() error = %v; want context.Canceled", err)
	}
	if _, err := schemaexecution.RetiredField(
		ctx, app, "tbl_unused", "fld_unused",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("RetiredField() error = %v; want context.Canceled", err)
	}
}

func createTestExecutionTable(
	t *testing.T,
	ctx context.Context,
	app core.App,
	displayName string,
	operationID string,
) v2.TableCreateReceipt {
	t.Helper()
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: displayName, OperationID: operationID,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func createTestExecutionField(
	t *testing.T,
	ctx context.Context,
	app core.App,
	tableID string,
	logicalType v2.LogicalType,
	displayName string,
	operationID string,
) v2.ApplyReceipt {
	t.Helper()
	recommended, err := v2.RecommendedDefaults(logicalType)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	revisions, err := catalog.Revisions(ctx, tableID)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{
		DisplayName: displayName, LogicalType: logicalType,
		Value: recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
		File: recommended.File, JSON: recommended.JSON,
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: tableID,
		ExpectedSchemaRev: revisions.Schema, Draft: &draft,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: operationID,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func newTestApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
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
	return app
}
