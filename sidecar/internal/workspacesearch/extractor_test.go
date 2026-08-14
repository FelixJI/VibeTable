package workspacesearch

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestExtractorCoversTextHTMLJSONOOXMLAndNativePDF(t *testing.T) {
	limits := DefaultExtractionLimits
	tests := []struct {
		name, mime string
		body       []byte
		want       string
	}{
		{"notes.md", "text/markdown", []byte("# Quarterly report"), "Quarterly report"},
		{"record.json", "application/json", []byte(`{"title":"Quarterly report"}`), "Quarterly report"},
		{"page.html", "text/html", []byte(`<p>Visible report</p><script>secret</script>`), "Visible report"},
		{"report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ooxml(t, "word/document.xml", `<w:document xmlns:w="w"><w:t>Document report</w:t></w:document>`), "Document report"},
		{"report.pdf", "application/pdf", []byte("%PDF-1.7\nBT (Native PDF report) Tj ET"), "Native PDF report"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Extract(context.Background(), test.name, test.mime, bytes.NewReader(test.body), limits)
			if result.Status != ExtractionIndexed || !strings.Contains(result.Text, test.want) {
				t.Fatalf("Extract() = %#v", result)
			}
			if strings.Contains(result.Text, "secret") {
				t.Fatal("script content was indexed")
			}
		})
	}
}

func TestExtractorFailsPerSourceWithoutBlockingAndHonorsLimits(t *testing.T) {
	limits := DefaultExtractionLimits
	limits.MaximumInputBytes = 8
	limited := Extract(context.Background(), "notes.txt", "text/plain", strings.NewReader("more than eight"), limits)
	password := Extract(context.Background(), "locked.pdf", "application/pdf", strings.NewReader("%PDF-1.7 /Encrypt"), DefaultExtractionLimits)
	noText := Extract(context.Background(), "scan.pdf", "application/pdf", strings.NewReader("%PDF-1.7 image"), DefaultExtractionLimits)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := Extract(ctx, "notes.txt", "text/plain", strings.NewReader("text"), DefaultExtractionLimits)
	if limited.Status != ExtractionResourceLimited {
		t.Fatalf("limited = %#v", limited)
	}
	if password.Status != ExtractionPasswordProtected {
		t.Fatalf("password = %#v", password)
	}
	if noText.Status != ExtractionNoTextLayer {
		t.Fatalf("noText = %#v", noText)
	}
	if cancelled.Status != ExtractionCancelled {
		t.Fatalf("cancelled = %#v", cancelled)
	}
}

func TestOOXMLRejectsDeclaredPartBeyondLimit(t *testing.T) {
	payload := ooxml(t, "word/document.xml", `<w:document xmlns:w="w"><w:t>too long</w:t></w:document>`)
	limits := DefaultExtractionLimits
	limits.MaximumPartBytes = 4
	result := Extract(context.Background(), "report.docx", "", bytes.NewReader(payload), limits)
	if result.Status != ExtractionResourceLimited || result.ErrorCode == nil || *result.ErrorCode != "extract.zip_part_limit" {
		t.Fatalf("Extract() = %#v", result)
	}
}

func TestExtractorTimesOutDuringIncrementalInputRead(t *testing.T) {
	limits := DefaultExtractionLimits
	limits.MaximumDuration = time.Millisecond
	result := Extract(
		context.Background(),
		"slow.txt",
		"text/plain",
		&delayedReader{remaining: 3},
		limits,
	)
	if result.Status != ExtractionResourceLimited || result.ErrorCode == nil ||
		*result.ErrorCode != "extract.timeout" {
		t.Fatalf("Extract() = %#v", result)
	}
}

func TestPDFExtractorReadsFlateStreamsArraysEscapesAndUTF16(t *testing.T) {
	content := []byte(`BT [(Quarterly\040) 20 (report)] TJ <FEFF6570636E5DE54F5C> Tj ET`)
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("%PDF-1.7\n<< /Filter /FlateDecode >>\nstream\n"), compressed.Bytes()...)
	payload = append(payload, []byte("\nendstream\n%%EOF")...)

	result := Extract(
		context.Background(), "report.pdf", "application/pdf",
		bytes.NewReader(payload), DefaultExtractionLimits,
	)
	if result.Status != ExtractionIndexed ||
		!strings.Contains(result.Text, "Quarterly report") ||
		!strings.Contains(result.Text, "数据工作") {
		t.Fatalf("Extract() = %#v", result)
	}
}

