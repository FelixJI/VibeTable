package workspacesearch

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func pdfQualificationDocument(t testing.TB, content []byte, flate bool) []byte {
	t.Helper()
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>"),
		[]byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		pdfQualificationStream(t, content, flate),
	}
	return pdfQualificationObjects(objects)
}

func pdfQualificationUTF16Document(t testing.TB) []byte {
	t.Helper()
	toUnicode := []byte(`/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def
/CMapName /VibeTableUTF16 def
/CMapType 2 def
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
5 beginbfchar
<FEFF> <FEFF>
<6570> <6570>
<636E> <636E>
<5DE5> <5DE5>
<4F5C> <4F5C>
endbfchar
endcmap
CMapName currentdict /CMap defineresource pop
end
end`)
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 4 0 R >> >> /Contents 8 0 R >>"),
		[]byte("<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H " +
			"/DescendantFonts [5 0 R] /ToUnicode 7 0 R >>"),
		[]byte("<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light " +
			"/CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 0 >> " +
			"/FontDescriptor 6 0 R /DW 1000 >>"),
		[]byte("<< /Type /FontDescriptor /FontName /STSong-Light /Flags 6 " +
			"/FontBBox [-25 -254 1000 880] /ItalicAngle 0 /Ascent 752 /Descent -271 " +
			"/CapHeight 737 /StemV 58 >>"),
		pdfQualificationStream(t, toUnicode, false),
		pdfQualificationStream(t, []byte(`BT /F1 12 Tf 72 720 Td <FEFF6570636E5DE54F5C> Tj ET`), true),
	}
	return pdfQualificationObjects(objects)
}

func pdfQualificationStream(t testing.TB, content []byte, flate bool) []byte {
	t.Helper()
	stream := append([]byte(nil), content...)
	filter := ""
	if flate {
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		if _, err := writer.Write(stream); err != nil {
			t.Fatalf("compress PDF content stream: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close PDF content stream: %v", err)
		}
		stream, filter = compressed.Bytes(), " /Filter /FlateDecode"
	}
	return append(
		[]byte(fmt.Sprintf("<< /Length %d%s >>\nstream\n", len(stream), filter)),
		append(stream, []byte("\nendstream")...)...,
	)
}

func pdfQualificationObjects(objects [][]byte) []byte {
	var document bytes.Buffer
	document.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, 1, len(objects)+1)
	for index, object := range objects {
		offsets = append(offsets, document.Len())
		_, _ = fmt.Fprintf(&document, "%d 0 obj\n", index+1)
		document.Write(object)
		document.WriteString("\nendobj\n")
	}
	xrefOffset := document.Len()
	_, _ = fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f\r\n", len(offsets))
	for _, offset := range offsets[1:] {
		_, _ = fmt.Fprintf(&document, "%010d 00000 n\r\n", offset)
	}
	_, _ = fmt.Fprintf(
		&document,
		"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets), xrefOffset,
	)
	return document.Bytes()
}

func assertValidPDFCrossReferences(t testing.TB, document []byte) {
	t.Helper()
	if err := validatePDFCrossReferences(document); err != nil {
		t.Fatal(err)
	}
}

