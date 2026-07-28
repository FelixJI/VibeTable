package replica

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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	_ "modernc.org/sqlite"
)

var (
	ErrRemoteUnavailable       = errors.New("replica.remote_unavailable")
	ErrRemoteIdentityInvalid   = errors.New("replica.remote_identity_invalid")
	ErrVerificationInvalid     = errors.New("replica.verification_invalid")
	ErrReplicationUnavailable  = errors.New("replica.replication_unavailable")
	ErrTakeoverModeInvalid     = errors.New("replica.takeover_mode_invalid")
	ErrLocalAuthorityUnchanged = errors.New("replica.local_authority_unchanged")
)

type RemoteIdentity struct {
	WorkspaceID string
	ReplicaID   string
	Strength    CoordinationStrength
}

type Checkpoint struct {
	WorkspaceID     string
	ReplicaID       string
	SnapshotID      string
	CatalogRevision uint64
	FenceEpoch      uint64
	ClaimID         string
	Roots           []objectrepo.ObjectID
	RootDigest      string
	Snapshot        snapshot.Record
	Manifests       []objectrepo.ManifestRecord
}

type ReplicationReceipt struct {
	WorkspaceID     string
	ReplicaID       string
	SnapshotID      string
	CatalogRevision uint64
	CheckpointID    string
	RootDigest      string
	CommittedAt     time.Time
}

type VerificationReceipt struct {
	WorkspaceID      string
	ReplicaID        string
	SnapshotID       string
	CatalogRevision  uint64
	CheckpointID     string
	RootDigest       string
	Reopened         bool
	AllRootsReadable bool
	VerifiedAt       time.Time
}

// VerifiedRemote is deliberately stronger than a "provider available" flag.
// A production adapter must independently bind the selected remote to a
// workspace identity, durably replicate an immutable checkpoint, and reopen
// that target before attesting that every requested root is readable.
type VerifiedRemote interface {
	VerifyIdentity(context.Context) (RemoteIdentity, error)
	LeaseStore() LeaseCASStore
	ReplicateCheckpoint(context.Context, Checkpoint) (ReplicationReceipt, error)
	ReopenAndVerifyRoots(context.Context, Checkpoint, ReplicationReceipt) (VerificationReceipt, error)
	AppendPublication(context.Context, Publication) error
	ListPublications(context.Context, string) ([]Publication, error)
}

type SnapshotCatalog interface {
	List(context.Context, string) ([]snapshot.Record, error)
}

type ConflictScan struct {
	WorkspaceID          string
	ReplicaID            string
	LocalSnapshotID      string
	LocalCatalogRevision uint64
	NextSnapshotSequence uint64
}

type IncomingConflict struct {
	Set             conflictresolution.Set
	ReplicaSnapshot snapshot.Record
	Roots           []objectrepo.ObjectID
	Verification    VerificationReceipt
	RecoveryBundle  *FilesystemRecoveryBundle
}

type ConflictRemote interface {
	DiscoverConflicts(
		context.Context,
		ConflictScan,
	) ([]IncomingConflict, error)
}

type ConflictSink interface {
	Add(context.Context, conflictresolution.Set) error
	Inspect(context.Context, string) (conflictresolution.Set, error)
}

// ConflictDependencyScanner owns the production relation/automation/plugin
// dependency scan. It must set Complete only after all three immutable
// candidates, including every whole-table component, were inspected.
type ConflictDependencyScanner interface {
	ScanConflictDependencies(
		context.Context,
		conflictresolution.Candidate,
		conflictresolution.Candidate,
		conflictresolution.Candidate,
	) (conflictresolution.DependencyGraph, error)
}

type AuthorityState struct {
	WorkspaceID string
	FenceEpoch  uint64
	ClaimID     string
}

type AuthorityTransfer interface {
	CurrentAuthority() AuthorityState
	ApplyReplicaClaim(context.Context, Claim) error
}

type ManagerOptions struct {
	WorkspaceID       string
	DeviceID          string
	QueuePath         string
	StatePath         string
	PublicationPath   string
	PublicationKey    []byte
	Remote            VerifiedRemote
	Catalog           SnapshotCatalog
	Repository        objectrepo.Repository
	Authority         AuthorityTransfer
	Conflicts         ConflictSink
	DependencyScanner ConflictDependencyScanner
	DestructiveSafe   func(context.Context) error
	Now               func() time.Time
}

type Status struct {
	CoordinationStrength CoordinationStrength
	SyncState            string
	PendingSync          bool
	LastVerifiedAt       *time.Time
}

type Manager struct {
	mu                sync.Mutex
	workspace         string
	device            string
	identity          RemoteIdentity
	remote            VerifiedRemote
	catalog           SnapshotCatalog
	repository        objectrepo.Repository
	authority         AuthorityTransfer
	conflicts         ConflictSink
	conflictRemote    ConflictRemote
	dependencyScanner ConflictDependencyScanner
	destructiveSafe   func(context.Context) error
	queue             *Queue
	state             *productionState
	strong            *StrongLease
	advisory          *AdvisoryDAG
	now               func() time.Time
}

