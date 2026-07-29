package filehistory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

func TestProvisionalSavesUseLocalSequenceUntilSerialAcceptance(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()

	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "offline/report.txt", Kind: RevisionFormal,
		Provisional: true, Content: []byte("offline-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal, Provisional: true,
		Content: []byte("offline-2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.RevisionOrdinal != 0 ||
		first.Revision.FormalVersion != nil ||
		first.Revision.LocalSequence == nil ||
		*first.Revision.LocalSequence != 1 ||
		second.Revision.RevisionOrdinal != 0 ||
		second.Revision.FormalVersion != nil ||
		second.Revision.LocalSequence == nil ||
		*second.Revision.LocalSequence != 2 ||
		second.Document.NextRevisionOrdinal != 1 ||
		second.Document.NextFormalVersion != 1 {
		t.Fatalf("provisional revisions consumed canonical counters: %#v %#v",
			first.Revision, second.Revision)
	}

	accepted, err := fixture.service.AcceptProvisional(
		ctx,
		AcceptProvisionalRequest{
			Token: fixture.token, DocumentID: testDocumentOne,
			CandidateRevisionID: second.Revision.RevisionID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Document.NextRevisionOrdinal != 3 ||
		accepted.Document.NextFormalVersion != 3 ||
		accepted.Document.EffectiveRevisionID != second.Revision.RevisionID {
		t.Fatalf("accepted document = %#v", accepted.Document)
	}
	gotFirst := revisionByID(accepted.Document, first.Revision.RevisionID)
	gotSecond := revisionByID(accepted.Document, second.Revision.RevisionID)
	if gotFirst == nil || gotSecond == nil ||
		gotFirst.RevisionOrdinal != 1 ||
		gotSecond.RevisionOrdinal != 2 ||
		gotFirst.FormalVersion == nil || *gotFirst.FormalVersion != 1 ||
		gotSecond.FormalVersion == nil || *gotSecond.FormalVersion != 2 ||
		gotFirst.LocalSequence == nil || *gotFirst.LocalSequence != 1 ||
		gotSecond.LocalSequence == nil || *gotSecond.LocalSequence != 2 {
		t.Fatalf("accepted revisions = %#v %#v", gotFirst, gotSecond)
	}
}

func TestConfiguredProvisionalClaimAppliesToSaveWithoutCallerFlag(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	if err := fixture.service.ConfigureClaimMode(
		fixture.token.ClaimID, true,
	); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "offline/automatic.txt", Kind: RevisionFormal,
		Content: []byte("first"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.RevisionOrdinal != 0 ||
		first.Revision.LocalSequence == nil ||
		*first.Revision.LocalSequence != 1 ||
		first.Revision.FormalVersion != nil {
		t.Fatalf("configured claim save = %#v", first.Revision)
	}
}

func TestPublishedProvisionalAcceptanceIsBoundToExactRootAndAtomicAcrossDocuments(
	t *testing.T,
) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "offline/one.txt", Kind: RevisionFormal,
		Provisional: true, Content: []byte("one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentTwo,
		Path: "offline/two.txt", Kind: RevisionFormal,
		Provisional: true, Content: []byte("two"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.LocalSequence == nil ||
		*first.Revision.LocalSequence != 1 ||
		second.Revision.LocalSequence == nil ||
		*second.Revision.LocalSequence != 2 {
		t.Fatalf("workspace-local sequence = %#v %#v",
			first.Revision, second.Revision)
	}

	stale, err := fixture.service.AcceptPublishedProvisional(
		ctx, fixture.token, objectrepo.ManifestID("manifest_stale"),
	)
	if err == nil || stale.NoOp || stale.Root != "" ||
		stale.MutationRevision != 0 {
		t.Fatalf("unverified publication acceptance = %#v, %v", stale, err)
	}
	before, err := fixture.service.Inspect(testDocumentOne)
	if err != nil || before.Revisions[0].RevisionOrdinal != 0 {
		t.Fatalf("stale publication changed revision = %#v, %v", before, err)
	}

	accepted, err := fixture.service.AcceptPublishedProvisional(
		ctx, fixture.token, second.Root,
	)
	if err != nil || accepted.NoOp {
		t.Fatalf("exact publication acceptance = %#v, %v", accepted, err)
	}
	for _, documentID := range []string{testDocumentOne, testDocumentTwo} {
		document, err := fixture.service.Inspect(documentID)
		if err != nil {
			t.Fatal(err)
		}
		revision := revisionByID(document, document.EffectiveRevisionID)
		if revision == nil ||
			revision.RevisionOrdinal != 1 ||
			revision.FormalVersion == nil ||
			*revision.FormalVersion != 1 ||
			revision.LocalSequence == nil {
			t.Fatalf("accepted document %s = %#v", documentID, document)
		}
	}
}

func TestPublishedAcceptanceCanonicalizesFrozenPrefixAfterNewerLocalSave(
	t *testing.T,
) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	published, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "offline/concurrent.txt", Kind: RevisionFormal,
		Provisional: true, Content: []byte("published-prefix"),
	})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(
			published.Revision.RevisionID,
		),
		Kind: RevisionFormal, Provisional: true,
		Content: []byte("newer-unpublished-tail"),
	})
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := fixture.service.AcceptPublishedProvisional(
		ctx,
		fixture.token,
		published.Root,
	)
	if err != nil || accepted.NoOp {
		t.Fatalf("published prefix acceptance = %#v, %v", accepted, err)
	}
	document, err := fixture.service.Inspect(testDocumentOne)
	if err != nil {
		t.Fatal(err)
	}
	gotPublished := revisionByID(
		document,
		published.Revision.RevisionID,
	)
	gotNewer := revisionByID(document, newer.Revision.RevisionID)
	if gotPublished == nil ||
		gotPublished.RevisionOrdinal != 1 ||
		gotPublished.FormalVersion == nil ||
		*gotPublished.FormalVersion != 1 ||
		gotNewer == nil ||
		gotNewer.RevisionOrdinal != 0 ||
		gotNewer.FormalVersion != nil ||
		document.EffectiveRevisionID != newer.Revision.RevisionID {
		t.Fatalf("prefix/tail acceptance = %#v", document)
	}

	retry, err := fixture.service.AcceptPublishedProvisional(
		ctx,
		fixture.token,
		published.Root,
	)
	if err != nil || !retry.NoOp || retry.Root != accepted.Root {
		t.Fatalf("idempotent published acceptance = %#v, %v", retry, err)
	}
}

func TestCompetingProvisionalBranchesAreAcceptedWithoutLosingEither(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	base, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "shared/report.txt", Kind: RevisionFormal,
		Content: []byte("base"),
	})
	if err != nil {
		t.Fatal(err)
	}
	left, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ParentRevisionID:          base.Revision.RevisionID,
		ExpectedEffectiveRevision: stringRef(base.Revision.RevisionID),
		Kind:                      RevisionFormal, Provisional: true,
		Content: []byte("left"),
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ParentRevisionID:          base.Revision.RevisionID,
		ExpectedEffectiveRevision: stringRef(left.Revision.RevisionID),
		Kind:                      RevisionFormal, Provisional: true,
		Content: []byte("right"),
	})
	if err != nil {
		t.Fatal(err)
	}

	leftAccepted, err := fixture.service.AcceptProvisional(
		ctx,
		AcceptProvisionalRequest{
			Token: fixture.token, DocumentID: testDocumentOne,
			CandidateRevisionID: left.Revision.RevisionID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	acceptedLeft := revisionByID(
		leftAccepted.Document, left.Revision.RevisionID,
	)
	preservedRight := revisionByID(
		leftAccepted.Document, right.Revision.RevisionID,
	)
	if len(leftAccepted.Document.Revisions) != 3 ||
		acceptedLeft == nil || acceptedLeft.RevisionOrdinal != 2 ||
		acceptedLeft.FormalVersion == nil ||
		*acceptedLeft.FormalVersion != 2 ||
		preservedRight == nil ||
		preservedRight.RevisionOrdinal != 0 ||
		preservedRight.FormalVersion != nil ||
		preservedRight.ParentRevisionID == nil ||
		*preservedRight.ParentRevisionID != base.Revision.RevisionID {
		t.Fatalf("first arbitration lost or rewrote a branch: %#v",
			leftAccepted.Document)
	}

	rightAccepted, err := fixture.service.AcceptProvisional(
		ctx,
		AcceptProvisionalRequest{
			Token: fixture.token, DocumentID: testDocumentOne,
			CandidateRevisionID: right.Revision.RevisionID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	acceptedRight := revisionByID(
		rightAccepted.Document, right.Revision.RevisionID,
	)
	if len(rightAccepted.Document.Revisions) != 3 ||
		acceptedRight == nil || acceptedRight.RevisionOrdinal != 3 ||
		acceptedRight.FormalVersion == nil ||
		*acceptedRight.FormalVersion != 3 ||
		rightAccepted.Document.EffectiveRevisionID != right.Revision.RevisionID {
		t.Fatalf("second arbitration = %#v", rightAccepted.Document)
	}
}

func TestLegacyV2RootReopensAndUpgradesOnNextCommit(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	saved, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "legacy.txt", Kind: RevisionFormal,
		Content: []byte("legacy"),
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.repository.GetManifest(ctx, saved.Root)
	if err != nil {
		t.Fatal(err)
	}
	var legacy rootPayload
	if err := decodeStrict(record.Payload, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.FormatVersion = legacyRootFormatVersion
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fixture.repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: fixture.token.Authority(),
		Manifests: []objectrepo.ManifestInput{{
			Name: "filehistory-root",
			Labels: map[string]string{
				"type":        "filehistory-root",
				"workspaceId": fixture.token.WorkspaceID,
			},
			Payload: raw,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(
		ctx,
		fixture.repository,
		fixture.coordinator,
		receipt.Manifests["filehistory-root"],
	)
	if err != nil {
		t.Fatalf("legacy v2 root did not reopen: %v", err)
	}
	next, err := reopened.Save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(saved.Revision.RevisionID),
		Kind:                      RevisionAutosave, Content: []byte("upgraded"),
		MimeType: "text/plain", CreatedBy: "test-user",
		DeviceID: testDeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := fixture.repository.GetManifest(ctx, next.Root)
	if err != nil {
		t.Fatal(err)
	}
	var root rootPayload
	if err := decodeStrict(upgraded.Payload, &root); err != nil {
		t.Fatal(err)
	}
	if root.FormatVersion != rootFormatVersion {
		t.Fatalf("upgraded root format = %d", root.FormatVersion)
	}
}

func TestProvisionalValidationRejectsMissingOrDuplicateLocalSequence(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "one.txt", Kind: RevisionAutosave,
		Provisional: true, Content: []byte("one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.repository.GetManifest(ctx, first.Root)
	if err != nil {
		t.Fatal(err)
	}
	var root rootPayload
	if err := decodeStrict(record.Payload, &root); err != nil {
		t.Fatal(err)
	}
	root.Documents[0].Revisions[0].LocalSequence = nil
	missing, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRootPayload(
		missing, fixture.token.WorkspaceID,
	); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("missing localSequence error = %v", err)
	}

	duplicate := cloneDocument(first.Document)
	duplicate.DocumentID = testDocumentTwo
	duplicate.RelativePath = "two.txt"
	duplicate.Revisions[0].DocumentID = testDocumentTwo
	duplicate.Revisions[0].RevisionID =
		"55555555-5555-4555-8555-555555555555"
	duplicate.EffectiveRevisionID = duplicate.Revisions[0].RevisionID
	root.Documents = []Document{first.Document, duplicate}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRootPayload(
		raw, fixture.token.WorkspaceID,
	); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("duplicate localSequence error = %v", err)
	}

	root.Documents = []Document{first.Document}
	root.FormatVersion = legacyRootFormatVersion
	raw, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRootPayload(
		raw, fixture.token.WorkspaceID,
	); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("provisional revision in v2 root error = %v", err)
	}
}
