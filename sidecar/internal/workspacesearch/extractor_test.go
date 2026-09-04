package workspacesearch

import (
	"archive/zip"
	"bytes"
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
		{
			"report.pdf", "application/pdf",
			pdfQualificationDocument(t, []byte(`BT /F1 12 Tf (Native PDF report) Tj ET`), false),
			"Native PDF report",
		},
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
	noTextPDF := pdfQualificationDocument(t, []byte(`q 1 0 0 1 0 0 cm Q`), false)
	noText := Extract(
		context.Background(), "scan.pdf", "application/pdf",
		bytes.NewReader(noTextPDF), DefaultExtractionLimits,
	)
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

func TestPDFExtractorReadsFlateStreamsArraysAndEscapes(t *testing.T) {
	content := []byte(`BT /F1 12 Tf [(Quarterly\040) 20 (report)] TJ ET`)
	payload := pdfQualificationDocument(t, content, true)

	result := Extract(
		context.Background(), "report.pdf", "application/pdf",
		bytes.NewReader(payload), DefaultExtractionLimits,
	)
	if result.Status != ExtractionIndexed ||
		!strings.Contains(result.Text, "Quarterly report") {
		t.Fatalf("Extract() = %#v", result)
	}
}

func TestPDFExtractorReadsUTF16FromType0Font(t *testing.T) {
	payload := pdfQualificationUTF16Document(t)
	assertValidPDFCrossReferences(t, payload)
	if repeated := pdfQualificationUTF16Document(t); !bytes.Equal(payload, repeated) {
		t.Fatal("UTF-16 PDF corpus generation is not deterministic")
	}
	for _, token := range [][]byte{
		[]byte("/Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H " +
			"/DescendantFonts [5 0 R] /ToUnicode 7 0 R"),
		[]byte("/Subtype /CIDFontType0 /BaseFont /STSong-Light"),
		[]byte("/CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 0 >>"),
		[]byte("/FontDescriptor 6 0 R /DW 1000"),
		[]byte("/Type /FontDescriptor /FontName /STSong-Light /Flags 6"),
		[]byte("7 0 obj\n<< /Length "),
		[]byte("begincodespacerange\n<0000> <FFFF>\nendcodespacerange"),
		[]byte("<6570> <6570>\n<636E> <636E>\n<5DE5> <5DE5>\n<4F5C> <4F5C>"),
		[]byte("8 0 obj\n<< /Length "),
		[]byte(" /Filter /FlateDecode >>\nstream\n"),
	} {
		if !bytes.Contains(payload, token) {
			t.Fatalf("UTF-16 font corpus is missing %q", token)
		}
	}
	if bytes.Contains(payload, []byte("/CIDToGIDMap")) || bytes.Contains(payload, []byte("/Identity-H")) {
		t.Fatal("non-embedded CJK font must not use a TrueType identity mapping")
	}

	result := Extract(
		context.Background(), "report.pdf", "application/pdf",
		bytes.NewReader(payload), DefaultExtractionLimits,
	)
	if result.Status != ExtractionIndexed || !strings.Contains(result.Text, "数据工作") {
		t.Fatalf("Extract() = %#v", result)
	}
}

func TestPDFQualificationCorpusUsesValidCrossReferenceTables(t *testing.T) {
	for _, document := range []struct {
		name    string
		content []byte
		flate   bool
	}{
		{"plain text layer", []byte(`BT /F1 12 Tf (Native PDF report) Tj ET`), false},
		{"Flate text layer", []byte(`BT /F1 12 Tf [(Quarterly\040) 20 (report)] TJ ET`), true},
		{"no text layer", []byte(`q 1 0 0 1 0 0 cm Q`), false},
	} {
		t.Run(document.name, func(t *testing.T) {
			payload := pdfQualificationDocument(t, document.content, document.flate)
			assertValidPDFCrossReferences(t, payload)
			if repeated := pdfQualificationDocument(t, document.content, document.flate); !bytes.Equal(payload, repeated) {
				t.Fatal("PDF corpus generation is not deterministic")
			}
		})
	}
}

func TestPDFExtractorRejectsCorruptFlateAndDecodedStreamLimit(t *testing.T) {
	corrupt := pdfQualificationDocument(t, []byte(`BT /F1 12 Tf (corrupt) Tj ET`), true)
	streamMarker := []byte("\nstream\n")
	streamStart := bytes.Index(corrupt, streamMarker)
	if streamStart < 0 {
		t.Fatal("content stream marker is missing")
	}
	streamStart += len(streamMarker)
	streamEnd := bytes.Index(corrupt[streamStart:], []byte("\nendstream"))
	if streamEnd <= 0 {
		t.Fatal("content stream terminator is missing")
	}
	for index := streamStart; index < streamStart+streamEnd; index++ {
		corrupt[index] = 0xff
	}
	assertValidPDFCrossReferences(t, corrupt)
	result := Extract(
		context.Background(), "corrupt.pdf", "application/pdf",
		bytes.NewReader(corrupt), DefaultExtractionLimits,
	)
	if result.Status != ExtractionFailed || result.ErrorCode == nil ||
		*result.ErrorCode != "extract.pdf_stream_invalid" {
		t.Fatalf("corrupt = %#v", result)
	}

	payload := pdfQualificationDocument(t, []byte("BT /F1 12 Tf (text longer than limit) Tj ET"), true)
	limits := DefaultExtractionLimits
	limits.MaximumPartBytes = 4
	result = Extract(context.Background(), "limited.pdf", "application/pdf", bytes.NewReader(payload), limits)
	if result.Status != ExtractionResourceLimited || result.ErrorCode == nil ||
		*result.ErrorCode != "extract.pdf_stream_limit" {
		t.Fatalf("limited = %#v", result)
	}
}

func TestPDFStreamReadMapsOnlyCausalCancellation(t *testing.T) {
	tests := []struct {
		name       string
		readErr    error
		wantStatus ExtractionStatus
		wantCode   string
	}{
		{"cancelled read", context.Canceled, ExtractionCancelled, "extract.cancelled"},
		{"deadline read", context.DeadlineExceeded, ExtractionResourceLimited, "extract.timeout"},
		{"corrupt read during cancellation", io.ErrUnexpectedEOF, ExtractionFailed, "extract.pdf_stream_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			reader := &blockedPDFReader{
				started: make(chan struct{}), release: make(chan struct{}), err: test.readErr,
			}
			result := make(chan ExtractionResult, 1)
			go func() {
				_, failure := readPDFStream(ctx, reader, DefaultExtractionLimits.MaximumPartBytes)
				result <- failure
			}()
			<-reader.started
			cancel()
			close(reader.release)
			failure := <-result
			assertExtractionCode(t, failure, test.wantStatus, test.wantCode)
		})
	}
}

