// Package workspacev2 composes the workspace-scoped v2 producer from the
// durable sidecar modules. It deliberately registers only methods whose
// production dependencies are present.
package workspacev2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/replica"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	_ "modernc.org/sqlite"
)

type Options struct {
	App                      core.App
	DataDir                  string
	WorkspaceID              string
	SessionEpoch             uint64
	FenceEpoch               uint64
	ClaimID                  string
	Ledger                   *auditledger.Ledger
	RequestShutdown          func()
	ReplicaRemote            replica.VerifiedRemote
	ReplicaRemoteFactory     func(context.Context) (replica.VerifiedRemote, error)
	ReplicaRoot              string
	ReplicaDeviceID          string
	ReplicaPublicationKey    []byte
	ReplicaDependencyScanner replica.ConflictDependencyScanner
	DisableReplicaWorker     bool
}

type Runtime struct {
	app                      core.App
	paths                    workspacePaths
	manifest                 contractsv2.WorkspaceManifest
	dispatcher               *protocolv2.Dispatcher
	state                    *stateStore
	coordinator              *writecoordinator.WorkspaceWriteCoordinator
	coordinatorPath          string
	repository               *objectrepo.KopiaRepository
	repositorySessionRelease func() error
	catalog                  *snapshot.DurableCatalog
	headStore                *filehistory.SQLiteHeadStore
	materializer             *filehistory.Materializer
	history                  *filehistory.Service
	retention                *productionRetention
	replicaConflict          *productionReplicaConflict
	ingestor                 *filehistory.Ingestor
	watcher                  *filehistory.Watcher
	watchMu                  sync.Mutex
	watchEvents              []filehistory.WatchEvent
	auditDrainMu             sync.Mutex
	businessAuditOutbox      *auditledger.PocketBaseOutbox
	fileAuditDrainer         *auditledger.Drainer
	snapshots                *snapshot.Coordinator
	frozenSource             *frozenSource
	scheduler                *snapshot.Scheduler
	schedulerCancel          context.CancelFunc
	schedulerWG              sync.WaitGroup
	ledger                   *auditledger.Ledger
	requestShutdown          func()
}

type CapabilityDocument struct {
	ContractVersion string              `json:"contractVersion"`
	WorkspaceID     string              `json:"workspaceId"`
	SessionEpoch    uint64              `json:"sessionEpoch"`
	FenceEpoch      uint64              `json:"fenceEpoch"`
	ClaimID         string              `json:"claimId"`
	RPCMethods      []string            `json:"rpcMethods"`
	Registrations   []protocolv2.Method `json:"registrations"`
}

