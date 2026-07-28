package filehistory

import (
	"context"
	"encoding/json"
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
