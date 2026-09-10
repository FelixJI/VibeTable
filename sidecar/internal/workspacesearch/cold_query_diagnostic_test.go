package workspacesearch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticFirstQueryAfterRebuild(t *testing.T) {
	root := filepath.Join("..", "..", "..", "build", "qa", "cold-query-diagnostic")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	runRoot, err := os.MkdirTemp(root, "run-")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(runRoot, "workspace-search.db")
	engine, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if engine != nil {
			_ = engine.Close()
		}
	}()
	ctx := context.Background()
	sources := make([]SourceDocument, 110000)
	for index := range sources {
		kind := "record"
		if index >= 100000 {
			kind = "file"
		}
		sources[index] = source(kind, fmt.Sprintf("source-%06d", index), "revision-1",
			fmt.Sprintf("needle-%06d", index), strings.Repeat("searchable document body ", 18), true)
	}
	started := time.Now()
	if err := engine.RebuildProjection(ctx, sources, ProjectionCheckpoint{
		BusinessOutboxRowID: 100000, FileHeadRevision: 10000, MutationRevision: 110000,
	}, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("index=%s rebuild=%s gomaxprocs=%d", dbPath, time.Since(started), runtime.GOMAXPROCS(0))
	maximum := time.Duration(0)
	for attempt := 0; attempt < 30; attempt++ {
		if attempt > 0 {
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			engine, err = Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
		}
		started = time.Now()
		result, err := engine.Query(ctx, request("needle-000042"))
		elapsed := time.Since(started)
		if err != nil || len(result.Hits) != 1 {
			t.Fatalf("query: hits=%d err=%v", len(result.Hits), err)
		}
		t.Logf("attempt=%d firstQuery=%s", attempt, elapsed)
		if elapsed > maximum {
			maximum = elapsed
		}
		if elapsed > 300*time.Millisecond {
			t.Errorf("first-screen SLO exceeded: %s > 300ms", elapsed)
		}
	}
	t.Logf("maximum=%s", maximum)
}

// Local diagnostic only: reuse an existing diagnostic index in a fresh process.
func TestDiagnosticFreshProcessQuery(t *testing.T) {
	dbPath := os.Getenv("VIBETABLE_DIAGNOSTIC_SEARCH_INDEX")
	if dbPath == "" {
		t.Fatal("VIBETABLE_DIAGNOSTIC_SEARCH_INDEX is required")
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatal(err)
	}
	engine, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for attempt := 0; attempt < 2; attempt++ {
		started := time.Now()
		result, err := engine.Query(context.Background(), request("needle-000042"))
		elapsed := time.Since(started)
		if err != nil || len(result.Hits) != 1 {
			t.Fatalf("query: hits=%d err=%v", len(result.Hits), err)
		}
		t.Logf("pid=%d attempt=%d query=%s gomaxprocs=%d", os.Getpid(), attempt, elapsed, runtime.GOMAXPROCS(0))
		if elapsed > 300*time.Millisecond {
			t.Errorf("first-screen SLO exceeded: %s > 300ms", elapsed)
		}
	}
}
