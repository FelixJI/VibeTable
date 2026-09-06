package main

import (
	"bytes"
	"encoding/json"
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
