package filehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestValidateRootPayloadReusesAuthoritativeTreeAndPathInvariants(
	t *testing.T,
) {
	fixture := newHistoryFixture(t)
	saved, err := fixture.save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "reports/q3.txt", Kind: RevisionFormal,
		Content: []byte("approved"),
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
	objects, err := ValidateRootPayload(
		record.Payload, fixture.token.WorkspaceID,
	)
	if err != nil || len(objects) != 1 ||
		objects[0].ObjectID != saved.Revision.ObjectID ||
		objects[0].ContentHash != saved.Revision.ContentHash ||
		objects[0].Size != saved.Revision.Size {
		t.Fatalf("objects=%#v err=%v", objects, err)
	}

	var root rootPayload
	if err := json.Unmarshal(record.Payload, &root); err != nil {
		t.Fatal(err)
	}
	root.Documents[0].EffectiveRevisionID =
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	tampered, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRootPayload(
		tampered, fixture.token.WorkspaceID,
	); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("invalid effective leaf error = %v", err)
	}
}

func TestValidateRootPayloadRejectsDuplicateCaseInsensitivePaths(
	t *testing.T,
) {
	fixture := newHistoryFixture(t)
	saved, err := fixture.save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "reports/q3.txt", Kind: RevisionFormal,
		Content: []byte("approved"),
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
	var root rootPayload
	if err := json.Unmarshal(record.Payload, &root); err != nil {
		t.Fatal(err)
	}
	duplicate := cloneDocument(root.Documents[0])
	duplicate.DocumentID = testDocumentTwo
	duplicate.RelativePath = "REPORTS/Q3.TXT"
	for index := range duplicate.Revisions {
		duplicate.Revisions[index].DocumentID = testDocumentTwo
	}
	root.Documents = append(root.Documents, duplicate)
	tampered, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRootPayload(
		tampered, fixture.token.WorkspaceID,
	); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("duplicate path error = %v", err)
	}
}

func TestValidateRootPayloadRejectsAggregateResourceLimits(t *testing.T) {
	fixture := newHistoryFixture(t)
	root := validatedRootFixture(t, fixture)

	t.Run("documents", func(t *testing.T) {
		oversized := root
		oversized.Documents = make(
			[]Document,
			MaxRootDocuments+1,
		)
		assertRootResourceLimit(t, oversized, fixture.token.WorkspaceID)
	})
	t.Run("revisions", func(t *testing.T) {
		oversized := root
		oversized.Documents = []Document{cloneDocument(root.Documents[0])}
		oversized.Documents[0].Revisions = make(
			[]Revision,
			MaxRootRevisions+1,
		)
		assertRootResourceLimit(t, oversized, fixture.token.WorkspaceID)
	})
}

func TestValidateRootPayloadRejectsExcessiveRevisionChainDepth(t *testing.T) {
	fixture := newHistoryFixture(t)
	root := validatedRootFixture(t, fixture)
	document := cloneDocument(root.Documents[0])
	base := document.Revisions[0]
	document.Revisions = make(
		[]Revision,
		0,
		MaxRevisionChainDepth+1,
	)
	var parent *string
	for index := 0; index <= MaxRevisionChainDepth; index++ {
		revisionID := fmt.Sprintf(
			"00000000-0000-4000-8000-%012x",
			index+1,
		)
		revision := base
		revision.RevisionID = revisionID
		revision.ParentRevisionID = parent
		revision.RestoredFromRevisionID = nil
		revision.Kind = RevisionAutosave
		revision.RevisionOrdinal = uint64(index + 1)
		revision.FormalVersion = nil
		revision.CreatedAt = base.CreatedAt.Add(
			time.Duration(index) * time.Nanosecond,
		)
		document.Revisions = append(document.Revisions, revision)
		parentID := revisionID
		parent = &parentID
	}
	document.EffectiveRevisionID =
		document.Revisions[len(document.Revisions)-1].RevisionID
	document.NextRevisionOrdinal = uint64(len(document.Revisions) + 1)
	document.NextFormalVersion = 1
	root.Documents = []Document{document}
	assertRootResourceLimit(t, root, fixture.token.WorkspaceID)
}

func TestValidateRevisionAncestryRejectsCycle(t *testing.T) {
	first := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	second := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	revisions := map[string]Revision{
		first:  {RevisionID: first, ParentRevisionID: &second},
		second: {RevisionID: second, ParentRevisionID: &first},
	}
	if err := validateRevisionAncestry(revisions); !errors.Is(
		err,
		ErrStateCorrupt,
	) {
		t.Fatalf("cycle error = %v", err)
	}
}

func validatedRootFixture(
	t *testing.T,
	fixture historyFixture,
) rootPayload {
	t.Helper()
	saved, err := fixture.save(context.Background(), SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		Path: "reports/q3.txt", Kind: RevisionFormal,
		Content: []byte("approved"),
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.repository.GetManifest(
		context.Background(),
		saved.Root,
	)
	if err != nil {
		t.Fatal(err)
	}
	var root rootPayload
	if err := json.Unmarshal(record.Payload, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertRootResourceLimit(
	t *testing.T,
	root rootPayload,
	workspaceID string,
) {
	t.Helper()
	payload, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRootPayload(payload, workspaceID); !errors.Is(
		err,
		ErrStateCorrupt,
	) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("resource limit error = %v", err)
	}
}
