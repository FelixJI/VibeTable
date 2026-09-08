package main

import (
	"bytes"
	"compress/zlib"
	"fmt"
)

func pdfQualificationDocument(content []byte, flate bool) ([]byte, error) {
	stream := append([]byte(nil), content...)
	filter := ""
	if flate {
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		if _, err := writer.Write(stream); err != nil {
			return nil, fmt.Errorf("compress PDF content stream: %w", err)
		}
		if err := writer.Close(); err != nil {
			return nil, fmt.Errorf("close PDF content stream: %w", err)
		}
		stream, filter = compressed.Bytes(), " /Filter /FlateDecode"
	}
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>"),
		[]byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		append(
			[]byte(fmt.Sprintf("<< /Length %d%s >>\nstream\n", len(stream), filter)),
			append(stream, []byte("\nendstream")...)...,
		),
	}
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
	return document.Bytes(), nil
}