func Open(ctx context.Context, options Options) (_ *Runtime, err error) {
	if options.App == nil || options.Ledger == nil {
		return nil, errors.New("workspace.v2.dependencies_required")
	}
	if !validUUID(options.WorkspaceID) ||
		!validUUID(options.ClaimID) ||
		options.SessionEpoch == 0 ||
		options.FenceEpoch == 0 {
		return nil, errors.New("workspace.v2.identity_invalid")
	}
	paths, manifest, err := validateBinding(
		options.DataDir,
		options.WorkspaceID,
	)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{
		paths.files, paths.data, paths.topology, paths.objects, paths.audit,
		paths.snapshots, paths.coordination, paths.temp,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, err
		}
	}
	if err := ensureKeyRotationStartupReady(
		paths,
		options.WorkspaceID,
	); err != nil {
		return nil, err
	}

	result := &Runtime{
		app:             options.App,
		paths:           paths,
		manifest:        manifest,
		ledger:          options.Ledger,
		requestShutdown: options.RequestShutdown,
	}
	defer func() {
		if err != nil {
			_ = result.Close(context.Background())
		}
	}()

	result.state, err = openStateStore(filepath.Join(paths.coordination, "workspace-v2.db"))
	if err != nil {
		return nil, err
	}
	if err := recoverCompletedKeyRotationReceipt(
		ctx,
		paths,
		result.state,
	); err != nil {
		return nil, err
	}
	result.coordinatorPath = filepath.Join(
		paths.coordination,
		"write-coordinator.db",
	)
	var persistedSessionEpoch uint64
	result.coordinator, persistedSessionEpoch, err = openCoordinator(
		ctx,
		result.coordinatorPath,
		options,
	)
	if err != nil {
		return nil, err
	}
	if err := writecoordinator.EnsurePocketBaseReceiptTable(
		ctx,
		options.App,
	); err != nil {
		return nil, err
	}

	configPath := filepath.Join(
		paths.coordination,
		"kopia.repository.config",
	)
	result.repositorySessionRelease, err =
		objectrepo.AcquireRepositorySession(ctx, configPath)
	if err != nil {
		return nil, err
	}
	result.repository, err = openRepository(ctx, paths, manifest, options)
	if err != nil {
		return nil, err
	}
	result.catalog, err = snapshot.OpenDurableCatalog(
		filepath.Join(paths.snapshots, "catalog.db"),
	)
	if err != nil {
		return nil, err
	}
	result.headStore, err = filehistory.OpenPersistentHeadStore(
		filepath.Join(paths.topology, "filehistory-head.db"),
	)
	if err != nil {
		return nil, err
	}
	result.retention, err = openProductionRetentionStore(
		filepath.Join(paths.coordination, "retention.db"),
	)
	if err != nil {
		return nil, err
	}
	if err := result.recoverPreparedMutation(ctx); err != nil {
		return nil, err
	}
	for persistedSessionEpoch < options.SessionEpoch {
		token, _ := result.coordinator.Current()
		if _, err := result.coordinator.RotateSession(ctx, token); err != nil {
			return nil, err
		}
		persistedSessionEpoch++
	}
	if result.history == nil {
		result.materializer, err = filehistory.OpenMaterializer(
			paths.files,
			filepath.Join(paths.coordination, "file-materializer"),
			result.repository,
		)
		if err != nil {
			return nil, err
		}
		result.history, err = filehistory.OpenCurrent(
			ctx,
			result.repository,
			result.coordinator,
			result.headStore,
			filehistory.WithMaterializer(result.materializer),
		)
		if err != nil {
			return nil, err
		}
	}
	if err := result.retention.bind(result); err != nil {
		return nil, err
	}
	if manifest.StorageMode == "mirrored" &&
		options.ReplicaRemote == nil {
		if strings.TrimSpace(options.ReplicaRoot) == "" {
			return nil, errors.New("replica.selected_root_required")
		}
		options.ReplicaPublicationKey, err = deriveReplicaPublicationKey(
			ctx,
			manifest,
		)
		if err != nil {
			return nil, err
		}
		options.ReplicaRemoteFactory = func(
			openCtx context.Context,
		) (replica.VerifiedRemote, error) {
			return replica.OpenFilesystemRemote(
				openCtx,
				options.ReplicaRoot,
				options.WorkspaceID,
				result.repository,
			)
		}
		options.ReplicaRemote, err = options.ReplicaRemoteFactory(ctx)
		if err != nil {
			if errors.Is(err, replica.ErrRemoteIdentityInvalid) {
				return nil, err
			}
			// A healthy local activity root remains authoritative while its
			// selected replica is offline. The production replica owner keeps
			// a durable pending marker and reconnects in the background.
			options.ReplicaRemote = nil
		}
	}
	result.replicaConflict, err = openProductionReplicaConflict(
		ctx, result, paths, options,
	)
	if err != nil {
		return nil, err
	}
	if err := ensureAuditGenesis(ctx, options.Ledger, manifest); err != nil {
		return nil, err
	}
	auditOutbox, err := auditledger.NewPocketBaseOutbox(options.App)
	if err != nil {
		return nil, err
	}
	result.businessAuditOutbox = auditOutbox
	result.fileAuditDrainer, err = auditledger.NewDrainer(
		options.Ledger, 256,
	)
	if err != nil {
		return nil, err
	}
	if err := result.drainFileHistoryAudit(ctx); err != nil {
		return nil, err
	}
	token, _ := result.coordinator.Current()
	source := &frozenSource{
		app:             options.App,
		paths:           paths,
		manifest:        manifest,
		ledger:          options.Ledger,
		auditOutbox:     auditOutbox,
		fileAuditOutbox: result.headStore,
		repository:      result.repository,
		history:         result.history,
		state:           result.state,
	}
	result.frozenSource = source
	barrier, err := snapshot.NewCoordinatedBarrier(
		result.coordinator,
		token,
		source,
	)
	if err != nil {
		return nil, err
	}
	result.snapshots = snapshot.NewCoordinator(
		result.repository,
		barrier,
		result.catalog,
	)
	if err := result.startSnapshotScheduler(ctx); err != nil {
		return nil, err
	}
	result.ingestor, err = filehistory.NewIngestor(result.history, nil)
	if err != nil {
		return nil, err
	}
	result.watcher, err = filehistory.NewWatcher(
		paths.files,
		result.ingestor,
		func() writecoordinator.Token {
			token, _ := result.coordinator.Current()
			return token
		},
		result.recordWatchEvent,
	)
	if err != nil {
		return nil, err
	}
	if err := result.watcher.Start(ctx); err != nil {
		return nil, err
	}
	sequence, err := result.state.sequence(
		ctx,
		options.WorkspaceID,
		options.SessionEpoch,
	)
	if err != nil {
		return nil, err
	}
	result.dispatcher = protocolv2.New()
	result.dispatcher.BindSession(protocolv2.Session{
		WorkspaceID: options.WorkspaceID,
		Epoch:       options.SessionEpoch,
		Sequence:    sequence,
	})
	result.dispatcher.SetSequenceCommit(func(
		ctx context.Context,
		session protocolv2.Session,
	) error {
		if session.WorkspaceID != options.WorkspaceID ||
			session.Epoch != options.SessionEpoch {
			return errors.New("workspace.session_epoch_stale")
		}
		return result.state.commitSequence(
			ctx,
			session.WorkspaceID,
			session.Epoch,
			session.Sequence,
		)
	})
	result.dispatcher.SetOperationReceiptStore(
		result.state.loadOperationReceipt,
		func(
			ctx context.Context,
			session protocolv2.Session,
			receipt protocolv2.OperationReceipt,
		) error {
			if session.WorkspaceID != options.WorkspaceID ||
				session.Epoch != options.SessionEpoch {
				return errors.New("workspace.session_epoch_stale")
			}
			return result.state.commitOperationReceipt(
				ctx,
				session,
				receipt,
			)
		},
	)
	result.dispatcher.SetAuthorityOperationReceiptStore(
		result.loadAuthorityOperationReceipt,
	)
	result.registerHandlers()
	result.retention.start()
	if result.replicaConflict != nil && !options.DisableReplicaWorker {
		// The replica worker can capture a protection snapshot while
		// reconciling selected files. Start it only after every Runtime
		// dependency (snapshot coordinator, ingestor, watcher and dispatcher)
		// is fully initialized.
		result.replicaConflict.startWorker()
	}
	return result, nil
}

