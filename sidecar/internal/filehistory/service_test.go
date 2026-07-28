package filehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const (
	testWorkspaceID = "11111111-1111-4111-8111-111111111111"
	testDeviceID    = "22222222-2222-4222-8222-222222222222"
	testDocumentOne = "33333333-3333-4333-8333-333333333333"
	testDocumentTwo = "44444444-4444-4444-8444-444444444444"
)

type historyFixture struct {
	repository  *objectrepo.MemoryRepository
	coordinator *writecoordinator.WorkspaceWriteCoordinator
	service     *Service
	token       writecoordinator.Token
}

func newHistoryFixture(t *testing.T) historyFixture {
	t.Helper()
	coordinator, err := writecoordinator.New(
		testWorkspaceID, 1, "claim-a", 1,
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
	nextID := 0
	service, err := New(
		repository,
		coordinator,
		WithClock(func() time.Time {
			return time.Date(2026, 7, 28, 12, 0, nextID, 0, time.UTC)
		}),
		WithIDGenerator(func() (string, error) {
			nextID++
			return uuid.NewSHA1(
				uuid.NameSpaceOID,
				[]byte(fmt.Sprintf("revision-%d", nextID)),
			).String(), nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return historyFixture{
		repository:  repository,
		coordinator: coordinator,
		service:     service,
		token:       token,
	}
}

func (fixture historyFixture) save(
	ctx context.Context,
	request SaveRequest,
) (SaveResult, error) {
	request.MimeType = "text/plain"
	request.CreatedBy = "test-user"
	request.DeviceID = testDeviceID
	return fixture.service.Save(ctx, request)
}

func (fixture historyFixture) restore(
	ctx context.Context,
	request RestoreRequest,
) (SaveResult, error) {
	request.CreatedBy = "test-user"
	request.DeviceID = testDeviceID
	return fixture.service.Restore(ctx, request)
}

func stringRef(value string) *string {
	return &value
}

func TestSaveNoOpFormalVersionsAndBranchingKeepOneEffectiveLeaf(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "reports/q3.txt",
		Kind: RevisionAutosave, Content: []byte("draft"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.RevisionOrdinal != 1 ||
		first.Revision.FormalVersion != nil ||
		first.MutationRevision != 1 {
		t.Fatalf("first save = %#v", first)
	}
	firstRoot := first.Root
	noOp, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionAutosave, Content: []byte("draft"),
	})
	if err != nil ||
		!noOp.NoOp ||
		noOp.Root != firstRoot ||
		noOp.MutationRevision != 1 {
		t.Fatalf("no-op = %#v, %v", noOp, err)
	}
	formalOne, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("draft"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if formalOne.Revision.VersionLabel() != "V1" ||
		formalOne.Revision.ObjectID != first.Revision.ObjectID {
		t.Fatalf("formal promotion = %#v", formalOne.Revision)
	}
	formalTwo, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(formalOne.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("published"),
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ParentRevisionID:          formalOne.Revision.RevisionID,
		ExpectedEffectiveRevision: stringRef(formalTwo.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("alternate"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch.Revision.VersionLabel() != "V3" ||
		branch.Revision.ParentRevisionID == nil ||
		*branch.Revision.ParentRevisionID != formalOne.Revision.RevisionID ||
		branch.Document.EffectiveRevisionID != branch.Revision.RevisionID {
		t.Fatalf("branch = %#v", branch)
	}
	if !isLeaf(branch.Document, formalTwo.Revision.RevisionID) ||
		!isLeaf(branch.Document, branch.Revision.RevisionID) ||
		isLeaf(branch.Document, formalOne.Revision.RevisionID) {
		t.Fatalf("leaf topology = %#v", branch.Document.Revisions)
	}
	activated, err := fixture.service.ActivateLeaf(
		ctx,
		fixture.token,
		testDocumentOne,
		formalTwo.Revision.RevisionID,
		stringRef(branch.Revision.RevisionID),
	)
	if err != nil ||
		activated.Document.EffectiveRevisionID != formalTwo.Revision.RevisionID {
		t.Fatalf("activate = %#v, %v", activated, err)
	}
	if _, err := fixture.service.ActivateLeaf(
		ctx,
		fixture.token,
		testDocumentOne,
		formalOne.Revision.RevisionID,
		stringRef(formalTwo.Revision.RevisionID),
	); !errors.Is(err, ErrNotLeaf) {
		t.Fatalf("non-leaf activation error = %v", err)
	}
}

func TestRestoreCreatesNextFormalLeafWithoutRewritingHistory(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "notes.txt",
		Kind: RevisionFormal, Content: []byte("one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("two"),
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := fixture.restore(ctx, RestoreRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		TargetRevisionID:          first.Revision.RevisionID,
		ExpectedEffectiveRevision: stringRef(second.Revision.RevisionID),
	})
	if err != nil {
		t.Fatal(err)
	}
	revision := restored.Revision
	if revision.Kind != RevisionRestore ||
		revision.VersionLabel() != "V3" ||
		revision.ParentRevisionID == nil ||
		*revision.ParentRevisionID != second.Revision.RevisionID ||
		revision.RestoredFromRevisionID == nil ||
		*revision.RestoredFromRevisionID != first.Revision.RevisionID ||
		revision.ObjectID != first.Revision.ObjectID ||
		len(restored.Document.Revisions) != 3 ||
		restored.Document.EffectiveRevisionID != revision.RevisionID {
		t.Fatalf("restore = %#v", restored)
	}
}

func TestRenameDeletePreserveIdentityHistoryAndReservePath(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "drafts/a.txt",
		Kind: RevisionFormal, Content: []byte("content"),
	})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := fixture.service.Rename(
		ctx,
		fixture.token,
		testDocumentOne,
		"archive/a.txt",
		stringRef(first.Revision.RevisionID),
	)
	if err != nil ||
		renamed.Document.DocumentID != testDocumentOne ||
		renamed.Document.RelativePath != "archive/a.txt" ||
		len(renamed.Document.Revisions) != 1 {
		t.Fatalf("rename = %#v, %v", renamed, err)
	}
	if _, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentTwo, Path: "archive/a.txt",
		Kind: RevisionFormal, Content: []byte("other"),
	}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("duplicate path error = %v", err)
	}
	deleted, err := fixture.service.Delete(
		ctx,
		fixture.token,
		testDocumentOne,
		stringRef(first.Revision.RevisionID),
	)
	if err != nil ||
		deleted.Document.Status != DocumentDeleted ||
		deleted.Document.EffectiveRevisionID != first.Revision.RevisionID ||
		len(deleted.Document.Revisions) != 1 {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
	repeated, err := fixture.service.Delete(
		ctx,
		fixture.token,
		testDocumentOne,
		stringRef(first.Revision.RevisionID),
	)
	if err != nil ||
		!repeated.NoOp ||
		repeated.MutationRevision != deleted.MutationRevision {
		t.Fatalf("repeated delete = %#v, %v", repeated, err)
	}
	if _, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionAutosave, Content: []byte("changed"),
	}); !errors.Is(err, ErrDocumentDeleted) {
		t.Fatalf("save deleted error = %v", err)
	}
	if _, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentTwo, Path: "archive/a.txt",
		Kind: RevisionFormal, Content: []byte("other"),
	}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("deleted path reuse error = %v", err)
	}
}

