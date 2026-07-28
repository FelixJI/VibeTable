package replica

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

type productionCatalog struct {
	records []snapshot.Record
}

func (catalog productionCatalog) List(
	_ context.Context,
	workspaceID string,
) ([]snapshot.Record, error) {
	var result []snapshot.Record
	for _, record := range catalog.records {
		if record.WorkspaceID == workspaceID {
			result = append(result, record)
		}
	}
	return result, nil
}

type productionAuthority struct {
	mu         sync.Mutex
	value      AuthorityState
	repository objectrepo.Repository
}

func (authority *productionAuthority) CurrentAuthority() AuthorityState {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.value
}

func (authority *productionAuthority) ApplyReplicaClaim(
	ctx context.Context,
	claim Claim,
) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	previous := objectrepo.Authority{
		WorkspaceID: authority.value.WorkspaceID,
		FenceEpoch:  authority.value.FenceEpoch,
		ClaimID:     authority.value.ClaimID,
	}
	next := objectrepo.Authority{
		WorkspaceID: claim.WorkspaceID,
		FenceEpoch:  claim.FenceEpoch,
		ClaimID:     claim.ClaimID,
	}
	if err := authority.repository.AcceptAuthority(
		ctx, &previous, next,
	); err != nil {
		return err
	}
	authority.value = AuthorityState{
		WorkspaceID: claim.WorkspaceID,
		FenceEpoch:  claim.FenceEpoch,
		ClaimID:     claim.ClaimID,
	}
	return nil
}

type productionRemote struct {
	mu            sync.Mutex
	identity      RemoteIdentity
	store         LeaseCASStore
	identityErr   error
	verifyInvalid bool
	replicateErr  error
	publications  []Publication
	replications  int
	incoming      []IncomingConflict
}

func (remote *productionRemote) VerifyIdentity(
	context.Context,
) (RemoteIdentity, error) {
	if remote.identityErr != nil {
		return RemoteIdentity{}, remote.identityErr
	}
	return remote.identity, nil
}

func (remote *productionRemote) LeaseStore() LeaseCASStore {
	return remote.store
}

func (remote *productionRemote) ReplicateCheckpoint(
	_ context.Context,
	checkpoint Checkpoint,
) (ReplicationReceipt, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.replications++
	if remote.replicateErr != nil {
		return ReplicationReceipt{}, remote.replicateErr
	}
	return ReplicationReceipt{
		WorkspaceID:     checkpoint.WorkspaceID,
		ReplicaID:       checkpoint.ReplicaID,
		SnapshotID:      checkpoint.SnapshotID,
		CatalogRevision: checkpoint.CatalogRevision,
		CheckpointID:    "sha256:" + strings.Repeat("c", 64),
		RootDigest:      checkpoint.RootDigest,
		CommittedAt: time.Date(
			2026, 7, 28, 0, 1, 0, 0, time.UTC,
		),
	}, nil
}

func (remote *productionRemote) ReopenAndVerifyRoots(
	_ context.Context,
	checkpoint Checkpoint,
	replication ReplicationReceipt,
) (VerificationReceipt, error) {
	receipt := VerificationReceipt{
		WorkspaceID:      checkpoint.WorkspaceID,
		ReplicaID:        checkpoint.ReplicaID,
		SnapshotID:       checkpoint.SnapshotID,
		CatalogRevision:  checkpoint.CatalogRevision,
		CheckpointID:     replication.CheckpointID,
		RootDigest:       checkpoint.RootDigest,
		Reopened:         true,
		AllRootsReadable: true,
		VerifiedAt: time.Date(
			2026, 7, 28, 0, 2, 0, 0, time.UTC,
		),
	}
	if remote.verifyInvalid {
		receipt.AllRootsReadable = false
	}
	return receipt, nil
}

func (remote *productionRemote) AppendPublication(
	_ context.Context,
	publication Publication,
) error {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	for _, existing := range remote.publications {
		if existing.PublicationID == publication.PublicationID {
			if existing.CanonicalHash == publication.CanonicalHash {
				return nil
			}
			return ErrPublicationExists
		}
	}
	remote.publications = append(remote.publications, publication)
	return nil
}

