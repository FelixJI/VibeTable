package filehistory

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

var errInjectedFinishMutation = errors.New("injected finish mutation failure")

type committedPendingFixture struct {
	repository      *objectrepo.MemoryRepository
	coordinatorPath string
	headPath        string
	token           writecoordinator.Token
}

func prepareCommittedPendingMutation(t *testing.T) committedPendingFixture {
	t.Helper()
	ctx := context.Background()
	coordinatorPath := historySQLitePath(t, "coordinator.db")
	headPath := historySQLitePath(t, "head.db")
	coordinator, err := writecoordinator.OpenPersistent(
		coordinatorPath, testWorkspaceID, 1, "claim-a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(ctx, nil, token.Authority()); err != nil {
		t.Fatal(err)
	}
	headStore, err := OpenPersistentHeadStore(headPath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenCurrent(
		ctx, repository, coordinator, headStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	coordinator.WithPersistenceFaultInjector(
		func(point writecoordinator.PersistenceFaultPoint) error {
			if point == writecoordinator.FaultBeforeFinishCommittedMutation &&
				!failed {
				failed = true
				return errInjectedFinishMutation
			}
			return nil
		},
	)
	_, err = service.Save(ctx, SaveRequest{
		Token:      token,
		DocumentID: testDocumentOne,
		Path:       "recovered.txt",
		Kind:       RevisionFormal,
		Content:    []byte("durable before coordinator finish"),
		MimeType:   "text/plain",
		CreatedBy:  "test-user",
		DeviceID:   testDeviceID,
	})
	if !errors.Is(err, errInjectedFinishMutation) {
		t.Fatalf("save error = %v", err)
	}
	if service.Root() != "" || len(service.List()) != 0 {
		t.Fatalf(
			"failed write installed memory state: root=%s documents=%d",
			service.Root(),
			len(service.List()),
		)
	}
	head, found, err := headStore.Load(ctx, token.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !found ||
		head.Root == "" ||
		head.Revision != 1 ||
		head.MutationRevision != 1 ||
		head.SessionEpoch != token.SessionEpoch ||
		head.FenceEpoch != token.FenceEpoch ||
		head.ClaimID != token.ClaimID {
		t.Fatalf("published head = %#v found=%v", head, found)
	}
	recovery := coordinator.RecoveryState()
	if recovery.PendingMutationRevision != 1 ||
		recovery.Counters.MutationRevision != 0 {
		t.Fatalf("in-process recovery state = %#v", recovery)
	}
	_, err = service.Save(ctx, SaveRequest{
		Token:      token,
		DocumentID: testDocumentTwo,
		Path:       "blocked.txt",
		Kind:       RevisionFormal,
		Content:    []byte("must remain blocked"),
		MimeType:   "text/plain",
		CreatedBy:  "test-user",
		DeviceID:   testDeviceID,
	})
	if !errors.Is(err, writecoordinator.ErrRecoveryRequired) {
		t.Fatalf("write after unresolved finish error = %v", err)
	}
	if err := headStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	return committedPendingFixture{
		repository:      repository,
		coordinatorPath: coordinatorPath,
		headPath:        headPath,
		token:           token,
	}
}

func TestOpenCurrentRecoversExactlyBoundCommittedMutation(t *testing.T) {
	fixture := prepareCommittedPendingMutation(t)
	ctx := context.Background()
	coordinator, err := writecoordinator.OpenPersistent(
		fixture.coordinatorPath,
		testWorkspaceID,
		fixture.token.FenceEpoch,
		fixture.token.ClaimID,
		fixture.token.SessionEpoch,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	before := coordinator.RecoveryState()
	if before.PendingMutationRevision != 1 ||
		before.Counters.MutationRevision != 0 {
		t.Fatalf("reopened recovery state = %#v", before)
	}
	headStore, err := OpenPersistentHeadStore(fixture.headPath)
	if err != nil {
		t.Fatal(err)
	}
	defer headStore.Close()
	service, err := OpenCurrent(
		ctx, fixture.repository, coordinator, headStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := service.Inspect(testDocumentOne)
	if err != nil {
		t.Fatal(err)
	}
	if document.RelativePath != "recovered.txt" ||
		len(document.Revisions) != 1 {
		t.Fatalf("recovered document = %#v", document)
	}
	after := coordinator.RecoveryState()
	if after.PendingMutationRevision != 0 ||
		after.Counters.MutationRevision != 1 {
		t.Fatalf("resolved recovery state = %#v", after)
	}
}

func TestOpenCurrentRejectsUnboundPreparedMutation(t *testing.T) {
	tests := []struct {
		name   string
		column string
		value  any
	}{
		{name: "mutation revision", column: "mutation_revision", value: 2},
		{name: "session epoch", column: "session_epoch", value: 2},
		{name: "fence epoch", column: "fence_epoch", value: 2},
		{name: "claim id", column: "claim_id", value: "claim-b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareCommittedPendingMutation(t)
			tamperedStore, err := OpenPersistentHeadStore(fixture.headPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tamperedStore.db.Exec(
				"UPDATE filehistory_heads SET "+test.column+" = ?",
				test.value,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := tamperedStore.Close(); err != nil {
				t.Fatal(err)
			}

			coordinator, err := writecoordinator.OpenPersistent(
				fixture.coordinatorPath,
				testWorkspaceID,
				fixture.token.FenceEpoch,
				fixture.token.ClaimID,
				fixture.token.SessionEpoch,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer coordinator.Close()
			headStore, err := OpenPersistentHeadStore(fixture.headPath)
			if err != nil {
				t.Fatal(err)
			}
			defer headStore.Close()
			_, err = OpenCurrent(
				context.Background(),
				fixture.repository,
				coordinator,
				headStore,
			)
			if !errors.Is(err, ErrHeadRecoveryUnproven) ||
				!errors.Is(err, writecoordinator.ErrRecoveryRequired) {
				t.Fatalf("OpenCurrent error = %v", err)
			}
			recovery := coordinator.RecoveryState()
			if recovery.PendingMutationRevision != 1 ||
				recovery.Counters.MutationRevision != 0 {
				t.Fatalf("recovery was mutated = %#v", recovery)
			}
		})
	}
}