func deriveReplicaPublicationKey(
	ctx context.Context,
	manifest contractsv2.WorkspaceManifest,
) ([]byte, error) {
	keys, err := objectrepo.NewKeyProvider(
		objectrepo.WindowsCredentialVault{},
	).Open(
		ctx,
		manifest.WorkspaceID,
		objectrepo.EncryptionMode(manifest.EncryptionMode),
	)
	if err != nil {
		return nil, err
	}
	defer clearBytes(keys.Password)
	authenticator := hmac.New(sha256.New, keys.Password)
	_, _ = authenticator.Write([]byte(
		"VibeTable replica publication v2\x00" + manifest.WorkspaceID,
	))
	return authenticator.Sum(nil), nil
}

func (runtime *Runtime) Dispatcher() *protocolv2.Dispatcher {
	return runtime.dispatcher
}

// CoordinateBusinessWrite is the only Workspace v2 entry point for formal
// PocketBase mutators. The callback receives a context carrying the exact
// prepared write intent; the authoritative PB transaction persists that
// receipt and audit envelope before it commits.
func (runtime *Runtime) CoordinateBusinessWrite(
	ctx context.Context,
	kind string,
	identity string,
	apply func(context.Context) error,
) error {
	if runtime == nil || runtime.coordinator == nil || apply == nil {
		return errors.New("workspace.business_write_unavailable")
	}
	token, _ := runtime.coordinator.Current()
	_, err := runtime.coordinator.Write(
		ctx,
		token,
		func(writeCtx context.Context, intent writecoordinator.WriteIntent) error {
			bound, bindErr := writecoordinator.WithBusinessIntent(
				writeCtx,
				intent,
				kind,
				identity,
			)
			if bindErr != nil {
				return bindErr
			}
			return apply(bound)
		},
	)
	if err == nil && runtime.replicaConflict != nil {
		// A successful canonical commit must become durably visible as
		// unsynchronized immediately. Waiting for the idle snapshot scheduler
		// leaves a data-loss window in which Desktop could mistake an empty
		// replica queue for a fully replicated workspace.
		err = runtime.replicaConflict.markLocalMutationPending()
	}
	return err
}