func OpenManager(ctx context.Context, options ManagerOptions) (_ *Manager, err error) {
	if strings.TrimSpace(options.WorkspaceID) == "" ||
		strings.TrimSpace(options.DeviceID) == "" ||
		options.Remote == nil ||
		options.Catalog == nil ||
		options.Repository == nil ||
		options.Authority == nil {
		return nil, ErrReplicationUnavailable
	}
	if receiver, ok := options.Remote.(interface {
		setPublicationKey([]byte)
	}); ok {
		receiver.setPublicationKey(options.PublicationKey)
	}
	identity, err := options.Remote.VerifyIdentity(ctx)
	if err != nil {
		return nil, errors.Join(ErrReplicationUnavailable, err)
	}
	if identity.WorkspaceID != options.WorkspaceID ||
		strings.TrimSpace(identity.ReplicaID) == "" ||
		(identity.Strength != Strong && identity.Strength != Advisory) {
		return nil, ErrRemoteIdentityInvalid
	}
	queue, err := OpenPersistentQueue(options.QueuePath)
	if err != nil {
		return nil, err
	}
	state, err := openProductionState(options.StatePath)
	if err != nil {
		_ = queue.Close()
		return nil, err
	}
	manager := &Manager{
		workspace:         options.WorkspaceID,
		device:            options.DeviceID,
		identity:          identity,
		remote:            options.Remote,
		catalog:           options.Catalog,
		repository:        options.Repository,
		authority:         options.Authority,
		conflicts:         options.Conflicts,
		dependencyScanner: options.DependencyScanner,
		destructiveSafe:   options.DestructiveSafe,
		queue:             queue,
		state:             state,
		now:               options.Now,
	}
	if manager.now == nil {
		manager.now = func() time.Time { return time.Now().UTC() }
	}
	defer func() {
		if err != nil {
			_ = manager.Close()
		}
	}()
	if identity.Strength == Strong {
		store := options.Remote.LeaseStore()
		if store == nil {
			return nil, ErrReplicationUnavailable
		}
		manager.strong, err = NewStrongLeaseWithStore(store)
	} else {
		if options.Remote.LeaseStore() != nil ||
			len(options.PublicationKey) < minimumPublicationKeyBytes {
			return nil, ErrReplicationUnavailable
		}
		manager.advisory, err = OpenPersistentAdvisoryDAG(
			options.PublicationPath,
			options.WorkspaceID,
			options.PublicationKey,
		)
	}
	if candidate, ok := options.Remote.(ConflictRemote); ok &&
		options.Conflicts != nil {
		manager.conflictRemote = candidate
	}
	if err != nil {
		return nil, err
	}
	if err := manager.refreshAdvisory(ctx); err != nil {
		return nil, err
	}
	if err := manager.recoverTakeover(ctx); err != nil {
		return nil, err
	}
	if err := manager.cleanupVerifiedPins(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func (manager *Manager) Status(ctx context.Context) (Status, error) {
	if manager == nil || manager.queue == nil {
		return Status{}, ErrReplicationUnavailable
	}
	if _, err := manager.verifyIdentity(ctx); err != nil {
		return Status{}, err
	}
	tasks, err := manager.queue.List(ctx)
	if err != nil {
		return Status{}, err
	}
	pending := false
	failed := false
	for _, task := range tasks {
		if !task.Completed {
			pending = true
			if task.LastError != "" {
				failed = true
			}
		}
	}
	syncState := "replicated"
	if pending {
		syncState = "pending"
	}
	if failed {
		syncState = "failed"
	}
	verifiedAt, found, err := manager.state.lastVerified(ctx)
	if err != nil {
		return Status{}, err
	}
	var last *time.Time
	if found {
		last = &verifiedAt
	}
	return Status{
		CoordinationStrength: manager.identity.Strength,
		SyncState:            syncState,
		PendingSync:          pending,
		LastVerifiedAt:       last,
	}, nil
}

// Synchronize durably protects and queues every locally published snapshot
// that does not yet have a verified remote receipt. Local commits are never
// rolled back when remote work fails.
func (manager *Manager) Synchronize(ctx context.Context) error {
	if manager == nil {
		return ErrReplicationUnavailable
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, err := manager.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := manager.ensureDestructiveSafe(ctx); err != nil {
		return err
	}
	if err := manager.queuePublishedSnapshots(ctx); err != nil {
		return err
	}
	if err := manager.queue.Drain(ctx, manager); err != nil {
		return err
	}
	return manager.discoverConflicts(ctx)
}

// QueuePublishedSnapshots durably pins and enqueues every unpublished local
// snapshot without performing remote I/O. It is safe on request and capture
// paths that must not block on network availability.
func (manager *Manager) QueuePublishedSnapshots(ctx context.Context) error {
	if manager == nil {
		return ErrReplicationUnavailable
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := manager.ensureDestructiveSafe(ctx); err != nil {
		return err
	}
	return manager.queuePublishedSnapshots(ctx)
}

func (manager *Manager) queuePublishedSnapshots(ctx context.Context) error {
	records, err := manager.catalog.List(ctx, manager.workspace)
	if err != nil {
		return err
	}
	for _, record := range records {
		synced, err := manager.state.isVerified(
			ctx, record.SnapshotID, record.CatalogRevision,
		)
		if err != nil {
			return err
		}
		if synced {
			continue
		}
		taskID := deterministicTaskID(
			manager.identity.ReplicaID,
			record.SnapshotID,
			record.CatalogRevision,
		)
		pending, err := manager.state.pending(ctx, taskID)
		if err != nil {
			return err
		}
		if !pending {
			authority := manager.authority.CurrentAuthority()
			if authority.WorkspaceID != manager.workspace {
				return ErrWorkspaceMismatch
			}
			reachabilityRoots, err := snapshot.ReachabilityObjectIDs(
				ctx,
				manager.repository,
				record,
			)
			if err != nil {
				return err
			}
			pin, err := manager.repository.Pin(
				ctx,
				objectrepo.Authority{
					WorkspaceID: authority.WorkspaceID,
					FenceEpoch:  authority.FenceEpoch,
					ClaimID:     authority.ClaimID,
				},
				reachabilityRoots,
				"pending-replica:"+taskID,
				nil,
			)
			if err != nil {
				return err
			}
			if err := manager.state.addPending(ctx, taskID, pin.PinID); err != nil {
				_ = manager.repository.ReleasePin(
					context.WithoutCancel(ctx),
					objectrepo.Authority{
						WorkspaceID: authority.WorkspaceID,
						FenceEpoch:  authority.FenceEpoch,
						ClaimID:     authority.ClaimID,
					},
					pin.PinID,
				)
				return err
			}
		}
		if err := manager.queue.Enqueue(SyncTask{
			TaskID: taskID, WorkspaceID: manager.workspace,
			SnapshotID: record.SnapshotID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) ConflictReady() bool {
	return manager != nil &&
		manager.conflictRemote != nil &&
		manager.conflicts != nil
}

func (manager *Manager) discoverConflicts(ctx context.Context) error {
	if !manager.ConflictReady() {
		return nil
	}
	records, err := manager.catalog.List(ctx, manager.workspace)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].SnapshotSequence <
			records[j].SnapshotSequence
	})
	local := records[len(records)-1]
	incoming, err := manager.conflictRemote.DiscoverConflicts(
		ctx,
		ConflictScan{
			WorkspaceID:          manager.workspace,
			ReplicaID:            manager.identity.ReplicaID,
			LocalSnapshotID:      local.SnapshotID,
			LocalCatalogRevision: local.CatalogRevision,
			NextSnapshotSequence: local.SnapshotSequence + 1,
		},
	)
	if err != nil {
		return err
	}
	known := make(map[string]snapshot.Record, len(records))
	for _, record := range records {
		known[record.SnapshotID] = record
	}
	authority := manager.authority.CurrentAuthority()
	for _, candidate := range incoming {
		set := candidate.Set
		if candidate.RecoveryBundle != nil {
			if err := manager.installRecoveryBundle(
				ctx,
				*candidate.RecoveryBundle,
				authority,
			); err != nil {
				return err
			}
		}
		baseRecord, baseExists := known[set.Base.SnapshotID]
		if !baseExists {
			return ErrVerificationInvalid
		}
		if len(set.Base.Files) == 0 {
			baseCandidate, err := manager.snapshotConflictCandidate(
				ctx,
				baseRecord,
			)
			if err != nil {
				return err
			}
			set.Base = baseCandidate
		}
		if len(set.Local.Files) == 0 {
			localCandidate, err := manager.snapshotConflictCandidate(
				ctx,
				local,
			)
			if err != nil {
				return err
			}
			set.Local = localCandidate
		}
		set.Dependencies, err = manager.scanConflictDependencies(
			ctx,
			set.Base,
			set.Local,
			set.Replica,
		)
		if err != nil {
			return err
		}
		if set.WorkspaceID != manager.workspace ||
			set.State != conflictresolution.StatePending ||
			set.Base.SnapshotID == "" ||
			set.Local.SnapshotID != local.SnapshotID ||
			set.Replica.SnapshotID !=
				candidate.ReplicaSnapshot.SnapshotID ||
			candidate.ReplicaSnapshot.WorkspaceID !=
				manager.workspace ||
			candidate.ReplicaSnapshot.SnapshotSequence == 0 ||
			candidate.Verification.WorkspaceID !=
				manager.workspace ||
			candidate.Verification.ReplicaID !=
				manager.identity.ReplicaID ||
			candidate.Verification.SnapshotID !=
				candidate.ReplicaSnapshot.SnapshotID ||
			candidate.Verification.CatalogRevision !=
				candidate.ReplicaSnapshot.CatalogRevision ||
			!candidate.Verification.Reopened ||
			!candidate.Verification.AllRootsReadable ||
			candidate.Verification.RootDigest !=
				rootsDigest(sortedRoots(candidate.Roots)) {
			return ErrVerificationInvalid
		}
		existing, err := manager.conflicts.Inspect(
			ctx, set.ConflictID,
		)
		if err == nil {
			if existing.WorkspaceID != set.WorkspaceID ||
				existing.Base.SnapshotID != set.Base.SnapshotID ||
				existing.Local.SnapshotID != set.Local.SnapshotID ||
				existing.Replica.SnapshotID !=
					set.Replica.SnapshotID {
				return ErrVerificationInvalid
			}
			continue
		}
		if !errors.Is(
			err, conflictresolution.ErrConflictNotFound,
		) {
			return err
		}
		if _, exists := known[set.Base.SnapshotID]; !exists {
			return ErrVerificationInvalid
		}
		if _, exists := known[set.Local.SnapshotID]; !exists {
			return ErrVerificationInvalid
		}
		if err := validateIncomingSnapshot(
			ctx,
			manager.repository,
			candidate.ReplicaSnapshot,
			candidate.Roots,
		); err != nil {
			return err
		}
		baseRoots, err := snapshot.ReachabilityObjectIDs(
			ctx,
			manager.repository,
			known[set.Base.SnapshotID],
		)
		if err != nil {
			return err
		}
		localRoots, err := snapshot.ReachabilityObjectIDs(
			ctx,
			manager.repository,
			known[set.Local.SnapshotID],
		)
		if err != nil {
			return err
		}
		pinRoots := unionRoots(baseRoots, localRoots, candidate.Roots)
		protected := make(map[objectrepo.ObjectID]struct{}, len(pinRoots))
		for _, root := range pinRoots {
			protected[root] = struct{}{}
		}
		for _, source := range []conflictresolution.Candidate{
			set.Base, set.Local, set.Replica,
		} {
			for _, file := range source.Files {
				if file.Deleted {
					continue
				}
				if _, ok := protected[objectrepo.ObjectID(file.ContentID)]; !ok {
					return ErrVerificationInvalid
				}
			}
			for _, objectID := range source.AttachmentObjects {
				if _, ok := protected[objectrepo.ObjectID(objectID)]; !ok {
					return ErrVerificationInvalid
				}
			}
		}
		pin, err := manager.repository.Pin(
			ctx,
			objectrepo.Authority{
				WorkspaceID: authority.WorkspaceID,
				FenceEpoch:  authority.FenceEpoch,
				ClaimID:     authority.ClaimID,
			},
			pinRoots,
			"conflict:"+set.ConflictID,
			nil,
		)
		if err != nil {
			return err
		}
		set.RootPinIDs = uniqueStringsReplica(
			append(set.RootPinIDs, pin.PinID),
		)
		if err := manager.conflicts.Add(ctx, set); err != nil {
			// The published recovery snapshot owns the same pin, so retain
			// it on conflict-store failure rather than risking data loss.
			return err
		}
	}
	return nil
}

func (manager *Manager) scanConflictDependencies(
	ctx context.Context,
	base conflictresolution.Candidate,
	local conflictresolution.Candidate,
	remote conflictresolution.Candidate,
) (conflictresolution.DependencyGraph, error) {
	if manager.dependencyScanner != nil {
		graph, err := manager.dependencyScanner.ScanConflictDependencies(
			ctx, base, local, remote,
		)
		if err != nil {
			return conflictresolution.DependencyGraph{}, err
		}
		return graph, nil
	}
	// File-only candidates have no table relation/automation/plugin closure to
	// discover. Any database or typed table divergence requires the production
	// scanner and therefore remains explicitly incomplete.
	databaseChanged :=
		base.BusinessDatabaseObjectID != local.BusinessDatabaseObjectID ||
			base.BusinessDatabaseObjectID != remote.BusinessDatabaseObjectID
	hasTables := len(base.Tables)+len(local.Tables)+len(remote.Tables) > 0
	graph := conflictresolution.DependencyGraph{
		Complete: !databaseChanged && !hasTables,
		Edges:    map[string][]string{},
	}
	for _, candidate := range []conflictresolution.Candidate{
		base, local, remote,
	} {
		for documentID := range candidate.Files {
			if _, exists := graph.Edges[documentID]; !exists {
				graph.Edges[documentID] = []string{}
			}
		}
		for tableID := range candidate.Tables {
			if _, exists := graph.Edges[tableID]; !exists {
				graph.Edges[tableID] = []string{}
			}
		}
	}
	return graph, nil
}

func (manager *Manager) installRecoveryBundle(
	ctx context.Context,
	bundle FilesystemRecoveryBundle,
	authority AuthorityState,
) error {
	objects := make(
		[]objectrepo.ObjectInput,
		0,
		len(bundle.Objects),
	)
	for id, content := range bundle.Objects {
		objects = append(objects, objectrepo.ObjectInput{
			Name:    "replica:" + string(id),
			Content: append([]byte(nil), content...),
		})
	}
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Name < objects[j].Name
	})
	manifests := make(
		[]objectrepo.ManifestInput,
		0,
		len(bundle.Manifests),
	)
	for _, manifest := range bundle.Manifests {
		manifests = append(manifests, objectrepo.ManifestInput{
			Name:    manifest.Name,
			Labels:  manifest.Labels,
			Payload: append(json.RawMessage(nil), manifest.Payload...),
		})
	}
	sort.Slice(manifests, func(i, j int) bool {
		if manifests[i].Name != manifests[j].Name {
			return manifests[i].Name < manifests[j].Name
		}
		return string(manifests[i].Payload) < string(manifests[j].Payload)
	})
	receipt, err := manager.repository.Commit(
		ctx,
		objectrepo.CommitRequest{
			Authority: objectrepo.Authority{
				WorkspaceID: authority.WorkspaceID,
				FenceEpoch:  authority.FenceEpoch,
				ClaimID:     authority.ClaimID,
			},
			Objects:   objects,
			Manifests: manifests,
		},
	)
	if err != nil {
		return err
	}
	for id := range bundle.Objects {
		if receipt.Objects["replica:"+string(id)] != id {
			return ErrVerificationInvalid
		}
	}
	for id, manifest := range bundle.Manifests {
		if receipt.Manifests[manifest.Name] != id {
			return ErrVerificationInvalid
		}
	}
	return nil
}

func (manager *Manager) snapshotConflictCandidate(
	ctx context.Context,
	record snapshot.Record,
) (conflictresolution.Candidate, error) {
	_, fileHeadID, err := snapshotReferencedManifestIDs(
		ctx,
		manager.repository,
		record,
	)
	if err != nil {
		return conflictresolution.Candidate{}, err
	}
	fileHead, err := manager.repository.GetManifest(ctx, fileHeadID)
	if err != nil {
		return conflictresolution.Candidate{}, err
	}
	var reference struct {
		HistoryRoot objectrepo.ManifestID `json:"historyRoot"`
	}
	if err := json.Unmarshal(fileHead.Payload, &reference); err != nil ||
		reference.HistoryRoot == "" {
		return conflictresolution.Candidate{}, ErrVerificationInvalid
	}
	history, err := manager.repository.GetManifest(
		ctx,
		reference.HistoryRoot,
	)
	if err != nil {
		return conflictresolution.Candidate{}, err
	}
	objects := make(map[objectrepo.ObjectID][]byte, 3)
	for _, name := range []string{
		"database", "workspace-settings", "file-state-root",
	} {
		id := record.ObjectMap[name]
		if id == "" {
			return conflictresolution.Candidate{}, ErrVerificationInvalid
		}
		reader, err := manager.repository.Open(ctx, id)
		if err != nil {
			return conflictresolution.Candidate{}, err
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, 512<<20))
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return conflictresolution.Candidate{}, err
		}
		objects[id] = content
	}
	var fileState struct {
		Attachments map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
	}
	fileStateRootID := record.ObjectMap["file-state-root"]
	if err := json.Unmarshal(objects[fileStateRootID], &fileState); err != nil {
		return conflictresolution.Candidate{}, ErrVerificationInvalid
	}
	for _, id := range fileState.Attachments {
		if id == "" {
			return conflictresolution.Candidate{}, ErrVerificationInvalid
		}
		reader, err := manager.repository.Open(ctx, id)
		if err != nil {
			return conflictresolution.Candidate{}, err
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, 512<<20))
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return conflictresolution.Candidate{}, err
		}
		objects[id] = content
	}
	return filesystemConflictCandidate(FilesystemRecoveryBundle{
		Snapshot: record,
		Objects:  objects,
		Manifests: map[objectrepo.ManifestID]objectrepo.ManifestRecord{
			history.ID: history,
		},
	})
}

