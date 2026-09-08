package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
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
		Profile: "pr", Records: 64, Files: 6, LogicalCorpusBytes: 16384,
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
		var report qualificationReport
		if err == nil {
			err = json.Unmarshal(raw, &report)
		}
		if err != nil {
			if runErr != nil {
				t.Fatalf("qualification returned before writing the report: %v", runErr)
			}
			t.Fatalf("qualification report missing from %s: %v", filepath.Base(path), err)
		}
		return report
	}
	emitted := readReport(outputPath)
	if emitted.SchemaVersion != 4 || emitted.Profile != "pr" || emitted.Records != 64 ||
		emitted.FileDocuments != 6 || emitted.LogicalCorpusBytes != 16384 ||
		emitted.MaterializedBytes != 16384 || emitted.ExtractedInputBytes != 16384 ||
		emitted.PDFDocuments != 4 || emitted.PDFIndexed != 2 || emitted.PDFNoText != 1 ||
		emitted.PDFResourceLimited != 1 {
		t.Fatalf("unexpected qualification report: %#v", emitted)
	}
	raw, err := os.ReadFile(config.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"pdfDocuments", "pdfIndexed", "pdfNoText", "pdfResourceLimited",
	} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("qualification report is missing %q: %s", field, raw)
		}
	}
	// This tiny corpus intentionally exceeds the fixed index-size multiplier.
	// Keep a real failing command so success-only output cannot satisfy the test.
	if runErr == nil || !slices.Contains(emitted.Failures, "index_multiplier_budget") {
		t.Fatalf("expected index budget failure, got failures=%v error=%v", emitted.Failures, runErr)
	}
	for _, failure := range []string{"extractor_corpus_incomplete", "pdf_fixture_status_mismatch", "corpus_bytes_incomplete"} {
		if slices.Contains(emitted.Failures, failure) {
			t.Fatalf("unexpected deterministic semantic failure %q: %v", failure, emitted.Failures)
		}
	}
	if !reflect.DeepEqual(emitted, readReport(config.ReportPath)) {
		t.Fatal("lane stdout did not retain the complete generated qualification report")
	}
}

func TestPDFStatusMismatchFailsQualificationWhenAggregateCountsMatch(t *testing.T) {
	noTextCode := "extract.pdf_no_text"
	summary := extractionSummary{Documents: 4, Indexed: 2}
	expected := []workspacesearch.ExtractionStatus{
		workspacesearch.ExtractionIndexed, workspacesearch.ExtractionIndexed,
		workspacesearch.ExtractionNoTextLayer, workspacesearch.ExtractionResourceLimited,
	}
	actual := []workspacesearch.ExtractionResult{
		{Status: workspacesearch.ExtractionNoTextLayer, ErrorCode: &noTextCode},
		{Status: workspacesearch.ExtractionIndexed},
		{Status: workspacesearch.ExtractionIndexed},
		{Status: workspacesearch.ExtractionResourceLimited},
	}
	for index := range expected {
		recordPDFExtraction(&summary, index, expected[index], actual[index])
	}
	if summary.PDFDocuments != 4 || summary.PDFIndexed != 2 || summary.PDFNoText != 1 ||
		summary.PDFResourceLimited != 1 {
		t.Fatalf("swapped status aggregate changed: %#v", summary)
	}
	report := qualificationReport{}
	appendExtractionFailures(&report, summary, 4)
	diagnostic := qualificationFailureMessage(report.Failures, summary.PDFStatusMismatchDetails)
	if !slices.Contains(report.Failures, "pdf_fixture_status_mismatch") ||
		!slices.Contains(report.Failures, "extractor_corpus_incomplete") ||
		len(summary.PDFStatusMismatchDetails) != 2 ||
		!strings.Contains(diagnostic, "fixture=0 expected=indexed actual=noTextLayer errorCode="+noTextCode) ||
		!strings.Contains(diagnostic, "fixture=2 expected=noTextLayer actual=indexed errorCode=<nil>") {
		t.Fatalf("swapped statuses did not expose qualification diagnostics: %s", diagnostic)
	}
	if strings.Contains(qualificationFailureMessage([]string{"index_multiplier_budget"}, nil), "pdfMismatchDetails") {
		t.Fatal("normal qualification failure contains empty PDF mismatch noise")
	}
}

