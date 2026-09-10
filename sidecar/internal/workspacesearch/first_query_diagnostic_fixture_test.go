//go:build firstquerydiagnostic

package workspacesearch

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

// Equivalent index inputs only; skipped 20 GiB IO is not release qualification.
func firstQueryDiagnosticSources(t *testing.T) []SourceDocument {
	t.Helper()
	const records, files = 100_000, 10_000
	const logicalBytes = int64(20) << 30
	result := make([]SourceDocument, 0, records+files)
	for index := range records + files {
		kind := "record"
		canonicalID := fmt.Sprintf("table-1:record-%06d", index)
		tableID, recordID := "table-1", fmt.Sprintf("record-%06d", index)
		open := contracts.SearchOpenTarget{Kind: "record", TableId: &tableID, RecordId: &recordID}
		body := "offline workbench 数据工作台 alpha"
		var size *int64
		if index >= records {
			kind = "file"
			fileIndex := index - records
			canonicalID = fmt.Sprintf("document-%06d", fileIndex)
			open = contracts.SearchOpenTarget{Kind: "file", DocumentId: &canonicalID}
			logical := logicalBytes / files
			if fileIndex == files-1 {
				logical += logicalBytes % files
			}
			prefix := fmt.Sprintf("needle-%06d quarterly report\noffline workbench 数据工作台 alpha\n", index)
			filler := "qualification corpus alpha 0123456789\n"
			body = firstQueryDiagnosticExtract(t, prefix+strings.Repeat(filler, 128))
			if fileIndex == 0 || fileIndex == files-1 {
				// Compare with the original full file shape, including its
				// 1 MiB filler reset and tail, using memory rather than disk IO.
				chunk := strings.Repeat(filler, (1<<20)/len(filler)+1)[:1<<20]
				tail := fmt.Sprintf("\nqualification-tail-%06d\n", fileIndex)
				var original strings.Builder
				original.WriteString(prefix)
				remaining := logical - int64(len(prefix)+len(tail))
				for remaining > 0 {
					count := min(remaining, int64(len(chunk)))
					original.WriteString(chunk[:count])
					remaining -= count
				}
				original.WriteString(tail)
				if int64(original.Len()) != logical || firstQueryDiagnosticExtract(t, original.String()) != body {
					t.Fatal("diagnostic file body differs from qualification extraction")
				}
			}
			size = &logical
		}
		result = append(result, SourceDocument{
			Kind: kind, CanonicalID: canonicalID,
			Title: fmt.Sprintf("needle-%06d quarterly report", index), Body: body,
			SourceRevision: fmt.Sprintf("revision-%06d", index),
			RevisionTime:   "2026-08-12T00:00:00Z", SizeBytes: size,
			Status: "active", Current: true,
			Metadata: []contracts.SearchMetadataItem{}, OpenTarget: open,
		})
	}
	return result
}

func firstQueryDiagnosticExtract(t *testing.T, input string) string {
	t.Helper()
	limits := DefaultExtractionLimits
	limits.MaximumInputBytes = int64(len(input))
	limits.MaximumTextCodePoints = 4096
	result := Extract(context.Background(), "qualification.txt", "text/plain", strings.NewReader(input), limits)
	if result.Status != ExtractionTruncated || utf8.RuneCountInString(result.Text) != 4096 {
		t.Fatalf("diagnostic extraction shape changed: status=%s length=%d", result.Status, utf8.RuneCountInString(result.Text))
	}
	return result.Text
}