func (remote *productionRemote) ListPublications(
	_ context.Context,
	workspaceID string,
) ([]Publication, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	var result []Publication
	for _, publication := range remote.publications {
		if publication.WorkspaceID == workspaceID {
			result = append(result, publication)
		}
	}
	return result, nil
}

func (remote *productionRemote) DiscoverConflicts(
	_ context.Context,
	_ ConflictScan,
) ([]IncomingConflict, error) {
	return append([]IncomingConflict(nil), remote.incoming...), nil
}

func productionManagerFixture(
	t *testing.T,
	remote *productionRemote,
	now time.Time,
) (
	ManagerOptions,
	*objectrepo.MemoryRepository,
	*objectrepo.ObjectID,
) {
	t.Helper()
	workspaceID := "11111111-1111-4111-8111-111111111111"
	claimID := "22222222-2222-4222-8222-222222222222"
	repository := objectrepo.NewMemory()
	authorityValue := objectrepo.Authority{
		WorkspaceID: workspaceID,
		FenceEpoch:  1,
		ClaimID:     claimID,
	}
	if err := repository.AcceptAuthority(
		context.Background(), nil, authorityValue,
	); err != nil {
		t.Fatal(err)
	}
	database := []byte("durable root")
	strictRecord := strictSnapshotFixture(
		t,
		repository,
		authorityValue,
		7,
		7,
		database,
		nil,
	)
	root := strictRecord.ObjectMap["database"]
	directory := t.TempDir()
	options := ManagerOptions{
		WorkspaceID:     workspaceID,
		DeviceID:        "33333333-3333-4333-8333-333333333333",
		QueuePath:       filepath.Join(directory, "queue.db"),
		StatePath:       filepath.Join(directory, "state.db"),
		PublicationPath: filepath.Join(directory, "publications.db"),
		PublicationKey:  publicationKey(),
		Remote:          remote,
		Catalog:         productionCatalog{records: []snapshot.Record{strictRecord}},
		Repository:      repository,
		Authority: &productionAuthority{
			value: AuthorityState{
				WorkspaceID: workspaceID,
				FenceEpoch:  1,
				ClaimID:     claimID,
			},
			repository: repository,
		},
		Now: func() time.Time { return now },
	}
	return options, repository, &root
}

func incomingSnapshot(
	t *testing.T,
	repository *objectrepo.MemoryRepository,
	authority objectrepo.Authority,
) snapshot.Record {
	t.Helper()
	return strictSnapshotFixture(
		t,
		repository,
		authority,
		8,
		8,
		[]byte("incoming durable root"),
		nil,
	)
}

