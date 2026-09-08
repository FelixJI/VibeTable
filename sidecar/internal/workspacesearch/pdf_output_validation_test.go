package workspacesearch

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPDFTextLimitDoesNotHideTrailingStreamFailure(t *testing.T) {
	prefix := []byte(`BT /F1 12 Tf (` + strings.Repeat("visible ", 128) + `) Tj ET`)
	corrupt := pdfQualificationStream(t, []byte{0x78, 0x9c, 0xff}, false)
	corrupt = bytes.Replace(corrupt, []byte(" >>"), []byte(" /Filter /FlateDecode >>"), 1)
	for _, flatePrefix := range []bool{false, true} {
		name := "plain prefix"
		if flatePrefix {
			name = "Flate prefix"
		}
		t.Run(name, func(t *testing.T) {
			encodedPrefix := pdfQualificationStream(t, prefix, flatePrefix)
			if flatePrefix && bytes.Contains(encodedPrefix, []byte("(visible ")) {
				t.Fatal("Flate fixture exposes its text before decompression")
			}
			totalLimit := int64(150)
			if flatePrefix {
				totalLimit += int64(len(prefix))
			}
			for _, test := range []struct {
				name       string
				streams    [][]byte
				partLimit  int64
				totalLimit int64
				status     ExtractionStatus
				code       string
				text       string
			}{
				{"corrupt", [][]byte{corrupt}, 2048, 4096, ExtractionFailed, "extract.pdf_stream_invalid", ""},
				{"part limit", [][]byte{pdfQualificationStream(t, bytes.Repeat([]byte{' '}, 2049), true)}, 2048, 4096, ExtractionResourceLimited, "extract.pdf_stream_limit", ""},
				{"total limit", [][]byte{pdfQualificationStream(t, bytes.Repeat([]byte{' '}, 80), true), pdfQualificationStream(t, bytes.Repeat([]byte{' '}, 80), true)}, 2048, totalLimit, ExtractionResourceLimited, "extract.pdf_total_limit", ""},
				{"valid tail", [][]byte{pdfQualificationStream(t, []byte("q Q"), true)}, 2048, 4096, ExtractionTruncated, "extract.text_limit", "visi"},
			} {
				t.Run(test.name, func(t *testing.T) {
					contents := []string{"5 0 R"}
					for index := range test.streams {
						contents = append(contents, fmt.Sprintf("%d 0 R", index+6))
					}
					objects := [][]byte{
						[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
						[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
						[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents [" + strings.Join(contents, " ") + "] >>"),
						[]byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
						encodedPrefix,
					}
					objects = append(objects, test.streams...)
					payload := pdfQualificationObjects(objects)
					assertValidPDFCrossReferences(t, payload)
					limits := DefaultExtractionLimits
					limits.MaximumTextCodePoints = 4
					limits.MaximumPartBytes = test.partLimit
					limits.MaximumUncompressed = test.totalLimit
					result := Extract(context.Background(), "report.pdf", "application/pdf", bytes.NewReader(payload), limits)
					if result.Status != test.status || result.ErrorCode == nil || *result.ErrorCode != test.code || result.Text != test.text {
						t.Fatalf("Extract() = %#v; want %s/%s with text %q", result, test.status, test.code, test.text)
					}
				})
			}
		})
	}
}
