//go:build !race

package filehistory

// Single non-race capacity qualification record for Task415 AC4 with the
// frozen budgets: page/ReadTree <= 2s, Save/Open <= 30s wall, and at most
// 256 MiB Go TotalAlloc per measured operation. Fixture construction always
// stays outside the measured windows. Under -race only the correctness
// contracts in capacity_fixture_test.go run; wall budgets are deliberately
// not applied there. Evidence is written only under build/qa/415-capacity/.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

type capacityOperationRecord struct {
	Scale           string  `json:"scale"`
	Operation       string  `json:"operation"`
	Documents       int     `json:"documents"`
	Revisions       int64   `json:"revisions"`
	ElapsedMillis   float64 `json:"elapsedMillis"`
	TotalAllocBytes uint64  `json:"totalAllocBytes"`
	HeapAllocBefore uint64  `json:"heapAllocBefore"`
	HeapAllocAfter  uint64  `json:"heapAllocAfter"`
	Detail          string  `json:"detail,omitempty"`
}

type capacityQualificationRecord struct {
	RecordedAt string                    `json:"recordedAt"`
	GoVersion  string                    `json:"goVersion"`
	Race       bool                      `json:"race"`
	Budgets    map[string]string         `json:"budgets"`
	Notes      []string                  `json:"notes"`
	Operations []capacityOperationRecord `json:"operations"`
}

type capacityRecorder struct {
	operations []capacityOperationRecord
}

func (recorder *capacityRecorder) measure(
	t *testing.T,
	scale string,
	operation string,
	documents int,
	revisions int64,
	wallBudget time.Duration,
	detail string,
	run func() error,
) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	runErr := run()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if runErr != nil {
		t.Fatalf("%s %s failed: %v", scale, operation, runErr)
	}
	totalAlloc := after.TotalAlloc - before.TotalAlloc
	if elapsed > wallBudget {
		t.Errorf(
			"%s %s elapsed = %v, want <= %v",
			scale, operation, elapsed, wallBudget,
		)
	}
	if totalAlloc > capacityTotalAllocBudget {
		t.Errorf(
			"%s %s TotalAlloc = %d MiB, want <= %d MiB",
			scale, operation, totalAlloc>>20, capacityTotalAllocBudget>>20,
		)
	}
	recorder.append(t, capacityOperationRecord{
		Scale: scale, Operation: operation,
		Documents: documents, Revisions: revisions,
		ElapsedMillis:   float64(elapsed.Microseconds()) / 1000,
		TotalAllocBytes: totalAlloc,
		HeapAllocBefore: before.HeapAlloc,
		HeapAllocAfter:  after.HeapAlloc,
		Detail:          detail,
	})
}

// measureRejected times an over-limit save that must fail with the shared
// resource-limit sentinel before any repository commit or head publication.
func (recorder *capacityRecorder) measureRejected(
	t *testing.T,
	scale string,
	operation string,
	documents int,
	revisions int64,
	run func() (SaveResult, error),
) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	result, err := run()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("%s %s error = %v, want filehistory.resource_limit", scale, operation, err)
	}
	if result.Root != "" || result.MutationRevision != 0 || result.NoOp ||
		result.Document.DocumentID != "" || result.Revision.RevisionID != "" {
		t.Fatalf("%s %s returned a partial result %#v", scale, operation, result)
	}
	totalAlloc := after.TotalAlloc - before.TotalAlloc
	if elapsed > capacitySaveOpenBudget {
		t.Errorf(
			"%s %s elapsed = %v, want <= %v",
			scale, operation, elapsed, capacitySaveOpenBudget,
		)
	}
	if totalAlloc > capacityTotalAllocBudget {
		t.Errorf(
			"%s %s TotalAlloc = %d MiB, want <= %d MiB",
			scale, operation, totalAlloc>>20, capacityTotalAllocBudget>>20,
		)
	}
	recorder.append(t, capacityOperationRecord{
		Scale: scale, Operation: operation,
		Documents: documents, Revisions: revisions,
		ElapsedMillis:   float64(elapsed.Microseconds()) / 1000,
		TotalAllocBytes: totalAlloc,
		HeapAllocBefore: before.HeapAlloc,
		HeapAllocAfter:  after.HeapAlloc,
		Detail:          "rejected with filehistory.resource_limit before any repository commit",
	})
}