func TestPDFExtractorRejectsCorruptFlateAndDecodedStreamLimit(t *testing.T) {
	corrupt := []byte("%PDF-1.7\n<< /Filter /FlateDecode >>\nstream\nnot-zlib\nendstream")
	result := Extract(
		context.Background(), "corrupt.pdf", "application/pdf",
		bytes.NewReader(corrupt), DefaultExtractionLimits,
	)
	if result.Status != ExtractionFailed || result.ErrorCode == nil ||
		*result.ErrorCode != "extract.pdf_stream_invalid" {
		t.Fatalf("corrupt = %#v", result)
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write([]byte("BT (text longer than limit) Tj ET"))
	_ = writer.Close()
	payload := append([]byte("%PDF-1.7\n<< /Filter /FlateDecode >>\nstream\n"), compressed.Bytes()...)
	payload = append(payload, []byte("\nendstream")...)
	limits := DefaultExtractionLimits
	limits.MaximumPartBytes = 4
	result = Extract(context.Background(), "limited.pdf", "application/pdf", bytes.NewReader(payload), limits)
	if result.Status != ExtractionResourceLimited || result.ErrorCode == nil ||
		*result.ErrorCode != "extract.pdf_stream_limit" {
		t.Fatalf("limited = %#v", result)
	}
}

func TestPDFStringDecoderHandlesEveryLiteralEscapeAndHexBoundary(t *testing.T) {
	literal := append([]byte(`(line\nreturn\rtab\tback\bform\fparen\(\)slash\\octal\101`), '\r', '\n')
	literal = append(literal, []byte("continued\\\nend)")...)
	decoded, ok := decodePDFString(literal)
	if !ok {
		t.Fatal("escaped PDF literal was rejected")
	}
	for _, want := range []string{
		"line\n", "return\r", "tab\t", "back\b", "form\f",
		"paren()", `slash\octalA`, "continuedend",
	} {
		if !strings.Contains(decoded, want) {
			t.Fatalf("decoded literal %q does not contain %q", decoded, want)
		}
	}

	for _, test := range []struct {
		token string
		want  string
	}{
		{"<41 42>", "AB"},
		{"<414>", "A@"},
	} {
		value, valid := decodePDFString([]byte(test.token))
		if !valid || value != test.want {
			t.Fatalf("decodePDFString(%q) = %q, %v", test.token, value, valid)
		}
	}
	for _, token := range [][]byte{{}, {'('}, []byte("<GG>"), {'(', 0xff, ')'}} {
		if value, valid := decodePDFString(token); valid || value != "" {
			t.Fatalf("invalid token %q decoded as %q", token, value)
		}
	}
	var output strings.Builder
	appendPDFToken(&output, []byte("()"))
	appendPDFToken(&output, []byte("<GG>"))
	if output.Len() != 0 {
		t.Fatalf("invalid or blank tokens appended %q", output.String())
	}
}

func TestValidateExtractionLimitsRejectsEveryNonPositiveBoundary(t *testing.T) {
	if err := ValidateExtractionLimits(DefaultExtractionLimits); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*ExtractionLimits)
	}{
		{"input", func(value *ExtractionLimits) { value.MaximumInputBytes = 0 }},
		{"entries", func(value *ExtractionLimits) { value.MaximumZIPEntries = 0 }},
		{"uncompressed", func(value *ExtractionLimits) { value.MaximumUncompressed = 0 }},
		{"part", func(value *ExtractionLimits) { value.MaximumPartBytes = 0 }},
		{"text", func(value *ExtractionLimits) { value.MaximumTextCodePoints = 0 }},
		{"duration", func(value *ExtractionLimits) { value.MaximumDuration = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultExtractionLimits
			test.mutate(&limits)
			if err := ValidateExtractionLimits(limits); err == nil {
				t.Fatal("invalid limits unexpectedly accepted")
			}
		})
	}
}