func TestWalkPDFMatchesChecksContextAfterFinalToken(t *testing.T) {
	payload := []byte("BT (searchable text) Tj ET")
	ctx, cancel := context.WithCancel(context.Background())
	err := walkPDFMatches(ctx, pdfTextOperator, payload, 1, func([]byte) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("tail cancellation = %v", err)
	}

	deadline := newFinishableContext()
	err = walkPDFMatches(deadline, pdfTextOperator, payload, 1, func([]byte) error {
		deadline.finish(context.DeadlineExceeded)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("tail deadline = %v", err)
	}
	assertExtractionCode(t, extractionContextError(deadline), ExtractionResourceLimited, "extract.timeout")
}

func TestPDFMatcherCancelsDuringFlateDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &blockedPDFReader{
		started: make(chan struct{}), release: make(chan struct{}),
		runes: strings.NewReader("x<< /FlateDecode >>\nstream\npayload\nendstream"),
	}
	result := make(chan []int, 1)
	go func() {
		result <- pdfFlateStream.FindReaderSubmatchIndex(&pdfContextReader{
			Context: ctx, RuneReader: source,
		})
	}()
	<-source.started
	cancel()
	close(source.release)
	wrapped, raw := <-result, pdfFlateStream.FindReaderSubmatchIndex(source)
	if wrapped != nil || raw == nil {
		t.Fatalf("flate discovery matches: context-aware=%v raw=%v", wrapped, raw)
	}
}

