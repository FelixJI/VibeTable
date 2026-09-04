package workspacesearch

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/net/html"
)

type ExtractionStatus string

const (
	ExtractionIndexed           ExtractionStatus = "indexed"
	ExtractionUnsupported       ExtractionStatus = "unsupported"
	ExtractionFailed            ExtractionStatus = "failed"
	ExtractionTruncated         ExtractionStatus = "truncated"
	ExtractionPasswordProtected ExtractionStatus = "passwordProtected"
	ExtractionNoTextLayer       ExtractionStatus = "noTextLayer"
	ExtractionResourceLimited   ExtractionStatus = "resourceLimited"
	ExtractionCancelled         ExtractionStatus = "cancelled"
)

type ExtractionLimits struct {
	MaximumInputBytes     int64
	MaximumZIPEntries     int
	MaximumUncompressed   int64
	MaximumPartBytes      int64
	MaximumTextCodePoints int
	MaximumDuration       time.Duration
}

var DefaultExtractionLimits = ExtractionLimits{
	MaximumInputBytes:     64 << 20,
	MaximumZIPEntries:     2_000,
	MaximumUncompressed:   256 << 20,
	MaximumPartBytes:      32 << 20,
	MaximumTextCodePoints: 2_000_000,
	MaximumDuration:       30 * time.Second,
}

type ExtractionResult struct {
	Status    ExtractionStatus `json:"status"`
	Text      string           `json:"text"`
	ErrorCode *string          `json:"errorCode"`
}

func Extract(
	ctx context.Context,
	name, mimeType string,
	reader io.Reader,
	limits ExtractionLimits,
) ExtractionResult {
	if err := ctx.Err(); err != nil {
		return extractionError(ExtractionCancelled, "extract.cancelled")
	}
	if err := ValidateExtractionLimits(limits); err != nil {
		return extractionError(ExtractionFailed, "extract.limits_invalid")
	}
	extractionCtx, cancel := context.WithTimeout(ctx, limits.MaximumDuration)
	defer cancel()
	payload, err := readLimitedContext(extractionCtx, reader, limits.MaximumInputBytes+1)
	if err != nil {
		if ctx.Err() != nil {
			return extractionError(ExtractionCancelled, "extract.cancelled")
		}
		if errors.Is(extractionCtx.Err(), context.DeadlineExceeded) {
			return extractionError(ExtractionResourceLimited, "extract.timeout")
		}
		return extractionError(ExtractionFailed, "extract.read_failed")
	}
	if int64(len(payload)) > limits.MaximumInputBytes {
		return extractionError(ExtractionResourceLimited, "extract.input_limit")
	}
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	var text string
	switch extension {
	case "txt", "md", "markdown", "csv", "yaml", "yml":
		if !utf8.Valid(payload) {
			return extractionError(ExtractionFailed, "extract.invalid_utf8")
		}
		text = string(payload)
	case "json":
		text, err = extractJSON(extractionCtx, payload)
	case "xml":
		text, err = extractXML(extractionCtx, bytes.NewReader(payload))
	case "html", "htm":
		text, err = extractHTML(extractionCtx, payload)
	case "docx", "pptx", "xlsx":
		return extractOOXML(extractionCtx, extension, payload, limits)
	case "pdf":
		return extractPDF(extractionCtx, payload, limits)
	default:
		if strings.HasPrefix(mimeType, "text/") && utf8.Valid(payload) {
			text = string(payload)
		} else {
			return extractionError(ExtractionUnsupported, "extract.unsupported")
		}
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return extractionError(ExtractionCancelled, "extract.cancelled")
		}
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(extractionCtx.Err(), context.DeadlineExceeded) {
			return extractionError(ExtractionResourceLimited, "extract.timeout")
		}
		return extractionError(ExtractionFailed, "extract.invalid_content")
	}
	return boundedExtraction(text, limits)
}

func readLimitedContext(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	var output bytes.Buffer
	buffer := make([]byte, 32*1024)
	for int64(output.Len()) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := limit - int64(output.Len())
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		read, err := reader.Read(chunk)
		if read > 0 {
			_, _ = output.Write(chunk[:read])
		}
		if errors.Is(err, io.EOF) {
			return output.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		if read == 0 {
			return nil, io.ErrNoProgress
		}
	}
	return output.Bytes(), nil
}

