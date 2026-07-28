package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	pbarchive "github.com/pocketbase/pocketbase/tools/archive"
	"github.com/pocketbase/pocketbase/tools/hook"

	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

const (
	attachmentFaultTableID    = "attachment_faults"
	attachmentFaultCollection = "attachment_faults"
	attachmentFaultFieldID    = "documents_id"
	attachmentFaultRecordID   = "faultrecord0001"
	attachmentCrashEnv        = "VIBETABLE_ATTACHMENT_CRASH_HELPER"
	attachmentCrashDataEnv    = "VIBETABLE_ATTACHMENT_CRASH_DATA"
	attachmentCrashMarkerEnv  = "VIBETABLE_ATTACHMENT_CRASH_MARKER"
)

type attachmentFaultFixture struct {
	app        *pocketbase.PocketBase
	manager    *attachments.Manager
	definition schema.TableDefinition
	apply      func(string, mutation.Operation) (mutation.Receipt, error)
}

func newAttachmentFaultFixture(
	t *testing.T,
	dataDir string,
	options ...attachments.Option,
) attachmentFaultFixture {
	t.Helper()
	app := bootstrapApp(t, dataDir)
	fileField := field(
		attachmentFaultFieldID,
		"documents",
		schema.FieldKindAttachment,
		schema.DataTypeFile,
	)
	fileField.Editor.Kind = "file"
	fileField.AttachmentPolicy = &schema.AttachmentPolicy{
		MaxFiles:          4,
		MaxBytesPerFile:   4096,
		AllowedMIMETypes:  []string{"text/plain"},
		ThumbnailVariants: []string{},
		Protected:         true,
	}
	fileField.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintAttachment, Policy: fileField.AttachmentPolicy,
	}}
	definition, err := schemaapi.New(app).ApplyChange(
		context.Background(),
		schemaapi.Change{
			Definition: baseTable(
				attachmentFaultTableID,
				attachmentFaultCollection,
				[]schema.FieldDefinition{
					field("title_id", "title", schema.FieldKindScalar, schema.DataTypeShortText),
					fileField,
				},
			),
			ExpectedRevision: 0,
		},
	)
	if err != nil {
		resetApp(t, app)
		t.Fatalf("create attachment fault table: %v", err)
	}
	manager, err := attachments.New(
		[]byte(strings.Repeat("f", 32)),
		options...,
	)
	if err != nil {
		resetApp(t, app)
		t.Fatal(err)
	}
	var sequence atomic.Int64
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithAttachmentManager(manager),
		mutation.WithClock(func() time.Time {
			return time.Date(2026, 7, 25, 2, 0, 0, 0, time.UTC)
		}),
		mutation.WithIDGenerator(func(kind string) string {
			next := sequence.Add(1)
			switch kind {
			case "record":
				return attachmentFaultRecordID
			case "changeSet":
				return fmt.Sprintf("flt_change_%05d", next)
			case "event":
				return fmt.Sprintf("flt_event_%06d", next)
			default:
				return fmt.Sprintf("flt_%s_%06d", kind, next)
			}
		}),
	)
	apply := func(key string, operation mutation.Operation) (mutation.Receipt, error) {
		return kernel.Apply(context.Background(), mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID:       "request_" + key,
			IdempotencyKey:  "idem_" + key,
			TableID:         definition.TableID,
			SchemaRevision:  definition.SchemaRevision,
			Operations:      []mutation.Operation{operation},
			Actor:           mutation.Actor{Type: "user", ID: "fault-test"},
		})
	}
	return attachmentFaultFixture{
		app: app, manager: manager, definition: definition, apply: apply,
	}
}

func (fixture attachmentFaultFixture) insert(t *testing.T) {
	t.Helper()
	recordID := attachmentFaultRecordID
	if _, err := fixture.apply("insert", mutation.Operation{
		Kind: mutation.OperationInsert, RecordID: &recordID,
		Values: map[string]any{"title": "fault fixture"},
	}); err != nil {
		t.Fatalf("insert attachment fault record: %v", err)
	}
}

func (fixture attachmentFaultFixture) setAttachments(
	t *testing.T,
	key string,
	handles []string,
	removed []string,
) error {
	t.Helper()
	_, err := fixture.apply(key, mutation.Operation{
		Kind:              mutation.OperationSetAttachments,
		RecordID:          stringPointerForAttachmentFault(attachmentFaultRecordID),
		FieldID:           attachmentFaultFieldID,
		UploadHandles:     handles,
		RemoveStoredNames: removed,
	})
	return err
}