// QueueSynchronize makes the RPC promise ("queued") durable together with its
// exact operation receipt before network work begins. Deterministic task IDs
// make replay after a crash safe.
func (manager *Manager) QueueSynchronize(
	ctx context.Context,
	receipt protocolv2.OperationReceipt,
) error {
	if receipt.WorkspaceID != manager.workspace ||
		receipt.Method != "replica.synchronize" ||
		receipt.Scope != protocolv2.WorkspaceScope ||
		receipt.OperationID == "" ||
		len(receipt.Result) == 0 {
		return errors.New("replica.operation_receipt_invalid")
	}
	if err := manager.ensureDestructiveSafe(ctx); err != nil {
		return err
	}
	if err := manager.state.storeOperationReceipt(ctx, receipt); err != nil {
		return err
	}
	return manager.QueuePublishedSnapshots(ctx)
}

func (manager *Manager) Sync(ctx context.Context, task SyncTask) error {
	identity, err := manager.verifyIdentity(ctx)
	if err != nil {
		return err
	}
	record, err := manager.snapshot(ctx, task.SnapshotID)
	if err != nil {
		return err
	}
	if err := validateSnapshotClosure(
		ctx,
		manager.repository,
		record,
	); err != nil {
		return err
	}
	authority := manager.authority.CurrentAuthority()
	if authority.WorkspaceID != manager.workspace {
		return ErrWorkspaceMismatch
	}
	if identity.Strength == Strong {
		if err := manager.ensureDestructiveSafe(ctx); err != nil {
			return err
		}
		current, found, err := manager.strong.store.Load(ctx, manager.workspace)
		if err != nil {
			return err
		}
		if !found ||
			current.Claim.FenceEpoch != authority.FenceEpoch ||
			current.Claim.ClaimID != authority.ClaimID ||
			current.Claim.ExpiresAt.Before(manager.now().UTC()) {
			return ErrStaleClaim
		}
	}
	checkpoint, err := makeCheckpoint(
		ctx,
		identity,
		manager.repository,
		record,
	)
	if err != nil {
		return err
	}
	replication, err := manager.remote.ReplicateCheckpoint(ctx, checkpoint)
	if err != nil {
		return err
	}
	if err := validateReplicationReceipt(checkpoint, replication); err != nil {
		return err
	}
	verification, err := manager.remote.ReopenAndVerifyRoots(
		ctx, checkpoint, replication,
	)
	if err != nil {
		return err
	}
	if err := validateVerificationReceipt(
		checkpoint, replication, verification,
	); err != nil {
		return err
	}
	if identity.Strength == Advisory {
		if err := manager.publishAdvisory(
			ctx, record, authority, replication,
		); err != nil {
			return err
		}
	}
	if err := manager.state.markVerified(
		context.WithoutCancel(ctx),
		task.TaskID,
		record,
		replication,
		verification,
	); err != nil {
		return err
	}
	pinID, found, err := manager.state.pendingPin(ctx, task.TaskID)
	if err != nil {
		return err
	}
	if found {
		if err := manager.repository.ReleasePin(
			context.WithoutCancel(ctx),
			objectrepo.Authority{
				WorkspaceID: authority.WorkspaceID,
				FenceEpoch:  authority.FenceEpoch,
				ClaimID:     authority.ClaimID,
			},
			pinID,
		); err != nil {
			return err
		}
		if err := manager.state.clearPending(
			context.WithoutCancel(ctx), task.TaskID,
		); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) ForceTakeover(
	ctx context.Context,
	mode ClaimMode,
) (Claim, error) {
	return manager.forceTakeover(ctx, mode, nil)
}

func (manager *Manager) ForceTakeoverWithReceipt(
	ctx context.Context,
	mode ClaimMode,
	build func(Claim) (protocolv2.OperationReceipt, error),
) (Claim, error) {
	if build == nil {
		return Claim{}, errors.New("replica.receipt_builder_required")
	}
	return manager.forceTakeover(ctx, mode, build)
}

func (manager *Manager) forceTakeover(
	ctx context.Context,
	mode ClaimMode,
	build func(Claim) (protocolv2.OperationReceipt, error),
) (Claim, error) {
	if mode != Writable && mode != Provisional {
		return Claim{}, ErrTakeoverModeInvalid
	}
	current := manager.authority.CurrentAuthority()
	if current.WorkspaceID != manager.workspace {
		return Claim{}, ErrWorkspaceMismatch
	}
	now := manager.now().UTC()
	identity, identityErr := manager.remote.VerifyIdentity(ctx)
	if identityErr != nil {
		if mode != Provisional ||
			!errors.Is(identityErr, ErrRemoteUnavailable) {
			return Claim{}, identityErr
		}
		claim := Claim{
			WorkspaceID:     manager.workspace,
			DeviceID:        manager.device,
			ClaimID:         uuid.NewString(),
			FenceEpoch:      current.FenceEpoch + 1,
			Nonce:           uuid.NewString(),
			Strength:        manager.identity.Strength,
			Mode:            Provisional,
			IssuedAt:        now,
			HeartbeatAt:     now,
			ExpiresAt:       now.Add(24 * time.Hour),
			PreviousClaimID: current.ClaimID,
		}
		receipt, err := buildTakeoverReceipt(build, claim)
		if err != nil {
			return Claim{}, err
		}
		if err := manager.state.prepareTakeover(
			ctx, current, claim, receipt,
		); err != nil {
			return Claim{}, err
		}
		if err := manager.authority.ApplyReplicaClaim(ctx, claim); err != nil {
			return Claim{}, err
		}
		if err := manager.state.completeTakeover(
			context.WithoutCancel(ctx), claim,
		); err != nil {
			return Claim{}, err
		}
		return claim, nil
	}
	if identity != manager.identity {
		return Claim{}, ErrRemoteIdentityInvalid
	}
	if identity.Strength != Strong {
		return Claim{}, ErrTakeoverUnsafe
	}
	if err := manager.ensureDestructiveSafe(ctx); err != nil {
		return Claim{}, err
	}
	next := Claim{
		WorkspaceID:     manager.workspace,
		DeviceID:        manager.device,
		ClaimID:         uuid.NewString(),
		Nonce:           uuid.NewString(),
		Strength:        Strong,
		Mode:            mode,
		ExpiresAt:       now.Add(5 * time.Minute),
		PreviousClaimID: current.ClaimID,
	}
	// Persist the exact normalized claim before the remote CAS. This closes
	// the remote-acquired/local-journal-missing kill window.
	next.IssuedAt = now
	next.HeartbeatAt = now
	next.FenceEpoch = current.FenceEpoch + 1
	receipt, err := buildTakeoverReceipt(build, next)
	if err != nil {
		return Claim{}, err
	}
	if err := manager.state.prepareTakeover(
		ctx, current, next, receipt,
	); err != nil {
		return Claim{}, err
	}
	claim, err := manager.acquireExactStrong(ctx, current, next)
	if err != nil {
		return Claim{}, err
	}
	if claim.FenceEpoch != current.FenceEpoch+1 {
		return Claim{}, ErrStaleClaim
	}
	if err := manager.authority.ApplyReplicaClaim(ctx, claim); err != nil {
		return Claim{}, err
	}
	if err := manager.state.completeTakeover(
		context.WithoutCancel(ctx), claim,
	); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

func buildTakeoverReceipt(
	build func(Claim) (protocolv2.OperationReceipt, error),
	claim Claim,
) (protocolv2.OperationReceipt, error) {
	if build == nil {
		return protocolv2.OperationReceipt{}, nil
	}
	receipt, err := build(claim)
	if err != nil {
		return protocolv2.OperationReceipt{}, err
	}
	if receipt.OperationID == "" ||
		receipt.WorkspaceID != claim.WorkspaceID ||
		receipt.Method != "replica.forceTakeover" ||
		receipt.Scope != protocolv2.WorkspaceScope ||
		len(receipt.Result) == 0 {
		return protocolv2.OperationReceipt{},
			errors.New("replica.operation_receipt_invalid")
	}
	return receipt, nil
}

func (manager *Manager) acquireExactStrong(
	ctx context.Context,
	current AuthorityState,
	next Claim,
) (Claim, error) {
	record, found, err := manager.strong.store.Load(ctx, manager.workspace)
	if err != nil {
		return Claim{}, err
	}
	if !found ||
		record.Claim.FenceEpoch != current.FenceEpoch ||
		record.Claim.ClaimID != current.ClaimID {
		if found && sameLeaseIdentity(record.Claim, next) {
			return record.Claim, nil
		}
		return Claim{}, ErrStaleClaim
	}
	if record.Claim.ExpiresAt.After(manager.now().UTC()) {
		return Claim{}, ErrTakeoverUnsafe
	}
	updated, err := manager.strong.store.CompareAndSwap(
		ctx,
		manager.workspace,
		record.Revision,
		true,
		next,
	)
	if err != nil {
		return Claim{}, err
	}
	if !sameLeaseIdentity(updated.Claim, next) ||
		updated.Revision != record.Revision+1 {
		return Claim{}, ErrCASConflict
	}
	return updated.Claim, nil
}

func (manager *Manager) recoverTakeover(ctx context.Context) error {
	previous, claim, state, found, err :=
		manager.state.loadTakeover(ctx)
	if err != nil || !found || state == "completed" {
		return err
	}
	current := manager.authority.CurrentAuthority()
	if current.WorkspaceID == claim.WorkspaceID &&
		current.FenceEpoch == claim.FenceEpoch &&
		current.ClaimID == claim.ClaimID {
		return manager.state.completeTakeover(
			context.WithoutCancel(ctx), claim,
		)
	}
	if current != previous {
		return ErrStaleClaim
	}
	if claim.Strength == Strong {
		record, remoteFound, err := manager.strong.store.Load(
			ctx, manager.workspace,
		)
		if err != nil {
			return err
		}
		if !remoteFound || !sameLeaseIdentity(record.Claim, claim) {
			if _, err := manager.acquireExactStrong(
				ctx, previous, claim,
			); err != nil {
				return err
			}
		}
	}
	if err := manager.authority.ApplyReplicaClaim(ctx, claim); err != nil {
		return err
	}
	return manager.state.completeTakeover(
		context.WithoutCancel(ctx), claim,
	)
}

func (manager *Manager) LoadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	if manager == nil || manager.state == nil {
		return protocolv2.OperationReceipt{}, false, nil
	}
	return manager.state.loadOperationReceipt(
		ctx, workspaceID, operationID,
	)
}

func (manager *Manager) SnapshotSyncState(
	ctx context.Context,
	record snapshot.Record,
) (string, error) {
	if manager == nil || manager.state == nil || manager.queue == nil {
		return "localOnly", nil
	}
	verified, err := manager.state.isVerified(
		ctx,
		record.SnapshotID,
		record.CatalogRevision,
	)
	if err != nil {
		return "", err
	}
	if verified {
		return "replicated", nil
	}
	taskID := deterministicTaskID(
		manager.identity.ReplicaID,
		record.SnapshotID,
		record.CatalogRevision,
	)
	tasks, err := manager.queue.List(ctx)
	if err != nil {
		return "", err
	}
	for _, task := range tasks {
		if task.TaskID != taskID {
			continue
		}
		switch {
		case task.Completed:
			return "failed", nil
		case task.LastError != "":
			return "failed", nil
		case task.InFlight:
			return "syncing", nil
		default:
			return "pending", nil
		}
	}
	return "localOnly", nil
}

func (manager *Manager) ensureDestructiveSafe(ctx context.Context) error {
	if manager == nil ||
		manager.identity.Strength != Strong ||
		manager.destructiveSafe == nil {
		return nil
	}
	return manager.destructiveSafe(ctx)
}

func (manager *Manager) cleanupVerifiedPins(ctx context.Context) error {
	pending, err := manager.state.verifiedPendingPins(ctx)
	if err != nil {
		return err
	}
	authority := manager.authority.CurrentAuthority()
	for taskID, pinID := range pending {
		if err := manager.repository.ReleasePin(
			ctx,
			objectrepo.Authority{
				WorkspaceID: authority.WorkspaceID,
				FenceEpoch:  authority.FenceEpoch,
				ClaimID:     authority.ClaimID,
			},
			pinID,
		); err != nil {
			return err
		}
		if err := manager.state.clearPending(ctx, taskID); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) Close() error {
	if manager == nil {
		return nil
	}
	var errs []error
	if manager.queue != nil {
		errs = append(errs, manager.queue.Close())
		manager.queue = nil
	}
	if manager.advisory != nil {
		errs = append(errs, manager.advisory.Close())
		manager.advisory = nil
	}
	// A strong lease store is owned by the remote adapter, not Manager.
	if manager.strong != nil {
		manager.strong.store = nil
		manager.strong = nil
	}
	if manager.state != nil {
		errs = append(errs, manager.state.close())
		manager.state = nil
	}
	return errors.Join(errs...)
}

func (manager *Manager) verifyIdentity(
	ctx context.Context,
) (RemoteIdentity, error) {
	identity, err := manager.remote.VerifyIdentity(ctx)
	if err != nil {
		return RemoteIdentity{}, err
	}
	if identity != manager.identity ||
		identity.WorkspaceID != manager.workspace {
		return RemoteIdentity{}, ErrRemoteIdentityInvalid
	}
	return identity, nil
}

func (manager *Manager) snapshot(
	ctx context.Context,
	snapshotID string,
) (snapshot.Record, error) {
	records, err := manager.catalog.List(ctx, manager.workspace)
	if err != nil {
		return snapshot.Record{}, err
	}
	for _, record := range records {
		if record.SnapshotID == snapshotID {
			return record, nil
		}
	}
	return snapshot.Record{}, errors.New("snapshot.not_found")
}

func (manager *Manager) publishAdvisory(
	ctx context.Context,
	record snapshot.Record,
	authority AuthorityState,
	replication ReplicationReceipt,
) error {
	if err := manager.refreshAdvisory(ctx); err != nil {
		return err
	}
	var previous string
	winner, _, found, err := manager.advisory.Winner()
	if err != nil {
		return err
	}
	if found {
		previous = winner.CanonicalHash
	}
	now := manager.now().UTC()
	publication, err := SealPublication(Publication{
		WorkspaceID: manager.workspace,
		Claim: Claim{
			WorkspaceID: manager.workspace,
			DeviceID:    manager.device,
			ClaimID:     authority.ClaimID,
			FenceEpoch:  authority.FenceEpoch,
			Nonce:       replication.CheckpointID,
			Strength:    Advisory,
			// Filesystem/advisory replicas can never prove exclusive
			// ownership. Their publications carry a provisional identity and
			// device-local mutation serial until conflict resolution selects a
			// canonical branch.
			Mode:        Provisional,
			IssuedAt:    now,
			HeartbeatAt: now,
			ExpiresAt:   now.Add(365 * 24 * time.Hour),
		},
		PreviousPublicationHash: previous,
		SnapshotID:              record.SnapshotID,
		CatalogRevision:         record.CatalogRevision,
		CheckpointID:            replication.CheckpointID,
		CreatedAt:               now,
	}, manager.advisory.macKey)
	if err != nil {
		return err
	}
	if err := manager.remote.AppendPublication(ctx, publication); err != nil {
		return err
	}
	if err := manager.advisory.PublishContext(ctx, publication); err != nil {
		return err
	}
	return manager.refreshAdvisory(ctx)
}

func (manager *Manager) refreshAdvisory(ctx context.Context) error {
	if manager == nil || manager.identity.Strength != Advisory {
		return nil
	}
	publications, err := manager.remote.ListPublications(
		ctx, manager.workspace,
	)
	if err != nil {
		return err
	}
	sort.Slice(publications, func(i, j int) bool {
		if publications[i].CreatedAt.Equal(publications[j].CreatedAt) {
			return publications[i].CanonicalHash <
				publications[j].CanonicalHash
		}
		return publications[i].CreatedAt.Before(publications[j].CreatedAt)
	})
	pending := append([]Publication(nil), publications...)
	for len(pending) > 0 {
		progress := false
		next := pending[:0]
		for _, publication := range pending {
			err := manager.advisory.PublishContext(ctx, publication)
			if err == nil || errors.Is(err, ErrPublicationExists) {
				progress = true
				continue
			}
			if errors.Is(err, ErrParentMissing) {
				next = append(next, publication)
				continue
			}
			return err
		}
		if !progress && len(next) > 0 {
			return ErrParentMissing
		}
		pending = next
	}
	return nil
}

func makeCheckpoint(
	ctx context.Context,
	identity RemoteIdentity,
	repository objectrepo.Repository,
	record snapshot.Record,
) (Checkpoint, error) {
	if err := validateSnapshotClosure(ctx, repository, record); err != nil {
		return Checkpoint{}, err
	}
	if err := validateIncomingSnapshot(
		ctx,
		repository,
		record,
		record.Objects,
	); err != nil {
		return Checkpoint{}, err
	}
	topologyManifestID, fileManifestID, err :=
		snapshotReferencedManifestIDs(ctx, repository, record)
	if err != nil {
		return Checkpoint{}, err
	}
	manifestIDs := []objectrepo.ManifestID{
		record.ManifestID,
		record.SealID,
		topologyManifestID,
		fileManifestID,
	}
	fileHead, err := repository.GetManifest(ctx, fileManifestID)
	if err != nil {
		return Checkpoint{}, err
	}
	var fileHeadState struct {
		HistoryRoot objectrepo.ManifestID `json:"historyRoot"`
	}
	if err := json.Unmarshal(
		fileHead.Payload,
		&fileHeadState,
	); err != nil {
		return Checkpoint{}, ErrVerificationInvalid
	}
	historyObjects := []objectrepo.ObjectID{}
	if fileHeadState.HistoryRoot != "" {
		history, err := repository.GetManifest(
			ctx,
			fileHeadState.HistoryRoot,
		)
		if err != nil || history.Name != "filehistory-root" {
			return Checkpoint{}, ErrVerificationInvalid
		}
		manifestIDs = append(manifestIDs, fileHeadState.HistoryRoot)
		var root struct {
			FormatVersion int    `json:"formatVersion"`
			WorkspaceID   string `json:"workspaceId"`
			Documents     []struct {
				Revisions []struct {
					ObjectID objectrepo.ObjectID `json:"objectId"`
				} `json:"revisions"`
			} `json:"documents"`
		}
		if err := json.Unmarshal(history.Payload, &root); err != nil ||
			root.FormatVersion != 2 ||
			root.WorkspaceID != record.WorkspaceID {
			return Checkpoint{}, ErrVerificationInvalid
		}
		for _, document := range root.Documents {
			for _, revision := range document.Revisions {
				if revision.ObjectID == "" {
					return Checkpoint{}, ErrVerificationInvalid
				}
				historyObjects = append(
					historyObjects,
					revision.ObjectID,
				)
			}
		}
	}
	seen := make(map[objectrepo.ManifestID]struct{}, len(manifestIDs))
	manifests := make(
		[]objectrepo.ManifestRecord,
		0,
		len(manifestIDs),
	)
	for _, id := range manifestIDs {
		if _, exists := seen[id]; exists {
			continue
		}
		stored, err := repository.GetManifest(ctx, id)
		if err != nil {
			return Checkpoint{}, err
		}
		if err := objectrepo.VerifyManifestRecord(stored); err != nil {
			return Checkpoint{}, err
		}
		seen[id] = struct{}{}
		manifests = append(manifests, stored)
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].ID < manifests[j].ID
	})
	roots := unionRoots(record.Objects, historyObjects)
	report, err := repository.Verify(ctx, roots)
	if err != nil || !report.Valid {
		return Checkpoint{}, ErrVerificationInvalid
	}
	return Checkpoint{
		WorkspaceID:     identity.WorkspaceID,
		ReplicaID:       identity.ReplicaID,
		SnapshotID:      record.SnapshotID,
		CatalogRevision: record.CatalogRevision,
		FenceEpoch:      record.FenceEpoch,
		ClaimID:         record.ClaimID,
		Roots:           roots,
		RootDigest:      rootsDigest(roots),
		Snapshot:        record,
		Manifests:       manifests,
	}, nil
}

