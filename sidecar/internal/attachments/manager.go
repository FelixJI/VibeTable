package attachments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gabriel-vasile/mimetype"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

const (
	maxStagedFiles      = 1000
	maxStagedBytes      = 100 << 20
	maxTotalStagedBytes = 256 << 20
	capabilityTTL       = 5 * time.Minute
)

var (
	handlePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	hashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type stagedFile struct {
	handle       string
	originalName string
	data         []byte
	mime         string
	size         int64
	sha256       string
	references   int
}

type Manager struct {
	secret []byte
	now    func() time.Time
	fault  func(string) error

	mu     sync.RWMutex
	staged map[string]stagedFile
	bytes  int64
}

type Option func(*Manager)

func WithClock(clock func() time.Time) Option {
	return func(manager *Manager) { manager.now = clock }
}

func WithFaultInjector(injector func(string) error) Option {
	return func(manager *Manager) { manager.fault = injector }
}

func New(secret []byte, options ...Option) (*Manager, error) {
	if len(secret) < 32 {
		return nil, errors.New("attachment capability secret must contain at least 32 bytes")
	}
	manager := &Manager{
		secret: append([]byte(nil), secret...),
		now:    func() time.Time { return time.Now().UTC() },
		fault:  func(string) error { return nil },
		staged: map[string]stagedFile{},
	}
	for _, option := range options {
		option(manager)
	}
	return manager, nil
}

func (manager *Manager) Stage(
	handle, originalName string,
	content []byte,
) error {
	return manager.stage(handle, originalName, content, true)
}

// StageOwned stages content whose backing slice ownership is transferred to the
// manager. Callers must not read or mutate content after this call.
func (manager *Manager) StageOwned(
	handle, originalName string,
	content []byte,
) error {
	return manager.stage(handle, originalName, content, false)
}

func (manager *Manager) stage(
	handle, originalName string,
	content []byte,
	copyContent bool,
) error {
	if !handlePattern.MatchString(handle) || strings.TrimSpace(originalName) == "" {
		return attachmentError(
			"mutation.attachment.invalid_upload", "upload handle and original name are required", false,
		)
	}
	if len(content) == 0 || len(content) > maxStagedBytes {
		return attachmentError(
			"mutation.attachment.invalid_upload", "upload size is outside the supported limit", false,
		)
	}
	sum := sha256.Sum256(content)
	safeOriginalName := path.Base(strings.ReplaceAll(originalName, "\\", "/"))
	if safeOriginalName == "." || safeOriginalName == "/" ||
		len([]byte(safeOriginalName)) > 255 ||
		!utf8.ValidString(safeOriginalName) ||
		strings.IndexFunc(safeOriginalName, unicode.IsControl) >= 0 {
		return attachmentError(
			"mutation.attachment.invalid_upload", "upload filename is invalid", false,
		)
	}
	sumHex := hex.EncodeToString(sum[:])
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if staged, exists := manager.staged[handle]; exists {
		if staged.originalName == safeOriginalName &&
			staged.sha256 == sumHex &&
			bytes.Equal(staged.data, content) {
			staged.references++
			manager.staged[handle] = staged
			return nil
		}
		return attachmentError(
			"mutation.attachment.handle_conflict", "upload handle is already staged", false,
		)
	}
	if len(manager.staged) >= maxStagedFiles {
		return attachmentError(
			"mutation.attachment.stage_limit", "too many files are staged", true,
		)
	}
	if manager.bytes+int64(len(content)) > maxTotalStagedBytes {
		return attachmentError(
			"mutation.attachment.stage_limit", "staged uploads exceed the memory limit", true,
		)
	}
	stored := content
	if copyContent {
		stored = append([]byte(nil), content...)
	}
	manager.staged[handle] = stagedFile{
		handle: handle, originalName: safeOriginalName, data: stored,
		mime: mimetype.Detect(stored).String(), size: int64(len(stored)),
		sha256: sumHex, references: 1,
	}
	manager.bytes += int64(len(stored))
	return nil
}

func (manager *Manager) Drop(handles ...string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, handle := range handles {
		if staged, exists := manager.staged[handle]; exists {
			if staged.references > 1 {
				staged.references--
				manager.staged[handle] = staged
				continue
			}
			manager.bytes -= staged.size
		}
		delete(manager.staged, handle)
	}
}

func (manager *Manager) Prepare(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
	change mutation.AttachmentChange,
) (mutation.AttachmentFinalizer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	field, err := attachmentField(definition, change.FieldID)
	if err != nil {
		return nil, err
	}
	policy := field.AttachmentPolicy
	if policy == nil {
		return nil, attachmentError(
			"mutation.attachment.invalid_policy", "attachment policy is unavailable", false,
		)
	}
	if duplicates(change.UploadHandles) || duplicates(change.RemoveStoredNames) {
		return nil, attachmentError(
			"mutation.attachment.duplicate", "attachment operation contains duplicate entries", false,
		)
	}
	manager.mu.RLock()
	uploads := make([]stagedFile, 0, len(change.UploadHandles))
	for _, handle := range change.UploadHandles {
		staged, ok := manager.staged[handle]
		if !ok {
			manager.mu.RUnlock()
			return nil, attachmentError(
				"mutation.attachment.handle_not_found", "staged upload handle was not found", false,
			)
		}
		uploads = append(uploads, staged)
	}
	manager.mu.RUnlock()

	currentNames := fileNames(record.GetRaw(field.PhysicalName))
	currentSet := make(map[string]struct{}, len(currentNames))
	for _, name := range currentNames {
		currentSet[name] = struct{}{}
	}
	removeSet := make(map[string]struct{}, len(change.RemoveStoredNames))
	for _, name := range change.RemoveStoredNames {
		if _, exists := currentSet[name]; !exists {
			return nil, attachmentError(
				"mutation.attachment.stored_name_not_found", "attachment to remove was not found", false,
			)
		}
		removeSet[name] = struct{}{}
	}
	for _, upload := range uploads {
		if upload.size > policy.MaxBytesPerFile {
			return nil, attachmentError(
				"mutation.attachment.too_large", "attachment exceeds maxBytesPerFile", false,
			)
		}
		if !mimeAllowed(upload.mime, policy.AllowedMIMETypes) {
			return nil, attachmentError(
				"mutation.attachment.mime_not_allowed", "detected MIME type is not allowed", false,
			)
		}
	}
	if err := manager.archiveNames(
		ctx, app, definition, record, field, change.RemoveStoredNames,
	); err != nil {
		return nil, err
	}
	remaining := make([]any, 0, len(currentNames)+len(uploads))
	for _, name := range currentNames {
		if _, remove := removeSet[name]; !remove {
			remaining = append(remaining, name)
		}
	}
	if len(remaining)+len(uploads) > policy.MaxFiles {
		return nil, attachmentError(
			"mutation.attachment.too_many", "attachment count exceeds maxFiles", false,
		)
	}
	files := make([]*filesystem.File, 0, len(uploads))
	for _, upload := range uploads {
		file, err := filesystem.NewFileFromBytes(upload.data, upload.originalName)
		if err != nil {
			return nil, attachmentError(
				"mutation.attachment.invalid_upload", "attachment could not be prepared", false,
			)
		}
		files = append(files, file)
		remaining = append(remaining, file)
	}
	record.Set(field.PhysicalName, remaining)
	if err := manager.fault("after_prepare"); err != nil {
		return nil, attachmentError(
			"mutation.attachment.failed", "attachment operation failed", true,
		)
	}

	finalize := func(txApp core.App, saved *core.Record) error {
		if err := manager.updateMetadata(
			txApp, definition.TableID, saved.Id, field,
			change.RemoveStoredNames, uploads, files,
		); err != nil {
			return err
		}
		if err := manager.fault("after_metadata"); err != nil {
			return attachmentError(
				"mutation.attachment.failed", "attachment metadata operation failed", true,
			)
		}
		return nil
	}
	return finalize, nil
}

func (manager *Manager) CleanupRecord(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && record_id={:record}",
		"", 0, 0,
		dbx.Params{"table": definition.TableID, "record": record.Id},
	)
	if err != nil {
		return attachmentError(
			"mutation.attachment.storage_failed", "attachment metadata could not be read", true,
		)
	}
	metadataByIdentity := make(map[string]*core.Record, len(records))
	for _, metadata := range records {
		metadataByIdentity[attachmentIdentity(
			definition.TableID,
			record.Id,
			metadata.GetString("field_id"),
			metadata.GetString("stored_name"),
		)] = metadata
	}
	referenced := map[string]struct{}{}
	for _, field := range definition.Fields {
		if field.Kind != schema.FieldKindAttachment {
			continue
		}
		for _, storedName := range fileNames(
			record.GetRaw(field.PhysicalName),
		) {
			identity := attachmentIdentity(
				definition.TableID,
				record.Id,
				field.FieldID,
				storedName,
			)
			referenced[identity] = struct{}{}
			if metadataByIdentity[identity] == nil {
				return attachmentError(
					"attachment.metadata_missing",
					"attachment metadata is missing",
					false,
				)
			}
		}
	}
	for identity := range metadataByIdentity {
		if _, exists := referenced[identity]; !exists {
			return attachmentError(
				"mutation.attachment.storage_failed",
				"attachment metadata has no record reference",
				false,
			)
		}
	}
	byField := make(map[string][]*core.Record)
	for _, metadata := range records {
		byField[metadata.GetString("field_id")] = append(
			byField[metadata.GetString("field_id")], metadata,
		)
	}
	for fieldID, metadata := range byField {
		field, fieldErr := attachmentField(definition, fieldID)
		if fieldErr != nil {
			return attachmentError(
				"mutation.attachment.storage_failed",
				"attachment field metadata is no longer valid",
				true,
			)
		}
		if err := manager.archiveMetadata(
			ctx, app, definition, record, field, metadata,
		); err != nil {
			return err
		}
	}
	for _, metadata := range records {
		if err := app.Delete(metadata); err != nil {
			return attachmentError(
				"mutation.attachment.storage_failed", "attachment metadata could not be deleted", true,
			)
		}
	}
	return nil
}

