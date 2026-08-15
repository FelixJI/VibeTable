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
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
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
	if err := source.appendPendingAudit(ctx); err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	releaseWriteLock, err := source.lockBusinessWrites(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	defer releaseWriteLock()
	database, err := source.snapshotDatabase(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	files, fileRevision, err := source.snapshotFiles(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	attachments, err := source.snapshotAttachments(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	anchor := source.ledger.Anchor()
	if anchor.LedgerSequence == 0 || anchor.Hash == "" {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{},
			errors.New("snapshot.audit_anchor_missing")
	}
	auditPrefix, _, err := source.ledger.ExportPrefix(anchor)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	settings, err := source.workspaceSettings(ctx)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
	dataRevision, computationWatermark, pendingWork, searchGeneration, err :=
		source.snapshotDerivedState(ctx, fileRevision)
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
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
		"historyRoot":   source.history.Root(),
		"fileRevision":  fileRevision,
	})
	if err != nil {
		return snapshot.BarrierView{}, writecoordinator.FrozenRoots{}, err
	}
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
	if _, err := drainer.Drain(ctx, source.auditOutbox); err != nil {
		return err
	}
	_, err = drainer.Drain(ctx, source.fileAuditOutbox)
	return err
}

func (source *frozenSource) snapshotFiles(
	ctx context.Context,
) (map[string][]byte, uint64, error) {
	documents := source.history.List()
	result := make(map[string][]byte, len(documents))
	var (
		total        int64
		fileRevision uint64
	)
	for _, document := range documents {
		if document.TopologyRevision > fileRevision {
			fileRevision = document.TopologyRevision
		}
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
			return nil, 0, errors.New("snapshot.file_state_too_large")
		}
		reader, err := source.repository.Open(ctx, effective.ObjectID)
		if err != nil {
			return nil, 0, err
		}
		content, readErr := io.ReadAll(io.LimitReader(
			reader,
			effective.Size+1,
		))
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, 0, err
		}
		if int64(len(content)) != effective.Size ||
			digestBytes(content) != effective.ContentHash {
			return nil, 0, filehistory.ErrStateCorrupt
		}
		result[document.RelativePath] = content
		total += effective.Size
	}
	return result, fileRevision, nil
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
