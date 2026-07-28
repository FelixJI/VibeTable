package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestManagedAttachmentsUploadReplaceDownloadIntegrityRollbackAndDelete(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	fileField := field(
		"documents_id", "documents", schema.FieldKindAttachment, schema.DataTypeFile,
	)
	fileField.Editor.Kind = "file"
	fileField.AttachmentPolicy = &schema.AttachmentPolicy{
		MaxFiles: 2, MaxBytesPerFile: 1024,
		AllowedMIMETypes:  []string{"text/plain"},
		ThumbnailVariants: []string{}, Protected: true,
	}
	fileField.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintAttachment, Policy: fileField.AttachmentPolicy,
	}}
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("attachment_notes", "attachment_notes", []schema.FieldDefinition{
			field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			fileField,
			autoDateField("created_at", schema.AutoDateRoleCreatedAt),
			autoDateField("updated_at", schema.AutoDateRoleUpdatedAt),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	failAfterMetadata := false
	failAfterVersionArchive := false
	manager, err := attachments.New(
		[]byte(strings.Repeat("b", 32)),
		attachments.WithClock(func() time.Time { return now }),
		attachments.WithFaultInjector(func(point string) error {
			if failAfterMetadata && point == "after_metadata" {
				return errors.New("injected")
			}
			if failAfterVersionArchive && point == "after_version_archive" {
				return errors.New("injected")
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	var sequence atomic.Int64
	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return now }),
		mutation.WithAttachmentManager(manager),
		mutation.WithIDGenerator(func(kind string) string {
			next := sequence.Add(1)
			switch kind {
			case "record":
				return "attachrecord001"
			case "changeSet":
				return fmt.Sprintf("attach_change_%06d", next)
			case "event":
				return fmt.Sprintf("attach_event_%06d", next)
			default:
				return fmt.Sprintf("%s_%06d", kind, next)
			}
		}),
	)
	recordID := "attachrecord001"
	apply := func(key string, operation mutation.Operation) (mutation.Receipt, error) {
		return kernel.Apply(ctx, mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID:       "request_" + key, IdempotencyKey: "idem_" + key,
			TableID: definition.TableID, SchemaRevision: definition.SchemaRevision,
			Operations: []mutation.Operation{operation},
			Actor:      mutation.Actor{Type: "user", ID: "local-user"},
		})
	}
	insertReceipt, err := apply("insert", mutation.Operation{
		Kind: mutation.OperationInsert, RecordID: &recordID,
		Values: map[string]any{"title": "with files"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.Stage("upload_one", `C:\unsafe\first.txt`, []byte("first file")); err != nil {
		t.Fatal(err)
	}
	insertUpdated, err := time.Parse(
		time.RFC3339Nano,
		insertReceipt.ComputedFields[recordID]["updated_at"].(string),
	)
	if err != nil {
		t.Fatal(err)
	}
	for !time.Now().UTC().After(insertUpdated.Add(time.Millisecond)) {
		time.Sleep(time.Millisecond)
	}
	receipt, err := apply("upload_one", mutation.Operation{
		Kind: mutation.OperationSetAttachments, RecordID: &recordID,
		FieldID: "documents_id", UploadHandles: []string{"upload_one"},
		RemoveStoredNames: []string{},
	})
	manager.Drop("upload_one")
	if err != nil {
		t.Fatalf("upload attachment: %#v", err)
	}
	if len(receipt.AffectedRows) != 1 ||
		receipt.AffectedRows[0].Operation != mutation.OperationSetAttachments {
		t.Fatalf("attachment receipt %#v", receipt)
	}
	insertTimes := insertReceipt.ComputedFields[recordID]
	attachmentTimes := receipt.ComputedFields[recordID]
	attachmentUpdated, parseErr := time.Parse(
		time.RFC3339Nano,
		attachmentTimes["updated_at"].(string),
	)
	if insertTimes["created_at"] == nil ||
		attachmentTimes["created_at"] != insertTimes["created_at"] ||
		parseErr != nil || !attachmentUpdated.After(insertUpdated) {
		t.Fatalf(
			"attachment Save autoDate receipt insert=%#v attachment=%#v",
			insertTimes,
			attachmentTimes,
		)
	}
	collection, _ := app.FindCollectionByNameOrId("attachment_notes")
	record, err := app.FindRecordById(collection, recordID)
	if err != nil {
		t.Fatal(err)
	}
	names := record.GetStringSlice("documents")
	if len(names) != 1 {
		t.Fatalf("stored attachment names %#v", names)
	}
	refs, err := manager.Refs(ctx, app, definition, record, "documents_id")
	if err != nil || len(refs) != 1 ||
		refs[0].OriginalName != "first.txt" ||
		refs[0].StoredName != names[0] ||
		refs[0].SHA256 == "" {
		t.Fatalf("managed refs %#v err=%v", refs, err)
	}
	token, err := manager.Token(
		ctx, app, definition.TableID, recordID, "documents_id", names[0], "",
	)
	if err != nil || token == "" {
		t.Fatalf("issue file token: token=%q err=%v", token, err)
	}
	download, err := manager.Open(ctx, app, refs[0].DownloadCapability)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(download.Reader)
	closeErr := download.Reader.Close()
	if err != nil || closeErr != nil || string(content) != "first file" {
		t.Fatalf("download content=%q read=%v close=%v", content, err, closeErr)
	}
	if _, err := manager.Open(ctx, app, refs[0].DownloadCapability+"tampered"); err == nil {
		t.Fatal("tampered capability was accepted")
	}
	now = now.Add(5 * time.Minute)
	if _, err := manager.Open(ctx, app, token); err == nil {
		t.Fatal("expired capability was accepted")
	}
	now = now.Add(-5 * time.Minute)
	if _, err := manager.Token(
		ctx, app, definition.TableID, recordID, "documents_id", "missing.txt", "",
	); err == nil {
		t.Fatal("token was issued for a missing attachment")
	}
	report, err := manager.Integrity(ctx, app)
	if err != nil || !report.Valid {
		t.Fatalf("integrity %#v err=%v", report, err)
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	managedKey := record.BaseFilesPath() + "/" + names[0]
	if err := fsys.Upload([]byte("other file"), managedKey); err != nil {
		t.Fatal(err)
	}
	report, err = manager.Integrity(ctx, app)
	if err != nil || report.Valid || len(report.CorruptFiles) != 1 ||
		report.CorruptFiles[0].Code != "attachment.hash_mismatch" {
		t.Fatalf("corrupt file report %#v err=%v", report, err)
	}
	if err := fsys.Upload([]byte("first file"), managedKey); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stage("upload_fail", "failed.txt", []byte("must rollback")); err != nil {
		t.Fatal(err)
	}
	failAfterMetadata = true
	_, err = apply("upload_fail", mutation.Operation{
		Kind: mutation.OperationSetAttachments, RecordID: &recordID,
		FieldID: "documents_id", UploadHandles: []string{"upload_fail"},
		RemoveStoredNames: []string{},
	})
	failAfterMetadata = false
	manager.Drop("upload_fail")
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.attachment.failed" {
		t.Fatalf("fault error %#v", err)
	}
	record, _ = app.FindRecordById(collection, recordID)
	if got := record.GetStringSlice("documents"); len(got) != 1 || got[0] != names[0] {
		t.Fatalf("rollback attachment names %#v", got)
	}
	report, err = manager.Integrity(ctx, app)
	if err != nil || !report.Valid {
		t.Fatalf("post-rollback integrity %#v err=%v", report, err)
	}

	metadata, err := app.FindFirstRecordByFilter(
		"vibetable_attachment_meta", "stored_name={:name}",
		map[string]any{"name": names[0]},
	)
	if err != nil || app.Delete(metadata) != nil {
		t.Fatalf("delete metadata for integrity test: %v", err)
	}
	report, err = manager.Integrity(ctx, app)
	if err != nil || report.Valid || len(report.MissingMetadata) != 1 {
		t.Fatalf("missing metadata report %#v err=%v", report, err)
	}
	_, err = apply("delete_missing_meta", mutation.Operation{
		Kind: mutation.OperationDelete, RecordID: &recordID,
	})
	if !errors.As(err, &productErr) ||
		productErr.Code != "attachment.metadata_missing" {
		t.Fatalf("hard delete without metadata error %#v", err)
	}
	if _, err := app.FindRecordById(collection, recordID); err != nil {
		t.Fatalf("hard delete without metadata was not rolled back: %v", err)
	}
	metadataCollection, err := app.FindCollectionByNameOrId(
		"vibetable_attachment_meta",
	)
	if err != nil {
		t.Fatal(err)
	}
	restoredMetadata := core.NewRecord(metadataCollection)
	restoredMetadata.Set("table_id", definition.TableID)
	restoredMetadata.Set("record_id", recordID)
	restoredMetadata.Set("field_id", "documents_id")
	restoredMetadata.Set("stored_name", refs[0].StoredName)
	restoredMetadata.Set("original_name", refs[0].OriginalName)
	restoredMetadata.Set("mime", refs[0].MIMEType)
	restoredMetadata.Set("size", refs[0].Size)
	restoredMetadata.Set("hash", refs[0].SHA256)
	if err := app.Save(restoredMetadata); err != nil {
		t.Fatalf("restore metadata after integrity test: %v", err)
	}
	fsys, err = app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	orphanKey := collection.Id + "/orphanrecord001/orphan.txt"
	if err := fsys.Upload([]byte("orphan"), orphanKey); err != nil {
		t.Fatal(err)
	}
	orphanThumbKey := collection.Id +
		"/orphanrecord001/thumbs_ghost.png/100x100_ghost.png"
	if err := fsys.Upload([]byte("orphan thumb"), orphanThumbKey); err != nil {
		t.Fatal(err)
	}
	report, err = manager.Integrity(ctx, app)
	if err != nil || len(report.OrphanFiles) != 2 {
		t.Fatalf("orphan report %#v err=%v", report, err)
	}
	for _, key := range []string{orphanKey, orphanThumbKey} {
		exists, existsErr := fsys.Exists(key)
		if existsErr != nil || !exists {
			t.Fatalf(
				"integrity scan silently removed orphan %q: exists=%v err=%v",
				key, exists, existsErr,
			)
		}
	}
	if err := fsys.Delete(orphanKey); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Delete(orphanThumbKey); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}

	if err := manager.Stage("upload_two", "second.txt", []byte("second file")); err != nil {
		t.Fatal(err)
	}
	failAfterVersionArchive = true
	_, err = apply("replace", mutation.Operation{
		Kind: mutation.OperationSetAttachments, RecordID: &recordID,
		FieldID: "documents_id", UploadHandles: []string{"upload_two"},
		RemoveStoredNames: []string{names[0]},
	})
	failAfterVersionArchive = false
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.attachment.version_archive_failed" {
		t.Fatalf("version archive fault error %#v", err)
	}
	record, _ = app.FindRecordById(collection, recordID)
	if got := record.GetStringSlice("documents"); len(got) != 1 ||
		got[0] != names[0] {
		t.Fatalf("version archive fault did not roll back %#v", got)
	}
	assertRecordCount(t, app, "vibetable_attachment_versions", 0)
	if _, err := apply("replace", mutation.Operation{
		Kind: mutation.OperationSetAttachments, RecordID: &recordID,
		FieldID: "documents_id", UploadHandles: []string{"upload_two"},
		RemoveStoredNames: []string{names[0]},
	}); err != nil {
		t.Fatalf("replace attachment: %#v", err)
	}
	manager.Drop("upload_two")
	record, _ = app.FindRecordById(collection, recordID)
	replaced := record.GetStringSlice("documents")
	if len(replaced) != 1 || replaced[0] == names[0] {
		t.Fatalf("replacement names %#v", replaced)
	}
	assertRecordCount(t, app, "vibetable_attachment_versions", 1)
	fsys, err = app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	oldExists, err := fsys.Exists(record.BaseFilesPath() + "/" + names[0])
	closeErr = fsys.Close()
	if err != nil || closeErr != nil || oldExists {
		t.Fatalf("replaced file remains: exists=%t err=%v close=%v", oldExists, err, closeErr)
	}

	target, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events",
		"request_id={:request}",
		map[string]any{"request": "request_upload_one"},
	)
	if err != nil {
		t.Fatal(err)
	}
	history, err := audit.New(
		app,
		kernel,
		mutation.MetadataSchemaSource{},
		[]byte(strings.Repeat("h", 32)),
		audit.WithClock(func() time.Time { return now }),
		audit.WithAttachmentHistory(manager),
	)
	if err != nil {
		t.Fatal(err)
	}
	version, err := app.FindFirstRecordByFilter(
		"vibetable_attachment_versions",
		"stored_name={:name}",
		map[string]any{"name": names[0]},
	)
	if err != nil {
		t.Fatal(err)
	}
	fsys, err = app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	versionKey := version.BaseFilesPath() + "/" + version.GetString("blob")
	if err := fsys.Upload([]byte("corrupt"), versionKey); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = history.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	var historyErr *audit.Error
	if !errors.As(err, &historyErr) ||
		historyErr.Code != "restore_attachment_corrupt" {
		t.Fatalf("corrupt historical attachment error %#v", err)
	}
	fsys, err = app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.Upload([]byte("first file"), versionKey); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}
	backupIntegrity, err := manager.Integrity(ctx, app)
	if err != nil || !backupIntegrity.Valid {
		t.Fatalf("attachment history integrity %#v err=%v", backupIntegrity, err)
	}
	if err := app.CreateBackup(ctx, "attachment_history.zip"); err != nil {
		t.Fatalf("create PocketBase archive primitive: %v", err)
	}
	backupFS, err := app.NewBackupsFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	backupReader, err := backupFS.GetReader("attachment_history.zip")
	if err != nil {
		_ = backupFS.Close()
		t.Fatal(err)
	}
	backupBytes, readBackupErr := io.ReadAll(backupReader)
	closeBackupErr := backupReader.Close()
	if readBackupErr != nil || closeBackupErr != nil {
		_ = backupFS.Close()
		t.Fatalf(
			"read attachment history backup: read=%v close=%v",
			readBackupErr, closeBackupErr,
		)
	}
	if closeErr := backupFS.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	archive, err := zip.NewReader(
		bytes.NewReader(backupBytes), int64(len(backupBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	versionCollection, err := app.FindCollectionByNameOrId(
		"vibetable_attachment_versions",
	)
	if err != nil {
		t.Fatal(err)
	}
	versionBlobFound := false
	for _, entry := range archive.File {
		if strings.Contains(entry.Name, versionCollection.Id) &&
			strings.HasSuffix(entry.Name, version.GetString("blob")) {
			versionBlobFound = true
		}
	}
	if !versionBlobFound {
		t.Fatal("backup archive does not contain attachment version binary")
	}

	preview, err := history.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil || !preview.CanApply ||
		!containsString(preview.Restorable, "documents") {
		t.Fatalf("attachment restore preview %#v err=%v", preview, err)
	}
	failAfterMetadata = true
	_, err = history.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: recordID, Token: preview.Token,
	})
	failAfterMetadata = false
	if !errors.As(err, &historyErr) ||
		historyErr.Code != "restore_validation_failed" {
		t.Fatalf("attachment restore fault %#v", err)
	}
	record, _ = app.FindRecordById(collection, recordID)
	if got := record.GetStringSlice("documents"); len(got) != 1 ||
		got[0] != replaced[0] {
		t.Fatalf("failed restore did not roll back %#v", got)
	}
	assertRecordCount(t, app, "vibetable_attachment_versions", 1)

	preview, err = history.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: recordID, Token: preview.Token,
	}); err != nil {
		t.Fatalf("restore replaced attachment: %#v", err)
	}
	record, _ = app.FindRecordById(collection, recordID)
	restoredNames := record.GetStringSlice("documents")
	if len(restoredNames) != 1 {
		t.Fatalf("restored attachment names %#v", restoredNames)
	}
	restoredRefs, err := manager.Refs(
		ctx, app, definition, record, "documents_id",
	)
	if err != nil || len(restoredRefs) != 1 ||
		restoredRefs[0].SHA256 != refs[0].SHA256 {
		t.Fatalf("restored attachment refs %#v err=%v", restoredRefs, err)
	}
	restoredDownload, err := manager.Open(
		ctx, app, restoredRefs[0].DownloadCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	restoredContent, readErr := io.ReadAll(restoredDownload.Reader)
	closeErr = restoredDownload.Reader.Close()
	if readErr != nil || closeErr != nil ||
		string(restoredContent) != "first file" {
		t.Fatalf(
			"restored attachment content=%q read=%v close=%v",
			restoredContent, readErr, closeErr,
		)
	}
	if _, err := apply("clear", mutation.Operation{
		Kind: mutation.OperationSetAttachments, RecordID: &recordID,
		FieldID: "documents_id", UploadHandles: []string{},
		RemoveStoredNames: restoredNames,
	}); err != nil {
		t.Fatalf("clear restored attachment: %#v", err)
	}
	record, _ = app.FindRecordById(collection, recordID)
	if got := record.GetStringSlice("documents"); len(got) != 0 {
		t.Fatalf("cleared attachment names %#v", got)
	}
	preview, err = history.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: target.Id, Scope: "row",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: recordID, Token: preview.Token,
	}); err != nil {
		t.Fatalf("restore cleared attachment: %#v", err)
	}
	if _, err := apply("delete", mutation.Operation{
		Kind: mutation.OperationDelete, RecordID: &recordID,
	}); err != nil {
		t.Fatalf("delete attachment record: %#v", err)
	}
	assertRecordCount(t, app, "vibetable_attachment_meta", 0)
	deleteEvent, err := app.FindFirstRecordByFilter(
		"vibetable_audit_events",
		"request_id={:request}",
		map[string]any{"request": "request_delete"},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err = history.PreviewRestore(ctx, audit.PreviewParams{
		TableID: definition.TableID, ItemID: recordID,
		TargetRevision: deleteEvent.Id, Scope: "row",
	})
	if err != nil || !preview.CanApply {
		t.Fatalf("deleted attachment preview %#v err=%v", preview, err)
	}
	if _, err := history.ApplyRestore(ctx, audit.ApplyParams{
		TableID: definition.TableID, ItemID: recordID, Token: preview.Token,
	}); err != nil {
		t.Fatalf("restore hard-deleted attachment record: %#v", err)
	}
	record, err = app.FindRecordById(collection, recordID)
	if err != nil || len(record.GetStringSlice("documents")) != 1 {
		t.Fatalf("hard-delete attachment restore record=%#v err=%v", record, err)
	}
	orphanContent := []byte("orphan historical binary")
	orphanFile, err := filesystem.NewFileFromBytes(
		orphanContent, "orphan.txt",
	)
	if err != nil {
		t.Fatal(err)
	}
	orphanVersion := core.NewRecord(versionCollection)
	orphanVersion.Set("table_id", definition.TableID)
	orphanVersion.Set("record_id", recordID)
	orphanVersion.Set("field_id", "documents_id")
	orphanVersion.Set("field_name", "documents")
	orphanVersion.Set("stored_name", "orphan_history.txt")
	orphanVersion.Set("original_name", "orphan.txt")
	orphanVersion.Set("mime", "text/plain; charset=utf-8")
	orphanVersion.Set("size", len(orphanContent))
	orphanHash := sha256.Sum256(orphanContent)
	orphanVersion.Set("hash", fmt.Sprintf("%x", orphanHash))
	orphanVersion.Set("blob", orphanFile)
	if err := app.Save(orphanVersion); err != nil {
		t.Fatal(err)
	}
	report, err = manager.Integrity(ctx, app)
	if err != nil || report.Valid || len(report.OrphanVersions) != 1 ||
		report.OrphanVersions[0].Code != "attachment.version_without_history" {
		t.Fatalf("orphan version integrity %#v err=%v", report, err)
	}
	if err := app.Delete(orphanVersion); err != nil {
		t.Fatal(err)
	}
	fsys, err = app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	orphanVersionKey := versionCollection.Id +
		"/orphanversion01/orphan.txt"
	if err := fsys.Upload(
		[]byte("unowned version file"), orphanVersionKey,
	); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = manager.Integrity(ctx, app)
	if err != nil || report.Valid || len(report.OrphanFiles) != 1 ||
		report.OrphanFiles[0].Code != "attachment.version_orphan_file" {
		t.Fatalf("orphan version file integrity %#v err=%v", report, err)
	}
	fsys, err = app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	exists, existsErr := fsys.Exists(orphanVersionKey)
	if existsErr != nil || !exists {
		t.Fatalf(
			"integrity scan silently removed version orphan %q: exists=%v err=%v",
			orphanVersionKey, exists, existsErr,
		)
	}
	if err := fsys.Delete(orphanVersionKey); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = manager.Integrity(ctx, app)
	if err != nil || !report.Valid {
		t.Fatalf("attachment history integrity %#v err=%v", report, err)
	}
}

