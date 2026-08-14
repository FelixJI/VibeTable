package filehistory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestQueryDocumentsProjectsEffectiveRevisionAndBooleanMetadata(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "reports/Quarter Plan.md",
		Kind: RevisionFormal, Content: []byte("alpha"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentTwo, Path: "exports/summary.json",
		Kind: RevisionFormal, Content: []byte("{\"ok\":true}"),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := fixture.service.QueryDocuments(DocumentQueryRequest{
		Logic: "and",
		Filters: []DocumentFilter{
			{Field: "displayName", Operator: "contains", Value: "quarter"},
			{Field: "sizeBytes", Operator: "gte", Value: float64(5)},
		},
		Sort:  []DocumentSort{{Field: "effectiveRevisionCreatedAt", Direction: "desc"}},
		Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Documents) != 1 {
		t.Fatalf("documents = %#v", result.Documents)
	}
	document := result.Documents[0]
	if document.DocumentID != testDocumentOne ||
		document.RelativePath != "reports/Quarter Plan.md" ||
		document.DisplayName != "Quarter Plan.md" ||
		document.Extension != "md" ||
		document.MimeType != "text/plain" ||
		document.SizeBytes != 5 ||
		document.EffectiveRevisionID != first.Revision.RevisionID ||
		document.EffectiveRevisionCreatedAt.IsZero() ||
		document.FormalVersion == nil || *document.FormalVersion != 1 ||
		document.Status != "active" || result.TopologyRevision == 0 {
		t.Fatalf("summary = %#v, topology = %d", document, result.TopologyRevision)
	}
	*document.FormalVersion = 99
	reloaded, err := fixture.service.QueryDocuments(DocumentQueryRequest{
		Logic: "and",
		Filters: []DocumentFilter{
			{Field: "documentId", Operator: "eq", Value: testDocumentOne},
		},
		Limit: 1,
	})
	if err != nil || len(reloaded.Documents) != 1 ||
		reloaded.Documents[0].FormalVersion == nil ||
		*reloaded.Documents[0].FormalVersion != 1 {
		t.Fatalf("caller mutated authoritative summary state: %#v, %v", reloaded, err)
	}

	orResult, err := fixture.service.QueryDocuments(DocumentQueryRequest{
		Logic: "or",
		Filters: []DocumentFilter{
			{Field: "extension", Operator: "eq", Value: "md"},
			{Field: "extension", Operator: "eq", Value: "json"},
		},
		Sort:  []DocumentSort{{Field: "displayName", Direction: "asc"}},
		Limit: 50,
	})
	if err != nil || len(orResult.Documents) != 2 {
		t.Fatalf("OR result = %#v, %v", orResult, err)
	}

	identityResult, err := fixture.service.QueryDocuments(DocumentQueryRequest{
		Logic: "and",
		Filters: []DocumentFilter{
			{Field: "documentId", Operator: "eq", Value: testDocumentTwo},
		},
		Sort:  []DocumentSort{{Field: "documentId", Direction: "asc"}},
		Limit: 1,
	})
	if err != nil || len(identityResult.Documents) != 1 ||
		identityResult.Documents[0].DocumentID != testDocumentTwo {
		t.Fatalf("identity result = %#v, %v", identityResult, err)
	}
}

func TestQueryDocumentsCursorIsStableAndRevisionBound(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "a.txt",
		Kind: RevisionFormal, Content: []byte("a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentTwo, Path: "b.txt",
		Kind: RevisionFormal, Content: []byte("b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := DocumentQueryRequest{
		Logic: "and", Sort: []DocumentSort{{Field: "relativePath", Direction: "asc"}}, Limit: 1,
	}
	page, err := fixture.service.QueryDocuments(request)
	if err != nil || len(page.Documents) != 1 || page.NextCursor == nil {
		t.Fatalf("first page = %#v, %v", page, err)
	}
	mismatched := request
	mismatched.Sort = []DocumentSort{{Field: "relativePath", Direction: "desc"}}
	mismatched.Cursor = page.NextCursor
	if _, err := fixture.service.QueryDocuments(mismatched); !errors.Is(err, ErrDocumentQueryInvalid) {
		t.Fatalf("mismatched cursor error = %v", err)
	}
	tamperedValue := *page.NextCursor + "A"
	tampered := request
	tampered.Cursor = &tamperedValue
	if _, err := fixture.service.QueryDocuments(tampered); !errors.Is(err, ErrDocumentQueryInvalid) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	request.Cursor = page.NextCursor
	second, err := fixture.service.QueryDocuments(request)
	if err != nil || len(second.Documents) != 1 || second.Documents[0].DocumentID == page.Documents[0].DocumentID {
		t.Fatalf("second page = %#v, %v", second, err)
	}

	_, err = fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("changed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.QueryDocuments(request)
	if !errors.Is(err, ErrDocumentCursorStale) {
		t.Fatalf("stale cursor error = %v", err)
	}
}

func TestQueryDocumentsKeysetDoesNotSkipOrRepeatDescendingTies(t *testing.T) {
	fixture := newHistoryFixture(t)
	base := time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id   string
		path string
		size int64
	}{
		{id: "a", path: "z.txt", size: 10},
		{id: "b", path: "a.txt", size: 10},
		{id: "c", path: "c.txt", size: 9},
		{id: "d", path: "b.txt", size: 9},
		{id: "e", path: "e.txt", size: 8},
	} {
		formalVersion := uint64(1)
		revision := Revision{
			RevisionID: item.id + "-revision", DocumentID: item.id,
			FormalVersion: &formalVersion, MimeType: "text/plain",
			Size: item.size, CreatedAt: base,
		}
		fixture.service.documents[item.id] = Document{
			DocumentID: item.id, RelativePath: item.path, Status: DocumentActive,
			EffectiveRevisionID: revision.RevisionID, Revisions: []Revision{revision},
		}
	}
	fixture.service.headMutationRevision = 7
	request := DocumentQueryRequest{
		Logic: "and",
		Sort: []DocumentSort{
			{Field: "sizeBytes", Direction: "desc"},
			{Field: "relativePath", Direction: "asc"},
		},
		Limit: 2,
	}

	var ids []string
	for {
		page, err := fixture.service.QueryDocuments(request)
		if err != nil {
			t.Fatal(err)
		}
		for _, document := range page.Documents {
			ids = append(ids, document.DocumentID)
		}
		if page.NextCursor == nil {
			break
		}
		request.Cursor = page.NextCursor
	}
	if got, want := fmt.Sprint(ids), "[b a d c e]"; got != want {
		t.Fatalf("keyset order = %s, want %s", got, want)
	}
}

func TestQueryDocumentsRejectsUnknownFieldsOperatorsAndOversizedPages(t *testing.T) {
	fixture := newHistoryFixture(t)
	tests := []DocumentQueryRequest{
		{Logic: "and", Filters: []DocumentFilter{{Field: "windowsMtime", Operator: "eq", Value: "x"}}, Limit: 10},
		{Logic: "and", Filters: []DocumentFilter{{Field: "sizeBytes", Operator: "contains", Value: float64(1)}}, Limit: 10},
		{Logic: "and", Limit: 501},
	}
	for _, request := range tests {
		if _, err := fixture.service.QueryDocuments(request); !errors.Is(err, ErrDocumentQueryInvalid) {
			t.Fatalf("request %#v error = %v", request, err)
		}
	}
}

func TestQueryDocumentsKeepsPageAllocationsBoundedAcrossTenThousandRevisionTrees(t *testing.T) {
	fixture := newHistoryFixture(t)
	const (
		documentCount        = 10_000
		revisionsPerDocument = 8
		maximumAllocations   = 2_000
	)
	fixture.service.documents = make(map[string]Document, documentCount)
	for documentIndex := range documentCount {
		documentID := fmt.Sprintf("document-%05d", documentIndex)
		revisions := make([]Revision, revisionsPerDocument)
		for revisionIndex := range revisionsPerDocument {
			formalVersion := uint64(revisionIndex + 1)
			revisions[revisionIndex] = Revision{
				RevisionID:    fmt.Sprintf("%s-revision-%d", documentID, revisionIndex),
				DocumentID:    documentID,
				FormalVersion: &formalVersion,
				MimeType:      "text/plain",
				Size:          int64(documentIndex + revisionIndex),
				CreatedAt: time.Date(
					2026, time.August, 12, 0, 0, revisionIndex, 0, time.UTC,
				),
			}
		}
		fixture.service.documents[documentID] = Document{
			DocumentID:          documentID,
			RelativePath:        fmt.Sprintf("documents/%s.txt", documentID),
			Status:              DocumentActive,
			EffectiveRevisionID: revisions[len(revisions)-1].RevisionID,
			Revisions:           revisions,
		}
	}
	fixture.service.headMutationRevision = 42
	request := DocumentQueryRequest{
		Logic: "and",
		Sort:  []DocumentSort{{Field: "documentId", Direction: "asc"}},
		Limit: 50,
	}

	var queryErr error
	allocations := testing.AllocsPerRun(3, func() {
		var result DocumentQueryResult
		result, queryErr = fixture.service.QueryDocuments(request)
		if queryErr == nil && (len(result.Documents) != request.Limit || result.NextCursor == nil) {
			queryErr = errors.New("bounded page contract was not returned")
		}
	})
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	if allocations > maximumAllocations {
		t.Fatalf(
			"QueryDocuments allocations = %.0f, want <= %d for %d documents with %d revisions each",
			allocations, maximumAllocations, documentCount, revisionsPerDocument,
		)
	}
	t.Logf("QueryDocuments allocations for 10k x 8 revisions: %.0f", allocations)
}
