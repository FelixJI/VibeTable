package workspacev2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type workspaceConflictExternalStage struct {
	FormatVersion uint64                          `json:"formatVersion"`
	Tables        []workspaceConflictTableStage   `json:"tables,omitempty"`
	Settings      *workspaceConflictSettingsStage `json:"settings,omitempty"`
}

type workspaceConflictTableStage struct {
	TableID          string                         `json:"tableId"`
	Expected         conflictresolution.TableState  `json:"expected"`
	Chosen           conflictresolution.TableState  `json:"chosen"`
	DatabaseObjectID objectrepo.ObjectID            `json:"databaseObjectId,omitempty"`
	Deleted          bool                           `json:"deleted"`
	Attachments      map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
}

type workspaceConflictSettingsStage struct {
	ExpectedObjectID objectrepo.ObjectID `json:"expectedObjectId"`
	ObjectID         objectrepo.ObjectID `json:"objectId"`
	// Previous is the exact authoritative projection observed while staging.
	// It makes rollback and restart deterministic even when the expected
	// snapshot carries the legacy "{}" no-setting marker.
	Previous json.RawMessage `json:"previous,omitempty"`
}

func (appender *workspaceConflictAppender) stageConflictTable(
	ctx context.Context,
	expected conflictresolution.TableState,
	chosen conflictresolution.TableState,
) (workspaceConflictTableStage, error) {
	if strings.TrimSpace(chosen.TableID) == "" {
		return workspaceConflictTableStage{},
			conflictresolution.ErrApplyUnproven
	}
	result := workspaceConflictTableStage{
		TableID:  chosen.TableID,
		Expected: expected,
		Chosen:   chosen,
		Deleted:  chosen.Deleted,
	}
	if chosen.Deleted {
		return result, nil
	}
	result.DatabaseObjectID = objectrepo.ObjectID(chosen.DatabaseObjectID)
	if result.DatabaseObjectID == "" {
		return workspaceConflictTableStage{},
			conflictresolution.ErrApplyUnproven
	}
	database, err := readConflictObject(
		ctx, appender.owner.runtime.repository, result.DatabaseObjectID,
	)
	if err != nil {
		return workspaceConflictTableStage{}, err
	}
	projection, err := conflictresolution.ProjectSQLiteDatabase(
		ctx, database, chosen.DatabaseObjectID, chosen.AttachmentObjects,
	)
	if err != nil {
		return workspaceConflictTableStage{}, err
	}
	projected, exists := projection.Tables[chosen.TableID]
	if !exists || !reflect.DeepEqual(projected, chosen) {
		return workspaceConflictTableStage{},
			conflictresolution.ErrApplyUnproven
	}
	result.Attachments = make(
		map[string]objectrepo.ObjectID, len(chosen.AttachmentObjects),
	)
	roots := make([]objectrepo.ObjectID, 0, len(chosen.AttachmentObjects))
	for key, id := range chosen.AttachmentObjects {
		if !strings.HasPrefix(key, chosen.TableID+"/") || id == "" {
			return workspaceConflictTableStage{},
				conflictresolution.ErrApplyUnproven
		}
		objectID := objectrepo.ObjectID(id)
		result.Attachments[key] = objectID
		roots = append(roots, objectID)
	}
	if len(roots) != 0 {
		report, err := appender.owner.runtime.repository.Verify(ctx, roots)
		if err != nil {
			return workspaceConflictTableStage{}, err
		}
		if !report.Valid {
			return workspaceConflictTableStage{},
				conflictresolution.ErrApplyUnproven
		}
	}
	return result, nil
}