func TestPDFQualificationDocumentsHaveDeterministicValidStructure(t *testing.T) {
	for _, test := range []struct {
		name, content string
		flate         bool
	}{
		{"plain", "BT /F1 12 Tf (plain PDF) Tj ET", false},
		{"Flate", "BT /F1 12 Tf (compressed PDF) Tj ET", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, err := pdfQualificationDocument([]byte(test.content), test.flate)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := pdfQualificationDocument([]byte(test.content), test.flate)
			if err != nil || !bytes.Equal(document, repeated) {
				t.Fatalf("PDF generation is not deterministic: %v", err)
			}
			if bytes.Contains(document, []byte("/Filter /FlateDecode")) != test.flate {
				t.Fatalf("Flate marker mismatch: %q", document)
			}
			if err := validateQualificationPDFStructure(document); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func validateQualificationPDFStructure(document []byte) error {
	if !bytes.HasPrefix(document, []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")) ||
		!bytes.HasSuffix(document, []byte("%%EOF\n")) {
		return fmt.Errorf("PDF header or EOF is invalid")
	}
	startMarker := []byte("startxref\n")
	start := bytes.LastIndex(document, startMarker)
	if start < 0 {
		return fmt.Errorf("startxref is missing")
	}
	xrefOffset, err := strconv.Atoi(strings.TrimSpace(string(
		document[start+len(startMarker) : len(document)-len("%%EOF\n")],
	)))
	if err != nil || xrefOffset < 0 || xrefOffset >= start ||
		!bytes.HasPrefix(document[xrefOffset:], []byte("xref\n")) {
		return fmt.Errorf("startxref is invalid")
	}
	trailer := bytes.Index(document[xrefOffset:start], []byte("trailer\n"))
	if trailer < 0 {
		return fmt.Errorf("trailer is missing")
	}
	trailer += xrefOffset
	if !bytes.Equal(document[trailer:start], []byte("trailer\n<< /Size 6 /Root 1 0 R >>\n")) {
		return fmt.Errorf("trailer is invalid")
	}
	lines := strings.Split(string(document[xrefOffset:trailer]), "\n")
	if len(lines) != 9 || lines[0] != "xref" || lines[1] != "0 6" ||
		lines[2] != "0000000000 65535 f\r" || lines[8] != "" {
		return fmt.Errorf("xref table is invalid: %q", lines)
	}
	for objectID, line := range lines[3:8] {
		if len(line) != 19 || line[10:] != " 00000 n\r" {
			return fmt.Errorf("xref entry %d is invalid", objectID+1)
		}
		offset, err := strconv.Atoi(line[:10])
		if err != nil || offset >= xrefOffset ||
			!bytes.HasPrefix(document[offset:], []byte(fmt.Sprintf("%d 0 obj\n", objectID+1))) {
			return fmt.Errorf("xref entry %d points outside its object", objectID+1)
		}
	}
	streamMarker := []byte("\nstream\n")
	stream := bytes.Index(document[:xrefOffset], streamMarker)
	length := bytes.LastIndex(document[:stream], []byte("/Length "))
	if stream < 0 || length < 0 {
		return fmt.Errorf("content stream or Length is missing")
	}
	var declared int
	if _, err := fmt.Sscanf(string(document[length:stream]), "/Length %d", &declared); err != nil {
		return fmt.Errorf("stream Length is invalid: %w", err)
	}
	stream += len(streamMarker)
	if declared < 0 || stream+declared > xrefOffset ||
		!bytes.HasPrefix(document[stream+declared:], []byte("\nendstream\nendobj\n")) {
		return fmt.Errorf("stream does not match its declared Length")
	}
	return nil
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
	extraction, extractedBytes, extractedDigest, err := extractCorpusFile(
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
	if extraction.Status != "truncated" || !strings.Contains(extraction.Text, "needle-000042") {
		t.Fatalf("extraction = %#v", extraction)
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
	if _, _, _, err := extractCorpusFile(cancelled, path+".read"); err == nil {
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
		Profile: "pr", Records: 10, Files: 5,
		LogicalCorpusBytes: 8192, ReportPath: "report.json", WorkRoot: workRoot,
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