func stringPointerForAttachmentFault(value string) *string {
	return &value
}

func attachmentFaultRecord(
	t *testing.T,
	app core.App,
) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(attachmentFaultCollection)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(collection, attachmentFaultRecordID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func deleteAttachmentFaultObject(
	t *testing.T,
	app core.App,
	record *core.Record,
	storedName string,
) {
	t.Helper()
	fsys, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	if err := fsys.Delete(record.BaseFilesPath() + "/" + storedName); err != nil {
		t.Fatalf("delete recoverable orphan %q: %v", storedName, err)
	}
}

func TestManagedAttachmentsMultiFileMidwayFailureRollsBackAtomically(t *testing.T) {
	failAfterFirstMetadata := false
	metadataWrites := 0
	fixture := newAttachmentFaultFixture(
		t,
		queryTempDir(t),
		attachments.WithFaultInjector(func(point string) error {
			if failAfterFirstMetadata && point == "after_metadata_write" {
				metadataWrites++
				if metadataWrites == 1 {
					return errors.New("injected multi-file metadata failure")
				}
			}
			return nil
		}),
	)
	defer resetApp(t, fixture.app)
	fixture.insert(t)
	if err := fixture.manager.Stage("multi_one", "one.txt", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Stage("multi_two", "two.txt", []byte("two")); err != nil {
		t.Fatal(err)
	}
	failAfterFirstMetadata = true
	err := fixture.setAttachments(
		t,
		"multi_midway_failure",
		[]string{"multi_one", "multi_two"},
		nil,
	)
	failAfterFirstMetadata = false
	fixture.manager.Drop("multi_one", "multi_two")
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.attachment.failed" ||
		metadataWrites != 1 {
		t.Fatalf("multi-file failure error=%#v metadataWrites=%d", err, metadataWrites)
	}
	record := attachmentFaultRecord(t, fixture.app)
	if got := record.GetStringSlice("documents"); len(got) != 0 {
		t.Fatalf("partially committed attachment names: %#v", got)
	}
	assertRecordCount(t, fixture.app, "vibetable_attachment_meta", 0)
	report, err := fixture.manager.Integrity(context.Background(), fixture.app)
	if err != nil || !report.Valid {
		t.Fatalf("multi-file rollback integrity=%#v err=%v", report, err)
	}
}

func TestManagedAttachmentsCommittedDBCleanupFailureIsReportedAndRecoverable(t *testing.T) {
	fixture := newAttachmentFaultFixture(t, queryTempDir(t))
	defer resetApp(t, fixture.app)
	fixture.insert(t)
	if err := fixture.manager.Stage("cleanup_old", "old.txt", []byte("old content")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.setAttachments(t, "cleanup_old", []string{"cleanup_old"}, nil); err != nil {
		t.Fatal(err)
	}
	fixture.manager.Drop("cleanup_old")
	before := attachmentFaultRecord(t, fixture.app)
	oldNames := before.GetStringSlice("documents")
	if len(oldNames) != 1 {
		t.Fatalf("old attachment names=%#v", oldNames)
	}
	fixture.app.OnRecordAfterUpdateSuccess().Bind(
		&hook.Handler[*core.RecordEvent]{
			Id:       "attachment-cleanup-failure",
			Priority: -100,
			Func: func(event *core.RecordEvent) error {
				if event.Record.Collection().Name == attachmentFaultCollection {
					return errors.New("injected post-commit cleanup failure")
				}
				return event.Next()
			},
		},
	)
	if err := fixture.manager.Stage("cleanup_new", "new.txt", []byte("new content")); err != nil {
		t.Fatal(err)
	}
	err := fixture.setAttachments(
		t,
		"cleanup_new",
		[]string{"cleanup_new"},
		oldNames,
	)
	fixture.manager.Drop("cleanup_new")
	fixture.app.OnRecordAfterUpdateSuccess().Unbind("attachment-cleanup-failure")
	if err == nil {
		t.Fatal("post-commit cleanup failure was not returned")
	}
	committed := attachmentFaultRecord(t, fixture.app)
	newNames := committed.GetStringSlice("documents")
	if len(newNames) != 1 || newNames[0] == oldNames[0] {
		t.Fatalf("database replacement did not commit: %#v", newNames)
	}
	report, integrityErr := fixture.manager.Integrity(context.Background(), fixture.app)
	if integrityErr != nil || report.Valid || len(report.OrphanFiles) != 1 ||
		report.OrphanFiles[0].StoredName != oldNames[0] {
		t.Fatalf("cleanup failure report=%#v err=%v", report, integrityErr)
	}
	deleteAttachmentFaultObject(t, fixture.app, committed, oldNames[0])
	report, integrityErr = fixture.manager.Integrity(context.Background(), fixture.app)
	if integrityErr != nil || !report.Valid {
		t.Fatalf("cleanup recovery integrity=%#v err=%v", report, integrityErr)
	}
}

func runAttachmentCrashHelper(
	t *testing.T,
	phase string,
	dataDir string,
	marker string,
	wantExit int,
) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestManagedAttachmentsCrashHelper$")
	command.Env = append(
		os.Environ(),
		attachmentCrashEnv+"="+phase,
		attachmentCrashDataEnv+"="+dataDir,
		attachmentCrashMarkerEnv+"="+marker,
	)
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != wantExit {
		t.Fatalf(
			"crash helper phase=%s exit=%v want=%d output=%s",
			phase,
			err,
			wantExit,
			output,
		)
	}
}

func TestManagedAttachmentsProcessExitDuringUploadLeavesNoDurableState(t *testing.T) {
	dataDir := queryTempDir(t)
	runAttachmentCrashHelper(t, "upload", dataDir, "", 31)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	manager, err := attachments.New([]byte(strings.Repeat("f", 32)))
	if err != nil {
		t.Fatal(err)
	}
	assertRecordCount(t, app, attachmentFaultCollection, 0)
	assertRecordCount(t, app, "vibetable_attachment_meta", 0)
	report, err := manager.Integrity(context.Background(), app)
	if err != nil || !report.Valid {
		t.Fatalf("upload-exit integrity=%#v err=%v", report, err)
	}
}

func TestManagedAttachmentsProcessExitDuringCommitReportsAndRecoversOrphans(t *testing.T) {
	dataDir := queryTempDir(t)
	runAttachmentCrashHelper(t, "commit", dataDir, "", 32)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	manager, err := attachments.New([]byte(strings.Repeat("f", 32)))
	if err != nil {
		t.Fatal(err)
	}
	record := attachmentFaultRecord(t, app)
	if got := record.GetStringSlice("documents"); len(got) != 0 {
		t.Fatalf("uncommitted DB attachment survived process exit: %#v", got)
	}
	report, err := manager.Integrity(context.Background(), app)
	if err != nil || report.Valid || len(report.OrphanFiles) != 2 {
		t.Fatalf("commit-exit report=%#v err=%v", report, err)
	}
	for _, issue := range report.OrphanFiles {
		deleteAttachmentFaultObject(t, app, record, issue.StoredName)
	}
	report, err = manager.Integrity(context.Background(), app)
	if err != nil || !report.Valid {
		t.Fatalf("commit-exit recovery integrity=%#v err=%v", report, err)
	}
}

func TestManagedAttachmentsProcessExitDuringCleanupReportsAndRecoversOrphans(t *testing.T) {
	dataDir := queryTempDir(t)
	marker := filepath.Join(dataDir, "cleanup-old-name.txt")
	runAttachmentCrashHelper(t, "cleanup", dataDir, marker, 33)
	oldNameBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	oldName := strings.TrimSpace(string(oldNameBytes))
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	manager, err := attachments.New([]byte(strings.Repeat("f", 32)))
	if err != nil {
		t.Fatal(err)
	}
	record := attachmentFaultRecord(t, app)
	newNames := record.GetStringSlice("documents")
	if len(newNames) != 1 || newNames[0] == oldName {
		t.Fatalf("cleanup-exit DB replacement=%#v old=%q", newNames, oldName)
	}
	report, err := manager.Integrity(context.Background(), app)
	if err != nil || report.Valid || len(report.OrphanFiles) != 1 ||
		report.OrphanFiles[0].StoredName != oldName {
		t.Fatalf("cleanup-exit report=%#v err=%v", report, err)
	}
	deleteAttachmentFaultObject(t, app, record, oldName)
	report, err = manager.Integrity(context.Background(), app)
	if err != nil || !report.Valid {
		t.Fatalf("cleanup-exit recovery integrity=%#v err=%v", report, err)
	}
}

func TestManagedAttachmentsCrashHelper(t *testing.T) {
	phase := os.Getenv(attachmentCrashEnv)
	if phase == "" {
		t.Skip("invoked only by process-exit attachment tests")
	}
	dataDir := os.Getenv(attachmentCrashDataEnv)
	fixtureOptions := []attachments.Option{}
	if phase == "commit" {
		fixtureOptions = append(
			fixtureOptions,
			attachments.WithFaultInjector(func(point string) error {
				if point == "after_metadata" {
					os.Exit(32)
				}
				return nil
			}),
		)
	}
	fixture := newAttachmentFaultFixture(t, dataDir, fixtureOptions...)
	switch phase {
	case "upload":
		if err := fixture.manager.Stage(
			"crash_upload_one",
			"upload-one.txt",
			[]byte("upload one"),
		); err != nil {
			t.Fatal(err)
		}
		if err := fixture.manager.Stage(
			"crash_upload_two",
			"upload-two.txt",
			[]byte("upload two"),
		); err != nil {
			t.Fatal(err)
		}
		os.Exit(31)
	case "commit":
		fixture.insert(t)
		if err := fixture.manager.Stage(
			"crash_commit_one",
			"commit-one.txt",
			[]byte("commit one"),
		); err != nil {
			t.Fatal(err)
		}
		if err := fixture.manager.Stage(
			"crash_commit_two",
			"commit-two.txt",
			[]byte("commit two"),
		); err != nil {
			t.Fatal(err)
		}
		if err := fixture.setAttachments(
			t,
			"crash_commit",
			[]string{"crash_commit_one", "crash_commit_two"},
			nil,
		); err != nil {
			t.Fatal(err)
		}
	case "cleanup":
		fixture.insert(t)
		if err := fixture.manager.Stage(
			"crash_cleanup_old",
			"cleanup-old.txt",
			[]byte("cleanup old"),
		); err != nil {
			t.Fatal(err)
		}
		if err := fixture.setAttachments(
			t,
			"crash_cleanup_old",
			[]string{"crash_cleanup_old"},
			nil,
		); err != nil {
			t.Fatal(err)
		}
		fixture.manager.Drop("crash_cleanup_old")
		oldNames := attachmentFaultRecord(t, fixture.app).GetStringSlice("documents")
		if len(oldNames) != 1 {
			t.Fatalf("cleanup helper old names=%#v", oldNames)
		}
		if err := os.WriteFile(
			os.Getenv(attachmentCrashMarkerEnv),
			[]byte(oldNames[0]+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		fixture.app.OnRecordAfterUpdateSuccess().Bind(
			&hook.Handler[*core.RecordEvent]{
				Id:       "attachment-cleanup-crash",
				Priority: -100,
				Func: func(event *core.RecordEvent) error {
					if event.Record.Collection().Name == attachmentFaultCollection {
						os.Exit(33)
					}
					return event.Next()
				},
			},
		)
		if err := fixture.manager.Stage(
			"crash_cleanup_new",
			"cleanup-new.txt",
			[]byte("cleanup new"),
		); err != nil {
			t.Fatal(err)
		}
		if err := fixture.setAttachments(
			t,
			"crash_cleanup_new",
			[]string{"crash_cleanup_new"},
			oldNames,
		); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown attachment crash phase %q", phase)
	}
	t.Fatalf("attachment crash phase %q did not terminate the helper", phase)
}

func attachmentAuditFingerprint(t *testing.T, app core.App) []string {
	t.Helper()
	events, err := app.FindRecordsByFilter(
		"vibetable_audit_events",
		"table_id={:table}",
		"",
		0,
		0,
		map[string]any{"table": attachmentFaultTableID},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0, len(events))
	for _, event := range events {
		raw, err := json.Marshal(map[string]any{
			"id":         event.Id,
			"requestId":  event.GetString("request_id"),
			"operation":  event.GetString("operation"),
			"beforeJson": event.GetRaw("before_json"),
			"afterJson":  event.GetRaw("after_json"),
		})
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		result = append(result, hex.EncodeToString(sum[:]))
	}
	sort.Strings(result)
	return result
}

func TestManagedAttachmentsWholeBackupRestorePreservesReferencesHashesContentAndAudit(t *testing.T) {
	sourceDir := queryTempDir(t)
	fixture := newAttachmentFaultFixture(t, sourceDir)
	fixture.insert(t)
	if err := fixture.manager.Stage(
		"backup_old",
		"backup-old.txt",
		[]byte("backup old content"),
	); err != nil {
		t.Fatal(err)
	}
	if err := fixture.setAttachments(t, "backup_old", []string{"backup_old"}, nil); err != nil {
		t.Fatal(err)
	}
	fixture.manager.Drop("backup_old")
	oldNames := attachmentFaultRecord(t, fixture.app).GetStringSlice("documents")
	if err := fixture.manager.Stage(
		"backup_new",
		"backup-new.txt",
		[]byte("backup new content"),
	); err != nil {
		t.Fatal(err)
	}
	if err := fixture.setAttachments(
		t,
		"backup_new",
		[]string{"backup_new"},
		oldNames,
	); err != nil {
		t.Fatal(err)
	}
	fixture.manager.Drop("backup_new")
	sourceRecord := attachmentFaultRecord(t, fixture.app)
	sourceRefs, err := fixture.manager.Refs(
		context.Background(),
		fixture.app,
		fixture.definition,
		sourceRecord,
		attachmentFaultFieldID,
	)
	if err != nil || len(sourceRefs) != 1 {
		t.Fatalf("source refs=%#v err=%v", sourceRefs, err)
	}
	sourceDownload, err := fixture.manager.Open(
		context.Background(),
		fixture.app,
		sourceRefs[0].DownloadCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	sourceContent, readErr := io.ReadAll(sourceDownload.Reader)
	closeErr := sourceDownload.Reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read source attachment: read=%v close=%v", readErr, closeErr)
	}
	sourceAudit := attachmentAuditFingerprint(t, fixture.app)
	backupIntegrity, err := fixture.manager.Integrity(
		context.Background(),
		fixture.app,
	)
	if err != nil || !backupIntegrity.Valid {
		t.Fatalf("attachment integrity=%#v err=%v", backupIntegrity, err)
	}
	const backupName = "attachment_fault_backup.zip"
	if err := fixture.app.CreateBackup(
		context.Background(),
		backupName,
	); err != nil {
		t.Fatalf("create PocketBase archive primitive: %v", err)
	}
	backupFS, err := fixture.app.NewBackupsFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := backupFS.GetReader(backupName)
	if err != nil {
		_ = backupFS.Close()
		t.Fatal(err)
	}
	archiveBytes, readErr := io.ReadAll(reader)
	closeReaderErr := reader.Close()
	closeBackupErr := backupFS.Close()
	if readErr != nil || closeReaderErr != nil || closeBackupErr != nil {
		t.Fatalf(
			"read whole backup read=%v closeReader=%v closeFS=%v",
			readErr,
			closeReaderErr,
			closeBackupErr,
		)
	}
	resetApp(t, fixture.app)

	archivePath := filepath.Join(t.TempDir(), "attachment-fault-backup.zip")
	if err := os.WriteFile(archivePath, archiveBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	restoredDir := queryTempDir(t)
	if err := pbarchive.Extract(archivePath, restoredDir); err != nil {
		t.Fatalf("extract whole backup: %v", err)
	}
	restoredApp := bootstrapApp(t, restoredDir)
	defer resetApp(t, restoredApp)
	restoredManager, err := attachments.New([]byte(strings.Repeat("f", 32)))
	if err != nil {
		t.Fatal(err)
	}
	restoredRecord := attachmentFaultRecord(t, restoredApp)
	restoredRefs, err := restoredManager.Refs(
		context.Background(),
		restoredApp,
		fixture.definition,
		restoredRecord,
		attachmentFaultFieldID,
	)
	if err != nil || len(restoredRefs) != 1 {
		t.Fatalf("restored refs=%#v err=%v", restoredRefs, err)
	}
	if restoredRefs[0].StoredName != sourceRefs[0].StoredName ||
		restoredRefs[0].OriginalName != sourceRefs[0].OriginalName ||
		restoredRefs[0].SHA256 != sourceRefs[0].SHA256 ||
		restoredRefs[0].Size != sourceRefs[0].Size {
		t.Fatalf("restored reference mismatch source=%#v restored=%#v", sourceRefs[0], restoredRefs[0])
	}
	restoredDownload, err := restoredManager.Open(
		context.Background(),
		restoredApp,
		restoredRefs[0].DownloadCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	restoredContent, readErr := io.ReadAll(restoredDownload.Reader)
	closeErr = restoredDownload.Reader.Close()
	if readErr != nil || closeErr != nil ||
		!bytes.Equal(restoredContent, sourceContent) {
		t.Fatalf(
			"restored content=%q source=%q read=%v close=%v",
			restoredContent,
			sourceContent,
			readErr,
			closeErr,
		)
	}
	if restoredAudit := attachmentAuditFingerprint(t, restoredApp); !reflect.DeepEqual(restoredAudit, sourceAudit) {
		t.Fatalf("restored audit fingerprint=%#v source=%#v", restoredAudit, sourceAudit)
	}
	report, err := restoredManager.Integrity(context.Background(), restoredApp)
	if err != nil || !report.Valid || report.CheckedMetadata != 1 ||
		report.CheckedVersions < 1 {
		t.Fatalf("restored integrity=%#v err=%v", report, err)
	}
}