func TestExtractorClassifiesInvalidLimitsReadersFormatsAndTruncation(t *testing.T) {
	invalidLimits := DefaultExtractionLimits
	invalidLimits.MaximumTextCodePoints = 0
	invalid := Extract(context.Background(), "notes.txt", "text/plain", strings.NewReader("text"), invalidLimits)
	readFailure := Extract(context.Background(), "notes.txt", "text/plain", failingReader{}, DefaultExtractionLimits)
	noProgress := Extract(context.Background(), "notes.txt", "text/plain", emptyReader{}, DefaultExtractionLimits)
	unsupported := Extract(context.Background(), "image.png", "image/png", strings.NewReader("png"), DefaultExtractionLimits)
	invalidUTF8 := Extract(context.Background(), "notes.txt", "text/plain", bytes.NewReader([]byte{0xff}), DefaultExtractionLimits)
	invalidPDF := Extract(context.Background(), "report.pdf", "application/pdf", strings.NewReader("not-pdf"), DefaultExtractionLimits)
	limits := DefaultExtractionLimits
	limits.MaximumTextCodePoints = 4
	truncated := Extract(context.Background(), "notes.txt", "text/plain", strings.NewReader("12345"), limits)

	assertExtractionCode(t, invalid, ExtractionFailed, "extract.limits_invalid")
	assertExtractionCode(t, readFailure, ExtractionFailed, "extract.read_failed")
	assertExtractionCode(t, noProgress, ExtractionFailed, "extract.read_failed")
	assertExtractionCode(t, unsupported, ExtractionUnsupported, "extract.unsupported")
	assertExtractionCode(t, invalidUTF8, ExtractionFailed, "extract.invalid_utf8")
	assertExtractionCode(t, invalidPDF, ExtractionFailed, "extract.pdf_invalid")
	assertExtractionCode(t, truncated, ExtractionTruncated, "extract.text_limit")
	if truncated.Text != "1234" {
		t.Fatalf("truncated text = %q", truncated.Text)
	}
}

func TestExtractionContextErrorDistinguishesDeadlineAndCancellation(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	assertExtractionCode(t, extractionContextError(cancelled), ExtractionCancelled, "extract.cancelled")
	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	assertExtractionCode(t, extractionContextError(deadline), ExtractionResourceLimited, "extract.timeout")
}

