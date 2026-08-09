package filehistory

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestOpenDiffPairReadsHistoricalAndExpectedEffectiveRevision(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "reports/q3.txt",
		Kind: RevisionFormal, Content: []byte("before"),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("after"),
	})
	if err != nil {
		t.Fatal(err)
	}

	pair, err := fixture.service.OpenDiffPair(
		ctx,
		testDocumentOne,
		first.Revision.RevisionID,
		second.Revision.RevisionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer pair.Close()
	historical, err := io.ReadAll(pair.HistoricalContent)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := io.ReadAll(pair.EffectiveContent)
	if err != nil {
		t.Fatal(err)
	}
	if string(historical) != "before" || string(effective) != "after" {
		t.Fatalf("contents = %q, %q", historical, effective)
	}
	if pair.DocumentID != testDocumentOne ||
		pair.HistoricalRevision.RevisionID != first.Revision.RevisionID ||
		pair.EffectiveRevision.RevisionID != second.Revision.RevisionID {
		t.Fatalf("pair = %#v", pair)
	}
}

func TestOpenDiffPairAndPostAssertionReportStableStale(t *testing.T) {
	fixture := newHistoryFixture(t)
	ctx := context.Background()
	first, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne, Path: "reports/q3.txt",
		Kind: RevisionFormal, Content: []byte("before"),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(first.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("after"),
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := fixture.save(ctx, SaveRequest{
		Token: fixture.token, DocumentID: testDocumentOne,
		ExpectedEffectiveRevision: stringRef(second.Revision.RevisionID),
		Kind:                      RevisionFormal, Content: []byte("latest"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.OpenDiffPair(
		ctx,
		testDocumentOne,
		first.Revision.RevisionID,
		second.Revision.RevisionID,
	); !errors.Is(err, ErrEffectiveRevisionStale) {
		t.Fatalf("initial CAS error = %v", err)
	}
	if err := fixture.service.AssertEffectiveRevision(
		testDocumentOne,
		second.Revision.RevisionID,
	); !errors.Is(err, ErrEffectiveRevisionStale) {
		t.Fatalf("post CAS error = %v", err)
	}
	if err := fixture.service.AssertEffectiveRevision(
		testDocumentOne,
		third.Revision.RevisionID,
	); err != nil {
		t.Fatalf("current assertion = %v", err)
	}
}