func validateSnapshotClosure(
	ctx context.Context,
	repository objectrepo.Repository,
	record snapshot.Record,
) error {
	objects := make(
		map[objectrepo.ObjectID]struct{},
		len(record.Objects),
	)
	for _, id := range record.Objects {
		objects[id] = struct{}{}
	}
	for _, id := range record.ObjectMap {
		if _, exists := objects[id]; !exists {
			return ErrVerificationInvalid
		}
	}
	fileStateRoot := record.ObjectMap["file-state-root"]
	if fileStateRoot == "" {
		// Legacy/internal test records have no typed root map. Their complete
		// flat root set is still independently verified by the remote.
		return nil
	}
	reader, err := repository.Open(ctx, fileStateRoot)
	if err != nil {
		return err
	}
	defer reader.Close()
	decoder := json.NewDecoder(io.LimitReader(reader, 64<<20))
	decoder.DisallowUnknownFields()
	var state struct {
		FormatVersion uint64                         `json:"formatVersion"`
		SourceRoot    string                         `json:"sourceRoot"`
		Files         map[string]objectrepo.ObjectID `json:"files"`
		Attachments   map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
	}
	if err := decoder.Decode(&state); err != nil ||
		decoder.Decode(&struct{}{}) != io.EOF ||
		state.FormatVersion != 1 ||
		state.Files == nil {
		return ErrVerificationInvalid
	}
	for _, child := range state.Files {
		if _, exists := objects[child]; !exists {
			return ErrVerificationInvalid
		}
	}
	for _, child := range state.Attachments {
		if _, exists := objects[child]; !exists {
			return ErrVerificationInvalid
		}
	}
	return nil
}