func validatePDFCrossReferences(document []byte) error {
	if !bytes.HasPrefix(document, []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")) ||
		!bytes.HasSuffix(document, []byte("%%EOF\n")) {
		return fmt.Errorf("PDF header or EOF marker is missing")
	}
	startMarker := []byte("startxref\n")
	start := bytes.LastIndex(document, startMarker)
	if start < 0 {
		return fmt.Errorf("startxref is missing")
	}
	xrefValue := document[start+len(startMarker):]
	xrefEnd := bytes.IndexByte(xrefValue, '\n')
	if xrefEnd <= 0 {
		return fmt.Errorf("startxref value is missing: %q", xrefValue)
	}
	if !bytes.Equal(xrefValue[xrefEnd:], []byte("\n%%EOF\n")) {
		return fmt.Errorf("startxref trailer = %q", xrefValue[xrefEnd:])
	}
	xrefOffset, err := strconv.Atoi(string(xrefValue[:xrefEnd]))
	if err != nil || xrefOffset < 0 || xrefOffset >= len(document) ||
		!bytes.HasPrefix(document[xrefOffset:], []byte("xref\n")) {
		return fmt.Errorf("startxref = %q", xrefValue[:xrefEnd])
	}

	xrefLines := strings.Split(string(document[xrefOffset:start]), "\n")
	if len(xrefLines) < 7 || xrefLines[0] != "xref" {
		return fmt.Errorf("xref header = %q", xrefLines)
	}
	var xrefSize int
	if _, err := fmt.Sscanf(xrefLines[1], "0 %d", &xrefSize); err != nil ||
		xrefSize < 2 || xrefLines[1] != fmt.Sprintf("0 %d", xrefSize) {
		return fmt.Errorf("xref header = %q", xrefLines)
	}
	objectCount := xrefSize - 1
	if len(xrefLines) != objectCount+6 || xrefLines[2] != "0000000000 65535 f\r" ||
		xrefLines[objectCount+3] != "trailer" ||
		xrefLines[objectCount+4] != fmt.Sprintf("<< /Size %d /Root 1 0 R >>", xrefSize) ||
		xrefLines[objectCount+5] != "" {
		return fmt.Errorf("xref header = %q", xrefLines)
	}
	offsets := make([]int, objectCount+1)
	for objectID := 1; objectID <= objectCount; objectID++ {
		if len(xrefLines[objectID+2])+1 != 20 || !strings.HasSuffix(xrefLines[objectID+2], "\r") {
			return fmt.Errorf("xref entry %d is not a 20-byte record: %q", objectID, xrefLines[objectID+2])
		}
		var offset int
		var generation int
		var state string
		if _, err := fmt.Sscanf(xrefLines[objectID+2], "%d %d %s", &offset, &generation, &state); err != nil ||
			generation != 0 || state != "n" || offset < 0 || offset >= len(document) ||
			xrefLines[objectID+2] != fmt.Sprintf("%010d 00000 n\r", offset) ||
			!bytes.HasPrefix(document[offset:], []byte(fmt.Sprintf("%d 0 obj\n", objectID))) {
			return fmt.Errorf("xref entry %d = %q", objectID, xrefLines[objectID+2])
		}
		offsets[objectID] = offset
	}
	for objectID := 1; objectID <= objectCount; objectID++ {
		objectEnd := xrefOffset
		if objectID < objectCount {
			objectEnd = offsets[objectID+1]
		}
		if objectEnd <= offsets[objectID] ||
			!bytes.HasSuffix(document[offsets[objectID]:objectEnd], []byte("\nendobj\n")) {
			return fmt.Errorf("object %d is not terminated before the next object", objectID)
		}
		if err := validatePDFStreamObject(document[offsets[objectID]:objectEnd]); err != nil {
			return fmt.Errorf("object %d: %w", objectID, err)
		}
	}
	return nil
}

func validatePDFStreamObject(object []byte) error {
	streamMarker := []byte("\nstream\n")
	streamStart := bytes.Index(object, streamMarker)
	if streamStart < 0 {
		return nil
	}
	lengthStart := bytes.Index(object[:streamStart], []byte("/Length "))
	if lengthStart < 0 {
		return fmt.Errorf("stream length is missing")
	}
	lengthValue := object[lengthStart+len("/Length "):]
	lengthEnd := 0
	for lengthEnd < len(lengthValue) && lengthValue[lengthEnd] >= '0' && lengthValue[lengthEnd] <= '9' {
		lengthEnd++
	}
	if lengthEnd == 0 || lengthEnd == len(lengthValue) || !isPDFTokenBoundary(lengthValue[lengthEnd]) {
		return fmt.Errorf("stream length is not a decimal token")
	}
	declaredLength, err := strconv.Atoi(string(lengthValue[:lengthEnd]))
	if err != nil {
		return fmt.Errorf("stream length is invalid: %v", err)
	}
	streamStart += len(streamMarker)
	if declaredLength > len(object)-streamStart {
		return fmt.Errorf("stream length %d exceeds object bytes", declaredLength)
	}
	streamEnd := streamStart + declaredLength
	if streamEnd < streamStart || !bytes.Equal(object[streamEnd:], []byte("\nendstream\nendobj\n")) {
		return fmt.Errorf("stream object does not end at declared length %d", declaredLength)
	}
	return nil
}

func isPDFTokenBoundary(value byte) bool {
	switch value {
	case 0, '\t', '\n', '\f', '\r', ' ', '(', ')', '<', '>', '[', ']', '/', '%':
		return true
	default:
		return false
	}
}

func TestPDFQualificationValidatorRejectsNonCanonicalXrefOffset(t *testing.T) {
	document := pdfQualificationDocument(t, []byte("q Q"), false)
	canonical := []byte("0000000015 00000 n\r")
	entry := bytes.Index(document, canonical)
	if entry < 0 {
		t.Fatalf("first xref entry %q is missing", canonical)
	}
	mutated := append([]byte(nil), document...)
	mutated[entry] = '+'
	if err := validatePDFCrossReferences(mutated); err == nil {
		t.Fatal("xref offset with a leading plus sign was accepted")
	}
}

func TestPDFQualificationValidatorRejectsInvalidLengthDelimiter(t *testing.T) {
	document := pdfQualificationDocument(t, bytes.Repeat([]byte{'q'}, 42), false)
	lengthToken := bytes.Index(document, []byte("/Length 42 "))
	if lengthToken < 0 {
		t.Fatal("content stream length token is missing")
	}
	mutated := append([]byte(nil), document...)
	mutated[lengthToken+len("/Length 42")] = 'x'
	if err := validatePDFCrossReferences(mutated); err == nil {
		t.Fatal("stream length with a non-delimiter suffix was accepted")
	}
}

func TestPDFQualificationValidatorRejectsInvalidLengthToken(t *testing.T) {
	document := pdfQualificationDocument(t, []byte("q Q"), false)
	lengthToken := bytes.Index(document, []byte("/Length 3 "))
	if lengthToken < 0 {
		t.Fatal("content stream length token is missing")
	}
	mutated := append([]byte(nil), document...)
	mutated[lengthToken+len("/Length ")] = 'x'
	if err := validatePDFCrossReferences(mutated); err == nil {
		t.Fatal("non-decimal stream length token was accepted")
	}
}

func TestPDFQualificationValidatorRejectsUnexpectedXrefToken(t *testing.T) {
	document := pdfQualificationDocument(t, []byte("q Q"), false)
	trailer := bytes.Index(document, []byte("trailer\n"))
	if trailer < 0 {
		t.Fatal("trailer is missing")
	}
	mutated := make([]byte, 0, len(document)+len("garbage\n"))
	mutated = append(mutated, document[:trailer]...)
	mutated = append(mutated, []byte("garbage\n")...)
	mutated = append(mutated, document[trailer:]...)
	if err := validatePDFCrossReferences(mutated); err == nil {
		t.Fatal("unexpected token between xref entries and trailer was accepted")
	}
}

func TestPDFQualificationValidatorRejectsInvalidStreamObjectTerminator(t *testing.T) {
	document := pdfQualificationDocument(t, []byte("q Q"), false)
	canonical := []byte("\nendstream\nendobj\nxref")
	terminator := bytes.Index(document, canonical)
	if terminator < 0 {
		t.Fatal("stream object terminator is missing")
	}
	mutated := append([]byte(nil), document...)
	copy(mutated[terminator:terminator+len(canonical)], []byte("\nendstream\nendobx\nxref"))
	if err := validatePDFCrossReferences(mutated); err == nil {
		t.Fatal("invalid stream object terminator was accepted")
	}
}

func TestPDFQualificationValidatorRejectsUnexpectedStartXrefTrailer(t *testing.T) {
	document := pdfQualificationDocument(t, []byte("q Q"), false)
	eof := bytes.LastIndex(document, []byte("%%EOF\n"))
	if eof < 0 {
		t.Fatal("EOF marker is missing")
	}
	mutated := make([]byte, 0, len(document)+len("garbage\n"))
	mutated = append(mutated, document[:eof]...)
	mutated = append(mutated, []byte("garbage\n")...)
	mutated = append(mutated, document[eof:]...)
	if err := validatePDFCrossReferences(mutated); err == nil {
		t.Fatal("unexpected token between startxref and EOF was accepted")
	}
}

func TestPDFQualificationValidatorRejectsOverflowedStartXref(t *testing.T) {
	document := pdfQualificationDocument(t, []byte("q Q"), false)
	marker := []byte("startxref\n")
	valueStart := bytes.LastIndex(document, marker)
	if valueStart < 0 {
		t.Fatal("startxref is missing")
	}
	valueStart += len(marker)
	valueEnd := bytes.IndexByte(document[valueStart:], '\n')
	if valueEnd <= 0 {
		t.Fatal("startxref value is missing")
	}
	valueEnd += valueStart
	mutated := make([]byte, 0, len(document)+64)
	mutated = append(mutated, document[:valueStart]...)
	mutated = append(mutated, bytes.Repeat([]byte{'9'}, 80)...)
	mutated = append(mutated, document[valueEnd:]...)
	if err := validatePDFCrossReferences(mutated); err == nil {
		t.Fatal("overflowed startxref value was accepted")
	}
}

func TestPDFQualificationValidatorRejectsInvalidNonStreamObjectTerminator(t *testing.T) {
	document := pdfQualificationDocument(t, []byte("q Q"), false)
	canonical := []byte("\nendobj\n2 0 obj")
	terminator := bytes.Index(document, canonical)
	if terminator < 0 {
		t.Fatal("first object terminator is missing")
	}
	mutated := append([]byte(nil), document...)
	copy(mutated[terminator:terminator+len(canonical)], []byte("\nendobx\n2 0 obj"))
	if err := validatePDFCrossReferences(mutated); err == nil {
		t.Fatal("invalid non-stream object terminator was accepted")
	}
}
