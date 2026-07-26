package backup

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
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/archive"
	"github.com/pocketbase/pocketbase/tools/filesystem"

	"github.com/vibetable/vibetable/sidecar/internal/attachments"
)

var namePattern = regexp.MustCompile(
	`^[a-z0-9][a-z0-9_-]{0,62}\.zip$`,
)

type IntegrityChecker interface {
	Integrity(context.Context, core.App) (
		attachments.IntegrityReport, error,
	)
}

type Service struct {
	app       core.App
	integrity IntegrityChecker
	now       func() time.Time
	restart   func() error
}

type Entry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	SHA256   string `json:"sha256"`
}

type CreateResult struct {
	Backup    Entry                       `json:"backup"`
	Integrity attachments.IntegrityReport `json:"integrity"`
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (err *Error) Error() string {
	return err.Code + ": " + err.Message
}

func New(app core.App, integrity IntegrityChecker) *Service {
	return &Service{
		app: app, integrity: integrity,
		now:     func() time.Time { return time.Now().UTC() },
		restart: app.Restart,
	}
}

// WithRestart overrides the process restart hook for integration tests. The
// production service always uses PocketBase's process restart implementation.
func (service *Service) WithRestart(restart func() error) *Service {
	if restart != nil {
		service.restart = restart
	}
	return service
}

// WithNow overrides backup timestamps for deterministic integration tests.
func (service *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		service.now = now
	}
	return service
}

func ValidateName(name string) error {
	if !namePattern.MatchString(name) ||
		path.Base(name) != name {
		return &Error{
			Code:    "backup.name_invalid",
			Message: "backup name must use lowercase letters, digits, _ or - and end in .zip",
		}
	}
	return nil
}

func (service *Service) Create(
	ctx context.Context,
	name string,
) (CreateResult, error) {
	if err := ValidateName(name); err != nil {
		return CreateResult{}, err
	}
	if service.integrity == nil {
		return CreateResult{}, backupError(
			"backup.integrity_unavailable",
			"attachment integrity checker is unavailable",
			false,
		)
	}
	report, err := service.integrity.Integrity(ctx, service.app)
	if err != nil {
		return CreateResult{}, backupError(
			"backup.integrity_failed",
			"attachment integrity could not be verified",
			true,
		)
	}
	if !report.Valid {
		return CreateResult{}, backupError(
			"backup.integrity_failed",
			"backup was blocked because attachment integrity is invalid",
			false,
		)
	}
	if err := service.app.CreateBackup(ctx, name); err != nil {
		return CreateResult{}, backupError(
			"backup.create_failed",
			"application backup could not be created",
			true,
		)
	}
	entry, err := service.entry(ctx, name)
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Backup: entry, Integrity: report}, nil
}

