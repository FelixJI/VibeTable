package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/fieldresource"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestFileFieldPurgeRequiresCurrentBackupAndRemovesEveryPhysicalResource(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "Files", "op_file_purge_table")
	recommended, err := v2.RecommendedDefaults(v2.LogicalFile)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{
		DisplayName: "Documents",
		LogicalType: v2.LogicalFile,
		Value:       recommended.Value,
		Constraints: recommended.Constraints,
		Storage:     recommended.Storage,
		Display:     recommended.Display,
		File:        recommended.File,
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, nil)
	executor := fieldchange.NewExecutor(
		app, store,
		fieldchange.WithProtectionSnapshotVerifier(
			func(_ context.Context, snapshotID string) error {
				if snapshotID != "snapshot_current" {
					return errors.New("protection snapshot is not current")
				}
				return nil
			},
		),
	)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	createPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action:            v2.ActionCreate,
		TableID:           table.TableID,
		ExpectedSchemaRev: table.SchemaRevision,
		Draft:             &draft,
		Actor:             actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	createReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: createPlan.PlanID, PlanHash: createPlan.PlanHash,
		OperationID: "op_create_file_field", Actor: actor,
		Confirmations: createPlan.Confirmations,
	})
	if err != nil {
		t.Fatal(err)
	}
	field := *createReceipt.Definition

	manager, err := attachments.New()
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithAttachmentManager(manager),
	)
	recordID := "filepurge000001"
	applyMutation := func(
		key string,
		operation mutation.Operation,
	) mutation.Receipt {
		receipt, applyErr := kernel.Apply(ctx, mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID:       "request_" + key,
			IdempotencyKey:  "idem_" + key,
			TableID:         table.TableID,
			SchemaRevision:  createReceipt.SchemaRevision,
			Operations:      []mutation.Operation{operation},
			Actor:           mutation.Actor{Type: "user", ID: "local-user"},
		})
		if applyErr != nil {
			t.Fatalf("%s mutation: %v", key, applyErr)
		}
		return receipt
	}
	applyMutation("insert", mutation.Operation{
		Kind: mutation.OperationInsert, RecordID: &recordID,
		Values: map[string]any{},
	})
	if err := manager.Stage(
		"purge_upload_1", `C:\untrusted\first.txt`, []byte("first"),
	); err != nil {
		t.Fatal(err)
	}
	applyMutation("upload_1", mutation.Operation{
		Kind: mutation.OperationSetAttachments, RecordID: &recordID,
		FieldID: field.Identity.FieldID, UploadHandles: []string{"purge_upload_1"},
		RemoveStoredNames: []string{},
	})
	manager.Drop("purge_upload_1")
	collection, err := app.FindCollectionByNameOrId(table.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil {
		t.Fatal(err)
	}
	firstStoredName := record.GetStringSlice(field.Identity.PhysicalName)[0]
	if err := manager.Stage(
		"purge_upload_2", `C:\untrusted\second.txt`, []byte("second"),
	); err != nil {
		t.Fatal(err)
	}
	applyMutation("upload_2", mutation.Operation{
		Kind: mutation.OperationSetAttachments, RecordID: &recordID,
		FieldID: field.Identity.FieldID, UploadHandles: []string{"purge_upload_2"},
		RemoveStoredNames: []string{firstStoredName},
	})
	manager.Drop("purge_upload_2")
	record, err = app.FindRecordById(collection, recordID)
	if err != nil {
		t.Fatal(err)
	}
	secondStoredName := record.GetStringSlice(field.Identity.PhysicalName)[0]
	version, err := app.FindFirstRecordByFilter(
		"vibetable_attachment_versions",
		"table_id={:table} && field_id={:field}",
		dbx.Params{"table": table.TableID, "field": field.Identity.FieldID},
	)
	if err != nil {
		t.Fatalf("expected immutable attachment version: %v", err)
	}
	versionBlobKey := version.BaseFilesPath() + "/" + version.GetString("blob")
	liveBlobKey := record.BaseFilesPath() + "/" + secondStoredName

	rollbackMarker := errors.New("force transaction rollback after attachment stage")
	err = app.RunInTransaction(func(txApp core.App) error {
		if stageErr := fieldresource.StageAttachmentPurge(
			ctx, txApp, table.TableID, field.Identity.FieldID,
			"op_attachment_cleanup_rollback",
		); stageErr != nil {
			return stageErr
		}
		return rollbackMarker
	})
	if !errors.Is(err, rollbackMarker) {
		t.Fatalf("attachment purge rollback fault = %v", err)
	}
	for _, internalCollection := range []string{
		"vibetable_attachment_meta",
		"vibetable_attachment_versions",
	} {
		records, findErr := app.FindRecordsByFilter(
			internalCollection,
			"table_id={:table} && field_id={:field}",
			"id", 0, 0,
			dbx.Params{"table": table.TableID, "field": field.Identity.FieldID},
		)
		if findErr != nil || len(records) == 0 {
			t.Fatalf(
				"%s did not roll back after staged purge: %#v, err=%v",
				internalCollection, records, findErr,
			)
		}
	}
	rollbackFS, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{liveBlobKey, versionBlobKey} {
		exists, existsErr := rollbackFS.Exists(key)
		if existsErr != nil || !exists {
			t.Fatalf(
				"attachment %q was lost before commit: exists=%v err=%v",
				key, exists, existsErr,
			)
		}
	}
	_ = rollbackFS.Close()

	retirePlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action:            v2.ActionRetire,
		TableID:           table.TableID,
		FieldID:           field.Identity.FieldID,
		ExpectedSchemaRev: createReceipt.SchemaRevision,
		Actor:             actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	retireReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: retirePlan.PlanID, PlanHash: retirePlan.PlanHash,
		OperationID: "op_retire_file_field", Actor: actor,
		Confirmations: retirePlan.Confirmations,
	})
	if err != nil {
		t.Fatal(err)
	}
	integrity, err := manager.Integrity(ctx, app)
	if err != nil || !integrity.Valid {
		t.Fatalf("retired file integrity = %#v, err=%v", integrity, err)
	}
	stalePurgeIntent := v2.FieldChangeIntent{
		Action:            v2.ActionPurge,
		TableID:           table.TableID,
		FieldID:           field.Identity.FieldID,
		ExpectedSchemaRev: retireReceipt.SchemaRevision,
		Actor:             actor,
		Confirmation:      field.DisplayName,
		BackupReceipt:     "snapshot_at_plan",
	}
	stalePurgePlan, err := planner.Plan(ctx, stalePurgeIntent)
	if err != nil {
		t.Fatal(err)
	}
	secondRecordID := "filepurge000002"
	if _, err := kernel.Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "request_after_backup",
		IdempotencyKey:  "idem_after_backup",
		TableID:         table.TableID,
		SchemaRevision:  retireReceipt.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind: mutation.OperationInsert, RecordID: &secondRecordID,
			Values: map[string]any{},
		}},
		Actor: mutation.Actor{Type: "user", ID: "local-user"},
	}); err != nil {
		t.Fatalf("mutate after backup: %v", err)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: stalePurgePlan.PlanID, PlanHash: stalePurgePlan.PlanHash,
		OperationID: "op_purge_stale_backup", Actor: actor,
		Confirmations:        stalePurgePlan.Confirmations,
		ProtectionSnapshotID: "snapshot_stale",
	}); err == nil {
		t.Fatal("purge accepted stale data and a stale protection snapshot")
	}
	failedAudit, err := app.FindFirstRecordByFilter(
		"vibetable_schema_audit",
		"operation_id='op_purge_stale_backup'",
	)
	if err != nil ||
		failedAudit.GetString("outcome") != "failed" ||
		failedAudit.GetString("error_code") != "field.change.data_conflict" {
		t.Fatalf("failed purge audit mismatch: %#v, err=%v", failedAudit, err)
	}
	purgeIntent := stalePurgeIntent
	purgeIntent.BackupReceipt = "snapshot_current"
	purgePlan, err := planner.Plan(ctx, purgeIntent)
	if err != nil {
		t.Fatal(err)
	}
	if !purgePlan.CanApply ||
		purgePlan.Steps[1].Details["removeAttachmentBlobs"] != true {
		t.Fatalf("purge plan did not expose attachment impact: %#v", purgePlan)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: purgePlan.PlanID, PlanHash: purgePlan.PlanHash,
		OperationID: "op_purge_without_confirmation", Actor: actor,
		ProtectionSnapshotID: "snapshot_current",
	}); err == nil {
		t.Fatal("purge without frozen confirmations was accepted")
	}
	purgeReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: purgePlan.PlanID, PlanHash: purgePlan.PlanHash,
		OperationID: "op_purge_file_field", Actor: actor,
		Confirmations:        purgePlan.Confirmations,
		ProtectionSnapshotID: "snapshot_current",
	})
	if err != nil {
		t.Fatalf("purge file field: %v", err)
	}
	if purgeReceipt.Definition != nil {
		t.Fatalf("purge returned a surviving definition: %#v", purgeReceipt)
	}
	if _, err := catalog.Field(ctx, table.TableID, field.Identity.FieldID); err == nil {
		t.Fatal("purged field metadata still exists")
	}
	collection, err = app.FindCollectionByNameOrId(table.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	if collection.Fields.GetByName(field.Identity.PhysicalName) != nil {
		t.Fatal("purged provider field still exists")
	}
	for _, internalCollection := range []string{
		"vibetable_attachment_meta",
		"vibetable_attachment_versions",
	} {
		records, findErr := app.FindRecordsByFilter(
			internalCollection,
			"table_id={:table} && field_id={:field}",
			"id",
			0,
			0,
			dbx.Params{"table": table.TableID, "field": field.Identity.FieldID},
		)
		if findErr != nil || len(records) != 0 {
			t.Fatalf("%s survived purge: %#v, err=%v", internalCollection, records, findErr)
		}
	}
	cleanupJob, err := app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type='field_resource_cleanup' && source_table_id={:table} && relation_field_id={:field}",
		dbx.Params{"table": table.TableID, "field": field.Identity.FieldID},
	)
	if err != nil || cleanupJob.GetString("state") != "complete" ||
		cleanupJob.GetString("cleanup_state") != "complete" {
		t.Fatalf("attachment cleanup job = %#v, err=%v", cleanupJob, err)
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	for _, key := range []string{liveBlobKey, versionBlobKey} {
		exists, existsErr := fsys.Exists(key)
		if existsErr != nil || exists {
			t.Fatalf("purged attachment object %q exists=%v err=%v", key, exists, existsErr)
		}
	}
	audit, err := app.FindFirstRecordByFilter(
		"vibetable_schema_audit",
		"operation_id='op_purge_file_field'",
	)
	if err != nil || audit.GetString("backup_receipt") != "snapshot_current" {
		t.Fatalf("purge audit receipt mismatch: %#v, err=%v", audit, err)
	}
}
