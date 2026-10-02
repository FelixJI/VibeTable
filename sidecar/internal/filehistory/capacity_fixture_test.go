package filehistory

// Legal capacity fixtures for the frozen shared root limits
// (MaxRootDocuments=10_000, MaxRootRevisions=10_000,
// MaxRevisionChainDepth=4_096). Every derived root starts from one real
// validated save (validatedRootFixture) and is published through the same
// repository manifest contract the service uses, so Open re-verifies the
// same invariants and object closure as the product path. No second storage
// authority, hashing scheme, or bulk import of real data is introduced; the
// old in-memory 10_000x8 direct map injection is not a legal root and is not
// used here.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

const (
	capacityPageLimitFocused = 50
	capacityPageLimitReduced = 500
	capacityTotalAllocBudget = 256 << 20
	capacityPageWallBudget   = 2 * time.Second
	capacitySaveOpenBudget   = 30 * time.Second
)

// capacitySpec describes one legal root shape. Single-revision documents
// exercise the breadth axis, while a handful of multi-version documents
// consume the same shared revision quota to prove the quota is not a
// per-document allowance.
type capacitySpec struct {
	name       string
	singleDocs int
	multiDocs  int
	multiChain int
}

func (spec capacitySpec) documentTotal() int {
	return spec.singleDocs + spec.multiDocs
}

func (spec capacitySpec) revisionTotal() int64 {
	return int64(spec.singleDocs) +
		int64(spec.multiDocs)*int64(spec.multiChain)
}

// capacityQualificationSpecs is the single frozen scale table for the
// derived legal roots. Scale 32 intentionally lives outside this table in
// both tests because it is built through the genuine Save product path
// (32 real Service.Save calls), not through a derived payload root.
func capacityQualificationSpecs() []capacitySpec {
	return []capacitySpec{
		{name: "s1000", singleDocs: 999, multiDocs: 1, multiChain: 5},
		{name: "near-limit-9990", singleDocs: 9_979, multiDocs: 1, multiChain: 11},
		{name: "exact-revisions-10000", singleDocs: 9_950, multiDocs: 5, multiChain: 10},
		{name: "exact-documents-10000", singleDocs: 10_000, multiDocs: 0, multiChain: 0},
		{name: "depth-4096", singleDocs: 0, multiDocs: 1, multiChain: MaxRevisionChainDepth},
	}
}

type capacityLayout struct {
	Root                  objectrepo.ManifestID
	DocumentTotal         int
	RevisionTotal         int64
	FirstSingleDocumentID string
	FirstMultiDocumentID  string
}

// appendCapacityDocument clones the validated base document into one legal
// document with a linear formal chain. Document and revision identifiers use
// the repository's existing uuid.NewString generator and the actual IDs are
// handed back through the built documents (captured in capacityLayout);
// deterministic name-based UUID derivation is not needed because nothing
// recomputes these fixture identifiers.
func appendCapacityDocument(
	documents []Document,
	base Document,
	chain int,
	relativePath string,
) []Document {
	documentID := uuid.NewString()
	baseRevision := base.Revisions[0]
	revisions := make([]Revision, chain)
	var parentID *string
	for chainIndex := range chain {
		revision := baseRevision
		revision.RevisionID = uuid.NewString()
		revision.DocumentID = documentID
		revision.ParentRevisionID = parentID
		revision.RestoredFromRevisionID = nil
		revision.LocalSequence = nil
		revision.Comment = nil
		revision.RevisionOrdinal = uint64(chainIndex + 1)
		revision.FormalVersion = uint64Pointer(uint64(chainIndex + 1))
		revision.CreatedAt = baseRevision.CreatedAt.Add(
			time.Duration(chainIndex) * time.Nanosecond,
		)
		revisionID := revision.RevisionID
		parentID = &revisionID
		revisions[chainIndex] = revision
	}
	document := base
	document.DocumentID = documentID
	document.RelativePath = relativePath
	document.EffectiveRevisionID = revisions[chain-1].RevisionID
	document.NextRevisionOrdinal = uint64(chain) + 1
	document.NextFormalVersion = uint64(chain) + 1
	document.Revisions = revisions
	return append(documents, document)
}

