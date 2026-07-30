package filehistory

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func newPersistentConflictFixture(
	t *testing.T,
) (
	*Service,
	*objectrepo.MemoryRepository,
	*SQLiteHeadStore,
	writecoordinator.Token,
) {
	t.Helper()
	coordinator, err := writecoordinator.New(
		testWorkspaceID,
		1,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(
		context.Background(), nil, token.Authority(),
	); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPersistentHeadStore(
		historySQLitePath(t, "conflict-head.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(
		repository,
		coordinator,
		WithHeadStore(store),
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, store, token
}

func conflictOperationReceipt(
	operationID string,
	recoveryIDs []string,
) protocolv2.OperationReceipt {
	raw, _ := json.Marshal(map[string]any{
		"operationId":         operationID,
		"state":               "applied",
		"recoverySnapshotIds": recoveryIDs,
	})
	return protocolv2.OperationReceipt{
		OperationID: operationID,
		WorkspaceID: testWorkspaceID,
		Method:      "conflict.apply",
		Scope:       protocolv2.WorkspaceScope,
		RequestHash: "sha256:request",
		Result:      raw,
	}
}

func TestConflictPrepareRetryUsesStableStageIdentity(t *testing.T) {
	service, _, store, _ := newPersistentConflictFixture(t)
	defer store.Close()
	applier, err := NewConflictApplier(service, store)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "d1111111-1111-4111-8111-111111111111"
	input := ConflictStage{
		PlanID:      "d2222222-2222-4222-8222-222222222222",
		OperationID: operationID,
		External:    json.RawMessage(`{"formatVersion":1}`),
		OperationReceipt: conflictOperationReceipt(
			operationID, []string{"local", "replica"},
		),
	}
	first, err := applier.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := applier.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.StageID == "" || second.StageID != first.StageID {
		t.Fatalf("retry stage identity: first=%q second=%q",
			first.StageID, second.StageID,
		)
	}
	loaded, err := applier.LoadStage(
		context.Background(), first.StageID,
	)
	if err != nil || loaded.OperationID != operationID {
		t.Fatalf("durable stage = %#v, %v", loaded, err)
	}
}

func TestConflictCommitResumesExternalReceiptAtOriginalMutationRevision(
	t *testing.T,
) {
	coordinator, err := writecoordinator.OpenPersistent(
		filepath.Join(t.TempDir(), "coordination.db"),
		testWorkspaceID,
		1,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(
		context.Background(), nil, token.Authority(),
	); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPersistentHeadStore(
		historySQLitePath(t, "resume-conflict-head.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(
		repository, coordinator, WithHeadStore(store),
	)
	if err != nil {
		t.Fatal(err)
	}
	applier, err := NewConflictApplier(service, store)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "a1111111-1111-4111-8111-111111111111"
	stage, err := applier.Prepare(
		context.Background(),
		ConflictStage{
			PlanID:      "a2222222-2222-4222-8222-222222222222",
			OperationID: operationID,
			External:    json.RawMessage(`{"formatVersion":1}`),
			OperationReceipt: conflictOperationReceipt(
				operationID, []string{"local", "replica"},
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	postReceiptFault := errors.New("fault after external receipt")
	_, err = applier.CommitWith(
		context.Background(),
		stage.StageID,
		func(
			context.Context,
			writecoordinator.WriteIntent,
			ConflictStage,
		) (ExternalApplyResult, error) {
			return ExternalApplyResult{Irreversible: true},
				postReceiptFault
		},
	)
	if !errors.Is(err, writecoordinator.ErrExternalCommitted) ||
		!errors.Is(err, postReceiptFault) {
		t.Fatalf("post-receipt commit error = %v", err)
	}
	recovery := coordinator.RecoveryState()
	if recovery.PendingMutationRevision != 1 ||
		recovery.Counters.MutationRevision != 0 {
		t.Fatalf("prepared recovery state = %#v", recovery)
	}
	commit, err := applier.CommitWith(
		context.Background(),
		stage.StageID,
		func(
			_ context.Context,
			intent writecoordinator.WriteIntent,
			_ ConflictStage,
		) (ExternalApplyResult, error) {
			if intent.MutationRevision != 1 {
				t.Fatalf("replay revision = %d",
					intent.MutationRevision,
				)
			}
			return ExternalApplyResult{Irreversible: true}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	recovery = coordinator.RecoveryState()
	if recovery.PendingMutationRevision != 0 ||
		recovery.Counters.MutationRevision != 1 ||
		commit.AuthorityRevision != 1 {
		t.Fatalf("recovered commit=%#v state=%#v", commit, recovery)
	}
	head, found, err := store.Load(context.Background(), testWorkspaceID)
	if err != nil || !found || head.MutationRevision != 1 {
		t.Fatalf("recovered head=%#v found=%v err=%v",
			head, found, err,
		)
	}
}

func TestConflictApplyPublishesNewFormalLeafAuditAndReceiptAtomically(t *testing.T) {
	service, repository, store, token :=
		newPersistentConflictFixture(t)
	defer store.Close()
	initial, err := service.Save(
		context.Background(),
		SaveRequest{
			Token:      token,
			DocumentID: testDocumentOne,
			Path:       "table.csv",
			Kind:       RevisionFormal,
			Content:    []byte("local"),
			MimeType:   "text/csv",
			CreatedBy:  "test",
			DeviceID:   testDeviceID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := repository.Commit(
		context.Background(),
		objectrepo.CommitRequest{
			Authority: token.Authority(),
			Objects: []objectrepo.ObjectInput{{
				Name:    "remote",
				Content: []byte("remote"),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	applier, err := NewConflictApplier(service, store)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	stage, err := applier.Prepare(
		context.Background(),
		ConflictStage{
			PlanID:      "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
			OperationID: operationID,
			Selections: []ConflictSelection{{
				DocumentID:       testDocumentOne,
				ExpectedPath:     "table.csv",
				ExpectedObjectID: initial.Revision.ObjectID,
				ChosenPath:       "renamed.csv",
				ChosenObjectID:   remote.Objects["remote"],
				ChosenMimeType:   "application/vnd.remote",
			}},
			RecoverySnapshotIDs: []string{"local", "replica"},
			OperationReceipt: conflictOperationReceipt(
				operationID, []string{"local", "replica"},
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := applier.Commit(
		context.Background(), stage.StageID,
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := service.Inspect(testDocumentOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Revisions) != 2 ||
		document.RelativePath != "renamed.csv" ||
		document.Revisions[1].Kind != RevisionFormal ||
		document.Revisions[1].ObjectID != remote.Objects["remote"] ||
		document.Revisions[1].MimeType != "application/vnd.remote" ||
		document.Revisions[1].ParentRevisionID == nil ||
		*document.Revisions[1].ParentRevisionID !=
			initial.Revision.RevisionID ||
		commit.AuthorityRevision != 2 {
		t.Fatalf("conflict leaf = %#v commit=%#v", document, commit)
	}
	receipt, found, err := store.LoadOperationReceipt(
		context.Background(), testWorkspaceID, operationID,
	)
	if err != nil || !found ||
		string(receipt.Result) !=
			string(conflictOperationReceipt(
				operationID, []string{"local", "replica"},
			).Result) {
		t.Fatalf("operation receipt=%#v found=%v err=%v",
			receipt, found, err,
		)
	}
	pending, err := store.Pending(context.Background(), 10)
	if err != nil || len(pending) != 2 ||
		pending[1].EventID !=
			"conflict-resolution:"+operationID {
		t.Fatalf("audit outbox=%#v err=%v", pending, err)
	}
}

func TestConflictApplyKeepBothCreatesSecondDocumentIdentity(t *testing.T) {
	service, repository, store, token :=
		newPersistentConflictFixture(t)
	defer store.Close()
	initial, err := service.Save(
		context.Background(),
		SaveRequest{
			Token: token, DocumentID: testDocumentOne,
			Path: "table.csv", Kind: RevisionFormal,
			Content: []byte("local"), MimeType: "text/csv",
			CreatedBy: "test", DeviceID: testDeviceID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := repository.Commit(
		context.Background(),
		objectrepo.CommitRequest{
			Authority: token.Authority(),
			Objects: []objectrepo.ObjectInput{{
				Name: "remote", Content: []byte("remote"),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	applier, err := NewConflictApplier(service, store)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "f1111111-1111-4111-8111-111111111111"
	copyID := "f2222222-2222-4222-8222-222222222222"
	stage, err := applier.Prepare(
		context.Background(),
		ConflictStage{
			PlanID:      "f3333333-3333-4333-8333-333333333333",
			OperationID: operationID,
			Selections: []ConflictSelection{{
				DocumentID:       testDocumentOne,
				ExpectedPath:     "table.csv",
				ExpectedObjectID: initial.Revision.ObjectID,
				ChosenPath:       "table.csv",
				ChosenObjectID:   initial.Revision.ObjectID,
			}},
			Copies: []ConflictCopy{{
				SourceDocumentID: testDocumentOne,
				DocumentID:       copyID,
				ChosenPath:       "table (replica conflict).csv",
				ChosenObjectID:   remote.Objects["remote"],
				ChosenMimeType:   "application/vnd.replica",
			}},
			RecoverySnapshotIDs: []string{"local", "replica"},
			OperationReceipt: conflictOperationReceipt(
				operationID, []string{"local", "replica"},
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applier.Commit(
		context.Background(), stage.StageID,
	); err != nil {
		t.Fatal(err)
	}
	original, err := service.Inspect(testDocumentOne)
	if err != nil || original.RelativePath != "table.csv" ||
		len(original.Revisions) != 2 {
		t.Fatalf("original = %#v, %v", original, err)
	}
	copied, err := service.Inspect(copyID)
	if err != nil ||
		copied.RelativePath != "table (replica conflict).csv" ||
		len(copied.Revisions) != 1 ||
		copied.Revisions[0].ObjectID != remote.Objects["remote"] ||
		copied.Revisions[0].MimeType != "application/vnd.replica" ||
		copied.Revisions[0].ParentRevisionID != nil ||
		copied.EffectiveRevisionID != copied.Revisions[0].RevisionID {
		t.Fatalf("copy = %#v, %v", copied, err)
	}
}

func TestConflictApplyRollsBackHeadWhenAuditPublicationFails(t *testing.T) {
	service, repository, store, token :=
		newPersistentConflictFixture(t)
	defer store.Close()
	initial, err := service.Save(
		context.Background(),
		SaveRequest{
			Token:      token,
			DocumentID: testDocumentOne,
			Path:       "table.csv",
			Kind:       RevisionFormal,
			Content:    []byte("local"),
			MimeType:   "text/csv",
			CreatedBy:  "test",
			DeviceID:   testDeviceID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := repository.Commit(
		context.Background(),
		objectrepo.CommitRequest{
			Authority: token.Authority(),
			Objects: []objectrepo.ObjectInput{{
				Name:    "remote",
				Content: []byte("remote"),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	applier, err := NewConflictApplier(service, store)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	stage, err := applier.Prepare(
		context.Background(),
		ConflictStage{
			PlanID:      "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
			OperationID: operationID,
			Selections: []ConflictSelection{{
				DocumentID:       testDocumentOne,
				ExpectedPath:     "table.csv",
				ExpectedObjectID: initial.Revision.ObjectID,
				ChosenPath:       "table.csv",
				ChosenObjectID:   remote.Objects["remote"],
			}},
			RecoverySnapshotIDs: []string{"replica"},
			OperationReceipt: conflictOperationReceipt(
				operationID, []string{"replica"},
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO filehistory_audit_outbox(
			event_id, source_epoch, source_sequence,
			mutation_identity, payload_hash, payload_json,
			occurred_at, status
		) VALUES (?, 'fault', 99, 'fault', 'fault', X'00',
		          '2026-07-28T00:00:00Z', 'pending')`,
		"conflict-resolution:"+operationID,
	); err != nil {
		t.Fatal(err)
	}
	before := service.Root()
	if _, err := applier.Commit(
		context.Background(), stage.StageID,
	); err == nil {
		t.Fatal("expected audit uniqueness fault")
	}
	if service.Root() != before {
		t.Fatal("failed publication changed in-memory head")
	}
	document, err := service.Inspect(testDocumentOne)
	if err != nil ||
		document.EffectiveRevisionID != initial.Revision.RevisionID ||
		len(document.Revisions) != 1 {
		t.Fatalf("partial revision published: %#v %v", document, err)
	}
	head, found, err := store.Load(
		context.Background(), testWorkspaceID,
	)
	if err != nil || !found || head.Root != before {
		t.Fatalf("head not rolled back: %#v %v", head, err)
	}
	if _, found, err := store.LoadOperationReceipt(
		context.Background(), testWorkspaceID, operationID,
	); err != nil || found {
		t.Fatalf("failed apply published receipt: found=%v err=%v",
			found, err,
		)
	}
}