// Drain blocks new coordinated writes, drains both authoritative outboxes and
// durably publishes the resulting ledger high-watermark. It is exposed only by
// the host-authenticated sidecar route and is intentionally absent from the
// advertised Web RPC capability catalog.
func (runtime *Runtime) Drain(
	ctx context.Context,
	deadline time.Time,
) (writecoordinator.HighWatermark, error) {
	if runtime == nil || runtime.coordinator == nil ||
		runtime.businessAuditOutbox == nil ||
		runtime.fileAuditDrainer == nil ||
		runtime.headStore == nil {
		return writecoordinator.HighWatermark{},
			errors.New("workspace.drain_unavailable")
	}
	token, _ := runtime.coordinator.Current()
	return runtime.coordinator.Drain(
		ctx,
		token,
		deadline,
		func(ctx context.Context) (writecoordinator.HighWatermark, error) {
			runtime.auditDrainMu.Lock()
			defer runtime.auditDrainMu.Unlock()
			if _, err := runtime.fileAuditDrainer.Drain(
				ctx,
				runtime.businessAuditOutbox,
			); err != nil {
				return writecoordinator.HighWatermark{}, err
			}
			if _, err := runtime.fileAuditDrainer.Drain(
				ctx,
				runtime.headStore,
			); err != nil {
				return writecoordinator.HighWatermark{}, err
			}
			anchor := runtime.ledger.Anchor()
			if anchor.LedgerSequence == 0 {
				return writecoordinator.HighWatermark{},
					errors.New("workspace.drain_anchor_missing")
			}
			return writecoordinator.HighWatermark{
				SourceEpoch:    anchor.SourceEpoch,
				SourceSequence: anchor.SourceSequence,
				ChainHash:      anchor.Hash,
			}, nil
		},
	)
}

func (runtime *Runtime) Capabilities() CapabilityDocument {
	token, _ := runtime.coordinator.Current()
	registrations := runtime.dispatcher.Methods()
	methods := make([]string, len(registrations))
	for index, registration := range registrations {
		methods[index] = registration.Name
	}
	return CapabilityDocument{
		ContractVersion: contractsv2.ContractVersion,
		WorkspaceID:     token.WorkspaceID,
		SessionEpoch:    token.SessionEpoch,
		FenceEpoch:      token.FenceEpoch,
		ClaimID:         token.ClaimID,
		RPCMethods:      methods,
		Registrations:   registrations,
	}
}

func (runtime *Runtime) Close(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	var closeErrors []error
	if runtime.schedulerCancel != nil {
		runtime.schedulerCancel()
		runtime.schedulerWG.Wait()
		runtime.schedulerCancel = nil
	}
	if runtime.watcher != nil {
		closeErrors = append(closeErrors, runtime.watcher.Close())
		runtime.watcher = nil
	}
	if runtime.replicaConflict != nil {
		closeErrors = append(
			closeErrors,
			runtime.replicaConflict.close(),
		)
		runtime.replicaConflict = nil
	}
	if runtime.retention != nil {
		closeErrors = append(closeErrors, runtime.retention.close())
		runtime.retention = nil
	}
	if runtime.headStore != nil {
		// The watcher is stopped first so the outbox has a stable tail. Drain
		// before closing its source database; otherwise an orderly shutdown can
		// strand a committed file-history audit event until another launch.
		closeErrors = append(closeErrors, runtime.drainFileHistoryAudit(ctx))
		closeErrors = append(closeErrors, runtime.headStore.Close())
		runtime.headStore = nil
	}
	if runtime.catalog != nil {
		closeErrors = append(closeErrors, runtime.catalog.Close())
		runtime.catalog = nil
	}
	if runtime.repository != nil {
		closeErrors = append(closeErrors, runtime.repository.Close(ctx))
		runtime.repository = nil
	}
	if runtime.repositorySessionRelease != nil {
		closeErrors = append(
			closeErrors,
			runtime.repositorySessionRelease(),
		)
		runtime.repositorySessionRelease = nil
	}
	if runtime.coordinator != nil {
		closeErrors = append(closeErrors, runtime.coordinator.Close())
		runtime.coordinator = nil
	}
	if runtime.state != nil {
		closeErrors = append(closeErrors, runtime.state.close())
		runtime.state = nil
	}
	return errors.Join(closeErrors...)
}

func (runtime *Runtime) startSnapshotScheduler(ctx context.Context) error {
	runtime.scheduler = snapshot.NewScheduler()
	now := time.Now().UTC()
	_, counters := runtime.coordinator.Current()
	runtime.scheduler.Changed(now, counters.MutationRevision)
	last, found, err := runtime.catalog.Last(
		ctx,
		runtime.manifest.WorkspaceID,
	)
	if err != nil {
		return err
	}
	if found {
		runtime.scheduler.Succeeded(
			last.CreatedAt,
			last.MutationRevision,
		)
	}
	schedulerCtx, cancel := context.WithCancel(context.Background())
	runtime.schedulerCancel = cancel
	runtime.schedulerWG.Add(1)
	go runtime.runSnapshotScheduler(schedulerCtx)
	return nil
}

