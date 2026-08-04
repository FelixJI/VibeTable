package fieldchange

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/fieldprojection"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestFieldMigrationCopiesSwitchesAndCleansProviderFields(t *testing.T) {
	t.Parallel()
	app, plan, collection := migrationFixture(t)
	store := NewPocketBasePlanStore(app)
	if _, err := store.Save(
		context.Background(), "intent-migration", time.Now(), plan,
	); err != nil {
		t.Fatal(err)
	}
	service := NewMigrationService(app, store)
	jobID, err := service.Enqueue(
		context.Background(), app, plan, "operation-migration",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), jobID)
	if err != nil || status.Phase != v2.MigrationCompleted ||
		status.Processed != 2 {
		t.Fatalf("status = %#v, %v", status, err)
	}
	updated, err := app.FindCollectionByNameOrId(collection.Id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Fields.GetById(plan.Before.Identity.ProviderFieldID) != nil ||
		updated.Fields.GetById(plan.After.Identity.ProviderFieldID) == nil ||
		updated.Fields.GetById(plan.After.Identity.ProviderFieldID).GetName() !=
			plan.After.Identity.PhysicalName {
		t.Fatalf("provider switch did not complete: %#v", updated.Fields)
	}
	rows, err := app.FindRecordsByFilter(updated, "", "+id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	present, missing := 0, 0
	for _, row := range rows {
		if row.GetBool(plan.After.Value.Presence.PhysicalName) {
			if row.GetFloat(plan.After.Identity.PhysicalName) != 12 {
				t.Fatalf("present migrated value = %#v", row)
			}
			present++
		} else {
			missing++
		}
	}
	if present != 1 || missing != 1 {
		t.Fatalf("migrated rows = %#v", rows)
	}
	revisions, err := NewCatalog(app).Revisions(context.Background(), plan.Intent.TableID)
	if err != nil || revisions.Schema != "schema_0002" {
		t.Fatalf("revisions = %#v, %v", revisions, err)
	}
	secondPlan := plan
	secondPlan.PlanID = "plan_migration_second"
	secondJobID, err := service.Enqueue(
		context.Background(), app, secondPlan, "operation-migration-second",
	)
	if err != nil || secondJobID == "" || secondJobID == jobID {
		t.Fatalf("enqueue successive migration = %q, %v", secondJobID, err)
	}
}

func TestFieldMigrationRecoveryDistinguishesRollbackFromPostSwitchCleanup(
	t *testing.T,
) {
	t.Parallel()
	app, plan, collection := migrationFixture(t)
	store := NewPocketBasePlanStore(app)
	if _, err := store.Save(
		context.Background(), "intent-rollback-recovery", time.Now(), plan,
	); err != nil {
		t.Fatal(err)
	}
	service := NewMigrationService(app, store)
	jobID, err := service.Enqueue(
		context.Background(), app, plan, "operation-rollback-recovery",
	)
	if err != nil {
		t.Fatal(err)
	}
	job, err := app.FindRecordById("vibetable_jobs", jobID)
	if err != nil {
		t.Fatal(err)
	}
	shadow := shadowNames(jobID, *plan.After)
	if err := service.ensureShadow(job, collection, *plan.After, shadow); err != nil {
		t.Fatal(err)
	}
	job.Set("state", "failed")
	job.Set("phase", v2.MigrationCleaning)
	job.Set("cleanup_state", "rollback_retry")
	job.Set("error_json", types.JSONRaw([]byte(
		`{"code":"field.migration.conversion_failed","path":"","message":"failed","details":{}}`,
	)))
	if err := app.Save(job); err != nil {
		t.Fatal(err)
	}

	if err := service.Run(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}
	updated, err := app.FindCollectionByNameOrId(collection.Id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Fields.GetById(plan.Before.Identity.ProviderFieldID) == nil {
		t.Fatal("rollback recovery deleted the authoritative provider")
	}
	if updated.Fields.GetById(plan.After.Identity.ProviderFieldID) != nil {
		t.Fatal("rollback recovery retained the disposable shadow provider")
	}
	status, err := service.Status(context.Background(), jobID)
	if err != nil || status.Phase != v2.MigrationRolledBack {
		t.Fatalf("rollback recovery status = %#v, %v", status, err)
	}
}

func TestFieldMigrationFaultsBeforeAtomicSwitchKeepOldAuthority(t *testing.T) {
	for _, faultPhase := range []v2.MigrationPhase{
		v2.MigrationValidating,
		v2.MigrationCopying,
		v2.MigrationVerifying,
		v2.MigrationSwitching,
	} {
		t.Run(string(faultPhase), func(t *testing.T) {
			t.Parallel()
			app, plan, collection := migrationFixture(t)
			store := NewPocketBasePlanStore(app)
			if _, err := store.Save(
				context.Background(), "intent-fault-"+string(faultPhase),
				time.Now(), plan,
			); err != nil {
				t.Fatal(err)
			}
			service := NewMigrationService(
				app,
				store,
				WithMigrationFaultInjector(func(phase v2.MigrationPhase) error {
					if phase == faultPhase {
						return context.DeadlineExceeded
					}
					return nil
				}),
			)
			jobID, err := service.Enqueue(
				context.Background(), app, plan,
				"operation-fault-"+string(faultPhase),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Run(context.Background(), jobID); err == nil {
				t.Fatal("faulted migration unexpectedly succeeded")
			}
			status, err := service.Status(context.Background(), jobID)
			if err != nil || status.Phase != v2.MigrationRolledBack {
				t.Fatalf("status = %#v, %v", status, err)
			}
			updated, _ := app.FindCollectionByNameOrId(collection.Id)
			if updated.Fields.GetById(plan.Before.Identity.ProviderFieldID) == nil ||
				updated.Fields.GetById(plan.Before.Identity.ProviderFieldID).GetName() !=
					plan.Before.Identity.PhysicalName ||
				updated.Fields.GetById(plan.After.Identity.ProviderFieldID) != nil {
				t.Fatalf("fault rollback changed authority: %#v", updated.Fields)
			}
			revisions, _ := NewCatalog(app).Revisions(
				context.Background(), plan.Intent.TableID,
			)
			if revisions.Schema != "schema_0001" {
				t.Fatalf("fault rollback revision = %#v", revisions)
			}
		})
	}
}

func TestFieldMigrationFenceAndCancellationAreDurableBeforeSwitch(t *testing.T) {
	t.Parallel()
	app, plan, collection := migrationFixture(t)
	store := NewPocketBasePlanStore(app)
	if _, err := store.Save(
		context.Background(), "intent-cancel", time.Now(), plan,
	); err != nil {
		t.Fatal(err)
	}
	service := NewMigrationService(app, store)
	jobID, err := service.Enqueue(
		context.Background(), app, plan, "operation-cancel",
	)
	if err != nil {
		t.Fatal(err)
	}
	var productErr *ProductError
	if err := CheckTableWriteFence(
		context.Background(), app, plan.Intent.TableID,
	); !errors.As(err, &productErr) ||
		productErr.Code != "field.migration.write_locked" {
		t.Fatalf("write fence error = %#v", err)
	}
	status, err := service.Cancel(context.Background(), jobID)
	if err != nil || status.Phase != v2.MigrationCancelled {
		t.Fatalf("Cancel() = %#v, %v", status, err)
	}
	if err := CheckTableWriteFence(
		context.Background(), app, plan.Intent.TableID,
	); err != nil {
		t.Fatalf("cancelled job retained write fence: %v", err)
	}
	updated, _ := app.FindCollectionByNameOrId(collection.Id)
	if updated.Fields.GetById(plan.Before.Identity.ProviderFieldID) == nil {
		t.Fatal("cancellation removed the authoritative field")
	}
}

func TestFieldBackfillUsesAuthoritativeWriterAndNeverSavesRecordsDirectly(t *testing.T) {
	t.Parallel()
	app, plan, collection := migrationFixture(t)
	plan.Intent.Action = v2.ActionBackfill
	plan.After = plan.Before
	plan.After.Value.Default.Enabled = true
	plan.After.Value.Default.Value = "backfilled"
	plan.After.Value.Default.Source = v2.DefaultUser
	plan.PlanHash, _ = hashPlan(plan)
	store := NewPocketBasePlanStore(app)
	if _, err := store.Save(
		context.Background(), "intent-backfill", time.Now(), plan,
	); err != nil {
		t.Fatal(err)
	}

	var called []struct {
		recordID string
		value    any
	}
	service := NewMigrationService(
		app,
		store,
		WithBackfillWriter(func(
			_ context.Context,
			_ v2.FieldChangePlan,
			_ string,
			recordID string,
			value any,
		) error {
			called = append(called, struct {
				recordID string
				value    any
			}{recordID: recordID, value: value})
			return nil
		}),
	)
	jobID, err := service.Enqueue(
		context.Background(), app, plan, "operation-backfill",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}
	if len(called) != 1 || called[0].value != "backfilled" {
		t.Fatalf("authoritative writer calls = %#v", called)
	}

	record, err := app.FindRecordById(collection, called[0].recordID)
	if err != nil {
		t.Fatal(err)
	}
	if value := (fieldprojection.Descriptor{Definition: *plan.Before}).
		ProductValue(physicalValues(record, *plan.Before)); value != nil {
		t.Fatalf("migration bypassed the authoritative writer: %#v", value)
	}
}

func TestFieldBackfillFailsClosedWithoutAuthoritativeWriter(t *testing.T) {
	t.Parallel()
	app, plan, _ := migrationFixture(t)
	plan.Intent.Action = v2.ActionBackfill
	plan.After = plan.Before
	plan.After.Value.Default.Enabled = true
	plan.After.Value.Default.Value = "backfilled"
	plan.After.Value.Default.Source = v2.DefaultUser
	plan.PlanHash, _ = hashPlan(plan)
	store := NewPocketBasePlanStore(app)
	if _, err := store.Save(
		context.Background(), "intent-backfill-closed", time.Now(), plan,
	); err != nil {
		t.Fatal(err)
	}
	service := NewMigrationService(app, store)
	jobID, err := service.Enqueue(
		context.Background(), app, plan, "operation-backfill-closed",
	)
	if err != nil {
		t.Fatal(err)
	}
	err = service.Run(context.Background(), jobID)
	var productErr *ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "field.migration.backfill_unavailable" {
		t.Fatalf("backfill error = %#v", err)
	}
	status, statusErr := service.Status(context.Background(), jobID)
	if statusErr != nil || status.Phase != v2.MigrationRolledBack {
		t.Fatalf("status = %#v, %v", status, statusErr)
	}
}

func TestV2RelationDefinitionSynchronizesRestrictMetadata(t *testing.T) {
	t.Parallel()
	app, _, _ := migrationFixture(t)
	definition := migrationDefinition(v2.LogicalRelation)
	definition.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_target",
		Cardinality:   "one",
		DeletePolicy:  "restrict",
		DisplayField:  "fld_title",
	}
	if err := saveDefinitionMetadata(app, "tbl_orders", definition); err != nil {
		t.Fatal(err)
	}
	record, err := app.FindFirstRecordByFilter(
		"vibetable_relations",
		"source_table_id='tbl_orders' && source_field_id={:field}",
		dbx.Params{"field": definition.Identity.FieldID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.GetString("target_table_id") != "tbl_target" ||
		record.GetString("cardinality") != "one" ||
		record.GetString("delete_policy") != "restrict" {
		t.Fatalf("relation metadata = %#v", record)
	}
}

func TestFieldMigrationResumePendingRecoversInterruptedRunningJob(t *testing.T) {
	t.Parallel()
	app, plan, _ := migrationFixture(t)
	store := NewPocketBasePlanStore(app)
	if _, err := store.Save(
		context.Background(), "intent-resume", time.Now(), plan,
	); err != nil {
		t.Fatal(err)
	}
	first := NewMigrationService(app, store)
	jobID, err := first.Enqueue(
		context.Background(), app, plan, "operation-resume",
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById("vibetable_jobs", jobID)
	if err != nil {
		t.Fatal(err)
	}
	record.Set("state", "running")
	record.Set("phase", v2.MigrationCopying)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	restarted := NewMigrationService(app, NewPocketBasePlanStore(app))
	t.Cleanup(restarted.Shutdown)
	if err := restarted.ResumePending(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := restarted.Status(context.Background(), jobID)
		if statusErr == nil && status.Phase == v2.MigrationCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := restarted.Status(context.Background(), jobID)
	t.Fatalf("resumed migration did not complete: %#v", status)
}

func migrationFixture(
	t *testing.T,
) (*pocketbase.PocketBase, v2.FieldChangePlan, *core.Collection) {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: migrationTempDir(t), HideStartBanner: true,
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
	before := migrationDefinition(v2.LogicalText)
	after := migrationDefinition(v2.LogicalNumber)
	after.Identity.ProviderFieldID = "pb_987654321012"
	after.Value.Presence.ProviderFieldID = "pb_zyxwvutsrqpo"
	collection := core.NewBaseCollection("orders_migration")
	collection.Fields.Add(
		&core.TextField{
			Id:   before.Identity.ProviderFieldID,
			Name: before.Identity.PhysicalName,
		},
		&core.BoolField{
			Id:     before.Value.Presence.ProviderFieldID,
			Name:   before.Value.Presence.PhysicalName,
			Hidden: true,
		},
	)
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	legacy := schema.TableDefinition{
		ContractVersion: schema.ContractVersion,
		TableID:         "tbl_orders", PhysicalName: collection.Name,
		DisplayName: "Orders", Kind: schema.TableKindBase,
		SchemaRevision: "schema_0001",
		ArchivePolicy:  schema.ArchivePolicy{Mode: schema.ArchiveModeNone},
		Fields:         []schema.FieldDefinition{toLegacyField(before)},
		Indexes:        []schema.IndexDefinition{},
	}
	legacyRaw, _ := json.Marshal(legacy)
	tables, _ := app.FindCollectionByNameOrId("vibetable_tables")
	table := core.NewRecord(tables)
	table.Set("table_id", legacy.TableID)
	table.Set("collection_id", collection.Id)
	table.Set("physical_name", collection.Name)
	table.Set("display_name", legacy.DisplayName)
	table.Set("kind", legacy.Kind)
	table.Set("schema_revision", 1)
	table.Set("data_revision", 2)
	table.Set("archive_policy", `{"mode":"none"}`)
	table.Set("definition_json", types.JSONRaw(legacyRaw))
	if err := app.Save(table); err != nil {
		t.Fatal(err)
	}
	if err := saveDefinitionMetadata(app, legacy.TableID, before); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		value   string
		present bool
	}{{"12", true}, {"", false}} {
		record := core.NewRecord(collection)
		record.Set(before.Identity.PhysicalName, input.value)
		record.Set(before.Value.Presence.PhysicalName, input.present)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	dataRevision := int64(2)
	plan := v2.FieldChangePlan{
		Contract: v2.Contract, PlanID: "plan_migration_12345678",
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		Intent: v2.FieldChangeIntent{
			Action: v2.ActionConvert, TableID: legacy.TableID,
			FieldID:              before.Identity.FieldID,
			ExpectedSchemaRev:    "schema_0001",
			ExpectedDataRevision: &dataRevision,
			ConversionRule:       "block",
			Actor:                v2.Actor{ID: "local-user", Kind: "user"},
		},
		Before: &before, After: &after,
		Classes:              []v2.ChangeClass{v2.ClassMigration},
		ExpectedSchemaRev:    "schema_0001",
		ExpectedDataRevision: &dataRevision,
		Impact: v2.Impact{
			Records: 2, Failures: []v2.FailureSample{},
			Dependencies: []v2.DependencyRef{},
		},
		Steps: []v2.PlanStep{}, Warnings: []v2.Diagnostic{},
		Errors: []v2.Diagnostic{}, Confirmations: []string{},
		CreatesMigration: true, CanApply: true,
	}
	plan.PlanHash, _ = hashPlan(plan)
	return app, plan, collection
}

func migrationTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "vibetable-field-migration-*")
	if err != nil {
		t.Fatalf("create field migration temp dir: %v", err)
	}
	t.Cleanup(func() {
		var cleanupErr error
		for range 20 {
			cleanupErr = os.RemoveAll(directory)
			if cleanupErr == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Errorf("remove field migration temp dir: %v", cleanupErr)
	})
	return directory
}

func migrationDefinition(logicalType v2.LogicalType) v2.FieldDefinition {
	recommended, _ := v2.RecommendedDefaults(logicalType)
	recommended.Value.Presence = v2.PresenceSpec{
		Mode:            v2.PresenceCompanion,
		ProviderFieldID: "pb_abcdefghijkl",
		PhysicalName:    "__vt_has_f_01jfieldx",
	}
	return v2.FieldDefinition{
		Contract: v2.Contract,
		Identity: v2.FieldIdentity{
			FieldID: "fld_01JFIELDX", PhysicalName: "f_01jfieldx",
			ProviderFieldID: "pb_123456789012",
		},
		DisplayName: "Amount", LogicalType: logicalType,
		Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
		Value:     recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
	}
}

func TestConversionRulesCoverLossyCardinalityAndTemporalChanges(t *testing.T) {
	relationOne := migrationDefinition(v2.LogicalRelation)
	relationMany := migrationDefinition(v2.LogicalRelation)
	relationOne.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_target", Cardinality: "one",
		DeletePolicy: "setNull", DisplayField: "fld_title",
	}
	relationMany.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_target", Cardinality: "many",
		DeletePolicy: "setNull", DisplayField: "fld_title",
	}
	many, supplied, err := convertMigrationValue(
		relationOne, relationMany, "record-a", "block",
	)
	if err != nil || !supplied ||
		!reflect.DeepEqual(many, []string{"record-a"}) {
		t.Fatalf("one-to-many = %#v, %v, %v", many, supplied, err)
	}
	_, _, err = convertMigrationValue(
		relationMany, relationOne, []string{"a", "b"}, "block",
	)
	if err == nil {
		t.Fatal("lossy many-to-one conversion was not blocked")
	}
	one, _, err := convertMigrationValue(
		relationMany, relationOne, []string{"a", "b"}, "last",
	)
	if err != nil || one != "b" {
		t.Fatalf("many-to-one last = %#v, %v", one, err)
	}

	date := migrationDefinition(v2.LogicalDate)
	dateTime := migrationDefinition(v2.LogicalDateTime)
	converted, _, err := convertMigrationValue(
		date, dateTime, "2026-07-28", "dateFill",
	)
	if err != nil || converted != "2026-07-28T00:00:00Z" {
		t.Fatalf("date-to-dateTime = %#v, %v", converted, err)
	}
	timeField := migrationDefinition(v2.LogicalTime)
	converted, _, err = convertMigrationValue(
		timeField, dateTime, "09:30:15", "dateFill",
	)
	if err != nil || converted != "1970-01-01T09:30:15Z" {
		t.Fatalf("time-to-dateTime = %#v, %v", converted, err)
	}
}

func TestSelectOptionDeletionRulesReplaceOrClearStoredOptionIDs(t *testing.T) {
	selectField := migrationDefinition(v2.LogicalSelect)
	replaced, supplied, err := convertMigrationValue(
		selectField, selectField, "opt_old",
		"selectOption:opt_old:replace:opt_new",
	)
	if err != nil || !supplied || replaced != "opt_new" {
		t.Fatalf("select replacement = %#v, %v, %v", replaced, supplied, err)
	}
	cleared, supplied, err := convertMigrationValue(
		selectField, selectField, "opt_old",
		"selectOption:opt_old:clear",
	)
	if err != nil || !supplied || cleared != nil {
		t.Fatalf("select clear = %#v, %v, %v", cleared, supplied, err)
	}

	multiField := migrationDefinition(v2.LogicalMultiSelect)
	replaced, supplied, err = convertMigrationValue(
		multiField, multiField, []string{"opt_old", "opt_new", "opt_other"},
		"selectOption:opt_old:replace:opt_new",
	)
	if err != nil || !supplied ||
		!reflect.DeepEqual(replaced, []string{"opt_new", "opt_other"}) {
		t.Fatalf("multiSelect replacement = %#v, %v, %v", replaced, supplied, err)
	}
	cleared, supplied, err = convertMigrationValue(
		multiField, multiField, []string{"opt_old"},
		"selectOption:opt_old:clear",
	)
	if err != nil || !supplied || cleared != nil {
		t.Fatalf("multiSelect clear = %#v, %v, %v", cleared, supplied, err)
	}
}
