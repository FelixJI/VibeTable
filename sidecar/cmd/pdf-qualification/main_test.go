package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