func TestExtractorPropagatesCancellationAcrossStructuredFormatsAndTextMIMEFallback(t *testing.T) {
	plain := Extract(
		context.Background(), "notes.unknown", "text/plain",
		strings.NewReader("fallback text"), DefaultExtractionLimits,
	)
	if plain.Status != ExtractionIndexed || plain.Text != "fallback text" {
		t.Fatalf("text MIME fallback = %#v", plain)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractJSON(cancelled, []byte(`{"value":"text"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("JSON cancellation = %v", err)
	}
	if _, err := extractXML(cancelled, strings.NewReader(`<root>text</root>`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("XML cancellation = %v", err)
	}
	if _, err := extractHTML(cancelled, []byte(`<p>text</p>`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("HTML cancellation = %v", err)
	}
	if ooxmlTextPart("unknown", "content.xml") {
		t.Fatal("unknown OOXML kind was treated as searchable")
	}
}

func TestExtractorTraversesJSONXMLAndHTMLValues(t *testing.T) {
	jsonResult := Extract(
		context.Background(), "record.json", "application/json",
		strings.NewReader(`{"name":"alpha","items":[1,true,false,null,{"nested":"beta"}]}`),
		DefaultExtractionLimits,
	)
	if jsonResult.Status != ExtractionIndexed {
		t.Fatalf("json = %#v", jsonResult)
	}
	for _, text := range []string{"name", "alpha", "items", "1", "true", "false", "nested", "beta"} {
		if !strings.Contains(jsonResult.Text, text) {
			t.Fatalf("json text %q does not contain %q", jsonResult.Text, text)
		}
	}
	xmlResult := Extract(
		context.Background(), "record.xml", "application/xml",
		strings.NewReader(`<root> alpha <child>beta</child> </root>`), DefaultExtractionLimits,
	)
	if xmlResult.Status != ExtractionIndexed || xmlResult.Text != "alpha beta" {
		t.Fatalf("xml = %#v", xmlResult)
	}
	htmlResult := Extract(
		context.Background(), "record.htm", "text/html",
		strings.NewReader(`<style>hidden</style><main>alpha <b>beta</b></main>`), DefaultExtractionLimits,
	)
	if htmlResult.Status != ExtractionIndexed || htmlResult.Text != "alpha beta" {
		t.Fatalf("html = %#v", htmlResult)
	}
	for _, test := range []struct {
		name, body string
	}{
		{"record.json", `{"broken":`},
		{"record.xml", `<root>`},
	} {
		result := Extract(context.Background(), test.name, "", strings.NewReader(test.body), DefaultExtractionLimits)
		assertExtractionCode(t, result, ExtractionFailed, "extract.invalid_content")
	}
}

func TestOOXMLSupportsDocumentPresentationAndWorkbookParts(t *testing.T) {
	tests := []struct {
		name, part, body, want string
	}{
		{"report.docx", "word/header1.xml", `<w:hdr xmlns:w="w"><w:t>Header text</w:t></w:hdr>`, "Header text"},
		{"slides.pptx", "ppt/slides/slide1.xml", `<p:sld xmlns:p="p"><p:t>Slide text</p:t></p:sld>`, "Slide text"},
		{"sheet.xlsx", "xl/sharedStrings.xml", `<sst><si><t>Shared text</t></si></sst>`, "Shared text"},
		{"sheet.xlsx", "xl/worksheets/sheet1.xml", `<worksheet><v>Cell text</v></worksheet>`, "Cell text"},
	}
	for _, test := range tests {
		t.Run(test.name+test.part, func(t *testing.T) {
			result := Extract(context.Background(), test.name, "", bytes.NewReader(ooxml(t, test.part, test.body)), DefaultExtractionLimits)
			if result.Status != ExtractionIndexed || !strings.Contains(result.Text, test.want) {
				t.Fatalf("Extract() = %#v", result)
			}
		})
	}
}

func TestOOXMLRejectsArchiveBoundsAndInvalidParts(t *testing.T) {
	invalid := Extract(context.Background(), "report.docx", "", strings.NewReader("not a zip"), DefaultExtractionLimits)
	assertExtractionCode(t, invalid, ExtractionFailed, "extract.ooxml_invalid")

	payload := ooxmlMany(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:t>one</w:t></w:document>`,
		"word/header1.xml":  `<w:hdr xmlns:w="w"><w:t>two</w:t></w:hdr>`,
	})
	entryLimits := DefaultExtractionLimits
	entryLimits.MaximumZIPEntries = 1
	assertExtractionCode(
		t,
		Extract(context.Background(), "report.docx", "", bytes.NewReader(payload), entryLimits),
		ExtractionResourceLimited,
		"extract.zip_entries_limit",
	)
	totalLimits := DefaultExtractionLimits
	totalLimits.MaximumUncompressed = 10
	assertExtractionCode(
		t,
		Extract(context.Background(), "report.docx", "", bytes.NewReader(payload), totalLimits),
		ExtractionResourceLimited,
		"extract.zip_total_limit",
	)
	brokenPart := ooxml(t, "word/document.xml", `<w:document>`)
	assertExtractionCode(
		t,
		Extract(context.Background(), "report.docx", "", bytes.NewReader(brokenPart), DefaultExtractionLimits),
		ExtractionFailed,
		"extract.ooxml_part_failed",
	)
	unsafe := Extract(
		context.Background(), "report.docx", "",
		bytes.NewReader(ooxml(t, "../word/document.xml", `<w:document/>`)), DefaultExtractionLimits,
	)
	if unsafe.Status != ExtractionIndexed || unsafe.Text != "" {
		t.Fatalf("unsafe entry = %#v", unsafe)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertExtractionCode(
		t,
		extractOOXML(ctx, "docx", payload, DefaultExtractionLimits),
		ExtractionCancelled,
		"extract.cancelled",
	)
}

type delayedReader struct{ remaining int }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, nil }

func (reader *delayedReader) Read(target []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	time.Sleep(3 * time.Millisecond)
	reader.remaining--
	target[0] = 'x'
	return 1, nil
}

func ooxml(t *testing.T, name, content string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	part, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func ooxmlMany(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, content := range entries {
		part, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func assertExtractionCode(t *testing.T, result ExtractionResult, status ExtractionStatus, code string) {
	t.Helper()
	if result.Status != status || result.ErrorCode == nil || *result.ErrorCode != code {
		t.Fatalf("extraction = %#v, want status=%s code=%s", result, status, code)
	}
}