func (appender *workspaceConflictAppender) stageConflictSettings(
	ctx context.Context,
	expected conflictresolution.SettingsState,
	chosen conflictresolution.SettingsState,
) (workspaceConflictSettingsStage, error) {
	id := objectrepo.ObjectID(chosen.ObjectID)
	if id == "" {
		return workspaceConflictSettingsStage{},
			conflictresolution.ErrApplyUnproven
	}
	raw, err := readConflictObject(
		ctx, appender.owner.runtime.repository, id,
	)
	if err != nil {
		return workspaceConflictSettingsStage{}, err
	}
	if err := validateConflictSettings(raw); err != nil {
		return workspaceConflictSettingsStage{}, err
	}
	previous, err := snapshotWorkspaceSettings(
		ctx,
		appender.owner.runtime.state,
	)
	if err != nil {
		return workspaceConflictSettingsStage{}, err
	}
	expectedID := objectrepo.ObjectID(expected.ObjectID)
	expectedRaw, err := readConflictObject(
		ctx,
		appender.owner.runtime.repository,
		expectedID,
	)
	if err != nil {
		return workspaceConflictSettingsStage{}, err
	}
	differ, err := workspaceSettingsDiffer(previous, expectedRaw)
	if err != nil {
		return workspaceConflictSettingsStage{}, err
	}
	if differ {
		return workspaceConflictSettingsStage{},
			conflictresolution.ErrStalePlan
	}
	return workspaceConflictSettingsStage{
		ExpectedObjectID: expectedID,
		ObjectID:         id,
		Previous:         append(json.RawMessage(nil), previous...),
	}, nil
}

func validateConflictSettings(raw []byte) error {
	_, _, err := decodeWorkspaceSettingsSnapshot(raw)
	return err
}

func (appender *workspaceConflictAppender) applyExternalStage(
	ctx context.Context,
	intent writecoordinator.WriteIntent,
	stage filehistory.ConflictStage,
) (result filehistory.ExternalApplyResult, err error) {
	if len(stage.External) == 0 {
		return result, nil
	}
	var external workspaceConflictExternalStage
	if err := decodeStrictReplicaOneShot(stage.External, &external); err != nil ||
		external.FormatVersion != 1 ||
		len(external.Tables) == 0 && external.Settings == nil {
		return result, conflictresolution.ErrApplyUnproven
	}
	if err := appender.conflictFault("before_database"); err != nil {
		return result, err
	}
	receiptAlready, err := appender.hasConflictBusinessReceipt(
		ctx, stage.OperationID,
	)
	if err != nil {
		return result, err
	}
	result.Irreversible = receiptAlready
	started, err := appender.owner.applier.ExternalStarted(
		ctx, stage.StageID,
	)
	if err != nil {
		return result, err
	}
	if !receiptAlready {
		if started {
			if err := appender.restoreExternalExpected(
				ctx, external,
			); err != nil {
				return result, err
			}
		}
		if err := appender.validateExternalExpected(
			ctx, external,
		); err != nil {
			return result, err
		}
		if !started {
			if err := appender.owner.applier.MarkExternalStarted(
				context.WithoutCancel(ctx), stage.StageID,
			); err != nil {
				return result, err
			}
		}
	}
	sources := map[objectrepo.ObjectID]*conflictCandidateSource{}
	defer func() {
		for _, source := range sources {
			source.close()
		}
	}()
	for _, table := range external.Tables {
		if table.Deleted {
			continue
		}
		if _, exists := sources[table.DatabaseObjectID]; exists {
			continue
		}
		source, err := appender.openConflictCandidateSource(
			ctx, table.DatabaseObjectID,
		)
		if err != nil {
			return result, err
		}
		sources[table.DatabaseObjectID] = source
	}
	businessCtx, err := writecoordinator.WithBusinessIntent(
		ctx,
		intent,
		"conflict.external",
		stage.OperationID,
	)
	if err != nil {
		return result, err
	}
	err = appender.owner.runtime.app.RunInTransaction(
		func(txApp core.App) error {
			if err := applyConflictTables(
				txApp, external.Tables, sources,
			); err != nil {
				return err
			}
			if err := appender.conflictFault("after_database"); err != nil {
				return err
			}
			if err := appender.applyConflictAttachments(
				ctx, external.Tables,
			); err != nil {
				return err
			}
			if err := appender.conflictFault("after_attachments"); err != nil {
				return err
			}
			if external.Settings != nil {
				raw, err := readConflictObject(
					ctx,
					appender.owner.runtime.repository,
					external.Settings.ObjectID,
				)
				if err != nil {
					return err
				}
				if err := validateConflictSettings(raw); err != nil {
					return err
				}
				if err := replaceWorkspaceSettings(
					ctx,
					appender.owner.runtime.state,
					raw,
					intent.MutationRevision,
				); err != nil {
					return err
				}
			}
			if err := appender.conflictFault("after_settings"); err != nil {
				return err
			}
			if receiptAlready {
				return nil
			}
			return writecoordinator.PersistPocketBaseReceipt(
				businessCtx, txApp, time.Now().UTC(),
			)
		},
	)
	if err != nil {
		rollbackErr := appender.restoreExternalExpected(
			context.WithoutCancel(ctx), external,
		)
		if rollbackErr != nil {
			appender.requestConflictShutdown()
		}
		return result, errors.Join(err, rollbackErr)
	}
	result.Irreversible = true
	if err := appender.owner.runtime.app.ReloadCachedCollections(); err != nil {
		appender.requestConflictShutdown()
		return result, err
	}
	if err := appender.validateExternalChosen(ctx, external); err != nil {
		appender.requestConflictShutdown()
		return result, errors.Join(conflictresolution.ErrApplyUnproven, err)
	}
	if err := appender.conflictFault("after_pb_receipt"); err != nil {
		// The authoritative PB receipt is already committed and cannot be
		// rolled back independently. Stop serving this runtime; startup uses
		// the applying conflict plan and the receipt as the unique proof, then
		// deterministically rolls the remaining phases forward.
		appender.requestConflictShutdown()
		return result, err
	}
	if err := appender.conflictFault("before_filehistory"); err != nil {
		appender.requestConflictShutdown()
		return result, err
	}
	return result, nil
}