func (manager *Manager) archiveNames(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
	field schema.FieldDefinition,
	names []string,
) error {
	if len(names) == 0 {
		return nil
	}
	metadata, err := app.FindRecordsByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && record_id={:record} && field_id={:field}",
		"", 0, 0,
		dbx.Params{
			"table":  definition.TableID,
			"record": record.Id,
			"field":  field.FieldID,
		},
	)
	if err != nil {
		return attachmentError(
			"mutation.attachment.storage_failed",
			"attachment metadata could not be read",
			true,
		)
	}
	byName := make(map[string]*core.Record, len(metadata))
	for _, item := range metadata {
		byName[item.GetString("stored_name")] = item
	}
	selected := make([]*core.Record, 0, len(names))
	for _, name := range names {
		item := byName[name]
		if item == nil {
			return attachmentError(
				"attachment.metadata_missing",
				"attachment metadata is missing",
				false,
			)
		}
		selected = append(selected, item)
	}
	return manager.archiveMetadata(
		ctx, app, definition, record, field, selected,
	)
}

func (manager *Manager) archiveMetadata(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
	field schema.FieldDefinition,
	metadata []*core.Record,
) error {
	if len(metadata) == 0 {
		return nil
	}
	if err := manager.fault("before_version_archive"); err != nil {
		return attachmentError(
			"mutation.attachment.version_archive_failed",
			"attachment history could not be archived",
			true,
		)
	}
	collection, err := app.FindCollectionByNameOrId(
		"vibetable_attachment_versions",
	)
	if err != nil {
		return attachmentError(
			"mutation.attachment.storage_failed",
			"attachment version collection is unavailable",
			true,
		)
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		return attachmentError(
			"mutation.attachment.storage_failed",
			"attachment storage is unavailable",
			true,
		)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	for _, item := range metadata {
		if err := ctx.Err(); err != nil {
			return err
		}
		storedName := item.GetString("stored_name")
		existing, findErr := app.FindFirstRecordByFilter(
			"vibetable_attachment_versions",
			"table_id={:table} && record_id={:record} && field_id={:field} && stored_name={:name}",
			dbx.Params{
				"table": definition.TableID, "record": record.Id,
				"field": field.FieldID, "name": storedName,
			},
		)
		if findErr == nil {
			if existing.GetString("hash") != item.GetString("hash") ||
				int64(existing.GetFloat("size")) != int64(item.GetFloat("size")) ||
				existing.GetString("original_name") !=
					item.GetString("original_name") ||
				existing.GetString("mime") != item.GetString("mime") ||
				existing.GetString("field_name") != field.PhysicalName ||
				existing.GetString("blob") == "" {
				return attachmentError(
					"mutation.attachment.version_conflict",
					"attachment history identity is inconsistent",
					false,
				)
			}
			if verifyErr := verifyObject(
				fsys,
				existing.BaseFilesPath()+"/"+existing.GetString("blob"),
				int64(existing.GetFloat("size")),
				existing.GetString("hash"),
			); verifyErr != nil {
				return attachmentError(
					"mutation.attachment.version_archive_failed",
					"existing attachment history is corrupt",
					false,
				)
			}
			continue
		}
		if !errors.Is(findErr, sql.ErrNoRows) {
			return attachmentError(
				"mutation.attachment.storage_failed",
				"attachment history could not be read",
				true,
			)
		}
		content, readErr := readVerifiedObject(
			fsys,
			record.BaseFilesPath()+"/"+storedName,
			int64(item.GetFloat("size")),
			item.GetString("hash"),
		)
		if readErr != nil {
			return readErr
		}
		blob, fileErr := filesystem.NewFileFromBytes(
			content, item.GetString("original_name"),
		)
		if fileErr != nil {
			return attachmentError(
				"mutation.attachment.version_archive_failed",
				"attachment history file could not be prepared",
				true,
			)
		}
		version := core.NewRecord(collection)
		version.Set("table_id", definition.TableID)
		version.Set("record_id", record.Id)
		version.Set("field_id", field.FieldID)
		version.Set("field_name", field.PhysicalName)
		version.Set("stored_name", storedName)
		version.Set("original_name", item.GetString("original_name"))
		version.Set("mime", item.GetString("mime"))
		version.Set("size", int64(item.GetFloat("size")))
		version.Set("hash", item.GetString("hash"))
		version.Set("blob", blob)
		if err := app.Save(version); err != nil {
			return attachmentError(
				"mutation.attachment.version_archive_failed",
				"attachment history could not be saved",
				true,
			)
		}
	}
	if err := manager.fault("after_version_archive"); err != nil {
		return attachmentError(
			"mutation.attachment.version_archive_failed",
			"attachment history could not be archived",
			true,
		)
	}
	return nil
}

func (manager *Manager) PreviewRestore(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	recordID, fieldID string,
	targetNames, currentNames []string,
) (RestorePlan, error) {
	field, err := attachmentField(definition, fieldID)
	if err != nil {
		return RestorePlan{}, err
	}
	if duplicates(targetNames) || duplicates(currentNames) {
		return RestorePlan{}, attachmentError(
			"attachment.history_invalid",
			"attachment history contains duplicate file names",
			false,
		)
	}
	if field.AttachmentPolicy == nil ||
		len(targetNames) > field.AttachmentPolicy.MaxFiles {
		return RestorePlan{}, attachmentError(
			"attachment.history_policy_mismatch",
			"historical attachments exceed the current field policy",
			false,
		)
	}
	plan := RestorePlan{
		TableID: definition.TableID, RecordID: recordID,
		FieldID:      field.FieldID,
		CurrentNames: append([]string(nil), currentNames...),
		Items:        make([]RestoreItem, 0, len(targetNames)),
	}
	currentSet := make(map[string]struct{}, len(currentNames))
	for _, name := range currentNames {
		currentSet[name] = struct{}{}
	}
	currentMetadata, err := app.FindRecordsByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && record_id={:record} && field_id={:field}",
		"", 0, 0,
		dbx.Params{
			"table":  definition.TableID,
			"record": recordID,
			"field":  field.FieldID,
		},
	)
	if err != nil {
		return RestorePlan{}, attachmentError(
			"attachment.history_unavailable",
			"current attachment metadata could not be read",
			true,
		)
	}
	currentByName := make(map[string]*core.Record, len(currentMetadata))
	for _, item := range currentMetadata {
		currentByName[item.GetString("stored_name")] = item
	}
	var liveRecord *core.Record
	if len(currentNames) > 0 {
		meta, metaErr := app.FindFirstRecordByFilter(
			"vibetable_tables", "table_id={:table}",
			dbx.Params{"table": definition.TableID},
		)
		if metaErr != nil {
			return RestorePlan{}, attachmentError(
				"attachment.history_unavailable",
				"table storage metadata could not be read",
				true,
			)
		}
		collection, collectionErr := app.FindCollectionByNameOrId(
			meta.GetString("collection_id"),
		)
		if collectionErr != nil {
			return RestorePlan{}, attachmentError(
				"attachment.history_unavailable",
				"table storage could not be read",
				true,
			)
		}
		liveRecord, err = app.FindRecordById(collection, recordID)
		if err != nil {
			return RestorePlan{}, attachmentError(
				"attachment.history_unavailable",
				"current attachment record could not be read",
				true,
			)
		}
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		return RestorePlan{}, attachmentError(
			"attachment.history_unavailable",
			"attachment storage is unavailable",
			true,
		)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	var totalSize int64
	for _, storedName := range currentNames {
		metadata := currentByName[storedName]
		if metadata == nil || liveRecord == nil {
			return RestorePlan{}, attachmentError(
				"attachment.history_missing",
				"current attachment metadata is missing",
				false,
			)
		}
		source := restoreItemFromRecord("live", metadata)
		if !validRestoreItem(source, storedName) {
			return RestorePlan{}, attachmentError(
				"attachment.history_corrupt",
				"current attachment metadata is invalid",
				false,
			)
		}
		if err := verifyObject(
			fsys,
			liveRecord.BaseFilesPath()+"/"+storedName,
			source.Size,
			source.SHA256,
		); err != nil {
			return RestorePlan{}, attachmentError(
				"attachment.history_corrupt",
				"current attachment binary is not restorable",
				false,
			)
		}
	}
	for _, storedName := range targetNames {
		if err := ctx.Err(); err != nil {
			return RestorePlan{}, err
		}
		var source RestoreItem
		if _, current := currentSet[storedName]; current {
			metadata := currentByName[storedName]
			if metadata == nil || liveRecord == nil {
				return RestorePlan{}, attachmentError(
					"attachment.history_missing",
					"current attachment metadata is missing",
					false,
				)
			}
			source = restoreItemFromRecord("live", metadata)
		} else {
			version, versionErr := app.FindFirstRecordByFilter(
				"vibetable_attachment_versions",
				"table_id={:table} && record_id={:record} && field_id={:field} && stored_name={:name}",
				dbx.Params{
					"table": definition.TableID, "record": recordID,
					"field": field.FieldID, "name": storedName,
				},
			)
			if versionErr != nil {
				return RestorePlan{}, attachmentError(
					"attachment.history_missing",
					"historical attachment binary is unavailable",
					false,
				)
			}
			source = restoreItemFromRecord("version", version)
			source.VersionID = version.Id
			blobName := version.GetString("blob")
			if blobName == "" {
				return RestorePlan{}, attachmentError(
					"attachment.history_corrupt",
					"historical attachment manifest is incomplete",
					false,
				)
			}
			if err := verifyObject(
				fsys,
				version.BaseFilesPath()+"/"+blobName,
				source.Size,
				source.SHA256,
			); err != nil {
				return RestorePlan{}, attachmentError(
					"attachment.history_corrupt",
					"historical attachment binary is not restorable",
					false,
				)
			}
		}
		if !validRestoreItem(source, storedName) {
			return RestorePlan{}, attachmentError(
				"attachment.history_corrupt",
				"historical attachment metadata is invalid",
				false,
			)
		}
		if source.Size > field.AttachmentPolicy.MaxBytesPerFile ||
			!mimeAllowed(source.MIMEType, field.AttachmentPolicy.AllowedMIMETypes) {
			return RestorePlan{}, attachmentError(
				"attachment.history_policy_mismatch",
				"historical attachment violates the current field policy",
				false,
			)
		}
		if totalSize > maxTotalStagedBytes-source.Size {
			return RestorePlan{}, attachmentError(
				"attachment.history_limit",
				"historical attachments exceed the restore memory limit",
				false,
			)
		}
		totalSize += source.Size
		plan.Items = append(plan.Items, source)
	}
	return plan, nil
}

func (manager *Manager) StageRestore(
	ctx context.Context,
	app core.App,
	plan RestorePlan,
	handlePrefix string,
) (mutation.AttachmentChange, func(), error) {
	change := mutation.AttachmentChange{
		FieldID: plan.FieldID,
		RemoveStoredNames: append(
			[]string(nil), plan.CurrentNames...,
		),
		UploadHandles: make([]string, 0, len(plan.Items)),
	}
	handles := make([]string, 0, len(plan.Items))
	cleanup := func() { manager.Drop(handles...) }
	fsys, err := app.NewFilesystem()
	if err != nil {
		return mutation.AttachmentChange{}, cleanup, attachmentError(
			"attachment.history_unavailable",
			"attachment storage is unavailable",
			true,
		)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	var liveRecord *core.Record
	var liveFieldName string
	for index, item := range plan.Items {
		if err := ctx.Err(); err != nil {
			cleanup()
			return mutation.AttachmentChange{}, func() {}, err
		}
		var key string
		switch item.Source {
		case "live":
			if liveRecord == nil {
				_, _, record, field, resolveErr := resolveRecordField(
					app, plan.TableID, plan.RecordID, plan.FieldID,
				)
				if resolveErr != nil {
					cleanup()
					return mutation.AttachmentChange{}, func() {}, resolveErr
				}
				liveRecord = record
				liveFieldName = field.PhysicalName
			}
			if !contains(
				fileNames(liveRecord.GetRaw(liveFieldName)),
				item.SourceStoredName,
			) {
				// A previous idempotent restore attempt may have committed and
				// then failed while reading the response. In that case the
				// formerly live source has already been archived by the same
				// attachment mutation, so retry from its immutable version.
				version, findErr := app.FindFirstRecordByFilter(
					"vibetable_attachment_versions",
					"table_id={:table} && record_id={:record} && field_id={:field} && stored_name={:name}",
					dbx.Params{
						"table": plan.TableID, "record": plan.RecordID,
						"field": plan.FieldID,
						"name":  item.SourceStoredName,
					},
				)
				if findErr != nil {
					cleanup()
					return mutation.AttachmentChange{}, func() {},
						attachmentError(
							"attachment.history_changed",
							"current attachment changed after restore preview",
							false,
						)
				}
				key = version.BaseFilesPath() + "/" + version.GetString("blob")
			} else {
				key = liveRecord.BaseFilesPath() + "/" + item.SourceStoredName
			}
		case "version":
			version, findErr := app.FindRecordById(
				"vibetable_attachment_versions", item.VersionID,
			)
			if findErr != nil ||
				version.GetString("table_id") != plan.TableID ||
				version.GetString("record_id") != plan.RecordID ||
				version.GetString("field_id") != plan.FieldID ||
				version.GetString("stored_name") != item.SourceStoredName {
				cleanup()
				return mutation.AttachmentChange{}, func() {},
					attachmentError(
						"attachment.history_changed",
						"historical attachment changed after restore preview",
						false,
					)
			}
			key = version.BaseFilesPath() + "/" + version.GetString("blob")
		default:
			cleanup()
			return mutation.AttachmentChange{}, func() {},
				attachmentError(
					"attachment.history_invalid",
					"attachment restore source is invalid",
					false,
				)
		}
		content, readErr := readVerifiedObject(
			fsys, key, item.Size, item.SHA256,
		)
		if readErr != nil {
			cleanup()
			return mutation.AttachmentChange{}, func() {},
				attachmentError(
					"attachment.history_corrupt",
					"attachment restore source is corrupt",
					false,
				)
		}
		handleIdentity := sha256.Sum256([]byte(
			handlePrefix + "\x00" + plan.TableID + "\x00" +
				plan.RecordID + "\x00" + plan.FieldID,
		))
		handle := fmt.Sprintf("history_%x_%04d", handleIdentity[:12], index)
		if err := manager.StageOwned(
			handle, item.OriginalName, content,
		); err != nil {
			cleanup()
			return mutation.AttachmentChange{}, func() {}, err
		}
		handles = append(handles, handle)
		change.UploadHandles = append(change.UploadHandles, handle)
	}
	return change, cleanup, nil
}

func restoreItemFromRecord(source string, record *core.Record) RestoreItem {
	return RestoreItem{
		Source:           source,
		SourceStoredName: record.GetString("stored_name"),
		OriginalName:     record.GetString("original_name"),
		MIMEType:         record.GetString("mime"),
		Size:             int64(record.GetFloat("size")),
		SHA256:           record.GetString("hash"),
	}
}

func validRestoreItem(item RestoreItem, storedName string) bool {
	return item.SourceStoredName == storedName &&
		strings.TrimSpace(item.OriginalName) != "" &&
		strings.TrimSpace(item.MIMEType) != "" &&
		item.Size > 0 &&
		item.Size <= maxStagedBytes &&
		hashPattern.MatchString(item.SHA256)
}

func (manager *Manager) updateMetadata(
	app core.App,
	tableID, recordID string,
	field schema.FieldDefinition,
	removed []string,
	uploads []stagedFile,
	files []*filesystem.File,
) error {
	removeSet := make(map[string]struct{}, len(removed))
	for _, name := range removed {
		removeSet[name] = struct{}{}
	}
	existing, err := app.FindRecordsByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && record_id={:record} && field_id={:field}",
		"", 0, 0,
		dbx.Params{"table": tableID, "record": recordID, "field": field.FieldID},
	)
	if err != nil {
		return attachmentError(
			"mutation.attachment.storage_failed", "attachment metadata could not be read", true,
		)
	}
	for _, metadata := range existing {
		if _, remove := removeSet[metadata.GetString("stored_name")]; remove {
			if err := app.Delete(metadata); err != nil {
				return attachmentError(
					"mutation.attachment.storage_failed", "attachment metadata could not be deleted", true,
				)
			}
		}
	}
	collection, err := app.FindCollectionByNameOrId("vibetable_attachment_meta")
	if err != nil {
		return attachmentError(
			"mutation.attachment.storage_failed", "attachment metadata collection is unavailable", true,
		)
	}
	for index, upload := range uploads {
		metadata := core.NewRecord(collection)
		metadata.Set("table_id", tableID)
		metadata.Set("record_id", recordID)
		metadata.Set("field_id", field.FieldID)
		metadata.Set("stored_name", files[index].Name)
		metadata.Set("original_name", upload.originalName)
		metadata.Set("mime", upload.mime)
		metadata.Set("size", upload.size)
		metadata.Set("hash", upload.sha256)
		if err := app.Save(metadata); err != nil {
			return attachmentError(
				"mutation.attachment.storage_failed", "attachment metadata could not be saved", true,
			)
		}
		if err := manager.fault("after_metadata_write"); err != nil {
			return attachmentError(
				"mutation.attachment.failed", "attachment metadata operation failed", true,
			)
		}
	}
	return nil
}

func (manager *Manager) Refs(
	ctx context.Context,
	app core.App,
	definition schema.TableDefinition,
	record *core.Record,
	fieldID string,
) ([]Ref, error) {
	field, err := attachmentField(definition, fieldID)
	if err != nil {
		return nil, err
	}
	metadata, err := app.FindRecordsByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && record_id={:record} && field_id={:field}",
		"", 0, 0,
		dbx.Params{"table": definition.TableID, "record": record.Id, "field": field.FieldID},
	)
	if err != nil {
		return nil, attachmentError(
			"attachment.metadata_unavailable", "attachment metadata could not be read", true,
		)
	}
	byName := make(map[string]*core.Record, len(metadata))
	for _, item := range metadata {
		byName[item.GetString("stored_name")] = item
	}
	names := fileNames(record.GetRaw(field.PhysicalName))
	result := make([]Ref, 0, len(names))
	for _, name := range names {
		item := byName[name]
		if item == nil {
			return nil, attachmentError(
				"attachment.metadata_missing", "attachment metadata is missing", false,
			)
		}
		capability, err := manager.capability(
			definition.TableID, record.Id, field.FieldID, name, "",
		)
		if err != nil {
			return nil, err
		}
		thumbnails := []Thumbnail{}
		if strings.HasPrefix(item.GetString("mime"), "image/") {
			thumbnails = make(
				[]Thumbnail, 0, len(field.AttachmentPolicy.ThumbnailVariants),
			)
			for _, variant := range field.AttachmentPolicy.ThumbnailVariants {
				token, err := manager.capability(
					definition.TableID, record.Id, field.FieldID, name, variant,
				)
				if err != nil {
					return nil, err
				}
				thumbnails = append(thumbnails, Thumbnail{
					Variant: variant, DownloadCapability: token,
				})
			}
		}
		result = append(result, Ref{
			ContractVersion: ContractVersion,
			TableID:         definition.TableID, RecordID: record.Id, FieldID: field.FieldID,
			StoredName: name, OriginalName: item.GetString("original_name"),
			MIMEType: item.GetString("mime"), Size: int64(item.GetFloat("size")),
			SHA256: item.GetString("hash"), DownloadCapability: capability,
			Thumbnails: thumbnails,
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (manager *Manager) RefsByID(
	ctx context.Context,
	app core.App,
	tableID, recordID, fieldID string,
) ([]Ref, error) {
	definition, _, record, _, err := resolveRecordField(
		app, tableID, recordID, fieldID,
	)
	if err != nil {
		return nil, err
	}
	return manager.Refs(ctx, app, definition, record, fieldID)
}

func (manager *Manager) Token(
	ctx context.Context,
	app core.App,
	tableID, recordID, fieldID, storedName, variant string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, _, record, field, err := resolveRecordField(
		app, tableID, recordID, fieldID,
	)
	if err != nil {
		return "", err
	}
	if !contains(fileNames(record.GetRaw(field.PhysicalName)), storedName) {
		return "", attachmentError(
			"attachment.not_found", "managed attachment was not found", false,
		)
	}
	if variant != "" && (!filesystem.ThumbSizeRegex.MatchString(variant) ||
		!contains(field.AttachmentPolicy.ThumbnailVariants, variant)) {
		return "", attachmentError(
			"attachment.thumbnail_not_found", "attachment thumbnail variant was not found", false,
		)
	}
	metadata, err := app.FindFirstRecordByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && record_id={:record} && field_id={:field} && stored_name={:name}",
		dbx.Params{
			"table": tableID, "record": recordID,
			"field": field.FieldID, "name": storedName,
		},
	)
	if err != nil {
		return "", attachmentError(
			"attachment.metadata_missing", "attachment metadata is missing", false,
		)
	}
	if variant != "" && !strings.HasPrefix(metadata.GetString("mime"), "image/") {
		return "", attachmentError(
			"attachment.thumbnail_not_found", "attachment thumbnail variant was not found", false,
		)
	}
	return manager.capability(tableID, recordID, field.FieldID, storedName, variant)
}

type capabilityClaims struct {
	TableID    string `json:"tableId"`
	RecordID   string `json:"recordId"`
	FieldID    string `json:"fieldId"`
	StoredName string `json:"storedName"`
	Variant    string `json:"variant"`
	ExpiresAt  int64  `json:"expiresAt"`
}

func (manager *Manager) capability(
	tableID, recordID, fieldID, storedName, variant string,
) (string, error) {
	raw, err := json.Marshal(capabilityClaims{
		TableID: tableID, RecordID: recordID, FieldID: fieldID,
		StoredName: storedName, Variant: variant,
		ExpiresAt: manager.now().Add(capabilityTTL).Unix(),
	})
	if err != nil {
		return "", attachmentError(
			"attachment.capability_failed", "download capability could not be created", true,
		)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, manager.secret)
	_, _ = mac.Write([]byte(payload))
	return "att1." + payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func (manager *Manager) Open(
	ctx context.Context,
	app core.App,
	capability string,
) (*Download, error) {
	claims, err := manager.verifyCapability(capability)
	if err != nil {
		return nil, err
	}
	definition, collection, record, field, err := resolveRecordField(
		app, claims.TableID, claims.RecordID, claims.FieldID,
	)
	if err != nil {
		return nil, err
	}
	_ = definition
	if !contains(fileNames(record.GetRaw(field.PhysicalName)), claims.StoredName) {
		return nil, attachmentError(
			"attachment.not_found", "managed attachment was not found", false,
		)
	}
	if claims.Variant != "" &&
		(!filesystem.ThumbSizeRegex.MatchString(claims.Variant) ||
			!contains(field.AttachmentPolicy.ThumbnailVariants, claims.Variant)) {
		return nil, attachmentError(
			"attachment.thumbnail_not_found", "attachment thumbnail variant was not found", false,
		)
	}
	metadata, err := app.FindFirstRecordByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && record_id={:record} && field_id={:field} && stored_name={:name}",
		dbx.Params{
			"table": claims.TableID, "record": claims.RecordID,
			"field": claims.FieldID, "name": claims.StoredName,
		},
	)
	if err != nil {
		return nil, attachmentError(
			"attachment.metadata_missing", "attachment metadata is missing", false,
		)
	}
	if claims.Variant != "" &&
		!strings.HasPrefix(metadata.GetString("mime"), "image/") {
		return nil, attachmentError(
			"attachment.thumbnail_not_found", "attachment thumbnail variant was not found", false,
		)
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		return nil, attachmentError(
			"attachment.storage_failed", "attachment storage is unavailable", true,
		)
	}
	fsys.SetContext(ctx)
	basePath := record.BaseFilesPath()
	originalPath := basePath + "/" + claims.StoredName
	expectedSize := int64(metadata.GetFloat("size"))
	expectedHash := metadata.GetString("hash")
	if claims.Variant == "" {
		reader, attributes, verifyErr := snapshotVerifiedObject(
			fsys, originalPath, expectedSize, expectedHash,
		)
		if verifyErr != nil {
			_ = fsys.Close()
			return nil, verifyErr
		}
		_ = collection
		return &Download{
			Reader: &filesystemReader{
				ReadSeekCloser: reader,
				filesystem:     fsys,
				removeOnClose:  reader.Name(),
			},
			Name:        metadata.GetString("original_name"),
			ContentType: attributes.contentType,
			Size:        attributes.size,
		}, nil
	}
	if verifyErr := verifyObject(
		fsys, originalPath, expectedSize, expectedHash,
	); verifyErr != nil {
		_ = fsys.Close()
		return nil, verifyErr
	}
	servedPath := basePath + "/thumbs_" + claims.StoredName + "/" +
		claims.Variant + "_" + claims.StoredName
	if exists, _ := fsys.Exists(servedPath); !exists {
		if err := fsys.CreateThumb(
			originalPath, servedPath, claims.Variant,
		); err != nil {
			_ = fsys.Close()
			return nil, attachmentError(
				"attachment.thumbnail_failed", "attachment thumbnail could not be created", true,
			)
		}
		// CreateThumb reopens the source object. Verify it again before exposing
		// the newly generated thumbnail so a concurrent source change fails closed.
		if verifyErr := verifyObject(
			fsys, originalPath, expectedSize, expectedHash,
		); verifyErr != nil {
			_ = fsys.Delete(servedPath)
			_ = fsys.Close()
			return nil, verifyErr
		}
	}
	attributes, err := fsys.Attributes(servedPath)
	if err != nil {
		_ = fsys.Close()
		return nil, attachmentError(
			"attachment.file_missing", "attachment file is missing", false,
		)
	}
	reader, err := fsys.GetReader(servedPath)
	if err != nil {
		_ = fsys.Close()
		return nil, attachmentError(
			"attachment.file_missing", "attachment file is missing", false,
		)
	}
	_ = collection
	return &Download{
		Reader:      &filesystemReader{ReadSeekCloser: reader, filesystem: fsys},
		Name:        metadata.GetString("original_name"),
		ContentType: attributes.ContentType, Size: attributes.Size,
	}, nil
}

type filesystemReader struct {
	io.ReadSeekCloser
	filesystem    *filesystem.System
	removeOnClose string
}

func (reader *filesystemReader) Close() error {
	closeErr := reader.ReadSeekCloser.Close()
	var removeErr error
	if reader.removeOnClose != "" {
		removeErr = os.Remove(reader.removeOnClose)
	}
	return errors.Join(closeErr, removeErr, reader.filesystem.Close())
}

func (manager *Manager) verifyCapability(value string) (capabilityClaims, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "att1" {
		return capabilityClaims{}, attachmentError(
			"attachment.capability_invalid", "download capability is invalid", false,
		)
	}
	signature, err := hex.DecodeString(parts[2])
	if err != nil {
		return capabilityClaims{}, attachmentError(
			"attachment.capability_invalid", "download capability is invalid", false,
		)
	}
	mac := hmac.New(sha256.New, manager.secret)
	_, _ = mac.Write([]byte(parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return capabilityClaims{}, attachmentError(
			"attachment.capability_invalid", "download capability is invalid", false,
		)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return capabilityClaims{}, attachmentError(
			"attachment.capability_invalid", "download capability is invalid", false,
		)
	}
	var claims capabilityClaims
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil ||
		claims.TableID == "" || claims.RecordID == "" || claims.FieldID == "" ||
		claims.StoredName == "" {
		return capabilityClaims{}, attachmentError(
			"attachment.capability_invalid", "download capability is invalid", false,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return capabilityClaims{}, attachmentError(
			"attachment.capability_invalid", "download capability is invalid", false,
		)
	}
	if manager.now().Unix() >= claims.ExpiresAt {
		return capabilityClaims{}, attachmentError(
			"attachment.capability_expired", "download capability has expired", false,
		)
	}
	return claims, nil
}

func (manager *Manager) Integrity(
	ctx context.Context,
	app core.App,
) (IntegrityReport, error) {
	report := IntegrityReport{
		MissingFiles: []IntegrityIssue{}, MissingMetadata: []IntegrityIssue{},
		CorruptFiles: []IntegrityIssue{}, OrphanFiles: []IntegrityIssue{},
		OrphanVersions: []IntegrityIssue{},
	}
	metadata, err := app.FindRecordsByFilter(
		"vibetable_attachment_meta", "", "", 0, 0,
	)
	if err != nil {
		return report, attachmentError(
			"attachment.integrity_failed", "attachment metadata could not be scanned", true,
		)
	}
	report.CheckedMetadata = len(metadata)
	fsys, err := app.NewFilesystem()
	if err != nil {
		return report, attachmentError(
			"attachment.integrity_failed", "attachment storage could not be scanned", true,
		)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	metadataIdentity := map[string]struct{}{}
	referencedStorage := map[string]IntegrityIssue{}
	for _, item := range metadata {
		issue := IntegrityIssue{
			TableID: item.GetString("table_id"), RecordID: item.GetString("record_id"),
			FieldID: item.GetString("field_id"), StoredName: item.GetString("stored_name"),
		}
		metadataIdentity[attachmentIdentity(
			issue.TableID, issue.RecordID, issue.FieldID, issue.StoredName,
		)] = struct{}{}
		_, collection, record, field, resolveErr := resolveRecordField(
			app, issue.TableID, issue.RecordID, issue.FieldID,
		)
		if resolveErr != nil ||
			!contains(fileNames(record.GetRaw(field.PhysicalName)), issue.StoredName) {
			issue.Code = "attachment.metadata_without_reference"
			report.MissingFiles = append(report.MissingFiles, issue)
			continue
		}
		key := record.BaseFilesPath() + "/" + issue.StoredName
		referencedStorage[key] = issue
		exists, existsErr := fsys.Exists(key)
		if existsErr != nil || !exists {
			issue.Code = "attachment.file_missing"
			report.MissingFiles = append(report.MissingFiles, issue)
			continue
		}
		attributes, attributesErr := fsys.Attributes(key)
		if attributesErr != nil {
			issue.Code = "attachment.file_unreadable"
			report.CorruptFiles = append(report.CorruptFiles, issue)
			continue
		}
		if attributes.Size != int64(item.GetFloat("size")) {
			issue.Code = "attachment.size_mismatch"
			report.CorruptFiles = append(report.CorruptFiles, issue)
			continue
		}
		reader, readerErr := fsys.GetReader(key)
		if readerErr != nil {
			issue.Code = "attachment.file_unreadable"
			report.CorruptFiles = append(report.CorruptFiles, issue)
			continue
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, reader)
		closeErr := reader.Close()
		if copyErr != nil || closeErr != nil {
			issue.Code = "attachment.file_unreadable"
			report.CorruptFiles = append(report.CorruptFiles, issue)
			continue
		}
		if hex.EncodeToString(hash.Sum(nil)) != item.GetString("hash") {
			issue.Code = "attachment.hash_mismatch"
			report.CorruptFiles = append(report.CorruptFiles, issue)
		}
		_ = collection
	}
	versions, err := app.FindRecordsByFilter(
		"vibetable_attachment_versions", "", "", 0, 0,
	)
	if err != nil {
		return report, attachmentError(
			"attachment.integrity_failed",
			"attachment versions could not be scanned",
			true,
		)
	}
	report.CheckedVersions = len(versions)
	historyReferences, err := attachmentHistoryReferences(app)
	if err != nil {
		return report, err
	}
	versionCollection, err := app.FindCollectionByNameOrId(
		"vibetable_attachment_versions",
	)
	if err != nil {
		return report, attachmentError(
			"attachment.integrity_failed",
			"attachment version collection is unavailable",
			true,
		)
	}
	for _, version := range versions {
		issue := IntegrityIssue{
			TableID:    version.GetString("table_id"),
			RecordID:   version.GetString("record_id"),
			FieldID:    version.GetString("field_id"),
			StoredName: version.GetString("stored_name"),
		}
		blobName := version.GetString("blob")
		key := version.BaseFilesPath() + "/" + blobName
		if blobName == "" {
			issue.Code = "attachment.version_file_missing"
			report.MissingFiles = append(report.MissingFiles, issue)
			continue
		}
		referencedStorage[key] = issue
		if verifyErr := verifyObject(
			fsys,
			key,
			int64(version.GetFloat("size")),
			version.GetString("hash"),
		); verifyErr != nil {
			var productErr *mutation.ProductError
			if errors.As(verifyErr, &productErr) &&
				productErr.Code == "attachment.file_missing" {
				issue.Code = "attachment.version_file_missing"
				report.MissingFiles = append(report.MissingFiles, issue)
			} else {
				issue.Code = "attachment.version_corrupt"
				report.CorruptFiles = append(report.CorruptFiles, issue)
			}
			continue
		}
		historyKey := attachmentHistoryIdentity(
			issue.TableID,
			issue.RecordID,
			version.GetString("field_name"),
			issue.StoredName,
		)
		if _, exists := historyReferences[historyKey]; !exists {
			issue.Code = "attachment.version_without_history"
			report.OrphanVersions = append(report.OrphanVersions, issue)
		}
	}
	tables, err := app.FindRecordsByFilter("vibetable_tables", "", "", 0, 0)
	if err != nil {
		return report, attachmentError(
			"attachment.integrity_failed", "table metadata could not be scanned", true,
		)
	}
	for _, table := range tables {
		collection, err := app.FindCollectionByNameOrId(table.GetString("collection_id"))
		if err != nil {
			continue
		}
		raw, marshalErr := json.Marshal(table.GetRaw("definition_json"))
		var definition schema.TableDefinition
		if marshalErr != nil || json.Unmarshal(raw, &definition) != nil {
			return report, attachmentError(
				"attachment.integrity_failed", "table schema could not be decoded", true,
			)
		}
		records, err := app.FindRecordsByFilter(collection, "", "", 0, 0)
		if err != nil {
			return report, attachmentError(
				"attachment.integrity_failed", "table records could not be scanned", true,
			)
		}
		for _, record := range records {
			for _, field := range definition.Fields {
				if field.Kind != schema.FieldKindAttachment {
					continue
				}
				for _, storedName := range fileNames(record.GetRaw(field.PhysicalName)) {
					issue := IntegrityIssue{
						TableID: definition.TableID, RecordID: record.Id,
						FieldID: field.FieldID, StoredName: storedName,
					}
					referencedStorage[record.BaseFilesPath()+"/"+storedName] = issue
					if _, exists := metadataIdentity[attachmentIdentity(
						definition.TableID, record.Id, field.FieldID, storedName,
					)]; !exists {
						issue.Code = "attachment.reference_without_metadata"
						report.MissingMetadata = append(report.MissingMetadata, issue)
					}
				}
			}
		}
		objects, err := fsys.List(collection.Id + "/")
		if err != nil {
			return report, attachmentError(
				"attachment.integrity_failed", "attachment storage could not be listed", true,
			)
		}
		for _, object := range objects {
			if isFilesystemTemporaryObject(object.Key) {
				continue
			}
			if _, known := referencedStorage[object.Key]; known {
				continue
			}
			if isReferencedThumbnail(object.Key, referencedStorage) {
				continue
			}
			parts := strings.Split(object.Key, "/")
			if len(parts) < 3 {
				continue
			}
			report.OrphanFiles = append(report.OrphanFiles, IntegrityIssue{
				Code: "attachment.orphan_file", TableID: table.GetString("table_id"),
				RecordID: parts[1], StoredName: path.Base(object.Key),
			})
		}
	}
	versionObjects, err := fsys.List(versionCollection.Id + "/")
	if err != nil {
		return report, attachmentError(
			"attachment.integrity_failed",
			"attachment version storage could not be listed",
			true,
		)
	}
	for _, object := range versionObjects {
		if object.IsDir || isFilesystemTemporaryObject(object.Key) {
			continue
		}
		if _, known := referencedStorage[object.Key]; known {
			continue
		}
		parts := strings.Split(object.Key, "/")
		issue := IntegrityIssue{
			Code:       "attachment.version_orphan_file",
			StoredName: path.Base(object.Key),
		}
		if len(parts) >= 2 {
			issue.RecordID = parts[1]
		}
		report.OrphanFiles = append(report.OrphanFiles, issue)
	}
	sort.Slice(report.MissingFiles, issueLess(report.MissingFiles))
	sort.Slice(report.MissingMetadata, issueLess(report.MissingMetadata))
	sort.Slice(report.CorruptFiles, issueLess(report.CorruptFiles))
	sort.Slice(report.OrphanFiles, issueLess(report.OrphanFiles))
	sort.Slice(report.OrphanVersions, issueLess(report.OrphanVersions))
	report.Valid = len(report.MissingFiles) == 0 &&
		len(report.MissingMetadata) == 0 && len(report.CorruptFiles) == 0 &&
		len(report.OrphanFiles) == 0 && len(report.OrphanVersions) == 0
	return report, nil
}

func readVerifiedObject(
	fsys *filesystem.System,
	key string,
	expectedSize int64,
	expectedHash string,
) ([]byte, error) {
	var content bytes.Buffer
	if _, err := copyVerifiedObject(
		fsys, key, expectedSize, expectedHash, &content,
	); err != nil {
		return nil, err
	}
	return content.Bytes(), nil
}

type verifiedObjectAttributes struct {
	contentType string
	size        int64
}

func snapshotVerifiedObject(
	fsys *filesystem.System,
	key string,
	expectedSize int64,
	expectedHash string,
) (*os.File, verifiedObjectAttributes, error) {
	snapshot, err := os.CreateTemp("", "vibetable-attachment-*")
	if err != nil {
		return nil, verifiedObjectAttributes{}, attachmentError(
			"attachment.file_unreadable",
			"attachment file could not be read",
			true,
		)
	}
	attributes, verifyErr := copyVerifiedObject(
		fsys, key, expectedSize, expectedHash, snapshot,
	)
	if verifyErr != nil {
		_ = snapshot.Close()
		_ = os.Remove(snapshot.Name())
		return nil, verifiedObjectAttributes{}, verifyErr
	}
	if _, seekErr := snapshot.Seek(0, io.SeekStart); seekErr != nil {
		_ = snapshot.Close()
		_ = os.Remove(snapshot.Name())
		return nil, verifiedObjectAttributes{}, attachmentError(
			"attachment.file_unreadable",
			"attachment file could not be read",
			true,
		)
	}
	return snapshot, attributes, nil
}

func copyVerifiedObject(
	fsys *filesystem.System,
	key string,
	expectedSize int64,
	expectedHash string,
	destination io.Writer,
) (verifiedObjectAttributes, error) {
	if expectedSize <= 0 || expectedSize > maxStagedBytes ||
		!hashPattern.MatchString(expectedHash) {
		return verifiedObjectAttributes{}, attachmentError(
			"attachment.integrity_failed",
			"attachment metadata is invalid",
			false,
		)
	}
	attributes, err := fsys.Attributes(key)
	if err != nil {
		return verifiedObjectAttributes{}, attachmentError(
			"attachment.file_missing",
			"attachment file is missing",
			false,
		)
	}
	if attributes.Size != expectedSize {
		return verifiedObjectAttributes{}, attachmentError(
			"attachment.size_mismatch",
			"attachment size does not match metadata",
			false,
		)
	}
	reader, err := fsys.GetReader(key)
	if err != nil {
		return verifiedObjectAttributes{}, attachmentError(
			"attachment.file_missing",
			"attachment file is missing",
			false,
		)
	}
	hash := sha256.New()
	written, readErr := io.Copy(
		io.MultiWriter(destination, hash),
		io.LimitReader(reader, expectedSize+1),
	)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || written != expectedSize {
		return verifiedObjectAttributes{}, attachmentError(
			"attachment.file_unreadable",
			"attachment file could not be read",
			true,
		)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return verifiedObjectAttributes{}, attachmentError(
			"attachment.hash_mismatch",
			"attachment hash does not match metadata",
			false,
		)
	}
	return verifiedObjectAttributes{
		contentType: attributes.ContentType,
		size:        attributes.Size,
	}, nil
}

func verifyObject(
	fsys *filesystem.System,
	key string,
	expectedSize int64,
	expectedHash string,
) error {
	_, err := copyVerifiedObject(
		fsys, key, expectedSize, expectedHash, io.Discard,
	)
	return err
}

func isReferencedThumbnail(
	key string,
	referenced map[string]IntegrityIssue,
) bool {
	parts := strings.Split(key, "/")
	if len(parts) < 4 || !strings.HasPrefix(parts[len(parts)-2], "thumbs_") {
		return false
	}
	originalName := strings.TrimPrefix(parts[len(parts)-2], "thumbs_")
	if originalName == "" {
		return false
	}
	originalKey := strings.Join(parts[:len(parts)-2], "/") + "/" + originalName
	_, exists := referenced[originalKey]
	return exists
}

func isFilesystemTemporaryObject(key string) bool {
	return strings.HasSuffix(
		strings.ToLower(path.Base(key)),
		".tmp",
	)
}

func attachmentIdentity(tableID, recordID, fieldID, storedName string) string {
	return tableID + "\x00" + recordID + "\x00" + fieldID + "\x00" + storedName
}

func attachmentHistoryIdentity(
	tableID, recordID, fieldName, storedName string,
) string {
	return tableID + "\x00" + recordID + "\x00" + fieldName + "\x00" + storedName
}

func attachmentHistoryReferences(
	app core.App,
) (map[string]struct{}, error) {
	const maxEvents = 100_000
	events, err := app.FindRecordsByFilter(
		"vibetable_audit_events", "", "", maxEvents+1, 0,
	)
	if err != nil {
		return nil, attachmentError(
			"attachment.integrity_failed",
			"attachment audit references could not be scanned",
			true,
		)
	}
	if len(events) > maxEvents {
		return nil, attachmentError(
			"attachment.integrity_failed",
			"attachment audit reference scan exceeds the safe limit",
			false,
		)
	}
	result := map[string]struct{}{}
	for _, event := range events {
		tableID := event.GetString("table_id")
		recordID := event.GetString("record_id")
		if tableID == "" || recordID == "" {
			return nil, attachmentError(
				"attachment.integrity_failed",
				"attachment audit reference is invalid",
				false,
			)
		}
		for _, field := range []string{"before_json", "after_json"} {
			value := event.GetRaw(field)
			if value == nil {
				continue
			}
			raw, marshalErr := json.Marshal(value)
			image := map[string]any{}
			if marshalErr != nil || json.Unmarshal(raw, &image) != nil {
				return nil, attachmentError(
					"attachment.integrity_failed",
					"attachment audit image is invalid",
					false,
				)
			}
			for fieldName, fieldValue := range image {
				for _, storedName := range fileNames(fieldValue) {
					result[attachmentHistoryIdentity(
						tableID, recordID, fieldName, storedName,
					)] = struct{}{}
				}
			}
		}
	}
	return result, nil
}

func resolveRecordField(
	app core.App,
	tableID, recordID, fieldID string,
) (schema.TableDefinition, *core.Collection, *core.Record, schema.FieldDefinition, error) {
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	)
	if err != nil {
		return schema.TableDefinition{}, nil, nil, schema.FieldDefinition{},
			attachmentError("attachment.not_found", "table was not found", false)
	}
	raw, _ := json.Marshal(meta.GetRaw("definition_json"))
	var definition schema.TableDefinition
	if json.Unmarshal(raw, &definition) != nil {
		return schema.TableDefinition{}, nil, nil, schema.FieldDefinition{},
			attachmentError("attachment.storage_failed", "table schema is invalid", true)
	}
	field, err := attachmentField(definition, fieldID)
	if err != nil {
		return schema.TableDefinition{}, nil, nil, schema.FieldDefinition{}, err
	}
	collection, err := app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		return schema.TableDefinition{}, nil, nil, schema.FieldDefinition{},
			attachmentError("attachment.not_found", "table storage was not found", false)
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return schema.TableDefinition{}, nil, nil, schema.FieldDefinition{},
				attachmentError("attachment.not_found", "record was not found", false)
		}
		return schema.TableDefinition{}, nil, nil, schema.FieldDefinition{},
			attachmentError("attachment.storage_failed", "record could not be read", true)
	}
	return definition, collection, record, field, nil
}

func attachmentField(
	definition schema.TableDefinition,
	fieldID string,
) (schema.FieldDefinition, error) {
	for _, field := range definition.Fields {
		if (field.FieldID == fieldID || field.PhysicalName == fieldID) &&
			field.Kind == schema.FieldKindAttachment &&
			field.DataType == schema.DataTypeFile {
			return field, nil
		}
	}
	return schema.FieldDefinition{}, attachmentError(
		"mutation.attachment.invalid_field", "attachment field was not found", false,
	)
}

func fileNames(value any) []string {
	if value == nil {
		return []string{}
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return []string{}
		}
		return []string{text}
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return []string{}
	}
	result := make([]string, 0, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		if text, ok := reflected.Index(index).Interface().(string); ok && text != "" {
			result = append(result, text)
		}
	}
	return result
}

func mimeAllowed(detected string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	detectedBase := strings.ToLower(strings.TrimSpace(strings.SplitN(detected, ";", 2)[0]))
	for _, value := range allowed {
		value = strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
		if value == detectedBase || value == "*/*" {
			return true
		}
		if strings.HasSuffix(value, "/*") &&
			strings.HasPrefix(detectedBase, strings.TrimSuffix(value, "*")) {
			return true
		}
	}
	return false
}

func duplicates(values []string) bool {
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func attachmentError(code, message string, retryable bool) *mutation.ProductError {
	return &mutation.ProductError{
		ContractVersion: mutation.ContractVersion,
		Code:            code, Message: message, Details: map[string]any{},
		Retryable: retryable,
	}
}

func issueLess(issues []IntegrityIssue) func(int, int) bool {
	return func(left, right int) bool {
		a := fmt.Sprintf(
			"%s/%s/%s/%s", issues[left].TableID, issues[left].RecordID,
			issues[left].FieldID, issues[left].StoredName,
		)
		b := fmt.Sprintf(
			"%s/%s/%s/%s", issues[right].TableID, issues[right].RecordID,
			issues[right].FieldID, issues[right].StoredName,
		)
		return a < b
	}
}
