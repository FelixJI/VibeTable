package replica

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
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
		CheckpointID:    "checkpoint-1",
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
	commit, err := repository.Commit(
		context.Background(),
		objectrepo.CommitRequest{
			Authority: authorityValue,
			Objects: []objectrepo.ObjectInput{{
				Name:    "root",
				Content: []byte("durable root"),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	root := commit.Objects["root"]
	directory := t.TempDir()
	options := ManagerOptions{
		WorkspaceID:     workspaceID,
		DeviceID:        "33333333-3333-4333-8333-333333333333",
		QueuePath:       filepath.Join(directory, "queue.db"),
		StatePath:       filepath.Join(directory, "state.db"),
		PublicationPath: filepath.Join(directory, "publications.db"),
		PublicationKey:  publicationKey(),
		Remote:          remote,
		Catalog: productionCatalog{records: []snapshot.Record{{
			SnapshotID:       "44444444-4444-4444-8444-444444444444",
			WorkspaceID:      workspaceID,
			CatalogRevision:  7,
			SnapshotSequence: 7,
			FenceEpoch:       1,
			ClaimID:          claimID,
			Objects:          []objectrepo.ObjectID{root},
		}}},
		Repository: repository,
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
	root objectrepo.ObjectID,
) snapshot.Record {
	t.Helper()
	snapshotID := "55555555-5555-4555-8555-555555555555"
	manifest := snapshot.Manifest{
		FormatVersion:    2,
		SnapshotID:       snapshotID,
		WorkspaceID:      authority.WorkspaceID,
		FenceEpoch:       authority.FenceEpoch,
		ClaimID:          authority.ClaimID,
		MutationRevision: 8,
		SnapshotSequence: 8,
		Trigger:          snapshot.TriggerAutomatic,
		CreatedAt: time.Date(
			2026, 7, 28, 0, 3, 0, 0, time.UTC,
		),
		CreatedByDevice:           "33333333-3333-4333-8333-333333333333",
		BusinessDatabaseObjectID:  root,
		TopologyRootObjectID:      root,
		FileStateRootObjectID:     root,
		WorkspaceSettingsObjectID: root,
		AuditPrefixObjectID:       root,
		MinimumAppVersion:         "0.1.0",
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(manifestRaw)
	sealRaw, err := json.Marshal(snapshot.Seal{
		FormatVersion: 2,
		SnapshotID:    snapshotID,
		ManifestHash: "sha256:" +
			hex.EncodeToString(digest[:]),
		RepositoryFormat: "kopia-v3",
		FenceEpoch:       authority.FenceEpoch,
		ClaimID:          authority.ClaimID,
		MutationRevision: 8,
		SnapshotSequence: 8,
		Verified:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := repository.Commit(
		context.Background(),
		objectrepo.CommitRequest{
			Authority: authority,
			Manifests: []objectrepo.ManifestInput{
				{
					Name:    "snapshot",
					Labels:  map[string]string{"type": "snapshot"},
					Payload: manifestRaw,
				},
				{
					Name: "snapshot-seal",
					Labels: map[string]string{
						"type": "snapshot-seal",
					},
					Payload: sealRaw,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Record{
		SnapshotID:       snapshotID,
		WorkspaceID:      authority.WorkspaceID,
		ManifestID:       receipt.Manifests["snapshot"],
		SealID:           receipt.Manifests["snapshot-seal"],
		SnapshotSequence: 8,
		FenceEpoch:       authority.FenceEpoch,
		ClaimID:          authority.ClaimID,
		MutationRevision: 8,
		Trigger:          snapshot.TriggerAutomatic,
		CreatedAt:        manifest.CreatedAt,
		Objects:          []objectrepo.ObjectID{root},
		ObjectMap: map[string]objectrepo.ObjectID{
			"file-state-root": root,
		},
		CatalogRevision: 8,
	}
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
		remoteRoot,
	)
	localSnapshot := options.Catalog.(productionCatalog).records[0]
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
		Roots:           []objectrepo.ObjectID{remoteRoot},
		Verification: VerificationReceipt{
			WorkspaceID:     claim.WorkspaceID,
			ReplicaID:       remote.identity.ReplicaID,
			SnapshotID:      replicaRecord.SnapshotID,
			CatalogRevision: replicaRecord.CatalogRevision,
			CheckpointID:    "incoming-checkpoint",
			RootDigest: rootsDigest(
				[]objectrepo.ObjectID{remoteRoot},
			),
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
