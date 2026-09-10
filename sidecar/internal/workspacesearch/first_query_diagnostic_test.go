//go:build firstquerydiagnostic

package workspacesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestFirstQueryQualificationDiagnostic(t *testing.T) {
	ctx := context.Background()
	sources := firstQueryDiagnosticSources(t)
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".ci", "project.json")); err != nil {
		t.Fatal("diagnostic must run from sidecar/internal/workspacesearch")
	}
	output := filepath.Join(repo, "build", "qa", "first-query-diagnostic")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	run, err := os.MkdirTemp(output, "sample-")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := Open(filepath.Join(run, "workspace-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	started := time.Now()
	if err := engine.RebuildProjection(ctx, sources, ProjectionCheckpoint{
		BusinessOutboxRowID: 100_000, FileHeadRevision: 10_000, MutationRevision: 110_000,
	}, nil); err != nil {
		t.Fatal(err)
	}
	rebuild := time.Since(started)
	queryRequest := request("needle-000042")
	// No Query, EXPLAIN, metadata probe, GC or profile between promotion and
	// this first query. The CI qualification's 300 ms contract is unchanged.
	started = time.Now()
	result, err := engine.Query(ctx, queryRequest)
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("first screen result invalid: hits=%d err=%v", len(result.Hits), err)
	}
	first := time.Since(started)
	if result.Hits[0].CanonicalId != "table-1:record-000042" {
		t.Fatalf("unexpected first hit: %s", result.Hits[0].CanonicalId)
	}
	warm := make([]time.Duration, 250)
	for index := range warm {
		queryRequest.Query = fmt.Sprintf("needle-%06d", index%len(sources))
		started = time.Now()
		if _, err := engine.Query(ctx, queryRequest); err != nil {
			t.Fatal(err)
		}
		warm[index] = time.Since(started)
	}
	sort.Slice(warm, func(left, right int) bool { return warm[left] < warm[right] })
	const budget = 300 * time.Millisecond
	p95 := warm[int(float64(len(warm)-1)*0.95)]
	report := struct {
		Kind                 string `json:"kind"`
		GeneratedAt          string `json:"generatedAt"`
		GoVersion            string `json:"goVersion"`
		Records              int    `json:"records"`
		Files                int    `json:"files"`
		FileBodyCodePoints   int    `json:"fileBodyCodePoints"`
		RebuildNanoseconds   int64  `json:"rebuildNanoseconds"`
		FirstNanoseconds     int64  `json:"firstNanoseconds"`
		FirstBudgetNs        int64  `json:"firstBudgetNanoseconds"`
		WarmP95Nanoseconds   int64  `json:"warmP95Nanoseconds"`
		Hits                 int    `json:"hits"`
		FirstScreenSloPassed bool   `json:"firstScreenSloPassed"`
	}{
		Kind:        "diagnostic-only-fresh-query-not-release-qualification",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), GoVersion: runtime.Version(),
		Records: 100_000, Files: 10_000, FileBodyCodePoints: 4096,
		RebuildNanoseconds: rebuild.Nanoseconds(), FirstNanoseconds: first.Nanoseconds(),
		FirstBudgetNs: budget.Nanoseconds(), WarmP95Nanoseconds: p95.Nanoseconds(),
		Hits: len(result.Hits), FirstScreenSloPassed: first <= budget,
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "report.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("report=%s first=%s firstNs=%d warmP95=%s rebuild=%s", run, first, first.Nanoseconds(), p95, rebuild)
	if first > budget {
		t.Errorf("first_screen_slo: %s > %s", first, budget)
	}
}