func extractJSON(ctx context.Context, payload []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var output strings.Builder
	var visit func(any) error
	visit = func(current any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch typed := current.(type) {
		case map[string]any:
			for key, item := range typed {
				output.WriteString(key)
				output.WriteByte(' ')
				if err := visit(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range typed {
				if err := visit(item); err != nil {
					return err
				}
			}
		case string:
			output.WriteString(typed)
			output.WriteByte(' ')
		case json.Number:
			output.WriteString(typed.String())
			output.WriteByte(' ')
		case bool:
			output.WriteString(strconv.FormatBool(typed))
			output.WriteByte(' ')
		}
		return nil
	}
	if err := visit(value); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func extractXML(ctx context.Context, reader io.Reader) (string, error) {
	decoder := xml.NewDecoder(reader)
	decoder.Strict = true
	var output strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if value, ok := token.(xml.CharData); ok {
			text := strings.TrimSpace(string(value))
			if text != "" {
				output.WriteString(text)
				output.WriteByte(' ')
			}
		}
	}
	return strings.TrimSpace(output.String()), nil
}

func extractHTML(ctx context.Context, payload []byte) (string, error) {
	document, err := html.Parse(bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	var output strings.Builder
	var visit func(*html.Node) error
	visit = func(node *html.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.Type == html.ElementNode && (node.Data == "script" || node.Data == "style") {
			return nil
		}
		if node.Type == html.TextNode {
			text := strings.TrimSpace(node.Data)
			if text != "" {
				output.WriteString(text)
				output.WriteByte(' ')
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(document); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func extractOOXML(
	ctx context.Context,
	kind string,
	payload []byte,
	limits ExtractionLimits,
) ExtractionResult {
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return extractionError(ExtractionFailed, "extract.ooxml_invalid")
	}
	if len(archive.File) > limits.MaximumZIPEntries {
		return extractionError(ExtractionResourceLimited, "extract.zip_entries_limit")
	}
	var total int64
	var output strings.Builder
	for _, file := range archive.File {
		if err := ctx.Err(); err != nil {
			return extractionContextError(ctx)
		}
		if file.UncompressedSize64 > uint64(limits.MaximumPartBytes) {
			return extractionError(ExtractionResourceLimited, "extract.zip_part_limit")
		}
		total += int64(file.UncompressedSize64)
		if total > limits.MaximumUncompressed {
			return extractionError(ExtractionResourceLimited, "extract.zip_total_limit")
		}
		if !ooxmlTextPart(kind, file.Name) {
			continue
		}
		part, err := file.Open()
		if err != nil {
			return extractionError(ExtractionFailed, "extract.ooxml_part_failed")
		}
		text, extractErr := extractXML(ctx, io.LimitReader(part, limits.MaximumPartBytes+1))
		closeErr := part.Close()
		if extractErr != nil || closeErr != nil {
			if ctx.Err() != nil {
				return extractionContextError(ctx)
			}
			return extractionError(ExtractionFailed, "extract.ooxml_part_failed")
		}
		output.WriteString(text)
		output.WriteByte(' ')
	}
	return boundedExtraction(strings.Join(strings.Fields(output.String()), " "), limits)
}

func ooxmlTextPart(kind, name string) bool {
	clean := filepath.ToSlash(name)
	if strings.Contains(clean, "../") || strings.HasPrefix(clean, "/") {
		return false
	}
	switch kind {
	case "docx":
		return clean == "word/document.xml" || strings.HasPrefix(clean, "word/header") || strings.HasPrefix(clean, "word/footer")
	case "pptx":
		return strings.HasPrefix(clean, "ppt/slides/slide") && strings.HasSuffix(clean, ".xml")
	case "xlsx":
		return clean == "xl/sharedStrings.xml" || (strings.HasPrefix(clean, "xl/worksheets/sheet") && strings.HasSuffix(clean, ".xml"))
	}
	return false
}

var (
	pdfTextOperator = regexp.MustCompile(
		`(?s)(\((?:\\.|[^\\)])*\)|<[[:xdigit:][:space:]]+>)\s*Tj\b`,
	)
	pdfTextArray   = regexp.MustCompile(`(?s)\[((?:\((?:\\.|[^\\)])*\)|<[[:xdigit:][:space:]]+>|[-+0-9.]+|\s)*)\]\s*TJ\b`)
	pdfStringToken = regexp.MustCompile(`(?s)\((?:\\.|[^\\)])*\)|<[[:xdigit:][:space:]]+>`)
	pdfFlateStream = regexp.MustCompile(`(?s)<<[^>]*?/FlateDecode[^>]*?>>\s*stream\r?\n(.*?)\r?\nendstream`)
)

func extractPDF(ctx context.Context, payload []byte, limits ExtractionLimits) ExtractionResult {
	if !bytes.HasPrefix(payload, []byte("%PDF-")) {
		return extractionError(ExtractionFailed, "extract.pdf_invalid")
	}
	if bytes.Contains(payload, []byte("/Encrypt")) {
		return extractionError(ExtractionPasswordProtected, "extract.password_required")
	}
	if err := ctx.Err(); err != nil {
		return extractionContextError(ctx)
	}
	payloads := [][]byte{payload}
	var decodedTotal int64
	for _, stream := range pdfFlateStream.FindAllSubmatch(payload, -1) {
		if err := ctx.Err(); err != nil {
			return extractionContextError(ctx)
		}
		reader, err := zlib.NewReader(bytes.NewReader(stream[1]))
		if err != nil {
			return extractionError(ExtractionFailed, "extract.pdf_stream_invalid")
		}
		decoded, failure := readPDFStream(ctx, reader, limits.MaximumPartBytes)
		if failure.Status != "" {
			return failure
		}
		decodedTotal += int64(len(decoded))
		if decodedTotal > limits.MaximumUncompressed {
			return extractionError(ExtractionResourceLimited, "extract.pdf_total_limit")
		}
		payloads = append(payloads, decoded)
	}
	text, err := extractPDFText(ctx, payloads)
	if err != nil {
		return extractionContextError(ctx)
	}
	if text == "" {
		return extractionError(ExtractionNoTextLayer, "extract.pdf_no_text")
	}
	return boundedExtraction(text, limits)
}

func readPDFStream(
	ctx context.Context,
	reader io.ReadCloser,
	limit int64,
) ([]byte, ExtractionResult) {
	decoded, readErr := readLimitedContext(ctx, reader, limit+1)
	closeErr := reader.Close()
	if errors.Is(readErr, context.Canceled) {
		return nil, extractionError(ExtractionCancelled, "extract.cancelled")
	}
	if errors.Is(readErr, context.DeadlineExceeded) {
		return nil, extractionError(ExtractionResourceLimited, "extract.timeout")
	}
	if readErr != nil || closeErr != nil {
		return nil, extractionError(ExtractionFailed, "extract.pdf_stream_invalid")
	}
	if int64(len(decoded)) > limit {
		return nil, extractionError(ExtractionResourceLimited, "extract.pdf_stream_limit")
	}
	return decoded, ExtractionResult{}
}

func extractPDFText(ctx context.Context, payloads [][]byte) (string, error) {
	var output strings.Builder
	for _, content := range payloads {
		if err := walkPDFMatches(ctx, pdfTextOperator, content, 1, func(token []byte) error {
			appendPDFToken(&output, token)
			return nil
		}); err != nil {
			return "", err
		}
		if err := walkPDFMatches(ctx, pdfTextArray, content, 1, func(array []byte) error {
			return walkPDFMatches(ctx, pdfStringToken, array, 0, func(token []byte) error {
				appendPDFToken(&output, token)
				return nil
			})
		}); err != nil {
			return "", err
		}
	}
	return strings.Join(strings.Fields(output.String()), " "), ctx.Err()
}

func walkPDFMatches(
	ctx context.Context,
	expression *regexp.Regexp,
	content []byte,
	capture int,
	visit func([]byte) error,
) error {
	for len(content) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		reader := &pdfContextReader{Context: ctx, Reader: bytes.NewReader(content)}
		match := expression.FindReaderSubmatchIndex(reader)
		if match == nil {
			return ctx.Err()
		}
		start, end := match[capture*2], match[capture*2+1]
		if start >= 0 {
			if err := visit(content[start:end]); err != nil {
				return err
			}
		}
		content = content[match[1]:]
	}
	return ctx.Err()
}

type pdfContextReader struct {
	context.Context
	*bytes.Reader
}

func (reader *pdfContextReader) ReadRune() (rune, int, error) {
	if err := reader.Err(); err != nil {
		return 0, 0, err
	}
	return reader.Reader.ReadRune()
}

func appendPDFToken(output *strings.Builder, token []byte) {
	decoded, ok := decodePDFString(token)
	if !ok || strings.TrimSpace(decoded) == "" {
		return
	}
	output.WriteString(decoded)
	output.WriteByte(' ')
}

func decodePDFString(token []byte) (string, bool) {
	if len(token) < 2 {
		return "", false
	}
	var decoded []byte
	if token[0] == '<' {
		compact := bytes.Map(func(value rune) rune {
			if value == ' ' || value == '\t' || value == '\r' || value == '\n' {
				return -1
			}
			return value
		}, token[1:len(token)-1])
		if len(compact)%2 != 0 {
			compact = append(compact, '0')
		}
		decoded = make([]byte, hex.DecodedLen(len(compact)))
		if _, err := hex.Decode(decoded, compact); err != nil {
			return "", false
		}
	} else {
		decoded = decodePDFLiteral(token[1 : len(token)-1])
	}
	if len(decoded) >= 2 && decoded[0] == 0xfe && decoded[1] == 0xff {
		units := make([]uint16, 0, (len(decoded)-2)/2)
		for index := 2; index+1 < len(decoded); index += 2 {
			units = append(units, uint16(decoded[index])<<8|uint16(decoded[index+1]))
		}
		return string(utf16.Decode(units)), true
	}
	if utf8.Valid(decoded) {
		return string(decoded), true
	}
	return "", false
}

func decodePDFLiteral(raw []byte) []byte {
	result := make([]byte, 0, len(raw))
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' || index+1 >= len(raw) {
			result = append(result, raw[index])
			continue
		}
		index++
		switch raw[index] {
		case 'n':
			result = append(result, '\n')
		case 'r':
			result = append(result, '\r')
		case 't':
			result = append(result, '\t')
		case 'b':
			result = append(result, '\b')
		case 'f':
			result = append(result, '\f')
		case '\r':
			if index+1 < len(raw) && raw[index+1] == '\n' {
				index++
			}
		case '\n':
		case '0', '1', '2', '3', '4', '5', '6', '7':
			value := int(raw[index] - '0')
			for count := 1; count < 3 && index+1 < len(raw) &&
				raw[index+1] >= '0' && raw[index+1] <= '7'; count++ {
				index++
				value = value*8 + int(raw[index]-'0')
			}
			result = append(result, byte(value))
		default:
			result = append(result, raw[index])
		}
	}
	return result
}

func boundedExtraction(text string, limits ExtractionLimits) ExtractionResult {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= limits.MaximumTextCodePoints {
		return ExtractionResult{Status: ExtractionIndexed, Text: text}
	}
	runes := []rune(text)
	code := "extract.text_limit"
	return ExtractionResult{
		Status:    ExtractionTruncated,
		Text:      string(runes[:limits.MaximumTextCodePoints]),
		ErrorCode: &code,
	}
}

func extractionError(status ExtractionStatus, code string) ExtractionResult {
	return ExtractionResult{Status: status, ErrorCode: &code}
}

func extractionContextError(ctx context.Context) ExtractionResult {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return extractionError(ExtractionResourceLimited, "extract.timeout")
	}
	return extractionError(ExtractionCancelled, "extract.cancelled")
}

func ValidateExtractionLimits(limits ExtractionLimits) error {
	if limits.MaximumInputBytes <= 0 || limits.MaximumZIPEntries <= 0 ||
		limits.MaximumUncompressed <= 0 || limits.MaximumPartBytes <= 0 ||
		limits.MaximumTextCodePoints <= 0 || limits.MaximumDuration <= 0 {
		return fmt.Errorf("invalid extraction limits")
	}
	return nil
}