func (runtime *Runtime) runSnapshotScheduler(ctx context.Context) {
	defer runtime.schedulerWG.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			token, counters := runtime.coordinator.Current()
			runtime.scheduler.Changed(
				now.UTC(),
				counters.MutationRevision,
			)
			if !runtime.scheduler.Due(now.UTC()).Due {
				continue
			}
			if err := runtime.ensureAutomaticSnapshotWithinLimit(
				ctx,
			); err != nil {
				runtime.scheduler.Deferred(now.UTC(), time.Hour)
				runtime.recordWatchEvent(filehistory.WatchEvent{Err: err})
				continue
			}
			record, _, err := runtime.snapshots.Capture(
				ctx,
				snapshot.CaptureRequest{
					WorkspaceID: runtime.manifest.WorkspaceID,
					Authority:   token.Authority(),
					Trigger:     snapshot.TriggerAutomatic,
					Pinned:      false,
				},
			)
			if err != nil {
				runtime.scheduler.Failed(now.UTC())
				runtime.recordWatchEvent(filehistory.WatchEvent{
					Err: err,
				})
				continue
			}
			runtime.scheduler.Succeeded(
				time.Now().UTC(),
				record.MutationRevision,
			)
			runtime.enqueueReplicaSnapshots(ctx)
		}
	}
}

func (runtime *Runtime) enqueueReplicaSnapshots(ctx context.Context) {
	if runtime == nil || runtime.replicaConflict == nil {
		return
	}
	if err := runtime.replicaConflict.queuePublishedSnapshots(
		context.WithoutCancel(ctx),
	); err != nil {
		runtime.recordWatchEvent(filehistory.WatchEvent{Err: err})
	}
}

func (runtime *Runtime) ensureAutomaticSnapshotWithinLimit(
	ctx context.Context,
) error {
	policy, _, err := runtime.state.retention(ctx)
	if err != nil {
		return err
	}
	if policy.RepositoryLimitBytes == nil {
		return runtime.retention.store.RecordQuotaState(
			context.WithoutCancel(ctx),
			0,
			nil,
			false,
			"",
			time.Now().UTC(),
		)
	}
	source, ok := any(runtime.repository).(objectrepo.RepositoryUsageSource)
	if !ok {
		err := errors.New("snapshot.repository_usage_unavailable")
		recordErr := runtime.retention.store.RecordQuotaState(
			context.WithoutCancel(ctx),
			0,
			policy.RepositoryLimitBytes,
			true,
			err.Error(),
			time.Now().UTC(),
		)
		return errors.Join(err, recordErr)
	}
	usage, err := source.RepositoryUsage(ctx)
	if err != nil {
		recordErr := runtime.retention.store.RecordQuotaState(
			context.WithoutCancel(ctx),
			0,
			policy.RepositoryLimitBytes,
			true,
			"snapshot.repository_usage_failed: "+err.Error(),
			time.Now().UTC(),
		)
		return errors.Join(err, recordErr)
	}
	paused := usage >= *policy.RepositoryLimitBytes
	warning := ""
	if paused {
		warning = "snapshot.repository_limit_reached"
	}
	if err := runtime.retention.store.RecordQuotaState(
		context.WithoutCancel(ctx),
		usage,
		policy.RepositoryLimitBytes,
		paused,
		warning,
		time.Now().UTC(),
	); err != nil {
		return err
	}
	if paused {
		return errors.New(warning)
	}
	return nil
}

func (runtime *Runtime) recordWatchEvent(event filehistory.WatchEvent) {
	if event.Confirmation != nil && runtime.state != nil {
		if err := runtime.state.upsertPendingFileChange(
			context.Background(),
			event,
		); err != nil {
			event.Err = errors.Join(event.Err, err)
		}
	} else if event.Mutated && event.Path != "" &&
		runtime.state != nil {
		if err := runtime.state.clearPendingFileChangePath(
			context.Background(),
			event.Path,
		); err != nil {
			event.Err = errors.Join(event.Err, err)
		}
	}
	runtime.watchMu.Lock()
	const maximumRetainedWatchEvents = 1_000
	if len(runtime.watchEvents) == maximumRetainedWatchEvents {
		copy(runtime.watchEvents, runtime.watchEvents[1:])
		runtime.watchEvents = runtime.watchEvents[:maximumRetainedWatchEvents-1]
	}
	runtime.watchEvents = append(runtime.watchEvents, event)
	runtime.watchMu.Unlock()
	if event.Mutated {
		if err := runtime.drainFileHistoryAudit(context.Background()); err != nil {
			runtime.recordWatchEvent(filehistory.WatchEvent{
				Path: event.Path, Err: err,
			})
		}
	}
}