func TestManagedAttachmentOpenRejectsTamperedProtectedOriginalAndThumbnail(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	fileField := field(
		"images_id", "images", schema.FieldKindAttachment, schema.DataTypeFile,
	)
	fileField.Editor.Kind = "file"
	fileField.AttachmentPolicy = &schema.AttachmentPolicy{
		MaxFiles:          1,
		MaxBytesPerFile:   1 << 20,
		AllowedMIMETypes:  []string{"image/png"},
		ThumbnailVariants: []string{"1x1"},
		Protected:         true,
	}
	fileField.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintAttachment, Policy: fileField.AttachmentPolicy,
	}}
	definition, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"protected_images",
			"protected_images",
			[]schema.FieldDefinition{
				field(
					"title_id",
					"title",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
				fileField,
			},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	manager, err := attachments.New(
		[]byte(strings.Repeat("p", 32)),
		attachments.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	var sequence atomic.Int64
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return now }),
		mutation.WithAttachmentManager(manager),
		mutation.WithIDGenerator(func(kind string) string {
			next := sequence.Add(1)
			switch kind {
			case "record":
				return "thumbrecord0001"
			case "changeSet":
				return fmt.Sprintf("thumb_change_%06d", next)
			case "event":
				return fmt.Sprintf("thumb_event_%06d", next)
			default:
				return fmt.Sprintf("%s_%06d", kind, next)
			}
		}),
	)
	recordID := "thumbrecord0001"
	apply := func(key string, operation mutation.Operation) (mutation.Receipt, error) {
		return kernel.Apply(ctx, mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID:       "request_" + key,
			IdempotencyKey:  "idem_" + key,
			TableID:         definition.TableID,
			SchemaRevision:  definition.SchemaRevision,
			Operations:      []mutation.Operation{operation},
			Actor:           mutation.Actor{Type: "user", ID: "local-user"},
		})
	}
	if _, err := apply("insert", mutation.Operation{
		Kind:     mutation.OperationInsert,
		RecordID: &recordID,
		Values:   map[string]any{"title": "protected image"},
	}); err != nil {
		t.Fatal(err)
	}

	imageContent := managedAttachmentPNG(t)
	if err := manager.Stage(
		"protected_image", "protected.png", imageContent,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := apply("upload", mutation.Operation{
		Kind:              mutation.OperationSetAttachments,
		RecordID:          &recordID,
		FieldID:           "images_id",
		UploadHandles:     []string{"protected_image"},
		RemoveStoredNames: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	manager.Drop("protected_image")
	collection, err := app.FindCollectionByNameOrId("protected_images")
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := manager.Refs(
		ctx, app, definition, record, "images_id",
	)
	if err != nil || len(refs) != 1 || len(refs[0].Thumbnails) != 1 {
		t.Fatalf("protected image refs %#v err=%v", refs, err)
	}
	original, err := manager.Open(
		ctx, app, refs[0].DownloadCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	originalContent, readErr := io.ReadAll(original.Reader)
	closeErr := original.Reader.Close()
	if readErr != nil || closeErr != nil ||
		!bytes.Equal(originalContent, imageContent) {
		t.Fatalf(
			"original read=%v close=%v equal=%v",
			readErr,
			closeErr,
			bytes.Equal(originalContent, imageContent),
		)
	}
	thumbnail, err := manager.Open(
		ctx, app, refs[0].Thumbnails[0].DownloadCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	thumbnailContent, readErr := io.ReadAll(thumbnail.Reader)
	closeErr = thumbnail.Reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("thumbnail read=%v close=%v", readErr, closeErr)
	}
	if _, decodeErr := png.Decode(bytes.NewReader(thumbnailContent)); decodeErr != nil {
		t.Fatalf("decode generated thumbnail: %v", decodeErr)
	}
	verifiedSnapshot, err := manager.Open(
		ctx, app, refs[0].DownloadCapability,
	)
	if err != nil {
		t.Fatal(err)
	}

	storedNames := record.GetStringSlice("images")
	if len(storedNames) != 1 {
		t.Fatalf("stored protected image names %#v", storedNames)
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	managedKey := record.BaseFilesPath() + "/" + storedNames[0]
	sameSizeTamper := append([]byte(nil), imageContent...)
	sameSizeTamper[len(sameSizeTamper)/2] ^= 0xff
	if err := fsys.Upload(sameSizeTamper, managedKey); err != nil {
		t.Fatal(err)
	}
	snapshotContent, readErr := io.ReadAll(verifiedSnapshot.Reader)
	closeErr = verifiedSnapshot.Reader.Close()
	if readErr != nil || closeErr != nil ||
		!bytes.Equal(snapshotContent, imageContent) {
		t.Fatalf(
			"verified snapshot changed after source tamper: read=%v close=%v equal=%v",
			readErr,
			closeErr,
			bytes.Equal(snapshotContent, imageContent),
		)
	}
	assertAttachmentOpenError(
		t,
		func() error {
			_, openErr := manager.Open(
				ctx, app, refs[0].DownloadCapability,
			)
			return openErr
		},
		"attachment.hash_mismatch",
	)
	if err := fsys.Upload(imageContent[:len(imageContent)-1], managedKey); err != nil {
		t.Fatal(err)
	}
	assertAttachmentOpenError(
		t,
		func() error {
			_, openErr := manager.Open(
				ctx, app, refs[0].Thumbnails[0].DownloadCapability,
			)
			return openErr
		},
		"attachment.size_mismatch",
	)
}

func managedAttachmentPNG(t *testing.T) []byte {
	t.Helper()
	source := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			source.Set(
				x,
				y,
				color.NRGBA{
					R: uint8(40 + x*30),
					G: uint8(70 + y*20),
					B: 180,
					A: 255,
				},
			)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func assertAttachmentOpenError(
	t *testing.T,
	open func() error,
	wantCode string,
) {
	t.Helper()
	err := open()
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != wantCode {
		t.Fatalf("open error = %#v, want %s", err, wantCode)
	}
}

func TestManagedAttachmentsRejectsDetectedMIMEAndLimits(t *testing.T) {
	manager, err := attachments.New([]byte(strings.Repeat("c", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stage("same", "one.txt", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stage("same", "two.txt", []byte("two")); err == nil {
		t.Fatal("duplicate staged handle was accepted")
	}
	manager.Drop("same")
	if err := manager.Stage("empty", "empty.txt", nil); err == nil {
		t.Fatal("empty upload was accepted")
	}
}
