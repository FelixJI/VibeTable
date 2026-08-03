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

type productionProvisionalAcceptor struct {
	records []snapshot.Record
	err     error
}

func (acceptor *productionProvisionalAcceptor) AcceptProvisionalPublication(
	_ context.Context,
	record snapshot.Record,
) error {
	acceptor.records = append(acceptor.records, record)
	return acceptor.err
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
	database := strictSnapshotDatabase(t, "durable root")
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
		WorkspaceID: workspaceID,
		DeviceID:    "33333333-3333-4333-8333-333333333333",
		QueuePath:   filepath.Join(directory, "queue.db"),
		StatePath:   filepath.Join(directory, "state.db"),
		Remote:      remote,
		Catalog:     productionCatalog{records: []snapshot.Record{strictRecord}},
		Repository:  repository,
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
		strictSnapshotDatabase(t, "incoming durable root"),
		nil,
	)
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
	acceptor := &productionProvisionalAcceptor{}
	options.ProvisionalAcceptor = acceptor
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
	if len(acceptor.records) != 1 ||
		acceptor.records[0].SnapshotID !=
			options.Catalog.(productionCatalog).records[0].SnapshotID {
		t.Fatalf("sole advisory head was not accepted: %#v", acceptor.records)
	}
}

func TestReadPersistedTakeoverClaimRestoresOfflineModeAndRejectsTampering(
	t *testing.T,
) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "replica-state.db")
	if claim, found, err := ReadPersistedTakeoverClaim(
		ctx, path,
	); err != nil || found || claim != (Claim{}) {
		t.Fatalf("missing state claim = %#v, %t, %v", claim, found, err)
	}
	state, err := openProductionState(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	claim := testClaim(
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"nonce",
		Advisory,
		now,
	)
	claim.Mode = Provisional
	claim.FenceEpoch = 4
	previous := AuthorityState{
		WorkspaceID: claim.WorkspaceID,
		FenceEpoch:  3,
		ClaimID:     "44444444-4444-4444-8444-444444444444",
	}
	if err := state.prepareTakeover(
		ctx, previous, claim, protocolv2.OperationReceipt{},
	); err != nil {
		t.Fatal(err)
	}
	if err := state.completeTakeover(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadPersistedTakeoverClaim(ctx, path)
	if err != nil || !found || !sameClaimIdentity(got, claim) {
		t.Fatalf("persisted claim = %#v, %t, %v", got, found, err)
	}

	state, err = openProductionState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.Exec(`
		UPDATE replica_takeover
		SET claim_json = '{"unknown":true}'
		WHERE singleton = 1`,
	); err != nil {
		t.Fatal(err)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPersistedTakeoverClaim(
		ctx, path,
	); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("tampered claim error = %v", err)
	}
}

func TestProductionSyncFailsClosedOnInvalidIndependentVerification(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	remote := &productionRemote{
		identity: RemoteIdentity{
			WorkspaceID: "11111111-1111-4111-8111-111111111111",
			ReplicaID:   "replica-a",
			Strength:    Advisory,
		},
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
	claim := Claim{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		ClaimID:     "22222222-2222-4222-8222-222222222222",
		FenceEpoch:  1,
	}
	remote := &productionRemote{
		identity: RemoteIdentity{
			WorkspaceID: claim.WorkspaceID,
			ReplicaID:   "replica-a",
			Strength:    Advisory,
		},
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
