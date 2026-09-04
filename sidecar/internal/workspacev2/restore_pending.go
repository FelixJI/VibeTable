package workspacev2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	workbenchcontracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	_ "modernc.org/sqlite"
)

const (
	restoreJournalName     = "restore-journal.json"
	restorePhaseRequested  = "requested"
	restorePhaseInstalling = "installing"
	restorePhaseInstalled  = "installed"
	restorePhaseCommitted  = "committed"
	restoreJournalMaxBytes = 1 << 20
)

type pendingSnapshotRestore struct {
	FormatVersion        int                          `json:"formatVersion"`
	OperationID          string                       `json:"operationId"`
	WorkspaceID          string                       `json:"workspaceId"`
	SnapshotID           string                       `json:"snapshotId"`
	SourceWorkspaceID    string                       `json:"sourceWorkspaceId,omitempty"`
	SourceSnapshotID     string                       `json:"sourceSnapshotId,omitempty"`
	CompletionOccurredAt string                       `json:"completionOccurredAt"`
	ProtectionSnapshotID string                       `json:"protectionSnapshotId"`
	Phase                string                       `json:"phase"`
	PreviousHead         filehistory.CurrentHead      `json:"previousHead"`
	NextHead             filehistory.CurrentHead      `json:"nextHead"`
	HeadAudit            auditledger.Envelope         `json:"headAudit"`
	DatabaseHash         string                       `json:"databaseHash"`
	SettingsHash         string                       `json:"settingsHash"`
	Files                map[string]restoreStagedFile `json:"files"`
	Attachments          map[string]restoreStagedFile `json:"attachments,omitempty"`
	PreviousSettings     bool                         `json:"previousSettings"`
	StorageCaptured      bool                         `json:"storageCaptured,omitempty"`
	PreviousStorage      bool                         `json:"previousStorage,omitempty"`
	Sequence             uint64                       `json:"sequence"`
	Method               string                       `json:"method"`
	Scope                string                       `json:"scope"`
	RequestHash          string                       `json:"requestHash"`
	Result               json.RawMessage              `json:"result"`
}

type restoreStagedFile struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

func restoreJournalPath(paths workspacePaths) string {
	return filepath.Join(paths.coordination, restoreJournalName)
}

func restoreStagingRoot(paths workspacePaths, operationID string) string {
	return filepath.Join(paths.temp, "restore-staging", operationID)
}

func restoreRollbackRoot(paths workspacePaths, operationID string) string {
	return filepath.Join(paths.temp, "restore-rollback", operationID)
}

