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
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const (
	maxSnapshotWorkingSet          = int64(512 << 20)
	maxSnapshotPackageContainer    = int64(256 << 20)
	maxSnapshotPackagePayloadBytes = int64(240 << 20)
	maxSnapshotPackageMetadata     = int64(16 << 20)
)

type frozenSource struct {
	app             core.App
	paths           workspacePaths
	manifest        contractsv2.WorkspaceManifest
	ledger          *auditledger.Ledger
	auditOutbox     auditledger.OutboxStore
	fileAuditOutbox auditledger.OutboxStore
	repository      objectrepo.Repository
	history         *filehistory.Service
	search          *workspacesearch.Engine
	searchStatus    func() contracts.SearchStatus
	state           *stateStore
	logger          *slog.Logger
}

func (source *frozenSource) Freeze(
	ctx context.Context,
	intent writecoordinator.CaptureIntent,
) (snapshot.BarrierView, writecoordinator.FrozenRoots, error) {
	// Capture already owns the workspace write-coordinator gate, so no v2
	// producer can enqueue another business/file audit event here. Drain and
	// acknowledge before taking SQLite's BEGIN IMMEDIATE lock; acknowledging
	// through a second PocketBase connection while that lock is held would
	// deadlock.
	completePhase := source.beginFreezePhase(ctx, "audit")
	if err := source.appendPendingAudit(ctx); err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase()
	completePhase = source.beginFreezePhase(ctx, "sqlite_lock")
	releaseWriteLock, err := source.lockBusinessWrites(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	defer releaseWriteLock()
	completePhase()
	completePhase = source.beginFreezePhase(ctx, "database")
	database, err := source.snapshotDatabase(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase()
	completePhase = source.beginFreezePhase(ctx, "files")
	historyRoot, fileHeadRevision := source.history.Head()
	fileRevision := fileHeadRevision
	if historyRoot == "" {
		// An absent history root keeps the bundle's canonical zero revision.
		fileRevision = 0
	}
	files, err := source.snapshotFiles(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase()
	completePhase = source.beginFreezePhase(ctx, "attachments")
	attachments, err := source.snapshotAttachments(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase()
	completePhase = source.beginFreezePhase(ctx, "audit_prefix")
	anchor := source.ledger.Anchor()
	if anchor.LedgerSequence == 0 || anchor.Hash == "" {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
			errors.New("snapshot.audit_anchor_missing")
	}
	auditPrefix, _, err := source.ledger.ExportPrefix(anchor)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase()
	completePhase = source.beginFreezePhase(ctx, "workspace_settings")
	settings, err := source.workspaceSettings(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase()
	completePhase = source.beginFreezePhase(ctx, "derived_state")
	dataRevision, computationWatermark, pendingWork, searchGeneration, err :=
		source.snapshotDerivedState(ctx, fileHeadRevision)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase()
	databaseHash := digestBytes(database)
	topologyPayload, err := json.Marshal(map[string]any{
		"formatVersion":         1,
		"workspaceId":           source.manifest.WorkspaceID,
		"topologySchemaVersion": source.manifest.TopologySchemaVersion,
		"businessSchemaVersion": source.manifest.BusinessSchemaVersion,
		"businessDatabaseHash":  databaseHash,
	})
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	filePayload, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"workspaceId":   source.manifest.WorkspaceID,
		"historyRoot":   historyRoot,
		"fileRevision":  fileRevision,
	})
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase = source.beginFreezePhase(ctx, "repository")
	receipt, err := source.repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: intent.Token.Authority(),
		Manifests: []objectrepo.ManifestInput{
			{
				Name: "topology-head",
				Labels: map[string]string{
					"type":        "topology-head",
					"workspaceId": source.manifest.WorkspaceID,
				},
				Payload: topologyPayload,
			},
			{
				Name: "file-state-head",
				Labels: map[string]string{
					"type":        "file-state-head",
					"workspaceId": source.manifest.WorkspaceID,
				},
				Payload: filePayload,
			},
		},
	})
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	completePhase()
	topologyRoot := receipt.Manifests["topology-head"]
	fileRoot := receipt.Manifests["file-state-head"]
	if !receipt.Durable || topologyRoot == "" || fileRoot == "" {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
			errors.New("snapshot.root_receipt_invalid")
	}
	view := snapshot.BarrierView{
		SchemaRevision:        source.manifest.TopologySchemaVersion,
		BusinessSchemaVersion: source.manifest.BusinessSchemaVersion,
		DataRevision:          dataRevision,
		ComputationWatermark:  computationWatermark,
		JobSchemaVersion:      jobs.DurableSchemaVersion,
		PendingWork:           pendingWork,
		SearchGeneration:      searchGeneration,
		FileRevision:          fileRevision,
		AuditRevision:         anchor.LedgerSequence,
		AuditAnchor:           anchor.Hash,
		AuditEpoch:            1,
		AuditSequence:         anchor.SourceSequence,
		Database:              database,
		Files:                 files,
		Attachments:           attachments,
		WorkspaceSettings:     settings,
		AuditPrefix:           auditPrefix,
		CreatedByDevice:       intent.Token.ClaimID,
		MinimumAppVersion:     "2.0.0",
	}
	return view, writecoordinator.FrozenRoots{
		DatabaseView: "sqlite-vacuum:" + databaseHash,
		TopologyRoot: topologyRoot,
		FileRoot:     fileRoot,
		AuditAnchor:  anchor.Hash,
	}, nil
}

func (source *frozenSource) snapshotDerivedState(
	ctx context.Context,
	fileRevision uint64,
) (uint64, uint64, snapshot.PendingWork, int64, error) {
	var dataRevision uint64
	if _, collectionErr := source.app.FindCollectionByNameOrId("vibetable_tables"); collectionErr == nil {
		if err := source.app.DB().NewQuery(
			"SELECT COALESCE(MAX(data_revision), 0) FROM vibetable_tables",
		).WithContext(ctx).Row(&dataRevision); err != nil {
			return 0, 0, snapshot.PendingWork{}, 0, err
		}
	} else if !errors.Is(collectionErr, sql.ErrNoRows) {
		return 0, 0, snapshot.PendingWork{}, 0, collectionErr
	}
	var jobs uint64
	if _, collectionErr := source.app.FindCollectionByNameOrId("vibetable_jobs"); collectionErr == nil {
		if err := source.app.DB().NewQuery(
			"SELECT COUNT(*) FROM vibetable_jobs WHERE state IN ('queued','running')",
		).WithContext(ctx).Row(&jobs); err != nil {
			return 0, 0, snapshot.PendingWork{}, 0, err
		}
	} else if !errors.Is(collectionErr, sql.ErrNoRows) {
		return 0, 0, snapshot.PendingWork{}, 0, collectionErr
	}
	var outbox uint64
	if _, collectionErr := source.app.FindCollectionByNameOrId("vibetable_outbox"); collectionErr == nil {
		if err := source.app.DB().NewQuery(
			"SELECT COUNT(*) FROM vibetable_outbox WHERE status='pending'",
		).WithContext(ctx).Row(&outbox); err != nil {
			return 0, 0, snapshot.PendingWork{}, 0, err
		}
	} else if !errors.Is(collectionErr, sql.ErrNoRows) {
		return 0, 0, snapshot.PendingWork{}, 0, collectionErr
	}
	searchStatus := contracts.SearchStatus{State: "idle"}
	if source.search != nil {
		current, statusErr := source.search.Status(ctx)
		if statusErr != nil {
			return 0, 0, snapshot.PendingWork{}, 0, statusErr
		}
		searchStatus = current
	}
	if source.searchStatus != nil {
		taskStatus := source.searchStatus()
		if taskStatus.State == "building" || taskStatus.State == "degraded" ||
			taskStatus.State == "failed" {
			searchStatus = taskStatus
		}
	}
	var searchCheckpoint workspacesearch.ProjectionCheckpoint
	if source.search != nil {
		var checkpointErr error
		searchCheckpoint, checkpointErr = source.search.ProjectionCheckpoint(ctx)
		if checkpointErr != nil {
			return 0, 0, snapshot.PendingWork{}, 0, checkpointErr
		}
	}
	var businessTail int64
	if _, collectionErr := source.app.FindCollectionByNameOrId("vibetable_outbox"); collectionErr == nil {
		if err := source.app.DB().NewQuery(`
			SELECT COALESCE(MAX(rowid), 0) FROM vibetable_outbox
			WHERE topic='data.changed'
		`).WithContext(ctx).Row(&businessTail); err != nil {
			return 0, 0, snapshot.PendingWork{}, 0, err
		}
	} else if !errors.Is(collectionErr, sql.ErrNoRows) {
		return 0, 0, snapshot.PendingWork{}, 0, collectionErr
	}
	checkpoint := ""
	if searchStatus.Checkpoint != nil {
		checkpoint = *searchStatus.Checkpoint
	}
	searchPending := searchProjectionPending(
		searchStatus, searchCheckpoint, businessTail, fileRevision,
	)
	if searchPending && checkpoint == "" {
		checkpoint = fmt.Sprintf(
			"durable:business=%d/%d,file=%d/%d",
			searchCheckpoint.BusinessOutboxRowID,
			businessTail,
			searchCheckpoint.FileHeadRevision,
			fileRevision,
		)
	}
	pending := snapshot.PendingWork{
		Jobs: jobs, BusinessOutbox: outbox,
		SearchRebuild:    searchPending,
		SearchCheckpoint: checkpoint,
	}
	return dataRevision, computationWatermark(dataRevision, jobs), pending, searchStatus.Generation, nil
}

func computationWatermark(dataRevision, pendingJobs uint64) uint64 {
	if pendingJobs != 0 {
		return 0
	}
	return dataRevision
}

func searchProjectionPending(
	status contracts.SearchStatus,
	checkpoint workspacesearch.ProjectionCheckpoint,
	businessTail int64,
	fileRevision uint64,
) bool {
	return status.State == "building" || status.State == "degraded" ||
		status.State == "failed" || checkpoint.BusinessOutboxRowID != businessTail ||
		checkpoint.FileHeadRevision != fileRevision
}

func (source *frozenSource) snapshotAttachments(
	ctx context.Context,
) (map[string][]byte, error) {
	filesystem, err := source.app.NewFilesystem()
	if err != nil {
		return nil, err
	}
	defer filesystem.Close()
	filesystem.SetContext(ctx)
	objects, err := filesystem.List("")
	if err != nil {
		return nil, err
	}
	result := map[string][]byte{}
	var total int64
	for _, object := range objects {
		if object == nil || object.IsDir ||
			strings.TrimSpace(object.Key) == "" {
			continue
		}
		if object.Size < 0 ||
			object.Size > maxSnapshotWorkingSet-total {
			return nil, errors.New("snapshot.attachment_state_too_large")
		}
		reader, err := filesystem.GetReader(object.Key)
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(
			reader, object.Size+1,
		))
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if int64(len(content)) != object.Size {
			return nil, errors.New("snapshot.attachment_state_changed")
		}
		result[object.Key] = content
		total += object.Size
	}
	return result, nil
}