func (service *Service) List(ctx context.Context) ([]Entry, error) {
	fsys, err := service.app.NewBackupsFilesystem()
	if err != nil {
		return nil, backupError(
			"backup.storage_failed",
			"backup storage is unavailable",
			true,
		)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	objects, err := fsys.List("")
	if err != nil {
		return nil, backupError(
			"backup.storage_failed",
			"backup storage could not be listed",
			true,
		)
	}
	result := make([]Entry, 0, len(objects))
	for _, object := range objects {
		if object.IsDir || !namePattern.MatchString(object.Key) {
			continue
		}
		entry, entryErr := entryFromFilesystem(
			fsys, object.Key, object.Size, object.ModTime,
		)
		if entryErr != nil {
			return nil, entryErr
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Modified == result[right].Modified {
			return result[left].Name < result[right].Name
		}
		return result[left].Modified > result[right].Modified
	})
	return result, nil
}

func (service *Service) Delete(ctx context.Context, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := service.entry(ctx, name); err != nil {
		return err
	}
	fsys, err := service.app.NewBackupsFilesystem()
	if err != nil {
		return backupError(
			"backup.storage_failed",
			"backup storage is unavailable",
			true,
		)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	if err := fsys.Delete(name); err != nil {
		return backupError(
			"backup.delete_failed",
			"backup archive could not be deleted",
			true,
		)
	}
	return nil
}

func (service *Service) Restore(
	ctx context.Context,
	name string,
) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := service.entry(ctx, name); err != nil {
		return err
	}
	safetyName := "pre_restore_" +
		service.now().Format("20060102_150405") + ".zip"
	if err := service.app.CreateBackup(ctx, safetyName); err != nil {
		return backupError(
			"backup.safety_copy_failed",
			"pre-restore safety backup could not be created",
			true,
		)
	}
	if runtime.GOOS == "windows" {
		entry, err := service.entry(ctx, name)
		if err != nil {
			return err
		}
		if err := writeRestoreMarker(service.app.DataDir(), restoreMarker{
			Name: name, SHA256: entry.SHA256,
		}); err != nil {
			return backupError(
				"backup.restore_failed",
				"restore marker could not be staged",
				true,
			)
		}
		if err := service.restart(); err != nil {
			_ = os.Remove(restoreMarkerPath(service.app.DataDir()))
			return backupError(
				"backup.restore_failed",
				"application could not restart to apply the backup",
				true,
			)
		}
		return nil
	}
	if err := service.app.RestoreBackup(ctx, name); err != nil {
		return backupError(
			"backup.restore_failed",
			"application backup could not be restored",
			true,
		)
	}
	return nil
}

const restoreMarkerName = ".vibetable_restore_pending.json"
const restoreRollbackRootName = ".vibetable_restore_rollback"

const (
	restorePhaseRequested = "requested"
	restorePhaseInstalled = "installed"
)

type restoreMarker struct {
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	Phase       string `json:"phase,omitempty"`
	RollbackDir string `json:"rollbackDir,omitempty"`
}

func restoreMarkerPath(dataDir string) string {
	return filepath.Join(dataDir, restoreMarkerName)
}

func writeRestoreMarker(dataDir string, marker restoreMarker) error {
	if err := ValidateName(marker.Name); err != nil {
		return err
	}
	if !validSHA256(marker.SHA256) {
		return errors.New("backup digest is invalid")
	}
	if marker.Phase == "" {
		marker.Phase = restorePhaseRequested
	}
	if marker.Phase != restorePhaseRequested &&
		marker.Phase != restorePhaseInstalled {
		return errors.New("restore marker phase is invalid")
	}
	if marker.Phase == restorePhaseInstalled {
		if !validRollbackDir(marker.RollbackDir) {
			return errors.New("restore rollback directory is invalid")
		}
	} else if marker.RollbackDir != "" {
		return errors.New("requested restore cannot contain rollback directory")
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dataDir, ".restore-marker-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
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
	return os.Rename(temporaryName, restoreMarkerPath(dataDir))
}

// ApplyPendingRestore runs before PocketBase opens data.db. PocketBase's own
// RestoreBackup intentionally rejects Windows because it cannot replace an
// open database there; staging the request and applying it on restart keeps
// the same safety-backup semantics without moving live file handles.
func ApplyPendingRestore(dataDir string) (bool, error) {
	marker, found, err := readRestoreMarker(dataDir)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if marker.Phase == restorePhaseInstalled {
		_, rollbackErr := RollbackPendingRestore(dataDir)
		if rollbackErr != nil {
			return false, fmt.Errorf(
				"rollback interrupted staged restore: %w",
				rollbackErr,
			)
		}
		return false, nil
	}
	backupPath := filepath.Join(dataDir, core.LocalBackupsDirName, marker.Name)
	if digest, err := hashLocalFile(backupPath); err != nil || digest != marker.SHA256 {
		return false, errors.New("staged backup digest does not match")
	}
	tempRoot := filepath.Join(dataDir, core.LocalTempDirName)
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return false, fmt.Errorf("create restore temp directory: %w", err)
	}
	extracted, err := os.MkdirTemp(tempRoot, "restore-new-")
	if err != nil {
		return false, fmt.Errorf("create extracted restore directory: %w", err)
	}
	defer os.RemoveAll(extracted)
	if err := archive.Extract(backupPath, extracted); err != nil {
		return false, fmt.Errorf("extract staged backup: %w", err)
	}
	if info, err := os.Stat(filepath.Join(extracted, "data.db")); err != nil || info.IsDir() {
		return false, errors.New("staged backup has no valid data.db")
	}
	rollbackRoot := filepath.Join(dataDir, restoreRollbackRootName)
	if err := os.MkdirAll(rollbackRoot, 0o700); err != nil {
		return false, fmt.Errorf("create restore rollback root: %w", err)
	}
	oldData, err := os.MkdirTemp(rollbackRoot, "restore-old-")
	if err != nil {
		return false, fmt.Errorf("create restore rollback directory: %w", err)
	}
	excluded := map[string]bool{
		core.LocalBackupsDirName: true,
		core.LocalTempDirName:    true,
		restoreRollbackRootName:  true,
	}
	if err := moveDirectoryContents(dataDir, oldData, excluded); err != nil {
		return false, fmt.Errorf("stage current data for rollback: %w", err)
	}
	marker.Phase = restorePhaseInstalled
	marker.RollbackDir = filepath.Base(oldData)
	if err := writeRestoreMarker(dataDir, marker); err != nil {
		rollbackErr := rollbackInstalledRestore(dataDir, oldData)
		return false, errors.Join(
			fmt.Errorf("persist restore transaction: %w", err),
			rollbackErr,
		)
	}
	if err := moveDirectoryContents(extracted, dataDir, excluded); err != nil {
		rollbackErr := rollbackInstalledRestore(dataDir, oldData)
		return false, errors.Join(
			fmt.Errorf("install restored data: %w", err),
			rollbackErr,
		)
	}
	return true, nil
}

// CommitPendingRestore removes rollback state only after PocketBase has opened
// the restored database and completed migrations. Until this point a crash or
// initialization error remains fully recoverable.
func CommitPendingRestore(dataDir string) error {
	marker, found, err := readRestoreMarker(dataDir)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if marker.Phase != restorePhaseInstalled {
		return errors.New("restore is not ready to commit")
	}
	oldData, err := resolveRollbackDir(dataDir, marker.RollbackDir)
	if err != nil {
		return err
	}
	if err := os.Remove(restoreMarkerPath(dataDir)); err != nil {
		return fmt.Errorf("remove committed restore marker: %w", err)
	}
	// Marker removal is the atomic commit point. A crash or cleanup failure
	// after it can at worst leave an inert temp directory; it cannot trigger a
	// rollback of already-validated data on the next start.
	_ = os.RemoveAll(oldData)
	_ = os.Remove(filepath.Join(dataDir, restoreRollbackRootName))
	return nil
}

// RollbackPendingRestore restores the pre-restore data snapshot. It is safe to
// call after initialization failure and is also invoked automatically when a
// previous process died between installing and committing a staged restore.
func RollbackPendingRestore(dataDir string) (bool, error) {
	marker, found, err := readRestoreMarker(dataDir)
	if err != nil {
		return false, err
	}
	if !found || marker.Phase != restorePhaseInstalled {
		return false, nil
	}
	oldData, err := resolveRollbackDir(dataDir, marker.RollbackDir)
	if err != nil {
		return false, err
	}
	if err := rollbackInstalledRestore(dataDir, oldData); err != nil {
		return false, err
	}
	return true, nil
}

func readRestoreMarker(dataDir string) (restoreMarker, bool, error) {
	raw, err := os.ReadFile(restoreMarkerPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return restoreMarker{}, false, nil
	}
	if err != nil {
		return restoreMarker{}, false, fmt.Errorf("read restore marker: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var marker restoreMarker
	if err := decoder.Decode(&marker); err != nil {
		return restoreMarker{}, false, fmt.Errorf(
			"decode restore marker: %w",
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return restoreMarker{}, false, errors.New(
			"restore marker contains trailing data",
		)
	}
	if marker.Phase == "" {
		marker.Phase = restorePhaseRequested
	}
	if err := ValidateName(marker.Name); err != nil ||
		!validSHA256(marker.SHA256) ||
		(marker.Phase != restorePhaseRequested &&
			marker.Phase != restorePhaseInstalled) ||
		(marker.Phase == restorePhaseInstalled &&
			!validRollbackDir(marker.RollbackDir)) ||
		(marker.Phase == restorePhaseRequested && marker.RollbackDir != "") {
		return restoreMarker{}, false, errors.New("restore marker is invalid")
	}
	return marker, true, nil
}

func validRollbackDir(name string) bool {
	return filepath.Base(name) == name &&
		strings.HasPrefix(name, "restore-old-") &&
		len(name) > len("restore-old-")
}

func resolveRollbackDir(dataDir string, name string) (string, error) {
	if !validRollbackDir(name) {
		return "", errors.New("restore rollback directory is invalid")
	}
	rollbackRoot := filepath.Clean(
		filepath.Join(dataDir, restoreRollbackRootName),
	)
	result := filepath.Clean(filepath.Join(rollbackRoot, name))
	if filepath.Dir(result) != rollbackRoot {
		return "", errors.New("restore rollback directory escapes rollback root")
	}
	if info, err := os.Stat(result); err != nil || !info.IsDir() {
		return "", errors.New("restore rollback directory is unavailable")
	}
	return result, nil
}

func rollbackInstalledRestore(dataDir string, oldData string) error {
	rollbackRoot := filepath.Join(dataDir, restoreRollbackRootName)
	if err := os.MkdirAll(rollbackRoot, 0o700); err != nil {
		return fmt.Errorf("create restore rollback root: %w", err)
	}
	failedData, err := os.MkdirTemp(rollbackRoot, "restore-failed-")
	if err != nil {
		return fmt.Errorf("create failed restore directory: %w", err)
	}
	excluded := map[string]bool{
		core.LocalBackupsDirName: true,
		core.LocalTempDirName:    true,
		restoreRollbackRootName:  true,
	}
	if err := moveDirectoryContents(dataDir, failedData, excluded); err != nil {
		_ = os.RemoveAll(failedData)
		return fmt.Errorf("stage failed restore for rollback: %w", err)
	}
	if err := moveDirectoryContents(oldData, dataDir, excluded); err != nil {
		restoreErr := moveDirectoryContents(failedData, dataDir, excluded)
		return errors.Join(
			fmt.Errorf("restore pre-restore data: %w", err),
			restoreErr,
		)
	}
	if err := os.Remove(restoreMarkerPath(dataDir)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove rolled back restore marker: %w", err)
	}
	if err := os.RemoveAll(failedData); err != nil {
		return fmt.Errorf("remove failed restore data: %w", err)
	}
	if err := os.RemoveAll(oldData); err != nil {
		return fmt.Errorf("remove consumed rollback data: %w", err)
	}
	_ = os.Remove(rollbackRoot)
	return nil
}

func hashLocalFile(name string) (string, error) {
	stream, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, stream); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func moveDirectoryContents(
	source string,
	destination string,
	excluded map[string]bool,
) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	moved := make([]string, 0, len(entries))
	for _, entry := range entries {
		if excluded[entry.Name()] {
			continue
		}
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(destination, entry.Name())
		if err := os.Rename(from, to); err != nil {
			// SQLite/Windows can remove transient journal sidecars between
			// ReadDir and Rename. A vanished source is already absent from the
			// live data set and is therefore safe to skip; every other rename
			// failure still rolls the transaction back.
			if errors.Is(err, os.ErrNotExist) {
				if _, statErr := os.Lstat(from); errors.Is(statErr, os.ErrNotExist) {
					continue
				}
			}
			rollbackErrors := []error{err}
			for index := len(moved) - 1; index >= 0; index-- {
				name := moved[index]
				if rollbackErr := os.Rename(
					filepath.Join(destination, name),
					filepath.Join(source, name),
				); rollbackErr != nil {
					rollbackErrors = append(rollbackErrors, rollbackErr)
				}
			}
			return errors.Join(rollbackErrors...)
		}
		moved = append(moved, entry.Name())
	}
	return nil
}

func (service *Service) entry(
	ctx context.Context,
	name string,
) (Entry, error) {
	fsys, err := service.app.NewBackupsFilesystem()
	if err != nil {
		return Entry{}, backupError(
			"backup.storage_failed",
			"backup storage is unavailable",
			true,
		)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	attributes, err := fsys.Attributes(name)
	if err != nil {
		return Entry{}, backupError(
			"backup.not_found",
			"backup archive was not found",
			false,
		)
	}
	return entryFromFilesystem(
		fsys, name, attributes.Size, attributes.ModTime,
	)
}

func entryFromFilesystem(
	fsys *filesystem.System,
	name string,
	size int64,
	modified time.Time,
) (Entry, error) {
	reader, err := fsys.GetReader(name)
	if err != nil {
		return Entry{}, backupError(
			"backup.storage_failed",
			"backup archive could not be read",
			true,
		)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, reader)
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil {
		return Entry{}, backupError(
			"backup.storage_failed",
			"backup archive could not be verified",
			true,
		)
	}
	return Entry{
		Name: name, Size: size,
		Modified: modified.UTC().Format(time.RFC3339Nano),
		SHA256:   hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func backupError(code, message string, retryable bool) *Error {
	return &Error{
		Code: code, Message: message, Retryable: retryable,
	}
}

func IsError(err error, code string) bool {
	var productErr *Error
	return errors.As(err, &productErr) && productErr.Code == code
}