func snapshotReferencedManifestIDs(
	ctx context.Context,
	repository objectrepo.Repository,
	record snapshot.Record,
) (objectrepo.ManifestID, objectrepo.ManifestID, error) {
	topologyRoot := record.ObjectMap["topology-root"]
	fileStateRoot := record.ObjectMap["file-state-root"]
	if topologyRoot == "" || fileStateRoot == "" {
		return "", "", ErrVerificationInvalid
	}
	topologyReader, err := repository.Open(ctx, topologyRoot)
	if err != nil {
		return "", "", err
	}
	var topology struct {
		ManifestID objectrepo.ManifestID `json:"manifestId"`
	}
	err = decodeReplicaObject(topologyReader, &topology)
	if err != nil || topology.ManifestID == "" {
		return "", "", ErrVerificationInvalid
	}
	fileReader, err := repository.Open(ctx, fileStateRoot)
	if err != nil {
		return "", "", err
	}
	var files struct {
		FormatVersion uint64                         `json:"formatVersion"`
		SourceRoot    objectrepo.ManifestID          `json:"sourceRoot"`
		Files         map[string]objectrepo.ObjectID `json:"files"`
		Attachments   map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
	}
	err = decodeReplicaObject(fileReader, &files)
	if err != nil ||
		files.FormatVersion != 1 ||
		files.SourceRoot == "" ||
		files.Files == nil {
		return "", "", ErrVerificationInvalid
	}
	return topology.ManifestID, files.SourceRoot, nil
}