func (appender *workspaceConflictAppender) validateExternalChosen(
	ctx context.Context,
	external workspaceConflictExternalStage,
) error {
	live := &frozenSource{
		app:   appender.owner.runtime.app,
		paths: appender.owner.runtime.paths,
		state: appender.owner.runtime.state,
	}
	if len(external.Tables) != 0 {
		database, err := live.snapshotDatabase(ctx)
		if err != nil {
			return err
		}
		attachments, err := live.snapshotAttachments(ctx)
		if err != nil {
			return err
		}
		attachmentIDs := make(map[string]string, len(attachments))
		for key, content := range attachments {
			attachmentIDs[key] = conflictObjectID(content)
		}
		projection, err := conflictresolution.ProjectSQLiteDatabase(
			ctx, database, "live", attachmentIDs,
		)
		if err != nil {
			return err
		}
		for _, table := range external.Tables {
			current, exists := projection.Tables[table.TableID]
			if !exists {
				current = conflictresolution.TableState{
					TableID: table.TableID,
					Deleted: true,
				}
			}
			current.DatabaseObjectID = table.Chosen.DatabaseObjectID
			if !reflect.DeepEqual(current, table.Chosen) {
				return conflictresolution.ErrApplyUnproven
			}
		}
	}
	if external.Settings != nil {
		current, err := live.workspaceSettings(ctx)
		if err != nil {
			return err
		}
		matches, err := appender.workspaceSettingsChoiceMatches(
			ctx,
			current,
			external.Settings.ObjectID,
		)
		if err != nil {
			return err
		}
		if !matches {
			return conflictresolution.ErrApplyUnproven
		}
	}
	return nil
}