func (runtime *Runtime) WatchEvents() []filehistory.WatchEvent {
	runtime.watchMu.Lock()
	defer runtime.watchMu.Unlock()
	result := make([]filehistory.WatchEvent, len(runtime.watchEvents))
	copy(result, runtime.watchEvents)
	return result
}

func (runtime *Runtime) drainFileHistoryAudit(ctx context.Context) error {
	if runtime.fileAuditDrainer == nil || runtime.headStore == nil {
		return errors.New("filehistory.audit_outbox_unavailable")
	}
	runtime.auditDrainMu.Lock()
	defer runtime.auditDrainMu.Unlock()
	_, err := runtime.fileAuditDrainer.Drain(ctx, runtime.headStore)
	return err
}

func (runtime *Runtime) tryDrainFileHistoryAudit(ctx context.Context) {
	if err := runtime.drainFileHistoryAudit(ctx); err != nil {
		runtime.recordWatchEvent(filehistory.WatchEvent{Err: err})
	}
}

type workspacePaths struct {
	root         string
	files        string
	metadata     string
	data         string
	topology     string
	objects      string
	audit        string
	snapshots    string
	coordination string
	temp         string
	manifest     string
}

func resolvePaths(dataDir string) (workspacePaths, error) {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return workspacePaths{}, err
	}
	absolute = filepath.Clean(absolute)
	metadata := filepath.Dir(absolute)
	if !sameName(filepath.Base(absolute), "data") ||
		!sameName(filepath.Base(metadata), ".vibetable") {
		return workspacePaths{}, errors.New("workspace.v2.data_layout_invalid")
	}
	root := filepath.Dir(metadata)
	result := workspacePaths{
		root: root, files: filepath.Join(root, "files"),
		metadata: metadata, data: absolute,
		topology:     filepath.Join(metadata, "topology"),
		objects:      filepath.Join(metadata, "objects"),
		audit:        filepath.Join(metadata, "audit"),
		snapshots:    filepath.Join(metadata, "snapshots"),
		coordination: filepath.Join(metadata, "coordination"),
		temp:         filepath.Join(metadata, "temp"),
		manifest:     filepath.Join(metadata, "workspace.json"),
	}
	return result, nil
}

// ValidateBinding performs the zero-write startup identity check used before
// PocketBase bootstraps or runs migrations.
func ValidateBinding(dataDir string, workspaceID string) error {
	_, _, err := validateBinding(dataDir, workspaceID)
	return err
}

func ValidateStartupBinding(
	dataDir string,
	workspaceID string,
	sessionEpoch uint64,
	fenceEpoch uint64,
	claimID string,
) error {
	if !validUUID(workspaceID) ||
		!validUUID(claimID) ||
		sessionEpoch == 0 ||
		fenceEpoch == 0 {
		return errors.New("workspace.v2.identity_invalid")
	}
	return ValidateBinding(dataDir, workspaceID)
}

func validateBinding(
	dataDir string,
	workspaceID string,
) (workspacePaths, contractsv2.WorkspaceManifest, error) {
	if !validUUID(workspaceID) {
		return workspacePaths{}, contractsv2.WorkspaceManifest{},
			errors.New("workspace.v2.identity_invalid")
	}
	paths, err := resolvePaths(dataDir)
	if err != nil {
		return workspacePaths{}, contractsv2.WorkspaceManifest{}, err
	}
	raw, err := readFileBounded(paths.manifest, 1<<20)
	if err != nil {
		return workspacePaths{}, contractsv2.WorkspaceManifest{},
			fmt.Errorf("read workspace manifest: %w", err)
	}
	manifest, err := contractsv2.DecodeStrict[contractsv2.WorkspaceManifest](raw)
	if err != nil {
		return workspacePaths{}, contractsv2.WorkspaceManifest{}, err
	}
	if manifest.WorkspaceID != workspaceID {
		return workspacePaths{}, contractsv2.WorkspaceManifest{},
			errors.New("workspace.identity_mismatch")
	}
	return paths, manifest, nil
}