func decodeReplicaObject(reader io.ReadCloser, target any) error {
	defer reader.Close()
	decoder := json.NewDecoder(io.LimitReader(reader, 64<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrVerificationInvalid
	}
	return nil
}

func rootsDigest(roots []objectrepo.ObjectID) string {
	hash := sha256.New()
	for _, root := range roots {
		_, _ = hash.Write([]byte(root))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func sortedRoots(roots []objectrepo.ObjectID) []objectrepo.ObjectID {
	result := append([]objectrepo.ObjectID(nil), roots...)
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	return result
}

func unionRoots(
	groups ...[]objectrepo.ObjectID,
) []objectrepo.ObjectID {
	seen := map[objectrepo.ObjectID]struct{}{}
	for _, group := range groups {
		for _, root := range group {
			if root != "" {
				seen[root] = struct{}{}
			}
		}
	}
	result := make([]objectrepo.ObjectID, 0, len(seen))
	for root := range seen {
		result = append(result, root)
	}
	return sortedRoots(result)
}

func uniqueStringsReplica(values []string) []string {
	seen := map[string]struct{}{}
	var result []string
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validateIncomingSnapshot(
	ctx context.Context,
	repository objectrepo.Repository,
	record snapshot.Record,
	roots []objectrepo.ObjectID,
) error {
	if record.SnapshotID == "" ||
		record.WorkspaceID == "" ||
		record.ManifestID == "" ||
		record.SealID == "" ||
		record.SnapshotSequence == 0 ||
		record.CatalogRevision == 0 {
		return ErrVerificationInvalid
	}
	rootSet := make(map[objectrepo.ObjectID]struct{}, len(roots))
	for _, root := range roots {
		rootSet[root] = struct{}{}
	}
	for _, root := range record.Objects {
		if _, exists := rootSet[root]; !exists {
			return ErrVerificationInvalid
		}
	}
	report, err := repository.Verify(ctx, roots)
	if err != nil {
		return err
	}
	if !report.Valid {
		return ErrVerificationInvalid
	}
	if err := snapshot.ValidateSnapshotBundle(ctx, repository, record); err != nil {
		if errors.Is(err, snapshot.ErrBundleInvalid) {
			return ErrVerificationInvalid
		}
		return err
	}
	return nil
}

func validateReplicationReceipt(
	checkpoint Checkpoint,
	receipt ReplicationReceipt,
) error {
	if receipt.WorkspaceID != checkpoint.WorkspaceID ||
		receipt.ReplicaID != checkpoint.ReplicaID ||
		receipt.SnapshotID != checkpoint.SnapshotID ||
		receipt.CatalogRevision != checkpoint.CatalogRevision ||
		receipt.RootDigest != checkpoint.RootDigest ||
		strings.TrimSpace(receipt.CheckpointID) == "" ||
		receipt.CommittedAt.IsZero() {
		return ErrVerificationInvalid
	}
	return nil
}

func validateVerificationReceipt(
	checkpoint Checkpoint,
	replication ReplicationReceipt,
	verification VerificationReceipt,
) error {
	if verification.WorkspaceID != checkpoint.WorkspaceID ||
		verification.ReplicaID != checkpoint.ReplicaID ||
		verification.SnapshotID != checkpoint.SnapshotID ||
		verification.CatalogRevision != checkpoint.CatalogRevision ||
		verification.CheckpointID != replication.CheckpointID ||
		verification.RootDigest != checkpoint.RootDigest ||
		!verification.Reopened ||
		!verification.AllRootsReadable ||
		verification.VerifiedAt.IsZero() {
		return ErrVerificationInvalid
	}
	return nil
}

func deterministicTaskID(
	replicaID string,
	snapshotID string,
	catalogRevision uint64,
) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%d",
		replicaID, snapshotID, catalogRevision,
	)))
	return hex.EncodeToString(sum[:16])
}