func (appender *workspaceConflictAppender) hasConflictBusinessReceipt(
	ctx context.Context,
	operationID string,
) (bool, error) {
	var count int
	err := appender.owner.runtime.app.DB().NewQuery(`
		SELECT COUNT(*)
		FROM workspace_v2_mutation_receipts
		WHERE workspace_id = {:workspace}
		  AND kind = 'conflict.external'
		  AND identity = {:identity}
	`).WithContext(ctx).Bind(map[string]any{
		"workspace": appender.owner.runtime.manifest.WorkspaceID,
		"identity":  operationID,
	}).Row(&count)
	if err != nil {
		return false, err
	}
	if count < 0 || count > 1 {
		return false, conflictresolution.ErrApplyUnproven
	}
	return count == 1, nil
}

func (appender *workspaceConflictAppender) validateExternalExpected(
	ctx context.Context,
	external workspaceConflictExternalStage,
) error {
	live := &frozenSource{
		app:   appender.owner.runtime.app,
		paths: appender.owner.runtime.paths,
		state: appender.owner.runtime.state,
	}
	if len(external.Tables) != 0 {
		database, err := live.snapshotDatabase(ctx)
		if err != nil {
			return err
		}
		attachments, err := live.snapshotAttachments(ctx)
		if err != nil {
			return err
		}
		attachmentIDs := make(map[string]string, len(attachments))
		for key, content := range attachments {
			attachmentIDs[key] = conflictObjectID(content)
		}
		projection, err := conflictresolution.ProjectSQLiteDatabase(
			ctx, database, "live", attachmentIDs,
		)
		if err != nil {
			return err
		}
		for _, table := range external.Tables {
			current, exists := projection.Tables[table.TableID]
			if !exists {
				current = conflictresolution.TableState{
					TableID: table.TableID,
					Deleted: true,
				}
			}
			current.DatabaseObjectID = table.Expected.DatabaseObjectID
			if !reflect.DeepEqual(current, table.Expected) {
				return conflictresolution.ErrStalePlan
			}
		}
	}
	if external.Settings != nil {
		current, err := live.workspaceSettings(ctx)
		if err != nil {
			return err
		}
		if len(external.Settings.Previous) == 0 &&
			objectrepo.ObjectID(conflictObjectID(current)) ==
				external.Settings.ExpectedObjectID {
			return nil
		}
		target, err := appender.workspaceSettingsExpectedRaw(
			ctx,
			*external.Settings,
		)
		if err != nil {
			return errors.Join(conflictresolution.ErrStalePlan, err)
		}
		differ, err := workspaceSettingsDiffer(current, target)
		if err != nil {
			return err
		}
		matches := !differ
		if !matches {
			return conflictresolution.ErrStalePlan
		}
	}
	return nil
}

func (appender *workspaceConflictAppender) workspaceSettingsChoiceMatches(
	ctx context.Context,
	current []byte,
	choice objectrepo.ObjectID,
) (bool, error) {
	target, err := readConflictObject(
		ctx,
		appender.owner.runtime.repository,
		choice,
	)
	if err != nil {
		return false, err
	}
	differ, err := workspaceSettingsDiffer(current, target)
	if err != nil {
		return false, err
	}
	return !differ, nil
}

func (appender *workspaceConflictAppender) workspaceSettingsExpectedRaw(
	ctx context.Context,
	settings workspaceConflictSettingsStage,
) ([]byte, error) {
	if len(settings.Previous) != 0 {
		if err := validateConflictSettings(settings.Previous); err != nil {
			return nil, err
		}
		return append([]byte(nil), settings.Previous...), nil
	}
	return readConflictObject(
		ctx,
		appender.owner.runtime.repository,
		settings.ExpectedObjectID,
	)
}

func conflictObjectID(content []byte) string {
	sum := sha256.Sum256(content)
	return "obj_" + hex.EncodeToString(sum[:])
}