// buildCapacityRoot publishes one legal root manifest derived from the
// fixture's real validated save. All synthetic revisions reference the object
// that the real save durably committed, so repository verification during
// Open reads real committed bytes instead of a parallel storage path.
func buildCapacityRoot(
	t *testing.T,
	fixture historyFixture,
	spec capacitySpec,
) capacityLayout {
	t.Helper()
	if spec.documentTotal() == 0 ||
		spec.documentTotal() > MaxRootDocuments ||
		spec.revisionTotal() > MaxRootRevisions ||
		spec.multiChain > MaxRevisionChainDepth ||
		spec.multiDocs < 0 {
		t.Fatalf("illegal capacity spec %#v", spec)
	}
	validated := validatedRootFixture(t, fixture)
	base := validated.Documents[0]
	documents := make([]Document, 0, spec.documentTotal())
	layout := capacityLayout{
		DocumentTotal: spec.documentTotal(),
		RevisionTotal: spec.revisionTotal(),
	}
	for index := range spec.singleDocs {
		documents = appendCapacityDocument(
			documents, base, 1,
			fmt.Sprintf("capacity/%s/single-%08d.txt", spec.name, index),
		)
		if index == 0 {
			layout.FirstSingleDocumentID = documents[len(documents)-1].DocumentID
		}
	}
	for index := range spec.multiDocs {
		documents = appendCapacityDocument(
			documents, base, spec.multiChain,
			fmt.Sprintf("capacity/%s/multi-%08d.txt", spec.name, index),
		)
		if index == 0 {
			layout.FirstMultiDocumentID = documents[len(documents)-1].DocumentID
		}
	}
	payload, err := json.Marshal(rootPayload{
		FormatVersion: rootFormatVersion,
		WorkspaceID:   fixture.token.WorkspaceID,
		Documents:     documents,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fixture.repository.Commit(context.Background(),
		objectrepo.CommitRequest{
			Authority: fixture.token.Authority(),
			Manifests: []objectrepo.ManifestInput{{
				Name: "filehistory-root",
				Labels: map[string]string{
					"type":        "filehistory-root",
					"workspaceId": fixture.token.WorkspaceID,
				},
				Payload: payload,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	layout.Root = receipt.Manifests["filehistory-root"]
	if layout.Root == "" {
		t.Fatal("capacity root manifest missing")
	}
	return layout
}

func openCapacityServiceAtRoot(
	t *testing.T,
	fixture historyFixture,
	root objectrepo.ManifestID,
	expectDocuments int,
) *Service {
	t.Helper()
	service, err := Open(
		context.Background(),
		fixture.repository,
		fixture.coordinator,
		root,
	)
	if err != nil {
		t.Fatalf("open capacity root %s: %v", root, err)
	}
	if listed := len(service.List()); listed != expectDocuments {
		t.Fatalf("capacity root %s lists %d documents, want %d", root, listed, expectDocuments)
	}
	return service
}

func openCapacityService(
	t *testing.T,
	fixture historyFixture,
	layout capacityLayout,
) *Service {
	t.Helper()
	return openCapacityServiceAtRoot(t, fixture, layout.Root, layout.DocumentTotal)
}

// saveOnService mirrors the shared fixture save defaults for a service that
// was opened separately from fixture.service.
func (fixture historyFixture) saveOnService(
	ctx context.Context,
	service *Service,
	request SaveRequest,
) (SaveResult, error) {
	request.MimeType = "text/plain"
	request.CreatedBy = "capacity-qualification"
	request.DeviceID = testDeviceID
	return service.Save(ctx, request)
}

type capacityStateSnapshot struct {
	root          objectrepo.ManifestID
	headRevision  uint64
	documentCount int
	usage         uint64
	rootPayload   []byte
}

func snapshotCapacityState(
	t *testing.T,
	ctx context.Context,
	fixture historyFixture,
	service *Service,
) capacityStateSnapshot {
	t.Helper()
	root, headRevision := service.Head()
	usage, err := fixture.repository.RepositoryUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.repository.GetManifest(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	return capacityStateSnapshot{
		root:          root,
		headRevision:  headRevision,
		documentCount: len(service.List()),
		usage:         usage,
		rootPayload:   record.Payload,
	}
}

// requireRejectedCapacitySave proves an over-limit save surfaces the shared
// resource-limit sentinel and leaves the published head, the durable
// repository content, and the in-memory projection completely untouched:
// neither the head nor any immutable history manifest is partially
// committed, and reads keep working after the rejection.
func requireRejectedCapacitySave(
	t *testing.T,
	ctx context.Context,
	fixture historyFixture,
	service *Service,
	request SaveRequest,
) {
	t.Helper()
	before := snapshotCapacityState(t, ctx, fixture, service)
	result, err := fixture.saveOnService(ctx, service, request)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("over-limit save error = %v, want filehistory.resource_limit", err)
	}
	if result.Root != "" || result.MutationRevision != 0 || result.NoOp ||
		result.Document.DocumentID != "" || result.Revision.RevisionID != "" {
		t.Fatalf("rejected save returned a partial result %#v", result)
	}
	after := snapshotCapacityState(t, ctx, fixture, service)
	if after.root != before.root ||
		after.headRevision != before.headRevision ||
		after.documentCount != before.documentCount ||
		after.usage != before.usage ||
		!bytes.Equal(after.rootPayload, before.rootPayload) {
		t.Fatalf(
			"rejected save mutated state: root %q->%q head %d->%d documents %d->%d usage %d->%d",
			before.root, after.root,
			before.headRevision, after.headRevision,
			before.documentCount, after.documentCount,
			before.usage, after.usage,
		)
	}
	if _, pageErr := service.QueryDocuments(DocumentQueryRequest{
		Logic: "and", Limit: capacityPageLimitFocused,
	}); pageErr != nil {
		t.Fatalf("page read after rejected save failed: %v", pageErr)
	}
}

// requireCapacityPageWalk pages through the complete bounded projection with
// keyset cursors and proves no document is skipped or repeated at the scale.
func requireCapacityPageWalk(
	t *testing.T,
	service *Service,
	expectDocuments int,
	limit int,
) {
	t.Helper()
	seen := make(map[string]struct{}, expectDocuments)
	request := DocumentQueryRequest{
		Logic: "and",
		Sort:  []DocumentSort{{Field: "relativePath", Direction: "asc"}},
		Limit: limit,
	}
	pages := 0
	for {
		page, err := service.QueryDocuments(request)
		if err != nil {
			t.Fatalf("capacity page %d error = %v", pages+1, err)
		}
		if page.TopologyRevision == 0 {
			t.Fatalf("capacity page %d omitted topology revision", pages+1)
		}
		for _, document := range page.Documents {
			if _, duplicate := seen[document.DocumentID]; duplicate {
				t.Fatalf(
					"capacity page %d repeated document %s",
					pages+1, document.DocumentID,
				)
			}
			seen[document.DocumentID] = struct{}{}
		}
		pages++
		if page.NextCursor == nil {
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(seen) != expectDocuments {
		t.Fatalf(
			"capacity page walk saw %d documents across %d pages, want %d",
			len(seen), pages, expectDocuments,
		)
	}
}

// TestFileHistoryCapacityContractsHoldOnLegalRoots runs the AC4 correctness
// contracts on every legal scale without any wall-clock budget so the same
// assertions hold under the race detector.
func TestFileHistoryCapacityContractsHoldOnLegalRoots(t *testing.T) {
	ctx := context.Background()

	// Scale 32 exercises the genuine Save product path: every document
	// enters through the coordinator write gate with a real object commit
	// and head CAS publication before the derived scales take over.
	small := newHistoryFixture(t)
	smallFirst := uuid.NewString()
	var lastRoot objectrepo.ManifestID
	for index := range 32 {
		documentID := smallFirst
		if index != 0 {
			documentID = uuid.NewString()
		}
		saved, err := small.save(ctx, SaveRequest{
			Token:      small.token,
			DocumentID: documentID,
			Path:       fmt.Sprintf("capacity/s32/doc-%08d.txt", index),
			Kind:       RevisionFormal,
			Content:    []byte(fmt.Sprintf("capacity-s32-content-%08d", index)),
		})
		if err != nil {
			t.Fatal(err)
		}
		lastRoot = saved.Root
	}
	smallReopened := openCapacityServiceAtRoot(t, small, lastRoot, 32)
	requireCapacityPageWalk(t, smallReopened, 32, capacityPageLimitFocused)
	smallDocument, err := smallReopened.Inspect(smallFirst)
	if err != nil || len(smallDocument.Revisions) != 1 ||
		smallDocument.EffectiveRevisionID != smallDocument.Revisions[0].RevisionID {
		t.Fatalf("s32 reopened document = %#v, err = %v", smallDocument, err)
	}

	for _, spec := range capacityQualificationSpecs() {
		t.Run(spec.name, func(t *testing.T) {
			fixture := newHistoryFixture(t)
			layout := buildCapacityRoot(t, fixture, spec)
			service := openCapacityService(t, fixture, layout)
			requireCapacityPageWalk(
				t, service, layout.DocumentTotal, capacityPageLimitReduced,
			)

			if spec.multiDocs > 0 {
				document, inspectErr := service.Inspect(layout.FirstMultiDocumentID)
				if inspectErr != nil ||
					len(document.Revisions) != spec.multiChain ||
					document.NextRevisionOrdinal != uint64(spec.multiChain)+1 {
					t.Fatalf(
						"%s multi-version document = %#v, err = %v",
						spec.name, document, inspectErr,
					)
				}
			}

			switch spec.name {
			case "s1000", "near-limit-9990":
				// A legal save must still commit close to the cap, and the
				// new current version must reopen from the durable root.
				grown, growErr := fixture.saveOnService(ctx, service, SaveRequest{
					Token:      fixture.token,
					DocumentID: uuid.NewString(),
					Path:       fmt.Sprintf("capacity/%s/grow.txt", spec.name),
					Kind:       RevisionFormal,
					Content:    []byte(spec.name + "-grown-content"),
				})
				if growErr != nil {
					t.Fatal(growErr)
				}
				reopened := openCapacityServiceAtRoot(
					t, fixture, grown.Root, layout.DocumentTotal+1,
				)
				requireCapacityPageWalk(
					t, reopened, layout.DocumentTotal+1, capacityPageLimitReduced,
				)
			case "exact-revisions-10000":
				// The revision quota is shared across the whole root: one
				// more revision anywhere exceeds it even though the
				// document count is still below its own cap.
				requireRejectedCapacitySave(t, ctx, fixture, service, SaveRequest{
					Token:      fixture.token,
					DocumentID: layout.FirstMultiDocumentID,
					Kind:       RevisionFormal,
					Content:    []byte("capacity-overquota-revision"),
				})
				requireRejectedCapacitySave(t, ctx, fixture, service, SaveRequest{
					Token:      fixture.token,
					DocumentID: uuid.NewString(),
					Path:       fmt.Sprintf("capacity/%s/overflow.txt", spec.name),
					Kind:       RevisionFormal,
					Content:    []byte("capacity-overquota-document"),
				})
			case "exact-documents-10000":
				requireRejectedCapacitySave(t, ctx, fixture, service, SaveRequest{
					Token:      fixture.token,
					DocumentID: uuid.NewString(),
					Path:       fmt.Sprintf("capacity/%s/overflow.txt", spec.name),
					Kind:       RevisionFormal,
					Content:    []byte("capacity-overlimit-document"),
				})
			case "depth-4096":
				requireRejectedCapacitySave(t, ctx, fixture, service, SaveRequest{
					Token:      fixture.token,
					DocumentID: layout.FirstMultiDocumentID,
					Kind:       RevisionFormal,
					Content:    []byte("capacity-overdepth-revision"),
				})
				document, inspectErr := service.Inspect(layout.FirstMultiDocumentID)
				if inspectErr != nil ||
					len(document.Revisions) != MaxRevisionChainDepth {
					t.Fatalf(
						"depth document after rejection = %#v, err = %v",
						document, inspectErr,
					)
				}
			}
		})
	}
}