// measureCapacityPageWalk applies the per-page budget to every page of one
// full keyset walk and records the slowest page as the walk summary.
func (recorder *capacityRecorder) measureCapacityPageWalk(
	t *testing.T,
	scale string,
	documents int,
	revisions int64,
	service *Service,
	limit int,
) {
	t.Helper()
	seen := make(map[string]struct{}, documents)
	request := DocumentQueryRequest{
		Logic: "and",
		Sort:  []DocumentSort{{Field: "relativePath", Direction: "asc"}},
		Limit: limit,
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	walkStart := time.Now()
	var maxElapsed time.Duration
	var maxTotalAlloc uint64
	pages := 0
	for {
		var beforePage, afterPage runtime.MemStats
		runtime.ReadMemStats(&beforePage)
		start := time.Now()
		page, err := service.QueryDocuments(request)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&afterPage)
		if err != nil {
			t.Fatalf("%s page %d error = %v", scale, pages+1, err)
		}
		pageAlloc := afterPage.TotalAlloc - beforePage.TotalAlloc
		if elapsed > capacityPageWallBudget {
			t.Errorf(
				"%s page %d elapsed = %v, want <= %v",
				scale, pages+1, elapsed, capacityPageWallBudget,
			)
		}
		if pageAlloc > capacityTotalAllocBudget {
			t.Errorf(
				"%s page %d TotalAlloc = %d MiB, want <= %d MiB",
				scale, pages+1, pageAlloc>>20, capacityTotalAllocBudget>>20,
			)
		}
		if elapsed > maxElapsed {
			maxElapsed = elapsed
			maxTotalAlloc = pageAlloc
		}
		for _, document := range page.Documents {
			if _, duplicate := seen[document.DocumentID]; duplicate {
				t.Fatalf(
					"%s page %d repeated document %s",
					scale, pages+1, document.DocumentID,
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
	walkElapsed := time.Since(walkStart)
	runtime.ReadMemStats(&after)
	if len(seen) != documents {
		t.Fatalf(
			"%s page walk saw %d documents across %d pages, want %d",
			scale, len(seen), pages, documents,
		)
	}
	recorder.append(t, capacityOperationRecord{
		Scale: scale, Operation: "pageWalk",
		Documents: documents, Revisions: revisions,
		ElapsedMillis:   float64(maxElapsed.Microseconds()) / 1000,
		TotalAllocBytes: maxTotalAlloc,
		HeapAllocBefore: before.HeapAlloc,
		HeapAllocAfter:  after.HeapAlloc,
		Detail: fmt.Sprintf(
			"slowest of %d pages at limit %d; whole walk %v",
			pages, limit, walkElapsed,
		),
	})
}

func (recorder *capacityRecorder) append(
	t *testing.T,
	record capacityOperationRecord,
) {
	recorder.operations = append(recorder.operations, record)
	entry := &recorder.operations[len(recorder.operations)-1]
	t.Logf(
		"capacity %s %s: docs=%d revisions=%d elapsed=%.3fms totalAlloc=%dMiB heap=%dMiB->%dMiB %s",
		entry.Scale, entry.Operation, entry.Documents, entry.Revisions,
		entry.ElapsedMillis, entry.TotalAllocBytes>>20,
		entry.HeapAllocBefore>>20, entry.HeapAllocAfter>>20, entry.Detail,
	)
}

// capacityEvidenceDirectory locates the repository build directory without
// writing anywhere else; evidence only ever lands in build/qa/415-capacity/.
func capacityEvidenceDirectory() string {
	working, err := os.Getwd()
	if err != nil {
		return ""
	}
	root := filepath.Clean(filepath.Join(working, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "sidecar", "go.mod")); err != nil {
		return ""
	}
	return filepath.Join(root, "build", "qa", "415-capacity")
}

func (recorder *capacityRecorder) write(t *testing.T) {
	t.Helper()
	record := capacityQualificationRecord{
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
		GoVersion:  runtime.Version(),
		Race:       false,
		Budgets: map[string]string{
			"pageReadTreeWall":       capacityPageWallBudget.String(),
			"saveOpenWall":           capacitySaveOpenBudget.String(),
			"perOperationTotalAlloc": fmt.Sprintf("%d bytes", capacityTotalAllocBudget),
		},
		Notes: []string{
			"measurement seams: page = in-process Service.QueryDocuments; readTreeInspect = Service.Inspect snapshot clone only. Neither includes the workspacev2 fileHistory.readTree projection, JSON serialization, RPC transport, or WPF/WebView2 rendering",
			"scale-32 documents enter through the real Save product path (32 real Service.Save calls in both tests); it intentionally sits outside capacityQualificationSpecs, which only lists the derived roots",
			"larger roots are derived from one real validated save and published through the repository manifest contract, then opened via verified Open; derived-root preparation stays outside every measured window",
			"measured save = real Service.Save through the coordinator write gate with full-root marshal, repository.Commit, and head CAS",
			"over-limit rejection atomicity is checked against: Service.Head root+revision, RepositoryUsage (objects+manifests bytes), root manifest payload bytes via GetManifest, in-memory List() count, and a post-rejection page read",
			"the real-save accumulation path (thousands of sequential full-root Saves) was not executed to reach the caps; each Save durably stores a full root copy by design",
			"near-limit real WPF/WebView2 first-screen evidence is owned separately by the integration owner",
		},
		Operations: recorder.operations,
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	directory := capacityEvidenceDirectory()
	if directory == "" {
		t.Log("capacity qualification evidence directory not located; keeping the in-test record only")
		return
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Logf("capacity qualification evidence directory unavailable: %v", err)
		return
	}
	target := filepath.Join(directory, "capacity-qualification.json")
	if err := os.WriteFile(target, append(payload, '\n'), 0o644); err != nil {
		t.Logf("capacity qualification record write failed: %v", err)
		return
	}
	t.Logf("capacity qualification record written to %s", target)
}

func TestFileHistoryCapacityQualificationRecord(t *testing.T) {
	ctx := context.Background()
	recorder := &capacityRecorder{}

	// Scale 32: the genuine Save product path. The 32nd save, the verified
	// reopen, one page walk, and one readTreeInspect are measured.
	small := newHistoryFixture(t)
	smallFirst := uuid.NewString()
	var lastRoot objectrepo.ManifestID
	for index := range 32 {
		documentID := smallFirst
		if index != 0 {
			documentID = uuid.NewString()
		}
		request := SaveRequest{
			Token:      small.token,
			DocumentID: documentID,
			Path:       fmt.Sprintf("capacity/s32/doc-%08d.txt", index),
			Kind:       RevisionFormal,
			Content:    []byte(fmt.Sprintf("capacity-s32-content-%08d", index)),
		}
		var saved SaveResult
		if index == 31 {
			recorder.measure(t, "s32", "save", 31, 31,
				capacitySaveOpenBudget,
				"32nd real save through the coordinator write gate",
				func() error {
					result, err := small.save(ctx, request)
					saved = result
					return err
				},
			)
		} else {
			result, err := small.save(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			saved = result
		}
		lastRoot = saved.Root
	}
	var smallService *Service
	recorder.measure(t, "s32", "open", 32, 32,
		capacitySaveOpenBudget,
		"verified reopen of the durable current root",
		func() error {
			service, err := Open(ctx, small.repository, small.coordinator, lastRoot)
			smallService = service
			return err
		},
	)
	recorder.measureCapacityPageWalk(t, "s32", 32, 32, smallService, capacityPageLimitFocused)
	recorder.measure(t, "s32", "readTreeInspect", 32, 32,
		capacityPageWallBudget,
		"Service.Inspect snapshot clone only; excludes workspacev2 fileHistory.readTree projection, serialization, transport, and UI rendering",
		func() error {
			_, err := smallService.Inspect(smallFirst)
			return err
		},
	)

	for _, spec := range capacityQualificationSpecs() {
		t.Run(spec.name, func(t *testing.T) {
			fixture := newHistoryFixture(t)
			layout := buildCapacityRoot(t, fixture, spec)
			service := openCapacityService(t, fixture, layout)

			switch spec.name {
			case "s1000", "near-limit-9990":
				recorder.measure(t, spec.name, "open",
					layout.DocumentTotal, layout.RevisionTotal,
					capacitySaveOpenBudget,
					"verified open of the derived legal root",
					func() error {
						_, err := Open(ctx, fixture.repository, fixture.coordinator, layout.Root)
						return err
					},
				)
				recorder.measureCapacityPageWalk(t, spec.name,
					layout.DocumentTotal, layout.RevisionTotal,
					service, capacityPageLimitFocused,
				)
				readTreeDocument := layout.FirstMultiDocumentID
				recorder.measure(t, spec.name, "readTreeInspect",
					layout.DocumentTotal, layout.RevisionTotal,
					capacityPageWallBudget,
					"Service.Inspect snapshot clone of the multi-version document only",
					func() error {
						_, err := service.Inspect(readTreeDocument)
						return err
					},
				)
				var grown SaveResult
				recorder.measure(t, spec.name, "save",
					layout.DocumentTotal, layout.RevisionTotal,
					capacitySaveOpenBudget,
					"real Service.Save: coordinator write gate + full-root marshal + repository.Commit + head CAS; derived-root preparation stays outside the window",
					func() error {
						result, err := fixture.saveOnService(ctx, service, SaveRequest{
							Token:      fixture.token,
							DocumentID: uuid.NewString(),
							Path:       fmt.Sprintf("capacity/%s/grow.txt", spec.name),
							Kind:       RevisionFormal,
							Content:    []byte(spec.name + "-grown-content"),
						})
						grown = result
						return err
					},
				)
				recorder.measure(t, spec.name, "open",
					layout.DocumentTotal+1, layout.RevisionTotal+1,
					capacitySaveOpenBudget,
					"verified reopen of the new current version",
					func() error {
						_, err := Open(ctx, fixture.repository, fixture.coordinator, grown.Root)
						return err
					},
				)
			case "exact-revisions-10000":
				recorder.measure(t, spec.name, "open",
					layout.DocumentTotal, layout.RevisionTotal,
					capacitySaveOpenBudget,
					"verified open at the exact shared revision quota",
					func() error {
						_, err := Open(ctx, fixture.repository, fixture.coordinator, layout.Root)
						return err
					},
				)
				recorder.measure(t, spec.name, "page",
					layout.DocumentTotal, layout.RevisionTotal,
					capacityPageWallBudget,
					"single page at the exact quota",
					func() error {
						_, err := service.QueryDocuments(DocumentQueryRequest{
							Logic: "and", Limit: capacityPageLimitFocused,
						})
						return err
					},
				)
				recorder.measureRejected(t, spec.name, "saveRejected",
					layout.DocumentTotal, layout.RevisionTotal,
					func() (SaveResult, error) {
						return fixture.saveOnService(ctx, service, SaveRequest{
							Token:      fixture.token,
							DocumentID: layout.FirstMultiDocumentID,
							Kind:       RevisionFormal,
							Content:    []byte("capacity-overquota-revision"),
						})
					},
				)
				recorder.measureRejected(t, spec.name, "saveRejected",
					layout.DocumentTotal, layout.RevisionTotal,
					func() (SaveResult, error) {
						return fixture.saveOnService(ctx, service, SaveRequest{
							Token:      fixture.token,
							DocumentID: uuid.NewString(),
							Path:       fmt.Sprintf("capacity/%s/overflow.txt", spec.name),
							Kind:       RevisionFormal,
							Content:    []byte("capacity-overquota-document"),
						})
					},
				)
			case "exact-documents-10000":
				recorder.measure(t, spec.name, "open",
					layout.DocumentTotal, layout.RevisionTotal,
					capacitySaveOpenBudget,
					"verified open at the exact document cap",
					func() error {
						_, err := Open(ctx, fixture.repository, fixture.coordinator, layout.Root)
						return err
					},
				)
				recorder.measureCapacityPageWalk(t, spec.name,
					layout.DocumentTotal, layout.RevisionTotal,
					service, capacityPageLimitReduced,
				)
				recorder.measureRejected(t, spec.name, "saveRejected",
					layout.DocumentTotal, layout.RevisionTotal,
					func() (SaveResult, error) {
						return fixture.saveOnService(ctx, service, SaveRequest{
							Token:      fixture.token,
							DocumentID: uuid.NewString(),
							Path:       fmt.Sprintf("capacity/%s/overflow.txt", spec.name),
							Kind:       RevisionFormal,
							Content:    []byte("capacity-overlimit-document"),
						})
					},
				)
			case "depth-4096":
				recorder.measure(t, spec.name, "open",
					layout.DocumentTotal, layout.RevisionTotal,
					capacitySaveOpenBudget,
					"verified open of the maximum legal chain depth",
					func() error {
						_, err := Open(ctx, fixture.repository, fixture.coordinator, layout.Root)
						return err
					},
				)
				recorder.measure(t, spec.name, "readTreeInspect",
					layout.DocumentTotal, layout.RevisionTotal,
					capacityPageWallBudget,
					"Service.Inspect snapshot clone of the 4096-deep chain only; excludes projection, serialization, transport, and UI rendering",
					func() error {
						_, err := service.Inspect(layout.FirstMultiDocumentID)
						return err
					},
				)
				recorder.measureRejected(t, spec.name, "saveRejected",
					layout.DocumentTotal, layout.RevisionTotal,
					func() (SaveResult, error) {
						return fixture.saveOnService(ctx, service, SaveRequest{
							Token:      fixture.token,
							DocumentID: layout.FirstMultiDocumentID,
							Kind:       RevisionFormal,
							Content:    []byte("capacity-overdepth-revision"),
						})
					},
				)
			}
		})
	}

	recorder.write(t)
}