type productionState struct {
	db *sql.DB
}

func openProductionState(path string) (*productionState, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("replica.state_path_required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		CREATE TABLE IF NOT EXISTS replica_pending (
			task_id TEXT PRIMARY KEY,
			pin_id TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS replica_verified (
			task_id TEXT NOT NULL UNIQUE,
			snapshot_id TEXT NOT NULL,
			catalog_revision INTEGER NOT NULL,
			verified_at INTEGER NOT NULL,
			replication_json BLOB NOT NULL,
			verification_json BLOB NOT NULL,
			PRIMARY KEY(snapshot_id, catalog_revision)
		);
		CREATE TABLE IF NOT EXISTS replica_takeover (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			state TEXT NOT NULL CHECK(state IN ('prepared','completed')),
			previous_json BLOB NOT NULL,
			claim_json BLOB NOT NULL,
			operation_receipt_json BLOB
		);
		CREATE TABLE IF NOT EXISTS replica_operation_receipts (
			operation_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			method TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json BLOB NOT NULL
		);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &productionState{db: db}, nil
}

func (state *productionState) addPending(
	ctx context.Context, taskID string, pinID string,
) error {
	_, err := state.db.ExecContext(ctx, `
		INSERT INTO replica_pending(task_id, pin_id) VALUES (?, ?)
		ON CONFLICT(task_id) DO UPDATE SET pin_id = excluded.pin_id
		WHERE replica_pending.pin_id = excluded.pin_id`,
		taskID, pinID,
	)
	return err
}

func (state *productionState) pending(
	ctx context.Context, taskID string,
) (bool, error) {
	var count int
	err := state.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM replica_pending WHERE task_id = ?`,
		taskID,
	).Scan(&count)
	return count == 1, err
}

func (state *productionState) pendingPin(
	ctx context.Context, taskID string,
) (string, bool, error) {
	var pin string
	err := state.db.QueryRowContext(
		ctx,
		`SELECT pin_id FROM replica_pending WHERE task_id = ?`,
		taskID,
	).Scan(&pin)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return pin, err == nil, err
}

func (state *productionState) clearPending(
	ctx context.Context, taskID string,
) error {
	_, err := state.db.ExecContext(
		ctx,
		`DELETE FROM replica_pending WHERE task_id = ?`,
		taskID,
	)
	return err
}

func (state *productionState) isVerified(
	ctx context.Context,
	snapshotID string,
	catalogRevision uint64,
) (bool, error) {
	var count int
	err := state.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM replica_verified
		WHERE snapshot_id = ? AND catalog_revision = ?`,
		snapshotID, catalogRevision,
	).Scan(&count)
	return count == 1, err
}