func (runtime *Runtime) stagePendingSnapshotRestore(
	ctx context.Context,
	sequence uint64,
	receipt protocolv2.OperationReceipt,
	record snapshot.Record,
	protection snapshot.Record,
	staged filehistory.StagedSnapshotRestore,
) error {
	paths, err := resolvePaths(runtime.app.DataDir())
	if err != nil {
		return err
	}
	journalPath := restoreJournalPath(paths)
	if _, err := os.Lstat(journalPath); err == nil {
		return errors.New("restore.already_pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := validateLocalRestoreStorageObjectMap(record.ObjectMap); err != nil {
		return err
	}
	staging := restoreStagingRoot(paths, receipt.OperationID)
	if err := os.MkdirAll(filepath.Dir(staging), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := os.Mkdir(filepath.Join(staging, "files"), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(staging, "storage"), 0o700); err != nil {
		return err
	}
	databaseID := record.ObjectMap["database"]
	settingsID := record.ObjectMap["workspace-settings"]
	if databaseID == "" || settingsID == "" {
		return errors.New("restore.snapshot_incomplete")
	}
	databaseHash, _, err := runtime.stageRestoreObject(
		ctx,
		databaseID,
		filepath.Join(staging, "data.db"),
	)
	if err != nil {
		return err
	}
	if err := verifySQLiteDatabase(filepath.Join(staging, "data.db")); err != nil {
		return err
	}
	if err := verifyLocalRestoreStorage(filepath.Join(staging, "data.db")); err != nil {
		return err
	}
	settingsHash, _, err := runtime.stageRestoreObject(
		ctx,
		settingsID,
		filepath.Join(staging, "settings.json"),
	)
	if err != nil {
		return err
	}
	settingsRaw, err := readFileBounded(
		filepath.Join(staging, "settings.json"),
		1<<20,
	)
	if err != nil {
		return errors.Join(errors.New("restore.settings_invalid"), err)
	}
	if _, err := decodeWorkspaceSettingsSnapshot(settingsRaw); err != nil {
		return errors.Join(errors.New("restore.settings_invalid"), err)
	}
	files := make(map[string]restoreStagedFile)
	for _, document := range staged.Documents {
		if document.Status != filehistory.DocumentActive ||
			document.EffectiveRevisionID == "" {
			continue
		}
		var effective *filehistory.Revision
		for index := range document.Revisions {
			if document.Revisions[index].RevisionID ==
				document.EffectiveRevisionID {
				effective = &document.Revisions[index]
				break
			}
		}
		if effective == nil {
			return errors.New("restore.file_state_invalid")
		}
		target, err := restoreRelativeTarget(
			filepath.Join(staging, "files"),
			document.RelativePath,
		)
		if err != nil {
			return err
		}
		hash, size, err := runtime.stageRestoreObject(
			ctx,
			effective.ObjectID,
			target,
		)
		if err != nil {
			return err
		}
		if hash != effective.ContentHash || size != effective.Size {
			return errors.New("restore.file_state_invalid")
		}
		files[document.RelativePath] = restoreStagedFile{
			Hash: hash, Size: size,
		}
	}
	attachments := make(map[string]restoreStagedFile)
	var attachmentBytes int64
	for name, id := range record.ObjectMap {
		if !strings.HasPrefix(name, "attachment:") {
			continue
		}
		relative := strings.TrimPrefix(name, "attachment:")
		target, err := restoreRelativeTarget(
			filepath.Join(staging, "storage"),
			relative,
		)
		if err != nil {
			return err
		}
		hash, size, err := runtime.stageRestoreObject(ctx, id, target)
		if err != nil {
			return err
		}
		if size > maxSnapshotWorkingSet-attachmentBytes {
			return errors.New("restore.resource_limit")
		}
		attachmentBytes += size
		attachments[relative] = restoreStagedFile{Hash: hash, Size: size}
	}
	journal := pendingSnapshotRestore{
		FormatVersion:        3,
		OperationID:          receipt.OperationID,
		WorkspaceID:          runtime.manifest.WorkspaceID,
		SnapshotID:           record.SnapshotID,
		SourceWorkspaceID:    record.SourceWorkspaceID,
		SourceSnapshotID:     record.SourceSnapshotID,
		CompletionOccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		ProtectionSnapshotID: protection.SnapshotID,
		Phase:                restorePhaseRequested,
		PreviousHead:         staged.PreviousHead,
		NextHead:             staged.NextHead,
		HeadAudit:            staged.Audit,
		DatabaseHash:         databaseHash,
		SettingsHash:         settingsHash,
		Files:                files,
		Attachments:          attachments,
		PreviousSettings:     false,
		StorageCaptured:      true,
		Sequence:             sequence,
		Method:               receipt.Method,
		Scope:                string(receipt.Scope),
		RequestHash:          receipt.RequestHash,
		Result:               append(json.RawMessage(nil), receipt.Result...),
	}
	if err := writeRestoreJournal(paths, journal); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func validateLocalRestoreStorageObjectMap(
	objectMap map[string]objectrepo.ObjectID,
) error {
	keys := make([]string, 0)
	for name := range objectMap {
		if !strings.HasPrefix(name, "attachment:") {
			continue
		}
		keys = append(keys, strings.TrimPrefix(name, "attachment:"))
	}
	return validateLocalRestoreStorageKeys(keys)
}

func validateLocalRestoreStorageKeys(keys []string) error {
	files := make(map[string]struct{}, len(keys))
	directories := make(map[string]struct{})
	for _, key := range keys {
		if err := validateLocalRestoreStorageKey(key); err != nil {
			return errors.New("restore.path_invalid")
		}
		canonical := strings.ToUpper(key)
		if _, found := files[canonical]; found {
			return errors.New("restore.path_invalid")
		}
		if _, found := directories[canonical]; found {
			return errors.New("restore.path_invalid")
		}
		for offset := strings.IndexByte(canonical, '/'); offset >= 0; {
			directory := canonical[:offset]
			if _, found := files[directory]; found {
				return errors.New("restore.path_invalid")
			}
			directories[directory] = struct{}{}
			next := strings.IndexByte(canonical[offset+1:], '/')
			if next < 0 {
				break
			}
			offset += next + 1
		}
		files[canonical] = struct{}{}
	}
	return nil
}

func validateLocalRestoreStorageKey(key string) error {
	if key == "" ||
		strings.Contains(key, `\`) ||
		path.IsAbs(key) ||
		path.Clean(key) != key ||
		key == "." {
		return errors.New("restore.path_invalid")
	}
	for _, component := range strings.Split(key, "/") {
		if strings.ContainsAny(component, `<>:"|?*`) ||
			strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, " ") {
			return errors.New("restore.path_invalid")
		}
		for _, character := range component {
			if character < ' ' {
				return errors.New("restore.path_invalid")
			}
		}
		base := component
		if index := strings.IndexByte(base, '.'); index >= 0 {
			base = base[:index]
		}
		base = strings.ToUpper(strings.TrimRight(base, " ."))
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			((strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
				isWindowsReservedDeviceDigit(base[3:])) {
			return errors.New("restore.path_invalid")
		}
	}
	return nil
}

func isWindowsReservedDeviceDigit(value string) bool {
	switch value {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	default:
		return false
	}
}

func (runtime *Runtime) stageRestoreObject(
	ctx context.Context,
	id objectrepo.ObjectID,
	target string,
) (string, int64, error) {
	reader, err := runtime.repository.Open(ctx, id)
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", 0, err
	}
	output, err := os.OpenFile(
		target,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return "", 0, err
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(
		io.MultiWriter(output, hasher),
		io.LimitReader(reader, maxSnapshotWorkingSet+1),
	)
	if size > maxSnapshotWorkingSet {
		copyErr = errors.New("restore.resource_limit")
	}
	syncErr := output.Sync()
	closeErr := output.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return "", 0, err
	}
	hash := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if string(id) != "obj_"+strings.TrimPrefix(hash, "sha256:") {
		return "", 0, errors.New("restore.object_hash_mismatch")
	}
	return hash, size, nil
}

func restoreRelativeTarget(root string, relative string) (string, error) {
	if relative == "" ||
		strings.Contains(relative, `\`) ||
		path.IsAbs(relative) ||
		path.Clean(relative) != relative ||
		relative == "." {
		return "", errors.New("restore.path_invalid")
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	prefix := filepath.Clean(root) + string(os.PathSeparator)
	if !strings.HasPrefix(target, prefix) {
		return "", errors.New("restore.path_invalid")
	}
	return target, nil
}

func verifySQLiteDatabase(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("restore.database_invalid")
	}
	return nil
}

func verifyLocalRestoreStorage(databasePath string) error {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	var raw []byte
	if err := db.QueryRow(`SELECT value FROM _params WHERE id = 'settings'`).Scan(&raw); err != nil {
		return errors.Join(errors.New("restore.attachment_storage_unsupported"), err)
	}
	var stored struct {
		S3 *struct {
			Enabled bool `json:"enabled"`
		} `json:"s3"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil || stored.S3 == nil || stored.S3.Enabled {
		return errors.New("restore.attachment_storage_unsupported")
	}
	return nil
}

func writeRestoreJournal(
	paths workspacePaths,
	journal pendingSnapshotRestore,
) error {
	if err := validateRestoreJournal(paths, journal); err != nil {
		return err
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	budgetRaw := raw
	if journal.FormatVersion == 3 {
		future := journal
		future.Phase = restorePhaseInstalling
		future.PreviousStorage = true
		budgetRaw, err = json.Marshal(future)
		if err != nil {
			return err
		}
	}
	if len(budgetRaw) > restoreJournalMaxBytes {
		return errors.New("restore.journal_resource_limit")
	}
	temporary, err := os.CreateTemp(
		paths.coordination,
		".restore-journal-*.tmp",
	)
	if err != nil {
		return err
	}
	tempName := temporary.Name()
	defer os.Remove(tempName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceGrantedFile(tempName, restoreJournalPath(paths))
}

func readRestoreJournal(
	paths workspacePaths,
) (pendingSnapshotRestore, bool, error) {
	raw, err := readFileBounded(restoreJournalPath(paths), restoreJournalMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return pendingSnapshotRestore{}, false, nil
	}
	if err != nil {
		return pendingSnapshotRestore{}, false, err
	}
	var journal pendingSnapshotRestore
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return pendingSnapshotRestore{}, false,
			errors.New("restore.journal_corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return pendingSnapshotRestore{}, false,
			errors.New("restore.journal_corrupt")
	}
	if err := validateRestoreJournal(paths, journal); err != nil {
		return pendingSnapshotRestore{}, false, err
	}
	return journal, true, nil
}

func validateRestoreJournal(
	paths workspacePaths,
	journal pendingSnapshotRestore,
) error {
	if (journal.FormatVersion != 1 && journal.FormatVersion != 2 &&
		journal.FormatVersion != 3) ||
		!validUUID(journal.OperationID) ||
		!validUUID(journal.WorkspaceID) ||
		!validUUID(journal.SnapshotID) ||
		!validUUID(journal.ProtectionSnapshotID) ||
		(journal.Phase != restorePhaseRequested &&
			journal.Phase != restorePhaseInstalling &&
			journal.Phase != restorePhaseInstalled &&
			journal.Phase != restorePhaseCommitted) ||
		journal.PreviousHead.WorkspaceID != journal.WorkspaceID ||
		journal.NextHead.WorkspaceID != journal.WorkspaceID ||
		journal.NextHead.Revision != journal.PreviousHead.Revision+1 ||
		journal.DatabaseHash == "" ||
		journal.SettingsHash == "" ||
		journal.Files == nil ||
		journal.Sequence == 0 ||
		journal.Method != "snapshot.applyRestore" ||
		journal.Scope != string(protocolv2.WorkspaceScope) ||
		journal.RequestHash == "" ||
		!json.Valid(journal.Result) {
		return errors.New("restore.journal_corrupt")
	}
	if (journal.FormatVersion == 3) != journal.StorageCaptured ||
		(journal.FormatVersion != 3 &&
			(journal.Attachments != nil || journal.PreviousStorage)) {
		return errors.New("restore.journal_corrupt")
	}
	if _, err := time.Parse(
		time.RFC3339Nano,
		journal.CompletionOccurredAt,
	); err != nil {
		return errors.New("restore.journal_corrupt")
	}
	if (journal.SourceWorkspaceID == "") !=
		(journal.SourceSnapshotID == "") ||
		(journal.SourceWorkspaceID != "" &&
			(!validUUID(journal.SourceWorkspaceID) ||
				!validUUID(journal.SourceSnapshotID))) {
		return errors.New("restore.journal_corrupt")
	}
	staging := restoreStagingRoot(paths, journal.OperationID)
	if filepath.Dir(staging) != filepath.Join(paths.temp, "restore-staging") {
		return errors.New("restore.journal_corrupt")
	}
	for relative, file := range journal.Files {
		if _, err := restoreRelativeTarget(
			filepath.Join(staging, "files"),
			relative,
		); err != nil || file.Size < 0 || file.Hash == "" {
			return errors.New("restore.journal_corrupt")
		}
	}
	attachmentKeys := make([]string, 0, len(journal.Attachments))
	for relative := range journal.Attachments {
		attachmentKeys = append(attachmentKeys, relative)
	}
	if err := validateLocalRestoreStorageKeys(attachmentKeys); err != nil {
		return errors.New("restore.journal_corrupt")
	}
	var attachmentBytes int64
	for relative, file := range journal.Attachments {
		if _, err := restoreRelativeTarget(
			filepath.Join(staging, "storage"),
			relative,
		); err != nil || file.Size < 0 || file.Hash == "" ||
			file.Size > maxSnapshotWorkingSet-attachmentBytes {
			return errors.New("restore.journal_corrupt")
		}
		attachmentBytes += file.Size
	}
	return nil
}

// ApplyPendingSnapshotRestore executes before PocketBase opens data.db. If a
// previous launch died after touching live state, it rolls back first and
// boots the old workspace; only a fresh requested journal starts installation.
func ApplyPendingSnapshotRestore(
	ctx context.Context,
	dataDir string,
	workspaceID string,
) (bool, error) {
	paths, err := resolvePaths(dataDir)
	if err != nil {
		return false, err
	}
	journal, found, err := readRestoreJournal(paths)
	if err != nil || !found {
		return false, err
	}
	if journal.WorkspaceID != workspaceID {
		return false, errors.New("restore.workspace_mismatch")
	}
	switch journal.Phase {
	case restorePhaseCommitted:
		return false, cleanupCommittedRestore(paths, journal)
	case restorePhaseInstalling, restorePhaseInstalled:
		if err := rollbackPendingSnapshotRestore(
			ctx,
			paths,
			journal,
		); err != nil {
			return false, err
		}
		return false, nil
	case restorePhaseRequested:
	default:
		return false, errors.New("restore.journal_corrupt")
	}
	if err := verifyRestoreStaging(paths, journal); err != nil {
		return false, err
	}
	rollback := restoreRollbackRoot(paths, journal.OperationID)
	if err := os.MkdirAll(filepath.Dir(rollback), 0o700); err != nil {
		return false, err
	}
	// The requested phase proves that no live file has moved yet. A previous
	// attempt may nevertheless have died after creating or staging this
	// operation's rollback directory, so discard only that validated,
	// operation-scoped directory before preparing it again.
	if err := os.RemoveAll(rollback); err != nil {
		return false, err
	}
	if err := os.Mkdir(rollback, 0o700); err != nil {
		return false, err
	}
	rollbackPrepared := true
	defer func() {
		if rollbackPrepared {
			_ = os.RemoveAll(rollback)
		}
	}()
	if journal.FormatVersion >= 2 {
		if err := stagePreviousWorkspaceSettings(
			ctx,
			paths,
			rollback,
		); err != nil {
			return false, err
		}
	}
	if journal.FormatVersion == 3 {
		journal.PreviousStorage = false
		info, err := os.Lstat(filepath.Join(paths.data, "storage"))
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return false, errors.New("restore.attachment_storage_invalid")
			}
			journal.PreviousStorage = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	journal.Phase = restorePhaseInstalling
	if err := writeRestoreJournal(paths, journal); err != nil {
		return false, err
	}
	rollbackPrepared = false
	if err := installRestoreFiles(ctx, paths, journal); err != nil {
		return false, errors.Join(
			err,
			rollbackPendingSnapshotRestore(ctx, paths, journal),
		)
	}
	store, err := filehistory.OpenPersistentHeadStore(
		filepath.Join(paths.topology, "filehistory-head.db"),
	)
	if err != nil {
		return false, errors.Join(
			err,
			rollbackPendingSnapshotRestore(ctx, paths, journal),
		)
	}
	_, publishErr := store.CompareAndSwapWithAudit(
		ctx,
		journal.PreviousHead,
		journal.NextHead,
		journal.HeadAudit,
	)
	closeErr := store.Close()
	if err := errors.Join(publishErr, closeErr); err != nil {
		return false, errors.Join(
			err,
			rollbackPendingSnapshotRestore(ctx, paths, journal),
		)
	}
	if err := writecoordinator.RotatePersistentAuditEpoch(
		ctx,
		filepath.Join(paths.coordination, "write-coordinator.db"),
		journal.WorkspaceID,
	); err != nil {
		return false, errors.Join(
			err,
			rollbackPendingSnapshotRestore(ctx, paths, journal),
		)
	}
	journal.Phase = restorePhaseInstalled
	if err := writeRestoreJournal(paths, journal); err != nil {
		return false, errors.Join(
			err,
			rollbackPendingSnapshotRestore(ctx, paths, journal),
		)
	}
	return true, nil
}

func verifyRestoreStaging(
	paths workspacePaths,
	journal pendingSnapshotRestore,
) error {
	staging := restoreStagingRoot(paths, journal.OperationID)
	if hash, _, err := hashRestoreFile(
		filepath.Join(staging, "data.db"),
	); err != nil || hash != journal.DatabaseHash {
		return errors.Join(errors.New("restore.staging_corrupt"), err)
	}
	if err := verifySQLiteDatabase(filepath.Join(staging, "data.db")); err != nil {
		return err
	}
	if hash, _, err := hashRestoreFile(
		filepath.Join(staging, "settings.json"),
	); err != nil || hash != journal.SettingsHash {
		return errors.Join(errors.New("restore.staging_corrupt"), err)
	}
	for relative, expected := range journal.Files {
		target, err := restoreRelativeTarget(
			filepath.Join(staging, "files"),
			relative,
		)
		if err != nil {
			return err
		}
		hash, size, err := hashRestoreFile(target)
		if err != nil || hash != expected.Hash || size != expected.Size {
			return errors.Join(errors.New("restore.staging_corrupt"), err)
		}
	}
	for relative, expected := range journal.Attachments {
		target, err := restoreRelativeTarget(
			filepath.Join(staging, "storage"),
			relative,
		)
		if err != nil {
			return err
		}
		hash, size, err := hashRestoreFile(target)
		if err != nil || hash != expected.Hash || size != expected.Size {
			return errors.Join(errors.New("restore.staging_corrupt"), err)
		}
	}
	return nil
}

func installRestoreFiles(
	ctx context.Context,
	paths workspacePaths,
	journal pendingSnapshotRestore,
) error {
	staging := restoreStagingRoot(paths, journal.OperationID)
	rollback := restoreRollbackRoot(paths, journal.OperationID)
	liveDatabase := filepath.Join(paths.data, "data.db")
	if err := os.Rename(
		liveDatabase,
		filepath.Join(rollback, "data.db"),
	); err != nil {
		return err
	}
	if err := os.Rename(
		filepath.Join(staging, "data.db"),
		liveDatabase,
	); err != nil {
		return err
	}
	if journal.FormatVersion == 3 {
		liveStorage := filepath.Join(paths.data, "storage")
		if journal.PreviousStorage {
			if err := os.Rename(
				liveStorage,
				filepath.Join(rollback, "storage"),
			); err != nil {
				return err
			}
		} else if _, err := os.Lstat(liveStorage); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return errors.New("restore.attachment_storage_conflict")
			}
			return err
		}
		if err := os.Rename(
			filepath.Join(staging, "storage"),
			liveStorage,
		); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(paths.files); err == nil {
		if err := os.Rename(
			paths.files,
			filepath.Join(rollback, "files"),
		); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(
		filepath.Join(staging, "files"),
		paths.files,
	); err != nil {
		return err
	}
	if journal.FormatVersion == 1 {
		liveSettings := filepath.Join(paths.metadata, "settings.json")
		if journal.PreviousSettings {
			if err := os.Rename(
				liveSettings,
				filepath.Join(rollback, "settings.json"),
			); err != nil {
				return err
			}
		}
		return os.Rename(
			filepath.Join(staging, "settings.json"),
			liveSettings,
		)
	}
	raw, err := readFileBounded(
		filepath.Join(staging, "settings.json"),
		1<<20,
	)
	if err != nil {
		return err
	}
	return replaceWorkspaceSettingsAtPath(
		ctx,
		paths,
		raw,
		journal.NextHead.MutationRevision,
	)
}

func rollbackPendingSnapshotRestore(
	ctx context.Context,
	paths workspacePaths,
	journal pendingSnapshotRestore,
) error {
	var rollbackErrors []error
	store, err := filehistory.OpenPersistentHeadStore(
		filepath.Join(paths.topology, "filehistory-head.db"),
	)
	if err == nil {
		current, found, loadErr := store.Load(ctx, journal.WorkspaceID)
		if loadErr != nil {
			rollbackErrors = append(rollbackErrors, loadErr)
		} else if found && current == journal.NextHead {
			rollbackHead := journal.PreviousHead
			rollbackHead.Revision = journal.NextHead.Revision + 1
			rollbackHead.MutationRevision =
				journal.NextHead.MutationRevision
			rollbackHead.SessionEpoch = journal.NextHead.SessionEpoch
			rollbackHead.FenceEpoch = journal.NextHead.FenceEpoch
			rollbackHead.ClaimID = journal.NextHead.ClaimID
			payload, _ := json.Marshal(map[string]any{
				"type":         "fileHistory.snapshotRestoreRolledBack",
				"workspaceId":  journal.WorkspaceID,
				"previousRoot": journal.NextHead.Root,
				"root":         rollbackHead.Root,
				"headRevision": rollbackHead.Revision,
			})
			envelope, envelopeErr := auditledger.NewEnvelope(
				fmt.Sprintf(
					"filehistory-head:%s:%020d",
					journal.WorkspaceID,
					rollbackHead.Revision,
				),
				"filehistory:"+journal.WorkspaceID,
				rollbackHead.Revision,
				"restore-rollback:"+journal.OperationID,
				payload,
				time.Now().UTC(),
			)
			if envelopeErr != nil {
				rollbackErrors = append(
					rollbackErrors,
					envelopeErr,
				)
			} else if _, casErr := store.CompareAndSwapWithAudit(
				ctx,
				current,
				rollbackHead,
				envelope,
			); casErr != nil {
				rollbackErrors = append(rollbackErrors, casErr)
			}
		} else if found && current != journal.PreviousHead {
			rollbackErrors = append(
				rollbackErrors,
				errors.New("restore.rollback_head_conflict"),
			)
		}
		rollbackErrors = append(rollbackErrors, store.Close())
	} else {
		rollbackErrors = append(rollbackErrors, err)
	}
	rollback := restoreRollbackRoot(paths, journal.OperationID)
	if _, err := os.Lstat(filepath.Join(rollback, "data.db")); err == nil {
		_ = os.Remove(filepath.Join(paths.data, "data.db"))
		rollbackErrors = append(
			rollbackErrors,
			os.Rename(
				filepath.Join(rollback, "data.db"),
				filepath.Join(paths.data, "data.db"),
			),
		)
	}
	if _, err := os.Lstat(filepath.Join(rollback, "files")); err == nil {
		_ = os.RemoveAll(paths.files)
		rollbackErrors = append(
			rollbackErrors,
			os.Rename(filepath.Join(rollback, "files"), paths.files),
		)
	}
	if journal.FormatVersion == 3 {
		rollbackErrors = append(
			rollbackErrors,
			rollbackSnapshotRestoreStorage(paths, journal),
		)
	}
	if journal.FormatVersion == 1 {
		liveSettings := filepath.Join(paths.metadata, "settings.json")
		if journal.PreviousSettings {
			if _, err := os.Lstat(
				filepath.Join(rollback, "settings.json"),
			); err == nil {
				_ = os.Remove(liveSettings)
				rollbackErrors = append(
					rollbackErrors,
					os.Rename(
						filepath.Join(rollback, "settings.json"),
						liveSettings,
					),
				)
			}
		} else {
			_ = os.Remove(liveSettings)
		}
	} else {
		previous, readErr := readFileBounded(
			filepath.Join(rollback, "settings.json"),
			1<<20,
		)
		if readErr != nil {
			rollbackErrors = append(rollbackErrors, readErr)
		} else {
			rollbackErrors = append(
				rollbackErrors,
				replaceWorkspaceSettingsAtPath(
					ctx,
					paths,
					previous,
					journal.PreviousHead.MutationRevision,
				),
			)
		}
	}
	if err := errors.Join(rollbackErrors...); err != nil {
		return err
	}
	if err := os.Remove(restoreJournalPath(paths)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.RemoveAll(restoreStagingRoot(paths, journal.OperationID))
	_ = os.RemoveAll(rollback)
	return nil
}

func rollbackSnapshotRestoreStorage(
	paths workspacePaths,
	journal pendingSnapshotRestore,
) error {
	return rollbackSnapshotRestoreStorageWithMatcher(
		paths, journal, snapshotRestoreStorageMatches,
	)
}

func rollbackSnapshotRestoreStorageWithMatcher(
	paths workspacePaths,
	journal pendingSnapshotRestore,
	storageMatches func(string, map[string]restoreStagedFile) (bool, error),
) error {
	conflict := errors.New("restore.attachment_storage_conflict")
	rollback := restoreRollbackRoot(paths, journal.OperationID)
	directoryExists := func(target string) (bool, error) {
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, conflict
		}
		return true, nil
	}
	validRollback, err := directoryExists(rollback)
	if err != nil || !validRollback {
		return errors.Join(conflict, err)
	}
	live := filepath.Join(paths.data, "storage")
	previous := filepath.Join(rollback, "storage")
	quarantine := filepath.Join(rollback, "storage-quarantine")
	previousExists, err := directoryExists(previous)
	if err != nil {
		return err
	}
	quarantineExists, err := directoryExists(quarantine)
	if err != nil {
		return err
	}
	if journal.PreviousStorage && !previousExists {
		if quarantineExists || journal.Phase != restorePhaseInstalling {
			return conflict
		}
		liveExists, err := directoryExists(live)
		if err != nil || !liveExists {
			return errors.Join(conflict, err)
		}
		if err := os.Rename(live, quarantine); err != nil {
			return err
		}
		matches, matchErr := storageMatches(quarantine, journal.Attachments)
		liveExists, liveErr := directoryExists(live)
		if liveErr != nil || liveExists {
			return errors.Join(conflict, matchErr, liveErr)
		}
		restoreErr := os.Rename(quarantine, live)
		if matchErr != nil || matches || restoreErr != nil {
			return errors.Join(conflict, matchErr, restoreErr)
		}
		return nil
	}
	if !quarantineExists {
		liveExists, err := directoryExists(live)
		if err != nil {
			return err
		}
		if !liveExists {
			if previousExists {
				return os.Rename(previous, live)
			}
			return nil
		}
		if err := os.Rename(live, quarantine); err != nil {
			return err
		}
	}
	matches, matchErr := snapshotRestoreStorageMatches(quarantine, journal.Attachments)
	if matchErr != nil || !matches {
		liveExists, liveErr := directoryExists(live)
		if liveErr != nil || liveExists {
			return errors.Join(conflict, matchErr, liveErr)
		}
		return errors.Join(conflict, matchErr, os.Rename(quarantine, live))
	}
	liveExists, err := directoryExists(live)
	if err != nil || liveExists {
		return errors.Join(conflict, err)
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return err
	}
	if previousExists {
		return os.Rename(previous, live)
	}
	return nil
}

func snapshotRestoreStorageMatches(
	root string,
	expected map[string]restoreStagedFile,
) (bool, error) {
	return snapshotRestoreStorageMatchesWithHasher(root, expected, hashRestoreFile)
}

func snapshotRestoreStorageMatchesWithHasher(
	root string,
	expected map[string]restoreStagedFile,
	hashFile func(string) (string, int64, error),
) (bool, error) {
	mismatch := errors.New("restore.attachment_storage_mismatch")
	invalid := errors.New("restore.attachment_storage_invalid")
	info, err := os.Lstat(root)
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, invalid
	}
	files := 0
	err = filepath.Walk(root, func(target string, info os.FileInfo, err error) error {
		if err != nil || target == root {
			return err
		}
		relative, err := filepath.Rel(root, target)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if info.IsDir() {
			if _, expectedFile := expected[relative]; expectedFile {
				return invalid
			}
			for name := range expected {
				if strings.HasPrefix(name, relative+"/") {
					return nil
				}
			}
			return mismatch
		}
		if !info.Mode().IsRegular() {
			return invalid
		}
		file, found := expected[relative]
		if !found {
			return mismatch
		}
		if info.Size() != file.Size {
			return mismatch
		}
		hash, size, err := hashFile(target)
		if err != nil {
			return err
		}
		if hash != file.Hash || size != file.Size {
			return mismatch
		}
		files++
		return nil
	})
	if errors.Is(err, mismatch) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return files == len(expected), nil
}

func (runtime *Runtime) CompletePendingSnapshotRestore(
	ctx context.Context,
) error {
	paths, err := resolvePaths(runtime.app.DataDir())
	if err != nil {
		return err
	}
	journal, found, err := readRestoreJournal(paths)
	if err != nil || !found {
		return err
	}
	if journal.WorkspaceID != runtime.manifest.WorkspaceID ||
		journal.Phase != restorePhaseInstalled {
		return errors.New("restore.journal_state_invalid")
	}
	runtime.quiesceWorkspaceSearch()
	// Search is derived and deliberately excluded from snapshots. Invalidate
	// its generation before committing completion, so a failure keeps the
	// installed journal recoverable and no restored job resumes on a stale hit.
	searchCode := "workspace_search.restore_rebuild_required"
	if invalidateErr := runtime.search.Invalidate(ctx); invalidateErr != nil {
		return publicSearchError(invalidateErr)
	}
	runtime.searchMu.Lock()
	runtime.searchStatus = workbenchcontracts.SearchStatus{
		State: "degraded", ErrorCode: &searchCode,
	}
	runtime.searchMu.Unlock()
	occurredAt, err := time.Parse(
		time.RFC3339Nano,
		journal.CompletionOccurredAt,
	)
	if err != nil {
		return errors.New("restore.journal_corrupt")
	}
	payload, err := json.Marshal(map[string]any{
		"type":                 "workspace.snapshotRestored",
		"workspaceId":          journal.WorkspaceID,
		"snapshotId":           journal.SnapshotID,
		"protectionSnapshotId": journal.ProtectionSnapshotID,
		"operationId":          journal.OperationID,
	})
	if err != nil {
		return err
	}
	envelope, err := auditledger.NewEnvelope(
		"snapshot-restore:"+journal.OperationID,
		"snapshot-restore:"+journal.OperationID,
		1,
		"snapshot-restore:"+journal.OperationID,
		payload,
		occurredAt,
	)
	if err != nil {
		return err
	}
	if _, err := runtime.ledger.Append(ctx, envelope); err != nil {
		return err
	}
	if journal.SourceWorkspaceID != "" {
		importPayload, marshalErr := json.Marshal(map[string]any{
			"type":              "workspace.snapshotImported",
			"sourceWorkspaceId": journal.SourceWorkspaceID,
			"sourceSnapshotId":  journal.SourceSnapshotID,
			"targetWorkspaceId": journal.WorkspaceID,
			"targetSnapshotId":  journal.SnapshotID,
			"operationId":       journal.OperationID,
		})
		if marshalErr != nil {
			return marshalErr
		}
		importEnvelope, envelopeErr := auditledger.NewEnvelope(
			"snapshot-import:"+journal.OperationID,
			"snapshot-import:"+journal.OperationID,
			1,
			"snapshot-import:"+journal.OperationID,
			importPayload,
			occurredAt,
		)
		if envelopeErr != nil {
			return envelopeErr
		}
		if _, appendErr := runtime.ledger.Append(
			ctx,
			importEnvelope,
		); appendErr != nil {
			return appendErr
		}
	}
	operationReceipt := protocolv2.OperationReceipt{
		OperationID: journal.OperationID,
		WorkspaceID: journal.WorkspaceID,
		Method:      journal.Method,
		Scope:       protocolv2.ScopeKind(journal.Scope),
		RequestHash: journal.RequestHash,
		Result:      append(json.RawMessage(nil), journal.Result...),
	}
	token, _ := runtime.coordinator.Current()
	committed, found, err := runtime.catalog.LoadOperationReceipt(
		ctx,
		journal.WorkspaceID,
		journal.OperationID,
	)
	if err != nil {
		return err
	}
	if found &&
		(committed.Method != operationReceipt.Method ||
			committed.Scope != operationReceipt.Scope ||
			committed.RequestHash != operationReceipt.RequestHash ||
			!bytes.Equal(committed.Result, operationReceipt.Result)) {
		return protocolv2.ErrOperationConflict
	}
	if !found {
		captureContext, bindErr := snapshot.WithOperationReceiptBuilder(
			ctx,
			func(snapshot.Record) (protocolv2.OperationReceipt, error) {
				return operationReceipt, nil
			},
		)
		if bindErr != nil {
			return bindErr
		}
		recovery, _, captureErr := runtime.snapshots.Capture(
			captureContext,
			snapshot.CaptureRequest{
				WorkspaceID: runtime.manifest.WorkspaceID,
				Authority:   token.Authority(),
				Trigger:     snapshot.TriggerRestore,
				Pinned:      true,
			},
		)
		if captureErr != nil || recovery.SnapshotID == "" {
			return errors.Join(
				errors.New("restore.recovery_snapshot_failed"),
				captureErr,
			)
		}
	}
	journal.Phase = restorePhaseCommitted
	if err := runtime.state.commitOperationReceipt(
		ctx,
		protocolv2.Session{
			WorkspaceID: journal.WorkspaceID,
			Epoch:       journal.NextHead.SessionEpoch,
			Sequence:    journal.Sequence,
		},
		operationReceipt,
	); err != nil {
		return err
	}
	if err := writeRestoreJournal(paths, journal); err != nil {
		return err
	}
	if err := cleanupCommittedRestore(paths, journal); err != nil {
		return err
	}
	if err := runtime.startRestoreSearchRebuild(context.Background()); err != nil {
		return errors.New("workspace_search.restore_rebuild_required")
	}
	return nil
}

func (runtime *Runtime) startRestoreSearchRebuild(ctx context.Context) error {
	if runtime.restoreSearchRebuild != nil {
		return runtime.restoreSearchRebuild(ctx)
	}
	_, err := runtime.rebuildWorkspaceSearch(ctx, nil, json.RawMessage("{}"))
	return err
}

func cleanupCommittedRestore(
	paths workspacePaths,
	journal pendingSnapshotRestore,
) error {
	if err := os.Remove(restoreJournalPath(paths)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.RemoveAll(restoreStagingRoot(paths, journal.OperationID))
	_ = os.RemoveAll(restoreRollbackRoot(paths, journal.OperationID))
	return nil
}

func RollbackPendingSnapshotRestore(
	ctx context.Context,
	dataDir string,
	workspaceID string,
) (bool, error) {
	paths, err := resolvePaths(dataDir)
	if err != nil {
		return false, err
	}
	journal, found, err := readRestoreJournal(paths)
	if err != nil || !found {
		return false, err
	}
	if journal.WorkspaceID != workspaceID {
		return false, errors.New("restore.workspace_mismatch")
	}
	if journal.Phase == restorePhaseCommitted {
		return false, errors.New("restore.already_committed")
	}
	if err := rollbackPendingSnapshotRestore(ctx, paths, journal); err != nil {
		return false, err
	}
	return true, nil
}

func hashRestoreFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, errors.Join(
			errors.New("restore.file_invalid"),
			err,
		)
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), size, nil
}