func (appender *workspaceConflictAppender) restoreExternalExpected(
	ctx context.Context,
	external workspaceConflictExternalStage,
) error {
	filesystem, err := appender.owner.runtime.app.NewFilesystem()
	if err != nil {
		return err
	}
	filesystem.SetContext(ctx)
	var errs []error
	for _, table := range external.Tables {
		errs = append(
			errs,
			filesystem.DeletePrefix(table.TableID+"/")...,
		)
		keys := make(
			[]string, 0, len(table.Expected.AttachmentObjects),
		)
		for key := range table.Expected.AttachmentObjects {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			content, readErr := readConflictObject(
				ctx,
				appender.owner.runtime.repository,
				objectrepo.ObjectID(
					table.Expected.AttachmentObjects[key],
				),
			)
			if readErr != nil {
				errs = append(errs, readErr)
				continue
			}
			errs = append(errs, filesystem.Upload(content, key))
		}
	}
	errs = append(errs, filesystem.Close())
	if external.Settings != nil {
		if external.Settings.ExpectedObjectID != "" {
			content, readErr := appender.workspaceSettingsExpectedRaw(
				ctx,
				*external.Settings,
			)
			if readErr != nil {
				errs = append(errs, readErr)
			} else {
				_, counters := appender.owner.runtime.coordinator.Current()
				errs = append(
					errs,
					replaceWorkspaceSettings(
						ctx,
						appender.owner.runtime.state,
						content,
						counters.MutationRevision,
					),
				)
			}
		}
	}
	return errors.Join(errs...)
}

// atomicReplaceConflictFile is the durable generic file replacement seam used
// by fault-injection tests and restore rollback artifacts. Live workspace
// settings never use it; their only authority is the coordination database.
func atomicReplaceConflictFile(target string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(
		filepath.Dir(target),
		".conflict-file-*.tmp",
	)
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return replaceConflictFile(temp, target)
}

func (appender *workspaceConflictAppender) requestConflictShutdown() {
	if appender == nil || appender.owner == nil ||
		appender.owner.runtime == nil ||
		appender.owner.runtime.requestShutdown == nil {
		return
	}
	appender.owner.runtime.requestShutdown()
}

type conflictCandidateSource struct {
	app  *pocketbase.PocketBase
	root string
}

func (source *conflictCandidateSource) close() {
	if source == nil {
		return
	}
	if source.app != nil {
		_ = source.app.ResetBootstrapState()
	}
	if source.root != "" {
		_ = os.RemoveAll(source.root)
	}
}

func (appender *workspaceConflictAppender) openConflictCandidateSource(
	ctx context.Context,
	databaseID objectrepo.ObjectID,
) (*conflictCandidateSource, error) {
	database, err := readConflictObject(
		ctx, appender.owner.runtime.repository, databaseID,
	)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(
		appender.owner.runtime.paths.temp,
		"conflict-candidate-*",
	)
	if err != nil {
		return nil, err
	}
	source := &conflictCandidateSource{root: root}
	if err := writeDurablePrivateFile(
		filepath.Join(root, "data.db"), database,
	); err != nil {
		source.close()
		return nil, err
	}
	source.app = pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:   root,
		HideStartBanner:  true,
		DataMaxOpenConns: 1,
	})
	if err := source.app.Bootstrap(); err != nil {
		source.close()
		return nil, err
	}
	return source, nil
}