func TestProductionStrongSyncPersistsVerifiedReceiptAndReleasesPin(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	store := newMemoryLeaseStore()
	lease, err := NewStrongLeaseWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	lease.now = func() time.Time { return now }
	claim, err := lease.Acquire(Claim{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		DeviceID:    "33333333-3333-4333-8333-333333333333",
		ClaimID:     "22222222-2222-4222-8222-222222222222",
		Nonce:       "nonce",
		Strength:    Strong,
		Mode:        Writable,
		ExpiresAt:   now.Add(time.Hour),
	}, false)
	if err != nil || claim.FenceEpoch != 1 {
		t.Fatal(err)
	}
	remote := &productionRemote{
		identity: RemoteIdentity{
			WorkspaceID: claim.WorkspaceID,
			ReplicaID:   "replica-a",
			Strength:    Strong,
		},
		store: store,
	}
	options, repository, _ := productionManagerFixture(
		t, remote, now,
	)
	manager, err := OpenManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.SyncState != "replicated" ||
		status.PendingSync ||
		status.LastVerifiedAt == nil {
		t.Fatalf("status = %#v", status)
	}
	syncState, err := manager.SnapshotSyncState(
		context.Background(),
		options.Catalog.(productionCatalog).records[0],
	)
	if err != nil || syncState != "replicated" {
		t.Fatalf("snapshot sync state = %q, %v", syncState, err)
	}
	pins, err := repository.ListPins(context.Background())
	if err != nil || len(pins) != 0 {
		t.Fatalf("pins=%#v err=%v", pins, err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.replications != 1 {
		t.Fatalf("restart duplicated replication: %d", remote.replications)
	}
}

func TestProductionStrongMirrorPausesWhenIntegrityIsCorrupt(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	store := newMemoryLeaseStore()
	lease, err := NewStrongLeaseWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	lease.now = func() time.Time { return now }
	claim, err := lease.Acquire(Claim{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		DeviceID:    "33333333-3333-4333-8333-333333333333",
		ClaimID:     "22222222-2222-4222-8222-222222222222",
		Nonce:       "nonce",
		Strength:    Strong,
		Mode:        Writable,
		ExpiresAt:   now.Add(time.Hour),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	remote := &productionRemote{
		identity: RemoteIdentity{
			WorkspaceID: claim.WorkspaceID,
			ReplicaID:   "replica-a",
			Strength:    Strong,
		},
		store: store,
	}
	options, _, _ := productionManagerFixture(t, remote, now)
	integrityErr := errors.New("retention.integrity_corrupt")
	options.DestructiveSafe = func(context.Context) error {
		return integrityErr
	}
	manager, err := OpenManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Synchronize(
		context.Background(),
	); !errors.Is(err, integrityErr) {
		t.Fatalf("Synchronize error = %v", err)
	}
	if remote.replications != 0 {
		t.Fatalf("corrupt mirror replicated %d checkpoints", remote.replications)
	}
}

func TestQueueSynchronizePersistsWithoutRunningRemoteIO(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	remote := &productionRemote{
		identity: RemoteIdentity{
			WorkspaceID: "11111111-1111-4111-8111-111111111111",
			ReplicaID:   "replica-a",
			Strength:    Advisory,
		},
	}
	options, _, _ := productionManagerFixture(t, remote, now)
	manager, err := OpenManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	receipt := protocolv2.OperationReceipt{
		OperationID: "77777777-7777-4777-8777-777777777777",
		WorkspaceID: options.WorkspaceID,
		Method:      "replica.synchronize",
		Scope:       protocolv2.WorkspaceScope,
		RequestHash: "sha256:" + strings.Repeat("a", 64),
		Result:      json.RawMessage(`{"state":"queued"}`),
	}
	if err := manager.QueueSynchronize(
		context.Background(),
		receipt,
	); err != nil {
		t.Fatal(err)
	}
	if remote.replications != 0 {
		t.Fatalf("queue call performed remote IO: %d", remote.replications)
	}
	status, err := manager.Status(context.Background())
	if err != nil || !status.PendingSync {
		t.Fatalf("queued status = %#v, %v", status, err)
	}
	if err := manager.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.replications != 1 {
		t.Fatalf("worker drain replications = %d", remote.replications)
	}
	if len(remote.publications) != 1 ||
		remote.publications[0].Claim.Mode != Provisional {
		t.Fatalf(
			"advisory publication is not provisional: %#v",
			remote.publications,
		)
	}
}

func TestProductionSyncFailsClosedOnInvalidIndependentVerification(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	store := newMemoryLeaseStore()
	lease, _ := NewStrongLeaseWithStore(store)
	lease.now = func() time.Time { return now }
	_, err := lease.Acquire(Claim{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		DeviceID:    "33333333-3333-4333-8333-333333333333",
		ClaimID:     "22222222-2222-4222-8222-222222222222",
		Nonce:       "nonce",
		Strength:    Strong,
		Mode:        Writable,
		ExpiresAt:   now.Add(time.Hour),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	remote := &productionRemote{
		identity: RemoteIdentity{
			WorkspaceID: "11111111-1111-4111-8111-111111111111",
			ReplicaID:   "replica-a",
			Strength:    Strong,
		},
		store:         store,
		verifyInvalid: true,
	}
	options, repository, _ := productionManagerFixture(t, remote, now)
	manager, err := OpenManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.PendingSync || status.SyncState != "failed" {
		t.Fatalf("status = %#v", status)
	}
	pins, err := repository.ListPins(context.Background())
	if err != nil || len(pins) != 1 ||
		pins[0].Purpose[:16] != "pending-replica:" {
		t.Fatalf("pending root not protected: %#v %v", pins, err)
	}
}

func TestProductionManagerRejectsUnverifiedOrMismatchedRemote(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	remote := &productionRemote{
		identity: RemoteIdentity{
			WorkspaceID: "other",
			ReplicaID:   "replica-a",
			Strength:    Advisory,
		},
	}
	options, _, _ := productionManagerFixture(t, remote, now)
	if _, err := OpenManager(
		context.Background(), options,
	); !errors.Is(err, ErrRemoteIdentityInvalid) {
		t.Fatalf("mismatched remote accepted: %v", err)
	}
	remote.identityErr = ErrRemoteUnavailable
	if _, err := OpenManager(
		context.Background(), options,
	); !errors.Is(err, ErrReplicationUnavailable) {
		t.Fatalf("offline remote advertised: %v", err)
	}
}

func TestProductionSyncPersistsVerifiedThreeWayCandidatesAndPinsRecovery(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	store := newMemoryLeaseStore()
	lease, _ := NewStrongLeaseWithStore(store)
	lease.now = func() time.Time { return now }
	claim, err := lease.Acquire(Claim{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		DeviceID:    "33333333-3333-4333-8333-333333333333",
		ClaimID:     "22222222-2222-4222-8222-222222222222",
		Nonce:       "nonce",
		Strength:    Strong,
		Mode:        Writable,
		ExpiresAt:   now.Add(time.Hour),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	remote := &productionRemote{
		identity: RemoteIdentity{
			WorkspaceID: claim.WorkspaceID,
			ReplicaID:   "replica-a",
			Strength:    Strong,
		},
		store: store,
	}
	options, repository, localRoot := productionManagerFixture(
		t, remote, now,
	)
	remoteObjectReceipt, err := repository.Commit(
		context.Background(),
		objectrepo.CommitRequest{
			Authority: objectrepo.Authority{
				WorkspaceID: claim.WorkspaceID,
				FenceEpoch:  claim.FenceEpoch,
				ClaimID:     claim.ClaimID,
			},
			Objects: []objectrepo.ObjectInput{{
				Name:    "remote",
				Content: []byte("remote candidate"),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := remoteObjectReceipt.Objects["remote"]
	replicaRecord := incomingSnapshot(
		t,
		repository,
		objectrepo.Authority{
			WorkspaceID: claim.WorkspaceID,
			FenceEpoch:  claim.FenceEpoch,
			ClaimID:     claim.ClaimID,
		},
	)
	localSnapshot := options.Catalog.(productionCatalog).records[0]
	replicaRoots := unionRoots(
		replicaRecord.Objects,
		[]objectrepo.ObjectID{remoteRoot},
	)
	conflictID := "66666666-6666-4666-8666-666666666666"
	documentID := "77777777-7777-4777-8777-777777777777"
	remote.incoming = []IncomingConflict{{
		Set: conflictresolution.Set{
			ConflictID:  conflictID,
			WorkspaceID: claim.WorkspaceID,
			State:       conflictresolution.StatePending,
			Revision:    1,
			Base: conflictresolution.Candidate{
				SnapshotID: localSnapshot.SnapshotID,
				Revision:   7,
				Files: map[string]conflictresolution.FileState{
					documentID: {
						DocumentID: documentID,
						Path:       "table.csv",
						ContentID:  string(*localRoot),
					},
				},
			},
			Local: conflictresolution.Candidate{
				SnapshotID: localSnapshot.SnapshotID,
				Revision:   7,
				Files: map[string]conflictresolution.FileState{
					documentID: {
						DocumentID: documentID,
						Path:       "table.csv",
						ContentID:  string(*localRoot),
					},
				},
			},
			Replica: conflictresolution.Candidate{
				SnapshotID: replicaRecord.SnapshotID,
				Revision:   8,
				Files: map[string]conflictresolution.FileState{
					documentID: {
						DocumentID: documentID,
						Path:       "table.csv",
						ContentID:  string(remoteRoot),
					},
				},
			},
			Dependencies: conflictresolution.DependencyGraph{
				Complete: true,
				Edges:    map[string][]string{documentID: {}},
			},
			CreatedAt: now,
		},
		ReplicaSnapshot: replicaRecord,
		Roots:           replicaRoots,
		Verification: VerificationReceipt{
			WorkspaceID:      claim.WorkspaceID,
			ReplicaID:        remote.identity.ReplicaID,
			SnapshotID:       replicaRecord.SnapshotID,
			CatalogRevision:  replicaRecord.CatalogRevision,
			CheckpointID:     "incoming-checkpoint",
			RootDigest:       rootsDigest(replicaRoots),
			Reopened:         true,
			AllRootsReadable: true,
			VerifiedAt:       now,
		},
	}}
	conflictPath := filepath.Join(t.TempDir(), "conflicts.db")
	engine, err := conflictresolution.OpenEngine(conflictPath)
	if err != nil {
		t.Fatal(err)
	}
	options.Conflicts = engine
	manager, err := OpenManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !manager.ConflictReady() {
		t.Fatal("verified conflict remote was not composed")
	}
	if err := manager.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	set, err := engine.Inspect(context.Background(), conflictID)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.RootPinIDs) != 1 ||
		set.Replica.SnapshotID != replicaRecord.SnapshotID {
		t.Fatalf("persisted conflict = %#v", set)
	}
	pins, err := repository.ListPins(context.Background())
	if err != nil || len(pins) != 1 ||
		pins[0].Purpose != "conflict:"+conflictID {
		t.Fatalf("recovery pin = %#v, %v", pins, err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := conflictresolution.OpenEngine(conflictPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Inspect(
		context.Background(), conflictID,
	); err != nil {
		t.Fatalf("candidate lost after restart: %v", err)
	}
}

func TestProductionTakeoverJournalRecoversEveryRemoteCASKillWindow(t *testing.T) {
	for _, afterRemoteCAS := range []bool{false, true} {
		t.Run(map[bool]string{
			false: "before-remote-cas",
			true:  "after-remote-cas",
		}[afterRemoteCAS], func(t *testing.T) {
			issued := time.Date(
				2026, 7, 28, 0, 0, 0, 0, time.UTC,
			)
			now := issued.Add(2 * time.Minute)
			store := newMemoryLeaseStore()
			lease, _ := NewStrongLeaseWithStore(store)
			lease.now = func() time.Time { return issued }
			oldClaim, err := lease.Acquire(Claim{
				WorkspaceID: "11111111-1111-4111-8111-111111111111",
				DeviceID:    "33333333-3333-4333-8333-333333333333",
				ClaimID:     "22222222-2222-4222-8222-222222222222",
				Nonce:       "old",
				Strength:    Strong,
				Mode:        Writable,
				ExpiresAt:   issued.Add(time.Minute),
			}, false)
			if err != nil {
				t.Fatal(err)
			}
			remote := &productionRemote{
				identity: RemoteIdentity{
					WorkspaceID: oldClaim.WorkspaceID,
					ReplicaID:   "replica-a",
					Strength:    Strong,
				},
				store: store,
			}
			options, _, _ := productionManagerFixture(
				t, remote, now,
			)
			next := Claim{
				WorkspaceID:     oldClaim.WorkspaceID,
				DeviceID:        "33333333-3333-4333-8333-333333333333",
				ClaimID:         "88888888-8888-4888-8888-888888888888",
				FenceEpoch:      oldClaim.FenceEpoch + 1,
				Nonce:           "next",
				Strength:        Strong,
				Mode:            Writable,
				IssuedAt:        now,
				HeartbeatAt:     now,
				ExpiresAt:       now.Add(5 * time.Minute),
				PreviousClaimID: oldClaim.ClaimID,
			}
			state, err := openProductionState(options.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			previous := options.Authority.CurrentAuthority()
			if err := state.prepareTakeover(
				context.Background(),
				previous,
				next,
				protocolv2.OperationReceipt{},
			); err != nil {
				t.Fatal(err)
			}
			if err := state.close(); err != nil {
				t.Fatal(err)
			}
			if afterRemoteCAS {
				record, found, err := store.Load(
					context.Background(), oldClaim.WorkspaceID,
				)
				if err != nil || !found {
					t.Fatal(err)
				}
				if _, err := store.CompareAndSwap(
					context.Background(),
					oldClaim.WorkspaceID,
					record.Revision,
					true,
					next,
				); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := OpenManager(
				context.Background(), options,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			got := options.Authority.CurrentAuthority()
			if got.FenceEpoch != next.FenceEpoch ||
				got.ClaimID != next.ClaimID {
				t.Fatalf("authority not recovered: %#v", got)
			}
			record, found, err := store.Load(
				context.Background(), oldClaim.WorkspaceID,
			)
			if err != nil || !found ||
				!sameLeaseIdentity(record.Claim, next) {
				t.Fatalf(
					"remote claim not recovered: %#v %v",
					record, err,
				)
			}
		})
	}
}

type productionDependencyScanner struct {
	graph conflictresolution.DependencyGraph
	err   error
}

func (scanner productionDependencyScanner) ScanConflictDependencies(
	context.Context,
	conflictresolution.Candidate,
	conflictresolution.Candidate,
	conflictresolution.Candidate,
) (conflictresolution.DependencyGraph, error) {
	return scanner.graph, scanner.err
}

func TestConflictDependencyScanFailsClosedWithoutBusinessScanner(t *testing.T) {
	manager := &Manager{}
	table := conflictresolution.TableState{
		TableID: "table-a", SchemaObjectID: "schema",
		RecordsObjectID: "records", ViewsObjectID: "views",
		AttachmentsObjectID: "attachments",
	}
	graph, err := manager.scanConflictDependencies(
		context.Background(),
		conflictresolution.Candidate{
			BusinessDatabaseObjectID: "database-base",
			Tables:                   map[string]conflictresolution.TableState{"table-a": table},
		},
		conflictresolution.Candidate{
			BusinessDatabaseObjectID: "database-local",
			Tables:                   map[string]conflictresolution.TableState{"table-a": table},
		},
		conflictresolution.Candidate{
			BusinessDatabaseObjectID: "database-replica",
			Tables:                   map[string]conflictresolution.TableState{"table-a": table},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Complete {
		t.Fatalf("missing production scanner fabricated completeness: %#v", graph)
	}
	if _, ok := graph.Edges["table-a"]; !ok {
		t.Fatalf("typed table omitted from incomplete graph: %#v", graph)
	}
}

func TestConflictDependencyScanUsesProvenRelationAutomationAndPluginClosure(t *testing.T) {
	expected := conflictresolution.DependencyGraph{
		Complete: true,
		Edges: map[string][]string{
			"table-a":           {"relation:table-b", "automation:notify", "plugin:calendar"},
			"relation:table-b":  {},
			"automation:notify": {},
			"plugin:calendar":   {},
		},
	}
	manager := &Manager{dependencyScanner: productionDependencyScanner{graph: expected}}
	graph, err := manager.scanConflictDependencies(
		context.Background(),
		conflictresolution.Candidate{},
		conflictresolution.Candidate{},
		conflictresolution.Candidate{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !graph.Complete ||
		len(graph.Edges["table-a"]) != 3 ||
		graph.Edges["table-a"][2] != "plugin:calendar" {
		t.Fatalf("scanner closure was not preserved: %#v", graph)
	}
}