func TestWin32PathsRejectAliasesReservedNamesAndInvalidComponents(
	t *testing.T,
) {
	for _, candidate := range []string{
		"foo/foo.",
		"foo/foo ",
		"CON",
		"NUL",
		"nested/con.txt",
		"nested/COM1.csv",
		"nested/LPT².log",
		"bad?.csv",
	} {
		t.Run(candidate, func(t *testing.T) {
			if _, err := normalizePath(candidate); !errors.Is(err, ErrPathInvalid) {
				t.Fatalf("normalizePath(%q) error = %v", candidate, err)
			}
		})
	}

	documents := map[string]Document{
		testDocumentOne: {
			DocumentID: testDocumentOne, RelativePath: "Report.csv",
		},
		testDocumentTwo: {
			DocumentID: testDocumentTwo, RelativePath: "report.csv",
		},
	}
	if err := validatePaths(documents); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("case-insensitive validatePaths error = %v", err)
	}

	fixture := newHistoryFixture(t)
	ctx := context.Background()
	if _, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "Report.csv",
		Kind: RevisionFormal, Content: []byte("first"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "report.csv",
		Kind: RevisionAutosave, Content: []byte("updated"),
	}); err != nil {
		t.Fatalf("equivalent path save error = %v", err)
	}
	if _, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentTwo, Path: "report.csv",
		Kind: RevisionFormal, Content: []byte("second"),
	}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("case-insensitive save conflict error = %v", err)
	}
}