func TestPDFTextAccumulatorStoresAtMostLimitPlusOneRune(t *testing.T) {
	text := pdfTextAccumulator{limit: 4}
	if text.appendToken([]byte("(123456789)")) || text.runes != 5 {
		t.Fatalf("bounded accumulator = %#v", text)
	}
	assertExtractionCode(t, text.result(), ExtractionTruncated, "extract.text_limit")
	if result := text.result(); result.Text != "1234" {
		t.Fatalf("truncated text = %q", result.Text)
	}
}

func TestPDFExtractorTruncatesUncompressedTextAtRuneLimit(t *testing.T) {
	limits := DefaultExtractionLimits
	limits.MaximumTextCodePoints = 4
	result := Extract(
		context.Background(), "report.pdf", "application/pdf",
		strings.NewReader("%PDF-1.7\nBT (123456) Tj ET"), limits,
	)
	assertExtractionCode(t, result, ExtractionTruncated, "extract.text_limit")
	if result.Text != "1234" {
		t.Fatalf("truncated text = %q", result.Text)
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
		{"<FEFF6570636E5DE54F5C>", "数据工作"},
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
	text := pdfTextAccumulator{limit: 4}
	if !text.appendToken([]byte("()")) || !text.appendToken([]byte("<GG>")) || text.runes != 0 {
		t.Fatalf("invalid or blank tokens appended %#v", text)
	}
}

func TestExtractorRejectsEveryNonPositiveLimit(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExtractionLimits, int64)
	}{
		{"input", func(limits *ExtractionLimits, value int64) { limits.MaximumInputBytes = value }},
		{"entries", func(limits *ExtractionLimits, value int64) { limits.MaximumZIPEntries = int(value) }},
		{"uncompressed", func(limits *ExtractionLimits, value int64) { limits.MaximumUncompressed = value }},
		{"part", func(limits *ExtractionLimits, value int64) { limits.MaximumPartBytes = value }},
		{"text", func(limits *ExtractionLimits, value int64) { limits.MaximumTextCodePoints = int(value) }},
		{"duration", func(limits *ExtractionLimits, value int64) { limits.MaximumDuration = time.Duration(value) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, value := range []int64{0, -1} {
				limits := DefaultExtractionLimits
				test.mutate(&limits, value)
				assertExtractionCode(
					t, Extract(context.Background(), "notes.txt", "text/plain", strings.NewReader("text"), limits),
					ExtractionFailed, "extract.limits_invalid",
				)
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

type blockedPDFReader struct {
	started, release chan struct{}
	err              error
	runes            io.RuneReader
}

func (reader *blockedPDFReader) Read([]byte) (int, error) {
	close(reader.started)
	<-reader.release
	return 0, reader.err
}

func (reader *blockedPDFReader) ReadRune() (rune, int, error) {
	if reader.started != nil {
		close(reader.started)
		<-reader.release
		reader.started = nil
	}
	return reader.runes.ReadRune()
}

func (*blockedPDFReader) Close() error { return nil }

type finishableContext struct {
	context.Context
	done chan struct{}
	err  error
}

func newFinishableContext() *finishableContext {
	return &finishableContext{Context: context.Background(), done: make(chan struct{})}
}

func (controlled *finishableContext) Done() <-chan struct{} { return controlled.done }
func (controlled *finishableContext) Err() error            { return controlled.err }
func (controlled *finishableContext) finish(err error) {
	controlled.err = err
	close(controlled.done)
}

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