func (source *frozenSource) snapshotDatabase(ctx context.Context) ([]byte, error) {
	provider, ok := source.app.ConcurrentDB().(interface {
		DB() *sql.DB
	})
	if !ok || provider.DB() == nil {
		return nil, errors.New("snapshot.sqlite_provider_unavailable")
	}
	file, err := os.CreateTemp(source.paths.temp, "database-view-*.db")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return nil, closeErr
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	defer os.Remove(path)
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := provider.DB().ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return nil, fmt.Errorf("snapshot sqlite view: %w", err)
	}
	return readFileBounded(path, maxSnapshotWorkingSet)
}

func (source *frozenSource) lockBusinessWrites(
	ctx context.Context,
) (func(), error) {
	provider, ok := source.app.NonconcurrentDB().(interface {
		DB() *sql.DB
	})
	if !ok || provider.DB() == nil {
		return nil, errors.New("snapshot.sqlite_provider_unavailable")
	}
	connection, err := provider.DB().Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("snapshot acquire sqlite write lock: %w", err)
	}
	return func() {
		_, _ = connection.ExecContext(
			context.Background(),
			"ROLLBACK",
		)
		_ = connection.Close()
	}, nil
}

func (source *frozenSource) appendPendingAudit(ctx context.Context) error {
	if source.auditOutbox == nil {
		return errors.New("snapshot.audit_outbox_required")
	}
	if source.fileAuditOutbox == nil {
		return errors.New("snapshot.file_audit_outbox_required")
	}
	drainer, err := auditledger.NewDrainer(source.ledger, 256)
	if err != nil {
		return err
	}
	// A ledger append is durable before the source acknowledgement. Draining
	// both transactional outboxes here therefore preserves each source's high
	// watermark and makes a crash between append and acknowledgement a safe
	// duplicate on the next capture.
	drain := func(name string, outbox auditledger.OutboxStore) error {
		var reader, writer *sql.DB
		var readerBefore, writerBefore sql.DBStats
		if source.logger != nil && name == "business" && source.app != nil {
			if provider, ok := source.app.ConcurrentDB().(interface{ DB() *sql.DB }); ok {
				reader = provider.DB()
			}
			if provider, ok := source.app.NonconcurrentDB().(interface{ DB() *sql.DB }); ok {
				writer = provider.DB()
			}
			if reader != nil {
				readerBefore = reader.Stats()
			}
			if writer != nil {
				writerBefore = writer.Stats()
			}
		}
		started := time.Now()
		report, drainErr := drainer.Drain(ctx, outbox)
		elapsed := time.Since(started)
		if source.logger != nil {
			details := []any{
				slog.String("source", name), slog.String("phase", report.Phase),
				slog.Int("appended", report.Appended), slog.Int("duplicates", report.Duplicates),
				slog.Int("acknowledged", report.Acknowledged),
				slog.Int("pendingCalls", report.PendingCalls), slog.Int("appendCalls", report.AppendCalls),
				slog.Int("ackCalls", report.AcknowledgeCalls),
				slog.Float64("pendingMs", snapshot.PhaseDurationMs(report.PendingDuration)),
				slog.Float64("appendMs", snapshot.PhaseDurationMs(report.AppendDuration)),
				slog.Float64("appendMaxMs", snapshot.PhaseDurationMs(report.AppendMaxDuration)),
				slog.Float64("ackMs", snapshot.PhaseDurationMs(report.AcknowledgeDuration)),
			}
			// Pool deltas include concurrent callers and provide context, not request attribution.
			if reader != nil {
				after := reader.Stats()
				details = append(details, slog.Int64("pbReadWaitCount", after.WaitCount-readerBefore.WaitCount),
					slog.Float64("pbReadWaitMs", snapshot.PhaseDurationMs(after.WaitDuration-readerBefore.WaitDuration)),
					slog.Int("pbReadInUseDelta", after.InUse-readerBefore.InUse))
			}
			if writer != nil {
				after := writer.Stats()
				details = append(details, slog.Int64("pbWriteWaitCount", after.WaitCount-writerBefore.WaitCount),
					slog.Float64("pbWriteWaitMs", snapshot.PhaseDurationMs(after.WaitDuration-writerBefore.WaitDuration)),
					slog.Int("pbWriteInUseDelta", after.InUse-writerBefore.InUse))
			}
			operationID := ""
			if operation, ok := protocolv2.OperationFromContext(ctx); ok {
				operationID = operation.OperationID
			}
			source.logger.Info("snapshot.freeze.audit.drain.returned",
				slog.String("module", "snapshot"), slog.String("workspaceId", source.manifest.WorkspaceID),
				slog.String("operationId", operationID), slog.String("errorCode", snapshot.CaptureErrorCode(drainErr)),
				slog.Float64("durationMs", snapshot.PhaseDurationMs(elapsed)), slog.Group("auditDrain", details...))
		}
		return drainErr
	}
	if err := drain("business", source.auditOutbox); err != nil {
		return err
	}
	return drain("file", source.fileAuditOutbox)
}