func (appender *workspaceConflictAppender) validateMixedCandidate(
	ctx context.Context,
	external workspaceConflictExternalStage,
) error {
	if len(external.Tables) == 0 {
		return nil
	}
	live := &frozenSource{
		app: appender.owner.runtime.app, paths: appender.owner.runtime.paths,
		state: appender.owner.runtime.state,
	}
	database, err := live.snapshotDatabase(ctx)
	if err != nil {
		return err
	}
	mixed, err := appender.openConflictDatabaseSource(database)
	if err != nil {
		return err
	}
	defer mixed.close()
	sources := map[objectrepo.ObjectID]*conflictCandidateSource{}
	defer func() {
		for _, source := range sources {
			source.close()
		}
	}()
	for _, table := range external.Tables {
		if table.Deleted {
			continue
		}
		if sources[table.DatabaseObjectID] != nil {
			continue
		}
		source, err := appender.openConflictCandidateSource(
			ctx, table.DatabaseObjectID,
		)
		if err != nil {
			return err
		}
		sources[table.DatabaseObjectID] = source
	}
	if err := mixed.app.RunInTransaction(func(txApp core.App) error {
		return applyConflictTables(txApp, external.Tables, sources)
	}); err != nil {
		return errors.Join(conflictresolution.ErrApplyUnproven, err)
	}
	provider, ok := mixed.app.ConcurrentDB().(interface {
		DB() *sql.DB
	})
	if !ok || provider.DB() == nil {
		return conflictresolution.ErrApplyUnproven
	}
	rows, err := provider.DB().QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	hasViolation := rows.Next()
	closeErr := rows.Close()
	if hasViolation || closeErr != nil {
		return errors.Join(
			conflictresolution.ErrApplyUnproven, closeErr,
		)
	}
	mixedView := &frozenSource{
		app:   mixed.app,
		paths: workspacePaths{temp: mixed.root},
	}
	mergedDatabase, err := mixedView.snapshotDatabase(ctx)
	if err != nil {
		return err
	}
	attachments, err := live.snapshotAttachments(ctx)
	if err != nil {
		return err
	}
	attachmentIDs := make(map[string]string, len(attachments))
	for key, content := range attachments {
		attachmentIDs[key] = conflictObjectID(content)
	}
	for _, table := range external.Tables {
		prefix := table.TableID + "/"
		for key := range attachmentIDs {
			if strings.HasPrefix(key, prefix) {
				delete(attachmentIDs, key)
			}
		}
		for key, id := range table.Chosen.AttachmentObjects {
			attachmentIDs[key] = id
		}
	}
	projection, err := conflictresolution.ProjectSQLiteDatabase(
		ctx, mergedDatabase, "merged", attachmentIDs,
	)
	if err != nil {
		return err
	}
	for _, table := range external.Tables {
		current, exists := projection.Tables[table.TableID]
		if !exists {
			current = conflictresolution.TableState{
				TableID: table.TableID, Deleted: true,
			}
		}
		current.DatabaseObjectID = table.Chosen.DatabaseObjectID
		if !reflect.DeepEqual(current, table.Chosen) {
			return conflictresolution.ErrApplyUnproven
		}
	}
	return nil
}

func (appender *workspaceConflictAppender) openConflictDatabaseSource(
	database []byte,
) (*conflictCandidateSource, error) {
	root, err := os.MkdirTemp(
		appender.owner.runtime.paths.temp,
		"conflict-merged-*",
	)
	if err != nil {
		return nil, err
	}
	source := &conflictCandidateSource{root: root}
	if err := writeDurablePrivateFile(
		filepath.Join(root, "data.db"), database,
	); err != nil {
		source.close()
		return nil, err
	}
	source.app = pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: root, HideStartBanner: true,
		DataMaxOpenConns: 1,
	})
	if err := source.app.Bootstrap(); err != nil {
		source.close()
		return nil, err
	}
	return source, nil
}

