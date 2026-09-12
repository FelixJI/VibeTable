package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
)

func TestCorpusReportRejectsStateMismatch(t *testing.T) {
	for _, expected := range []workspacesearch.ExtractionStatus{workspacesearch.ExtractionFailed, workspacesearch.ExtractionIndexed} {
		t.Run(string(expected), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "broken.pdf"), []byte("not a PDF"), 0o600); err != nil {
				t.Fatal(err)
			}
			definition := corpus{Version: 1, Budgets: corpusBudgets{64 << 20, 32 << 20, 256 << 20, 2_000_000, 30}, Cases: []corpusCase{{File: "broken.pdf", Tier: "MUST", ExpectedStatuses: []workspacesearch.ExtractionStatus{expected}, EmptyTextOnRejection: true}}}
			manifest, err := json.Marshal(definition)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "manifest.json")
			if err := os.WriteFile(path, manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = run(path, root, &output)
			mismatch := expected != workspacesearch.ExtractionFailed
			if (err != nil) != mismatch {
				t.Fatalf("run error = %v, mismatch = %v", err, mismatch)
			}
			var report struct {
				Failed int           `json:"failed"`
				Cases  []observation `json:"cases"`
			}
			if err := json.Unmarshal(output.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Cases) != 1 || report.Cases[0].Status != workspacesearch.ExtractionFailed || report.Cases[0].CodePoints != 0 || report.Cases[0].ErrorCode == nil || *report.Cases[0].ErrorCode != "extract.pdf_invalid" {
				t.Fatalf("actual extraction not preserved: %+v", report)
			}
			if (report.Failed == 1) != mismatch || (len(report.Cases[0].Mismatches) == 1) != mismatch {
				t.Fatalf("mismatch not recorded: %+v", report)
			}
		})
	}
}

func TestMillisecondsMustBeFiniteAndNonNegative(t *testing.T) {
	for _, value := range []float64{-0.1, math.Inf(1), math.Inf(-1), math.NaN()} {
		if finiteNonNegative(value) {
			t.Fatalf("accepted invalid milliseconds %v", value)
		}
	}
	if !finiteNonNegative(296.875) || !finiteNonNegative(0) {
		t.Fatal("rejected valid milliseconds")
	}
}

