package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQualificationEmitsTheCompleteReportForLaneEvidence(t *testing.T) {
	root := t.TempDir()
	outputPath := filepath.Join(root, "stdout.json")
	output, err := os.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = output
	t.Cleanup(func() {
		os.Stdout = previous
		_ = output.Close()
	})
	workRoot, err := qualificationWorkRoot()
	if err != nil {
		t.Fatal(err)
	}
	config := qualificationConfig{
		Profile: "pr", Records: 64, Files: 2, LogicalCorpusBytes: 8192,
		ReportPath: filepath.Join(root, "report.json"), WorkRoot: workRoot,
	}
	if err := validateConfig(config); err != nil {
		t.Fatal(err)
	}
	runErr := run(context.Background(), config)
	os.Stdout = previous
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	readReport := func(path string) qualificationReport {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var report qualificationReport
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatalf("qualification report missing from %s: %v", filepath.Base(path), err)
		}
		return report
	}
	emitted := readReport(outputPath)
	if emitted.SchemaVersion != 3 || emitted.Profile != "pr" || emitted.Records != 64 ||
		emitted.FileDocuments != 2 || emitted.MaterializedBytes != 8192 {
		t.Fatalf("unexpected qualification report: %#v", emitted)
	}
	// Even a small corpus can fail a size/performance budget. Its complete
	// diagnostics must survive without turning that failure into success.
	if (runErr == nil) != (len(emitted.Failures) == 0) {
		t.Fatalf("report failures %v disagree with command result %v", emitted.Failures, runErr)
	}
	if !reflect.DeepEqual(emitted, readReport(config.ReportPath)) {
		t.Fatal("lane stdout did not retain the complete generated qualification report")
	}
}

func TestMaterializedCorpusIsFullyWrittenAndFullyReadByProductExtractor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qualification.txt")
	digest, err := materializeCorpusFile(context.Background(), path, 128*1024, 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 128*1024 {
		t.Fatalf("materialized size = %d", info.Size())
	}
	text, extractedBytes, extractedDigest, status, err := extractCorpusFile(
		context.Background(), path,
	)
	if err != nil {
		t.Fatal(err)
	}
	if extractedBytes != info.Size() || extractedDigest != digest {
		t.Fatalf(
			"full read evidence = bytes %d/%d digest %s/%s",
			extractedBytes, info.Size(), extractedDigest, digest,
		)
	}
	if status != "truncated" || !strings.Contains(text, "needle-000042") {
		t.Fatalf("extraction = status %s text %q", status, text)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(raw)
	if digest != hex.EncodeToString(want[:]) ||
		!strings.HasSuffix(string(raw), "qualification-tail-000007\n") {
		t.Fatal("streamed file digest or tail marker is incomplete")
	}
}

func TestMaterializationAndExtractionHonorCancellation(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "cancelled.txt")
	if _, err := materializeCorpusFile(cancelled, path, 2<<20, 1, 1); err == nil {
		t.Fatal("cancelled materialization unexpectedly succeeded")
	}
	if err := os.WriteFile(path+".read", []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := extractCorpusFile(cancelled, path+".read"); err == nil {
		t.Fatal("cancelled extraction unexpectedly succeeded")
	}
}

func TestQualificationProfilesAndBudgetsAreFailClosed(t *testing.T) {
	if requiredLogicalCorpusBytes != int64(20)<<30 || maximumRSSBytes != uint64(1)<<30 {
		t.Fatalf("qualification scale changed: logical=%d rss=%d", requiredLogicalCorpusBytes, maximumRSSBytes)
	}
	if maximumRebuildDuration != 4*time.Minute {
		t.Fatalf("qualification rebuild budget changed: %v", maximumRebuildDuration)
	}
	if maximumFirstScreen != 300*time.Millisecond || maximumWarmP95.Milliseconds() != 150 ||
		maximumIncrementalP95.Seconds() != 2 {
		t.Fatalf(
			"qualification SLO changed: first=%v warm=%v incremental=%v",
			maximumFirstScreen,
			maximumWarmP95,
			maximumIncrementalP95,
		)
	}
	workRoot, err := qualificationWorkRoot()
	if err != nil {
		t.Fatal(err)
	}
	release := qualificationConfig{
		Profile: "release", Records: 100_000, Files: 10_000,
		LogicalCorpusBytes: requiredLogicalCorpusBytes, ReportPath: "report.json", WorkRoot: workRoot,
	}
	if err := validateConfig(release); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []qualificationConfig{
		{Profile: "release", Records: 1, Files: 1, LogicalCorpusBytes: 1024, ReportPath: "report.json", WorkRoot: workRoot},
		{Profile: "unknown", Records: 1, Files: 1, LogicalCorpusBytes: 1024, ReportPath: "report.json", WorkRoot: workRoot},
		{Profile: "pr", Records: 1, Files: 1, LogicalCorpusBytes: 1024},
		{Profile: "pr", Records: 1, Files: 1, LogicalCorpusBytes: 1024, ReportPath: "report.json", WorkRoot: t.TempDir()},
		{Profile: "pr", Records: 1, Files: 1, LogicalCorpusBytes: 1024, ReportPath: "report.json", WorkRoot: filepath.Join(workRoot, "..", "outside")},
	} {
		if err := validateConfig(invalid); err == nil {
			t.Fatalf("invalid config accepted: %#v", invalid)
		}
	}
	quick := qualificationConfig{
		Profile: "pr", Records: 10, Files: 2,
		LogicalCorpusBytes: 4096, ReportPath: "report.json", WorkRoot: workRoot,
	}
	if err := validateConfig(quick); err != nil {
		t.Fatal(err)
	}
}

func TestIndexRebuildBudgetExcludesRepresentativeWorkspacePreparation(t *testing.T) {
	materialization := 20 * time.Minute
	extraction := 45 * time.Second
	projection := 30 * time.Second

	rebuild := indexRebuildDuration(extraction, projection)

	if rebuild != 75*time.Second || rebuild >= materialization {
		t.Fatalf("index rebuild duration = %v", rebuild)
	}
}