func applyConflictTables(
	txApp core.App,
	tables []workspaceConflictTableStage,
	sources map[objectrepo.ObjectID]*conflictCandidateSource,
) error {
	sorted := append([]workspaceConflictTableStage(nil), tables...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TableID < sorted[j].TableID
	})
	selected := make(map[string]workspaceConflictTableStage, len(sorted))
	replacements := make(map[string]map[string]any, len(sorted))
	for _, table := range sorted {
		selected[table.TableID] = table
		if table.Deleted {
			continue
		}
		source := sources[table.DatabaseObjectID]
		if source == nil || source.app == nil {
			return conflictresolution.ErrApplyUnproven
		}
		collection, err := source.app.FindCollectionByNameOrId(table.TableID)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(collection)
		if err != nil {
			return err
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			return err
		}
		replacements[table.TableID] = schema
	}
	current, err := txApp.FindAllCollections()
	if err != nil {
		return err
	}
	configs := make([]map[string]any, 0, len(current))
	seen := map[string]struct{}{}
	for _, collection := range current {
		if collection.System {
			continue
		}
		if table, changed := selected[collection.Id]; changed {
			seen[collection.Id] = struct{}{}
			if table.Deleted {
				continue
			}
			configs = append(configs, replacements[collection.Id])
			continue
		}
		raw, err := json.Marshal(collection)
		if err != nil {
			return err
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			return err
		}
		configs = append(configs, schema)
	}
	for tableID, schema := range replacements {
		if _, exists := seen[tableID]; !exists {
			configs = append(configs, schema)
		}
	}
	if len(configs) != 0 {
		if err := txApp.ImportCollections(configs, true); err != nil {
			return err
		}
	} else {
		for _, table := range sorted {
			collection, err := txApp.FindCollectionByNameOrId(
				table.TableID,
			)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil || collection.System {
				return errors.Join(
					conflictresolution.ErrApplyUnproven, err,
				)
			}
			if err := txApp.Delete(collection); err != nil {
				return err
			}
		}
	}
	for _, table := range sorted {
		if table.Deleted {
			continue
		}
		source := sources[table.DatabaseObjectID]
		sourceCollection, err :=
			source.app.FindCollectionByNameOrId(table.TableID)
		if err != nil {
			return err
		}
		if sourceCollection.IsView() {
			continue
		}
		targetCollection, err :=
			txApp.FindCollectionByNameOrId(table.TableID)
		if err != nil {
			return err
		}
		if err := txApp.TruncateCollection(targetCollection); err != nil {
			return err
		}
		columns, records, err := source.rawRecords(
			sourceCollection.Name,
		)
		if err != nil {
			return err
		}
		quoted := make([]string, len(columns))
		values := make([]string, len(columns))
		for index, column := range columns {
			quoted[index] = quoteConflictSQLite(column)
			values[index] = "{:v" +
				fmt.Sprint(index) + "}"
		}
		statement := "INSERT INTO " +
			quoteConflictSQLite(targetCollection.Name) +
			" (" + strings.Join(quoted, ",") + ") VALUES (" +
			strings.Join(values, ",") + ")"
		for _, record := range records {
			params := make(map[string]any, len(record))
			for index, value := range record {
				params["v"+fmt.Sprint(index)] = value
			}
			if _, err := txApp.DB().NewQuery(statement).
				Bind(params).Execute(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (source *conflictCandidateSource) rawRecords(
	table string,
) ([]string, [][]any, error) {
	provider, ok := source.app.ConcurrentDB().(interface {
		DB() *sql.DB
	})
	if !ok || provider.DB() == nil {
		return nil, nil, conflictresolution.ErrApplyUnproven
	}
	rows, err := provider.DB().Query(
		"SELECT * FROM " + quoteConflictSQLite(table),
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, nil, err
		}
		for index, value := range values {
			if raw, ok := value.([]byte); ok {
				values[index] = append([]byte(nil), raw...)
			}
		}
		result = append(result, values)
	}
	return columns, result, rows.Err()
}

func quoteConflictSQLite(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (appender *workspaceConflictAppender) applyConflictAttachments(
	ctx context.Context,
	tables []workspaceConflictTableStage,
) error {
	filesystem, err := appender.owner.runtime.app.NewFilesystem()
	if err != nil {
		return err
	}
	defer filesystem.Close()
	filesystem.SetContext(ctx)
	for _, table := range tables {
		for _, deleteErr := range filesystem.DeletePrefix(table.TableID + "/") {
			if deleteErr != nil {
				return deleteErr
			}
		}
		keys := make([]string, 0, len(table.Attachments))
		for key := range table.Attachments {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			content, err := readConflictObject(
				ctx,
				appender.owner.runtime.repository,
				table.Attachments[key],
			)
			if err != nil {
				return err
			}
			if err := filesystem.Upload(content, key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (appender *workspaceConflictAppender) conflictFault(
	point string,
) error {
	if appender == nil || appender.owner == nil ||
		appender.owner.conflictApplyFault == nil {
		return nil
	}
	return appender.owner.conflictApplyFault(point)
}