func (source *frozenSource) snapshotFiles(
	ctx context.Context,
) (map[string][]byte, error) {
	documents := source.history.List()
	result := make(map[string][]byte, len(documents))
	var total int64
	for _, document := range documents {
		if document.Status != filehistory.DocumentActive ||
			document.EffectiveRevisionID == "" {
			continue
		}
		var effective *filehistory.Revision
		for index := range document.Revisions {
			revision := &document.Revisions[index]
			if revision.RevisionID == document.EffectiveRevisionID {
				effective = revision
			}
		}
		if effective == nil || effective.Size < 0 ||
			effective.Size > maxSnapshotWorkingSet-total {
			return nil, errors.New("snapshot.file_state_too_large")
		}
		reader, err := source.repository.Open(ctx, effective.ObjectID)
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(
			reader,
			effective.Size+1,
		))
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if int64(len(content)) != effective.Size ||
			digestBytes(content) != effective.ContentHash {
			return nil, filehistory.ErrStateCorrupt
		}
		result[document.RelativePath] = content
		total += effective.Size
	}
	return result, nil
}

func (source *frozenSource) workspaceSettings(
	ctx context.Context,
) ([]byte, error) {
	return snapshotWorkspaceSettings(ctx, source.state)
}

func readFileBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("snapshot.resource_limit")
	}
	return raw, nil
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Completion is logged only after success, so a canceled capture retains the
// last entered stage without logging business data or storage error text.
func (source *frozenSource) beginFreezePhase(ctx context.Context, phase string) func() {
	if source.logger == nil {
		return func() {}
	}
	operationID := ""
	if operation, ok := protocolv2.OperationFromContext(ctx); ok {
		operationID = operation.OperationID
	}
	attributes := []any{
		slog.String("module", "snapshot"),
		slog.String("workspaceId", source.manifest.WorkspaceID),
		slog.String("operationId", operationID),
	}
	source.logger.Info("snapshot.freeze."+phase+".entered", attributes...)
	started := time.Now()
	return func() {
		elapsed := time.Since(started)
		source.logger.Info("snapshot.freeze."+phase+".completed", append(attributes, slog.Float64("durationMs", snapshot.PhaseDurationMs(elapsed)))...)
	}
}