func sameName(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func openRepository(
	ctx context.Context,
	paths workspacePaths,
	manifest contractsv2.WorkspaceManifest,
	options Options,
) (*objectrepo.KopiaRepository, error) {
	if manifest.RepositoryFormat != "kopia-v3" {
		return nil, errors.New("repository.format_unsupported")
	}
	mode := objectrepo.EncryptionMode(manifest.EncryptionMode)
	keys, err := objectrepo.NewKeyProvider(
		objectrepo.WindowsCredentialVault{},
	).Open(ctx, manifest.WorkspaceID, mode)
	if err != nil {
		return nil, err
	}
	defer func() {
		for index := range keys.Password {
			keys.Password[index] = 0
		}
	}()
	configPath := filepath.Join(paths.coordination, "kopia.repository.config")
	storagePath := filepath.Join(paths.objects, "kopia")
	_, statErr := os.Stat(configPath)
	var repository *objectrepo.KopiaRepository
	switch {
	case statErr == nil:
		repository, err = objectrepo.OpenKopia(
			ctx, configPath, string(keys.Password),
		)
	case errors.Is(statErr, os.ErrNotExist):
		if mode == objectrepo.EncryptionProtected {
			return nil, objectrepo.ErrKeyMissing
		}
		if entries, readErr := os.ReadDir(storagePath); readErr == nil && len(entries) != 0 {
			return nil, errors.New("repository.config_missing")
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, readErr
		}
		repository, err = objectrepo.CreateKopiaFilesystem(
			ctx, storagePath, configPath, string(keys.Password),
		)
	default:
		return nil, statErr
	}
	if err != nil {
		return nil, err
	}
	authority := objectrepo.Authority{
		WorkspaceID: options.WorkspaceID,
		FenceEpoch:  options.FenceEpoch,
		ClaimID:     options.ClaimID,
	}
	if statErr == nil {
		err = repository.AcceptAuthority(ctx, &authority, authority)
		if errors.Is(err, objectrepo.ErrStaleAuthority) {
			err = repository.AcceptAuthority(ctx, nil, authority)
		}
	} else {
		err = repository.AcceptAuthority(ctx, nil, authority)
	}
	if err != nil {
		_ = repository.Close(context.Background())
		return nil, err
	}
	return repository, nil
}

func openCoordinator(
	ctx context.Context,
	path string,
	options Options,
) (*writecoordinator.WorkspaceWriteCoordinator, uint64, error) {
	sessionEpoch := options.SessionEpoch
	if _, err := os.Stat(path); err == nil {
		var (
			workspaceID string
			fenceEpoch  uint64
			claimID     string
			persisted   uint64
		)
		db, openErr := sql.Open("sqlite", path)
		if openErr != nil {
			return nil, 0, openErr
		}
		queryErr := db.QueryRowContext(ctx, `
			SELECT workspace_id, session_epoch, fence_epoch, claim_id
			FROM coordination_state WHERE singleton = 1`,
		).Scan(&workspaceID, &persisted, &fenceEpoch, &claimID)
		closeErr := db.Close()
		if queryErr != nil {
			return nil, 0, errors.Join(queryErr, closeErr)
		}
		if closeErr != nil {
			return nil, 0, closeErr
		}
		if workspaceID != options.WorkspaceID ||
			fenceEpoch != options.FenceEpoch ||
			claimID != options.ClaimID ||
			persisted > options.SessionEpoch {
			return nil, 0, writecoordinator.ErrStaleToken
		}
		sessionEpoch = persisted
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, 0, err
	}
	coordinator, err := writecoordinator.OpenPersistent(
		path,
		options.WorkspaceID,
		options.FenceEpoch,
		options.ClaimID,
		sessionEpoch,
	)
	if err != nil {
		return nil, 0, err
	}
	return coordinator, sessionEpoch, nil
}

func (runtime *Runtime) recoverPreparedMutation(ctx context.Context) error {
	recovery := runtime.coordinator.RecoveryState()
	if recovery.PendingMutationRevision == 0 {
		return nil
	}
	businessReceipt, found, err :=
		writecoordinator.LoadPocketBaseReceipt(
			ctx,
			runtime.app,
			recovery.Token,
			recovery.PendingMutationRevision,
		)
	if err != nil {
		return err
	}
	if found && businessReceipt.Kind == "conflict.external" {
		if err := runtime.recoverPreparedExternalConflict(
			ctx, businessReceipt.Identity,
		); err != nil {
			return err
		}
		recovery = runtime.coordinator.RecoveryState()
		if recovery.PendingMutationRevision == 0 {
			return nil
		}
	}
	_, retentionMutation, err := runtime.state.retention(ctx)
	if err != nil {
		return err
	}
	proofs := 0
	if retentionMutation == recovery.PendingMutationRevision {
		proofs++
	}
	catalogCommitted, err := runtime.catalog.HasCommittedMutation(
		ctx,
		recovery.Token.WorkspaceID,
		recovery.PendingMutationRevision,
		recovery.Token.SessionEpoch,
		recovery.Token.FenceEpoch,
		recovery.Token.ClaimID,
	)
	if err != nil {
		return err
	}
	if catalogCommitted {
		proofs++
	}
	head, found, err := runtime.headStore.Load(
		ctx,
		recovery.Token.WorkspaceID,
	)
	if err != nil {
		return err
	}
	if found &&
		head.MutationRevision == recovery.PendingMutationRevision &&
		head.SessionEpoch == recovery.Token.SessionEpoch &&
		head.FenceEpoch == recovery.Token.FenceEpoch &&
		head.ClaimID == recovery.Token.ClaimID {
		proofs++
	}
	businessCommitted, err := writecoordinator.HasPocketBaseReceipt(
		ctx,
		runtime.app,
		recovery.Token,
		recovery.PendingMutationRevision,
	)
	if err != nil {
		return err
	}
	if businessCommitted {
		proofs++
	}
	if businessCommitted && found &&
		head.MutationRevision == recovery.PendingMutationRevision {
		externalConflict, err :=
			runtime.headStore.HasExternalConflictProof(
				ctx, recovery.PendingMutationRevision,
			)
		if err != nil {
			return err
		}
		if externalConflict {
			// A conflict external stage deliberately commits its PocketBase
			// receipt and file-history head as one cross-domain publication.
			// The correlated head-store proof collapses the two domain
			// observations into the single expected coordinator proof.
			proofs--
		}
	}
	if runtime.retention != nil {
		retentionCommitted, err := runtime.retention.hasCommittedMutation(
			ctx,
			writecoordinator.WriteIntent{
				Token:            recovery.Token,
				MutationRevision: recovery.PendingMutationRevision,
			},
		)
		if err != nil {
			return err
		}
		if retentionCommitted {
			proofs++
		}
	}
	if proofs > 1 {
		return errors.New("workspace.mutation_recovery_ambiguous")
	}
	return runtime.coordinator.ResolvePreparedMutation(
		ctx,
		recovery.Token,
		recovery.PendingMutationRevision,
		proofs == 1,
	)
}

func (runtime *Runtime) recoverPreparedExternalConflict(
	ctx context.Context,
	operationID string,
) (err error) {
	if strings.TrimSpace(operationID) == "" {
		return errors.New("workspace.conflict_recovery_identity_required")
	}
	if runtime.materializer == nil {
		runtime.materializer, err = filehistory.OpenMaterializer(
			runtime.paths.files,
			filepath.Join(
				runtime.paths.coordination,
				"file-materializer",
			),
			runtime.repository,
		)
		if err != nil {
			return err
		}
	}
	if runtime.history == nil {
		runtime.history, err =
			filehistory.OpenCurrentForPreparedRecovery(
				ctx,
				runtime.repository,
				runtime.coordinator,
				runtime.headStore,
				filehistory.WithMaterializer(runtime.materializer),
			)
		if err != nil {
			return err
		}
	}
	applier, err := filehistory.NewConflictApplier(
		runtime.history, runtime.headStore,
	)
	if err != nil {
		return err
	}
	engine, err := conflictresolution.OpenEngine(
		joinCoordination(runtime.paths, "conflicts.db"),
	)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, engine.Close())
	}()
	owner := &productionReplicaConflict{
		runtime:   runtime,
		conflicts: engine,
		applier:   applier,
	}
	return engine.RecoverOperation(
		ctx,
		operationID,
		&workspaceConflictAppender{owner: owner},
	)
}

func ensureAuditGenesis(
	ctx context.Context,
	ledger *auditledger.Ledger,
	manifest contractsv2.WorkspaceManifest,
) error {
	if ledger.Anchor().LedgerSequence != 0 {
		return ledger.Verify()
	}
	createdAt, err := time.Parse(time.RFC3339, manifest.CreatedAt)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"type":        "workspace.v2.genesis",
		"workspaceId": manifest.WorkspaceID,
		"format":      manifest.FormatVersion,
	})
	if err != nil {
		return err
	}
	envelope, err := auditledger.NewEnvelope(
		"workspace-v2-genesis:"+manifest.WorkspaceID,
		"workspace-v2-system:"+manifest.WorkspaceID,
		1,
		"workspace-v2-genesis",
		payload,
		createdAt,
	)
	if err != nil {
		return err
	}
	_, err = ledger.Append(ctx, envelope)
	return err
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil &&
		strings.ToLower(value) == value
}