func TestObservationsRejectInvalidInputAndPreservesQualificationContract(t *testing.T) {
	int64Pointer := func(value int64) *int64 { return &value }
	float64Pointer := func(value float64) *float64 { return &value }
	stringPointer := func(value string) *string { return &value }
	failedCode := "extract.pdf_invalid"
	definition := corpus{
		Version: 1,
		Budgets: corpusBudgets{64 << 20, 32 << 20, 256 << 20, 2_000_000, 30},
		Cases: []corpusCase{
			{File: "broken.pdf", Tier: "MUST", ExpectedStatuses: []workspacesearch.ExtractionStatus{workspacesearch.ExtractionFailed, workspacesearch.ExtractionCancelled}, EmptyTextOnRejection: true},
			{File: "empty.pdf", Tier: "MUST", ExpectedStatuses: []workspacesearch.ExtractionStatus{workspacesearch.ExtractionNoTextLayer}, EmptyTextOnRejection: true},
		},
	}
	valid := externalObservations{Version: 1, Budgets: definition.Budgets, ProcessLimits: processLimits{MemoryBytes: 1 << 30, CPUSeconds: 30, DeadlineSeconds: 30}, Observations: []externalObservation{
		{File: "broken.pdf", Result: externalResult{Status: workspacesearch.ExtractionFailed, ErrorCode: &failedCode}, ElapsedMilliseconds: float64Pointer(12.125), CPUMilliseconds: float64Pointer(7.5), PeakJobMemoryBytes: int64Pointer(2048), PeakWorkerWorkingSetBytes: int64Pointer(4096), AllProcessesExited: true, WorkerReason: stringPointer("WorkerFailed")},
		{File: "empty.pdf", Result: externalResult{Status: workspacesearch.ExtractionNoTextLayer}, ElapsedMilliseconds: float64Pointer(3), CPUMilliseconds: float64Pointer(2), PeakJobMemoryBytes: int64Pointer(1024), PeakWorkerWorkingSetBytes: int64Pointer(2048), AllProcessesExited: true, WorkerReason: stringPointer("Succeeded")},
	}}

	write := func(t *testing.T, source externalObservations) (string, string) {
		t.Helper()
		root := t.TempDir()
		manifest, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(root, "manifest.json")
		if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		observationsPath := filepath.Join(root, "observations.json")
		if err := os.WriteFile(observationsPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return manifestPath, observationsPath
	}

	for _, test := range []struct {
		name string
		edit func(*externalObservations)
		want string
	}{
		{"missing file", func(source *externalObservations) { source.Observations = source.Observations[:1] }, "file set"},
		{"duplicate file", func(source *externalObservations) { source.Observations[1].File = "broken.pdf" }, "duplicate"},
		{"extraneous file", func(source *externalObservations) { source.Observations[1].File = "extra.pdf" }, "file set"},
		{"different budget", func(source *externalObservations) { source.Budgets.DeadlineSeconds++ }, "version or budgets"},
		{"invalid process limit", func(source *externalObservations) { source.ProcessLimits.CPUSeconds++ }, "process limits"},
		{"negative metric", func(source *externalObservations) { source.Observations[0].CPUMilliseconds = float64Pointer(-1) }, "metrics must be non-negative"},
		{"missing metrics", func(source *externalObservations) { source.Observations[0].WorkerReason = nil }, "metrics are required"},
		{"failed body", func(source *externalObservations) { source.Observations[0].Result.Text = "partial" }, "invalid failed worker result"},
		{"failed indexed", func(source *externalObservations) {
			source.Observations[0].Result.Status = workspacesearch.ExtractionIndexed
		}, "invalid failed worker result"},
		{"unclosed process", func(source *externalObservations) { source.Observations[0].AllProcessesExited = false }, "unclosed processes"},
		{"unknown status", func(source *externalObservations) { source.Observations[0].Result.Status = "invented" }, "unknown extraction status"},
		{"unknown worker reason", func(source *externalObservations) { source.Observations[0].WorkerReason = stringPointer("Other") }, "unknown worker reason"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := valid
			source.Observations = append([]externalObservation(nil), valid.Observations...)
			test.edit(&source)
			manifestPath, observationsPath := write(t, source)
			var output bytes.Buffer
			err := runObservations(manifestPath, observationsPath, &output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if output.Len() != 0 {
				t.Fatalf("invalid observations must not report: %s", &output)
			}
		})
	}

	manifestPath, observationsPath := write(t, valid)
	var output bytes.Buffer
	if err := runObservations(manifestPath, observationsPath, &output); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Failed        int            `json:"failed"`
		ProcessLimits *processLimits `json:"processLimits"`
		Cases         []observation  `json:"cases"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Failed != 0 || report.ProcessLimits == nil || *report.ProcessLimits != valid.ProcessLimits || len(report.Cases) != 2 || report.Cases[0].ElapsedMilliseconds != 12.125 || report.Cases[0].CPUMilliseconds == nil || *report.Cases[0].CPUMilliseconds != 7.5 || report.Cases[0].PeakJobMemoryBytes == nil || *report.Cases[0].PeakJobMemoryBytes != 2048 || report.Cases[0].PeakWorkerWorkingSetBytes == nil || *report.Cases[0].PeakWorkerWorkingSetBytes != 4096 || report.Cases[0].AllProcessesExited == nil || !*report.Cases[0].AllProcessesExited || report.Cases[0].WorkerReason == nil || *report.Cases[0].WorkerReason != "WorkerFailed" {
		t.Fatalf("metrics missing from report: %+v", report)
	}

	zeroMetrics := valid
	zeroMetrics.Observations = append([]externalObservation(nil), valid.Observations...)
	zeroMetrics.Observations[0].ElapsedMilliseconds = float64Pointer(0)
	zeroMetrics.Observations[0].CPUMilliseconds = float64Pointer(0)
	zeroMetrics.Observations[0].PeakJobMemoryBytes = int64Pointer(0)
	zeroMetrics.Observations[0].PeakWorkerWorkingSetBytes = int64Pointer(0)
	zeroMetrics.Observations[0].WorkerReason = stringPointer("Cancelled")
	zeroMetrics.Observations[0].Result.Status = workspacesearch.ExtractionCancelled
	zeroMetrics.Observations[0].Result.ErrorCode = nil
	manifestPath, observationsPath = write(t, zeroMetrics)
	output.Reset()
	if err := runObservations(manifestPath, observationsPath, &output); err != nil {
		t.Fatalf("explicit zero metrics must be accepted: %v", err)
	}
}

func TestCorpusCannotSilentlyChangeExtractorBudget(t *testing.T) {
	root := t.TempDir()
	definition := corpus{Version: 1, Budgets: corpusBudgets{64 << 20, 32 << 20, 256 << 20, 2_000_000, 31}, Cases: []corpusCase{{File: "absent.pdf", ExpectedStatuses: []workspacesearch.ExtractionStatus{workspacesearch.ExtractionFailed}}}}
	manifest, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = run(path, root, &output)
	if err == nil || err.Error() != "PDF corpus budgets differ from actual extractor limits" || output.Len() != 0 {
		t.Fatalf("budget mismatch must stop before extraction: error=%v, output=%s", err, &output)
	}
}

func TestCorpusReportChecksExactOutputAndErrorCode(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "broken.pdf"), []byte("not a PDF"), 0o600); err != nil {
			t.Fatal(err)
		}
		points, code := 0, "extract.pdf_invalid"
		if mismatch {
			points, code = 1, "extract.text_limit"
		}
		definition := map[string]any{
			"corpusVersion": 1,
			"budgets":       corpusBudgets{64 << 20, 32 << 20, 256 << 20, 2_000_000, 30},
			"cases":         []map[string]any{{"file": "broken.pdf", "expectedStatuses": []string{"failed"}, "expectedCodePoints": points, "expectedErrorCode": code}},
		}
		manifest, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "manifest.json")
		if err := os.WriteFile(path, manifest, 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err = run(path, root, &output)
		if (err != nil) != mismatch {
			t.Fatalf("exact output/error mismatch=%v, run error=%v", mismatch, err)
		}
		var report struct {
			Cases []observation `json:"cases"`
		}
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		expectedCount := 0
		if mismatch {
			expectedCount = 2
		}
		if len(report.Cases) != 1 || len(report.Cases[0].Mismatches) != expectedCount {
			t.Fatalf("missing exact assertions: %+v", report)
		}
	}
}

func TestCorpusReportRejectsSameLengthWrongText(t *testing.T) {
	root := t.TempDir()
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.7\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	content := "BT /F1 12 Tf 72 720 Td (BB) Tj ET"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f\n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n\n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	if err := os.WriteFile(filepath.Join(root, "text.pdf"), pdf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, character := range []string{"B", "C"} {
		definition := map[string]any{
			"corpusVersion": 1,
			"budgets":       corpusBudgets{64 << 20, 32 << 20, 256 << 20, 2_000_000, 30},
			"cases":         []map[string]any{{"file": "text.pdf", "expectedStatuses": []string{"indexed"}, "expectedCodePoints": 2, "expectedRepeatedCharacter": character}},
		}
		manifest, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "manifest.json")
		if err := os.WriteFile(path, manifest, 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err = run(path, root, &output)
		if (err != nil) != (character == "C") {
			t.Fatalf("wrong text must fail despite matching length: character=%s error=%v", character, err)
		}
	}
}
