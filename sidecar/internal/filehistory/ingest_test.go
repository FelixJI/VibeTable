package filehistory

import (
	"context"
	"testing"
)

func externalMetadata(change ExternalChange) ExternalChange {
	change.MimeType = "text/plain"
	change.CreatedBy = "external-editor"
	change.DeviceID = testDeviceID
	return change
}

func TestExternalIngestStableSaveRenameMoveCopyDelete(t *testing.T) {
	fixture := newHistoryFixture(t)
	ingestor, err := NewIngestor(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := ingestor.Ingest(ctx, externalMetadata(ExternalChange{
		Token: fixture.token, Kind: ExternalStableSave,
		DocumentID: testDocumentOne,
		TargetPath: "drafts/report.txt",
		Content:    []byte("draft"), ContentProvided: true,
	}))
	if err != nil || created.Save == nil ||
		created.Save.Revision.RevisionOrdinal != 1 {
		t.Fatalf("created = %#v, %v", created, err)
	}
	firstRoot := created.Save.Root
	noOp, err := ingestor.Ingest(ctx, externalMetadata(ExternalChange{
		Token: fixture.token, Kind: ExternalStableSave,
		DocumentID: testDocumentOne,
		TargetPath: "drafts/report.txt",
		Content:    []byte("draft"), ContentProvided: true,
	}))
	if err != nil || noOp.Save == nil ||
		!noOp.Save.NoOp || noOp.Save.Root != firstRoot {
		t.Fatalf("no-op = %#v, %v", noOp, err)
	}
	renamed, err := ingestor.Ingest(ctx, ExternalChange{
		Token: fixture.token, Kind: ExternalRename,
		SourcePath: "drafts/report.txt",
		TargetPath: "drafts/renamed.txt",
	})
	if err != nil || renamed.Mutation == nil ||
		renamed.Mutation.Document.RelativePath !=
			"drafts/renamed.txt" {
		t.Fatalf("rename = %#v, %v", renamed, err)
	}
	moved, err := ingestor.Ingest(ctx, ExternalChange{
		Token: fixture.token, Kind: ExternalMove,
		DocumentID: testDocumentOne,
		TargetPath: "archive/renamed.txt",
	})
	if err != nil || moved.Mutation == nil ||
		moved.Mutation.Document.RelativePath !=
			"archive/renamed.txt" {
		t.Fatalf("move = %#v, %v", moved, err)
	}
	copied, err := ingestor.Ingest(ctx, externalMetadata(ExternalChange{
		Token: fixture.token, Kind: ExternalCopy,
		SourceDocumentID: testDocumentOne,
		NewDocumentID:    testDocumentTwo,
		TargetPath:       "archive/copy.txt",
	}))
	if err != nil || copied.Save == nil ||
		copied.Save.Revision.ObjectID != created.Save.Revision.ObjectID ||
		copied.Save.Document.DocumentID != testDocumentTwo {
		t.Fatalf("copy = %#v, %v", copied, err)
	}
	deleted, err := ingestor.Ingest(ctx, ExternalChange{
		Token: fixture.token, Kind: ExternalDelete,
		DocumentID: testDocumentOne,
	})
	if err != nil || deleted.Mutation == nil ||
		deleted.Mutation.Document.Status != DocumentDeleted {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
}

func TestExternalIngestAmbiguousHashRequiresConfirmationWithoutMutation(t *testing.T) {
	fixture := newHistoryFixture(t)
	original, err := fixture.save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "original.txt", Kind: RevisionFormal,
		Content: []byte("same-content"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ingestor, err := NewIngestor(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ingestor.Ingest(
		context.Background(),
		externalMetadata(ExternalChange{
			Token: fixture.token, Kind: ExternalStableSave,
			TargetPath: "unknown.txt",
			Content:    []byte("same-content"), ContentProvided: true,
		}),
	)
	if err != nil || result.Confirmation == nil ||
		len(result.Confirmation.CandidateDocumentIDs) != 1 ||
		result.Confirmation.CandidateDocumentIDs[0] !=
			testDocumentOne {
		t.Fatalf("confirmation = %#v, %v", result, err)
	}
	if fixture.service.Root() != original.Root ||
		len(fixture.service.List()) != 1 {
		t.Fatal("ambiguous external edit mutated history")
	}
}

func TestExternalIngestUnknownRenameRequiresConfirmation(t *testing.T) {
	fixture := newHistoryFixture(t)
	ingestor, err := NewIngestor(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ingestor.Ingest(
		context.Background(),
		ExternalChange{
			Token: fixture.token, Kind: ExternalRename,
			SourcePath: "missing.txt", TargetPath: "renamed.txt",
		},
	)
	if err != nil || result.Confirmation == nil ||
		result.Confirmation.Reason == "" {
		t.Fatalf("confirmation = %#v, %v", result, err)
	}
	if fixture.service.Root() != "" ||
		len(fixture.service.List()) != 0 {
		t.Fatal("unknown rename created or linked a document")
	}
}