func (state *productionState) markVerified(
	ctx context.Context,
	taskID string,
	record snapshot.Record,
	replication ReplicationReceipt,
	verification VerificationReceipt,
) error {
	replicationRaw, err := json.Marshal(replication)
	if err != nil {
		return err
	}
	verificationRaw, err := json.Marshal(verification)
	if err != nil {
		return err
	}
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO replica_verified(
			task_id, snapshot_id, catalog_revision, verified_at,
			replication_json, verification_json
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(snapshot_id, catalog_revision) DO UPDATE SET
			task_id = excluded.task_id,
			verified_at = excluded.verified_at,
			replication_json = excluded.replication_json,
			verification_json = excluded.verification_json`,
		taskID,
		record.SnapshotID,
		record.CatalogRevision,
		verification.VerifiedAt.UnixNano(),
		replicationRaw,
		verificationRaw,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (state *productionState) verifiedPendingPins(
	ctx context.Context,
) (map[string]string, error) {
	rows, err := state.db.QueryContext(ctx, `
		SELECT pending.task_id, pending.pin_id
		FROM replica_pending AS pending
		INNER JOIN replica_verified AS verified
			ON verified.task_id = pending.task_id
		ORDER BY pending.task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var taskID, pinID string
		if err := rows.Scan(&taskID, &pinID); err != nil {
			return nil, err
		}
		result[taskID] = pinID
	}
	return result, rows.Err()
}

func (state *productionState) lastVerified(
	ctx context.Context,
) (time.Time, bool, error) {
	var value sql.NullInt64
	err := state.db.QueryRowContext(
		ctx,
		`SELECT MAX(verified_at) FROM replica_verified`,
	).Scan(&value)
	if err != nil {
		return time.Time{}, false, err
	}
	if !value.Valid || value.Int64 == 0 {
		return time.Time{}, false, nil
	}
	return time.Unix(0, value.Int64).UTC(), true, nil
}

func (state *productionState) prepareTakeover(
	ctx context.Context,
	previous AuthorityState,
	claim Claim,
	receipt protocolv2.OperationReceipt,
) error {
	previousRaw, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	claimRaw, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	var receiptRaw any
	if receipt.OperationID != "" {
		encoded, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		receiptRaw = encoded
	}
	result, err := state.db.ExecContext(ctx, `
		INSERT INTO replica_takeover(
			singleton, state, previous_json, claim_json,
			operation_receipt_json
		) VALUES (1, 'prepared', ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			state = 'prepared',
			previous_json = excluded.previous_json,
			claim_json = excluded.claim_json,
			operation_receipt_json =
				excluded.operation_receipt_json
		WHERE replica_takeover.state = 'completed'`,
		previousRaw, claimRaw, receiptRaw,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	existingPrevious, existingClaim, existingState, found, err :=
		state.loadTakeover(ctx)
	if err != nil {
		return err
	}
	if !found || existingState != "prepared" ||
		existingPrevious != previous ||
		!sameLeaseIdentity(existingClaim, claim) {
		return ErrStaleClaim
	}
	return nil
}

func (state *productionState) completeTakeover(
	ctx context.Context,
	claim Claim,
) error {
	raw, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE replica_takeover SET state = 'completed'
		WHERE singleton = 1 AND state = 'prepared' AND claim_json = ?`,
		raw,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrLocalAuthorityUnchanged
	}
	var receiptRaw []byte
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(operation_receipt_json, X'')
		FROM replica_takeover WHERE singleton = 1`,
	).Scan(&receiptRaw)
	if err != nil {
		return err
	}
	if len(receiptRaw) > 0 {
		var receipt protocolv2.OperationReceipt
		if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO replica_operation_receipts(
				operation_id, workspace_id, method, scope,
				request_hash, result_json
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(operation_id) DO NOTHING`,
			receipt.OperationID,
			receipt.WorkspaceID,
			receipt.Method,
			string(receipt.Scope),
			receipt.RequestHash,
			[]byte(receipt.Result),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (state *productionState) loadTakeover(
	ctx context.Context,
) (AuthorityState, Claim, string, bool, error) {
	var previousRaw, claimRaw []byte
	var status string
	err := state.db.QueryRowContext(ctx, `
		SELECT previous_json, claim_json, state
		FROM replica_takeover WHERE singleton = 1`,
	).Scan(&previousRaw, &claimRaw, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorityState{}, Claim{}, "", false, nil
	}
	if err != nil {
		return AuthorityState{}, Claim{}, "", false, err
	}
	var previous AuthorityState
	var claim Claim
	if err := json.Unmarshal(previousRaw, &previous); err != nil {
		return AuthorityState{}, Claim{}, "", false, err
	}
	if err := json.Unmarshal(claimRaw, &claim); err != nil {
		return AuthorityState{}, Claim{}, "", false, err
	}
	return previous, claim, status, true, nil
}

func (state *productionState) storeOperationReceipt(
	ctx context.Context,
	receipt protocolv2.OperationReceipt,
) error {
	_, err := state.db.ExecContext(ctx, `
		INSERT INTO replica_operation_receipts(
			operation_id, workspace_id, method, scope,
			request_hash, result_json
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(operation_id) DO NOTHING`,
		receipt.OperationID,
		receipt.WorkspaceID,
		receipt.Method,
		string(receipt.Scope),
		receipt.RequestHash,
		[]byte(receipt.Result),
	)
	if err != nil {
		return err
	}
	existing, found, err := state.loadOperationReceipt(
		ctx, receipt.WorkspaceID, receipt.OperationID,
	)
	if err != nil {
		return err
	}
	if !found ||
		existing.Method != receipt.Method ||
		existing.Scope != receipt.Scope ||
		existing.RequestHash != receipt.RequestHash ||
		string(existing.Result) != string(receipt.Result) {
		return protocolv2.ErrOperationConflict
	}
	return nil
}

func (state *productionState) loadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	var receipt protocolv2.OperationReceipt
	var scope string
	var result []byte
	err := state.db.QueryRowContext(ctx, `
		SELECT operation_id, workspace_id, method, scope,
		       request_hash, result_json
		FROM replica_operation_receipts
		WHERE workspace_id = ? AND operation_id = ?`,
		workspaceID, operationID,
	).Scan(
		&receipt.OperationID,
		&receipt.WorkspaceID,
		&receipt.Method,
		&scope,
		&receipt.RequestHash,
		&result,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocolv2.OperationReceipt{}, false, nil
	}
	if err != nil {
		return protocolv2.OperationReceipt{}, false, err
	}
	receipt.Scope = protocolv2.ScopeKind(scope)
	receipt.Result = append(json.RawMessage(nil), result...)
	return receipt, true, nil
}

func (state *productionState) close() error {
	if state == nil || state.db == nil {
		return nil
	}
	_, checkpointErr := state.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := state.db.Close()
	state.db = nil
	return errors.Join(checkpointErr, closeErr)
}