func TestOpenRejectsCyclicRevisionGraphEvenWhenEffectiveRevisionIsALeaf(t *testing.T) {
	fixture := newHistoryFixture(t)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	revisionA := "55555555-5555-4555-8555-555555555555"
	revisionB := "66666666-6666-4666-8666-666666666666"
	revisionLeaf := "77777777-7777-4777-8777-777777777777"
	parentA := revisionB
	parentB := revisionA
	payload, err := json.Marshal(rootPayload{
		FormatVersion: rootFormatVersion,
		WorkspaceID:   fixture.token.WorkspaceID,
		Documents: []Document{{
			ContractVersion: contractVersion,
			WorkspaceID:     fixture.token.WorkspaceID,
			DocumentID:      testDocumentOne, RelativePath: "file.txt",
			Status: DocumentActive, TopologyRevision: 1,
			EffectiveRevisionID: revisionLeaf,
			NextRevisionOrdinal: 4,
			NextFormalVersion:   4,
			Revisions: []Revision{
				{
					ContractVersion: contractVersion,
					RevisionID:      revisionA, DocumentID: testDocumentOne,
					ParentRevisionID: &parentA,
					Kind:             RevisionFormal, RevisionOrdinal: 1,
					FormalVersion: uint64Pointer(1),
					ObjectID:      contentObjectID([]byte("a")),
					ContentHash:   contentHash([]byte("a")), Size: 1,
					MimeType: "text/plain", CreatedAt: now,
					CreatedBy: "test-user", DeviceID: testDeviceID,
				},
				{
					ContractVersion: contractVersion,
					RevisionID:      revisionB, DocumentID: testDocumentOne,
					ParentRevisionID: &parentB,
					Kind:             RevisionFormal, RevisionOrdinal: 2,
					FormalVersion: uint64Pointer(2),
					ObjectID:      contentObjectID([]byte("b")),
					ContentHash:   contentHash([]byte("b")), Size: 1,
					MimeType: "text/plain", CreatedAt: now,
					CreatedBy: "test-user", DeviceID: testDeviceID,
				},
				{
					ContractVersion: contractVersion,
					RevisionID:      revisionLeaf, DocumentID: testDocumentOne,
					Kind: RevisionFormal, RevisionOrdinal: 3,
					FormalVersion: uint64Pointer(3),
					ObjectID:      contentObjectID([]byte("leaf")),
					ContentHash:   contentHash([]byte("leaf")), Size: 4,
					MimeType: "text/plain", CreatedAt: now,
					CreatedBy: "test-user", DeviceID: testDeviceID,
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fixture.repository.Commit(context.Background(), objectrepo.CommitRequest{
		Authority: fixture.token.Authority(),
		Objects: []objectrepo.ObjectInput{
			{Name: "a", Content: []byte("a")},
			{Name: "b", Content: []byte("b")},
			{Name: "leaf", Content: []byte("leaf")},
		},
		Manifests: []objectrepo.ManifestInput{{
			Name: "filehistory-root",
			Labels: map[string]string{
				"type": "filehistory-root", "workspaceId": fixture.token.WorkspaceID,
			},
			Payload: payload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		receipt.Manifests["filehistory-root"],
	); !errors.Is(err, ErrStateCorrupt) {
		_ = reopened
		t.Fatalf("cyclic root error = %v", err)
	}
}

func TestRootReopensAndVerifiesAllReachableObjects(t *testing.T) {
	fixture := newHistoryFixture(t)
	saved, err := fixture.save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "file.txt",
		Kind: RevisionFormal, Content: []byte("durable"),
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		saved.Root,
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reopened.Inspect(testDocumentOne)
	if err != nil ||
		document.EffectiveRevisionID != saved.Revision.RevisionID ||
		document.Revisions[0].ObjectID != saved.Revision.ObjectID {
		t.Fatalf("reopened document = %#v, %v", document, err)
	}
}

func TestV2RevisionMetadataAndUUIDValidation(t *testing.T) {
	fixture := newHistoryFixture(t)
	saved, err := fixture.save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "metadata.txt", Kind: RevisionFormal,
		Content: []byte("metadata"), Comment: "first formal",
	})
	if err != nil {
		t.Fatal(err)
	}
	revision := saved.Revision
	if saved.Document.ContractVersion != contractVersion ||
		saved.Document.WorkspaceID != testWorkspaceID ||
		saved.Document.Status != DocumentActive ||
		saved.Document.NextRevisionOrdinal != 2 ||
		saved.Document.NextFormalVersion != 2 ||
		revision.ContractVersion != contractVersion ||
		revision.DocumentID != testDocumentOne ||
		revision.RevisionOrdinal != 1 ||
		revision.FormalVersion == nil ||
		*revision.FormalVersion != 1 ||
		revision.ObjectID != contentObjectID([]byte("metadata")) ||
		revision.ContentHash != contentHash([]byte("metadata")) ||
		revision.Size != int64(len("metadata")) ||
		revision.MimeType != "text/plain" ||
		revision.CreatedBy != "test-user" ||
		revision.DeviceID != testDeviceID ||
		revision.Comment == nil ||
		*revision.Comment != "first formal" {
		t.Fatalf("v2 metadata = %#v %#v", saved.Document, revision)
	}
	invalid := []SaveRequest{
		{
			Token: fixture.token, DocumentID: "not-a-uuid",
			Path: "invalid-1.txt", Kind: RevisionFormal,
			Content: []byte("x"), MimeType: "text/plain",
			CreatedBy: "test-user", DeviceID: testDeviceID,
		},
		{
			Token: fixture.token, DocumentID: testDocumentTwo,
			Path: "invalid-2.txt", Kind: RevisionFormal,
			Content: []byte("x"), MimeType: "not a mime",
			CreatedBy: "test-user", DeviceID: testDeviceID,
		},
		{
			Token: fixture.token, DocumentID: testDocumentTwo,
			Path: "invalid-3.txt", Kind: RevisionFormal,
			Content: []byte("x"), MimeType: "text/plain",
			CreatedBy: "test-user", DeviceID: "not-a-uuid",
		},
	}
	for index, request := range invalid {
		if _, err := fixture.service.Save(
			context.Background(), request,
		); err == nil {
			t.Fatalf("invalid request %d was accepted", index)
		}
	}
}

func TestOpenRejectsTamperedRevisionSizeMetadata(t *testing.T) {
	fixture := newHistoryFixture(t)
	saved, err := fixture.save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "metadata.txt", Kind: RevisionFormal,
		Content: []byte("metadata"),
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.repository.GetManifest(
		context.Background(), saved.Root,
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload rootPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Documents[0].Revisions[0].Size++
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fixture.repository.Commit(
		context.Background(),
		objectrepo.CommitRequest{
			Authority: fixture.token.Authority(),
			Manifests: []objectrepo.ManifestInput{{
				Name: "filehistory-root",
				Labels: map[string]string{
					"type":        "filehistory-root",
					"workspaceId": testWorkspaceID,
				},
				Payload: raw,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		receipt.Manifests["filehistory-root"],
	); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("tampered size accepted: %v", err)
	}
}
