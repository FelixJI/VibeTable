package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	contracts "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
)

const (
	requiredLogicalCorpusBytes = int64(20) << 30
	maximumRSSBytes            = uint64(1) << 30
	// The rebuild budget is calibrated for the qualification hardware class.
	// The dev baseline rebuilds the full 20 GiB corpus in ~93 s; the first
	// GitHub-hosted Windows runner execution (2026-08-15, run 31871131141)
	// failed a 2-minute budget, and the measured follow-up run (31872949111)
	// rebuilt in 184.05 s, ~2x the dev baseline on the corpus-heavy phases.
	// 4 minutes keeps ~30% headroom over the measured runner time while still
	// failing closed on a ~1.3x regression measured on CI (~2.6x on the dev
	// baseline).
	maximumRebuildDuration = 4 * time.Minute
	maximumIndexMultiplier = 32.0
	maximumWarmP95         = 150 * time.Millisecond
	maximumFirstScreen     = 300 * time.Millisecond
	maximumIncrementalP95  = 2 * time.Second
	searchableCodePoints   = 4096
	streamBufferBytes      = 1 << 20
	qualificationPDFCount  = 4
)

type qualificationConfig struct {
	Profile            string
	Records            int
	Files              int
	LogicalCorpusBytes int64
	ReportPath         string
	WorkRoot           string
}

type qualificationReport struct {
	SchemaVersion               int      `json:"schemaVersion"`
	Profile                     string   `json:"profile"`
	GeneratedAt                 string   `json:"generatedAt"`
	Records                     int      `json:"records"`
	FileDocuments               int      `json:"fileDocuments"`
	LogicalCorpusBytes          int64    `json:"logicalCorpusBytes"`
	MaterializedBytes           int64    `json:"materializedBytes"`
	ExtractedInputBytes         int64    `json:"extractedInputBytes"`
	CorpusDigest                string   `json:"corpusDigest"`
	SourceSearchableBytes       int64    `json:"sourceSearchableBytes"`
	ExtractorDocuments          int      `json:"extractorDocuments"`
	ExtractorIndexed            int      `json:"extractorIndexed"`
	ExtractorTruncated          int      `json:"extractorTruncated"`
	ExtractorFailures           int      `json:"extractorFailures"`
	PDFDocuments                int      `json:"pdfDocuments"`
	PDFIndexed                  int      `json:"pdfIndexed"`
	PDFNoText                   int      `json:"pdfNoText"`
	PDFResourceLimited          int      `json:"pdfResourceLimited"`
	IndexBytes                  int64    `json:"indexBytes"`
	IndexMultiplier             float64  `json:"indexMultiplier"`
	PeakHeapBytes               uint64   `json:"peakHeapBytes"`
	PeakRSSBytes                uint64   `json:"peakRssBytes"`
	MaterializationMilliseconds float64  `json:"materializationMilliseconds"`
	ExtractionMilliseconds      float64  `json:"extractionMilliseconds"`
	CorpusMilliseconds          float64  `json:"corpusMilliseconds"`
	ProjectionMilliseconds      float64  `json:"projectionMilliseconds"`
	RebuildMilliseconds         float64  `json:"rebuildMilliseconds"`
	QualificationMilliseconds   float64  `json:"qualificationMilliseconds"`
	FirstScreenMilliseconds     float64  `json:"firstScreenMilliseconds"`
	WarmQueryP95Milliseconds    float64  `json:"warmQueryP95Milliseconds"`
	IncrementalP95Milliseconds  float64  `json:"incrementalP95Milliseconds"`
	Failures                    []string `json:"failures"`
}

func main() {
	profile := flag.String("profile", "release", "qualification profile: pr or release")
	records := flag.Int("records", 100_000, "structured record count")
	files := flag.Int("files", 10_000, "file document count")
	logicalBytes := flag.Int64(
		"logical-bytes", requiredLogicalCorpusBytes, "actual file corpus bytes",
	)
	reportPath := flag.String("report", "", "JSON report path")
	workRoot := flag.String("work-root", "", "repository build/qa work root")
	flag.Parse()
	config := qualificationConfig{
		Profile: *profile, Records: *records, Files: *files,
		LogicalCorpusBytes: *logicalBytes, ReportPath: *reportPath,
		WorkRoot: *workRoot,
	}
	if err := validateConfig(config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validateConfig(config qualificationConfig) error {
	if config.ReportPath == "" || config.WorkRoot == "" {
		return fmt.Errorf("qualification requires --report PATH and --work-root PATH")
	}
	if config.Profile != "pr" && config.Profile != "release" {
		return fmt.Errorf("qualification profile must be pr or release")
	}
	expectedWorkRoot, err := qualificationWorkRoot()
	if err != nil {
		return err
	}
	workRoot, err := filepath.Abs(config.WorkRoot)
	if err != nil || filepath.Clean(workRoot) != expectedWorkRoot {
		return fmt.Errorf(
			"qualification --work-root must be repository build/qa/workbench-qualification-runs",
		)
	}
	if config.Records < 1 || config.Files <= qualificationPDFCount ||
		config.LogicalCorpusBytes < int64(config.Files)*1024 {
		return fmt.Errorf("qualification corpus dimensions are invalid")
	}
	if config.Profile == "release" &&
		(config.Records != 100_000 || config.Files != 10_000 ||
			config.LogicalCorpusBytes != requiredLogicalCorpusBytes) {
		return fmt.Errorf(
			"release qualification requires --records 100000 --files 10000 --logical-bytes %d",
			requiredLogicalCorpusBytes,
		)
	}
	return nil
}

func qualificationWorkRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve qualification working directory: %w", err)
	}
	for {
		project := filepath.Join(current, ".ci", "project.json")
		goModule := filepath.Join(current, "sidecar", "go.mod")
		if _, projectErr := os.Stat(project); projectErr == nil {
			if _, moduleErr := os.Stat(goModule); moduleErr == nil {
				return filepath.Clean(filepath.Join(
					current, "build", "qa", "workbench-qualification-runs",
				)), nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("qualification repository root was not found")
		}
		current = parent
	}
}

func run(ctx context.Context, config qualificationConfig) error {
	if err := os.MkdirAll(config.WorkRoot, 0o755); err != nil {
		return fmt.Errorf("create qualification work root: %w", err)
	}
	root, err := os.MkdirTemp(config.WorkRoot, "run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	engine, err := workspacesearch.Open(filepath.Join(root, "workspace-search.db"))
	if err != nil {
		return err
	}
	defer engine.Close()
	// Materialization prepares the representative workspace and proves that the
	// complete 20 GiB corpus exists. The rebuild SLO begins with extraction from
	// that existing workspace and includes projection, not fixture preparation.
	qualificationStarted := time.Now()
	corpusStarted := time.Now()
	sources, sourceBytes, corpusResult, err := corpus(
		ctx,
		filepath.Join(root, "corpus"),
		config.Records,
		config.Files,
		config.LogicalCorpusBytes,
	)
	if err != nil {
		return err
	}
	corpusDuration := time.Since(corpusStarted)
	extractionDuration := corpusDuration - corpusResult.MaterializationDuration
	projectionStarted := time.Now()
	if err := engine.RebuildProjection(
		ctx,
		sources,
		workspacesearch.ProjectionCheckpoint{
			BusinessOutboxRowID: int64(config.Records),
			FileHeadRevision:    uint64(config.Files),
			MutationRevision:    uint64(config.Records + config.Files),
		},
		nil,
	); err != nil {
		return err
	}
	projectionDuration := time.Since(projectionStarted)
	rebuildDuration := indexRebuildDuration(extractionDuration, projectionDuration)
	request := searchRequest("needle-000042")
	started := time.Now()
	if result, queryErr := engine.Query(ctx, request); queryErr != nil || len(result.Hits) != 1 {
		return fmt.Errorf("first screen result invalid: hits=%d err=%w", len(result.Hits), queryErr)
	}
	firstScreen := time.Since(started)
	warm := make([]time.Duration, 250)
	for index := range warm {
		request.Query = fmt.Sprintf("needle-%06d", index%(config.Records+config.Files))
		started = time.Now()
		if _, err := engine.Query(ctx, request); err != nil {
			return err
		}
		warm[index] = time.Since(started)
	}
	incrementalCount := min(100, len(sources))
	incremental := make([]time.Duration, incrementalCount)
	for index := range incremental {
		source := sources[index]
		source.SourceRevision = fmt.Sprintf("revision-update-%06d", index)
		source.Body += " updated"
		started = time.Now()
		if err := engine.Upsert(ctx, source); err != nil {
			return err
		}
		incremental[index] = time.Since(started)
	}
	indexBytes, err := databaseBytes(filepath.Join(root, "workspace-search.db"))
	if err != nil {
		return err
	}
	peakRSS, heapBytes, err := processMemory()
	if err != nil {
		return err
	}
	report := qualificationReport{
		SchemaVersion: 4, Profile: config.Profile,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Records:     config.Records, FileDocuments: config.Files,
		LogicalCorpusBytes:    corpusResult.LogicalBytes,
		MaterializedBytes:     corpusResult.MaterializedBytes,
		ExtractedInputBytes:   corpusResult.ExtractedInputBytes,
		CorpusDigest:          corpusResult.Digest,
		SourceSearchableBytes: sourceBytes,
		ExtractorDocuments:    corpusResult.Extraction.Documents,
		ExtractorIndexed:      corpusResult.Extraction.Indexed,
		ExtractorTruncated:    corpusResult.Extraction.Truncated,
		ExtractorFailures:     corpusResult.Extraction.Failures,
		PDFDocuments:          corpusResult.Extraction.PDFDocuments,
		PDFIndexed:            corpusResult.Extraction.PDFIndexed,
		PDFNoText:             corpusResult.Extraction.PDFNoText,
		PDFResourceLimited:    corpusResult.Extraction.PDFResourceLimited,
		IndexBytes:            indexBytes,
		IndexMultiplier:       float64(indexBytes) / float64(max(1, sourceBytes)),
		PeakHeapBytes:         heapBytes, PeakRSSBytes: peakRSS,
		MaterializationMilliseconds: milliseconds(corpusResult.MaterializationDuration),
		ExtractionMilliseconds:      milliseconds(extractionDuration),
		CorpusMilliseconds:          milliseconds(corpusDuration),
		ProjectionMilliseconds:      milliseconds(projectionDuration),
		RebuildMilliseconds:         milliseconds(rebuildDuration),
		QualificationMilliseconds:   milliseconds(time.Since(qualificationStarted)),
		FirstScreenMilliseconds:     milliseconds(firstScreen),
		WarmQueryP95Milliseconds:    milliseconds(percentile(warm, 0.95)),
		IncrementalP95Milliseconds:  milliseconds(percentile(incremental, 0.95)),
		Failures:                    []string{},
	}
	if corpusResult.LogicalBytes != config.LogicalCorpusBytes ||
		corpusResult.MaterializedBytes != config.LogicalCorpusBytes ||
		corpusResult.ExtractedInputBytes != config.LogicalCorpusBytes {
		report.Failures = append(report.Failures, "corpus_bytes_incomplete")
	}
	appendExtractionFailures(&report, corpusResult.Extraction, config.Files)
	if config.Profile == "release" && corpusResult.LogicalBytes < requiredLogicalCorpusBytes {
		report.Failures = append(report.Failures, "logical_corpus_too_small")
	}
	if peakRSS > maximumRSSBytes {
		report.Failures = append(report.Failures, "peak_rss_budget")
	}
	if rebuildDuration > maximumRebuildDuration {
		report.Failures = append(report.Failures, "rebuild_budget")
	}
	if report.IndexMultiplier > maximumIndexMultiplier {
		report.Failures = append(report.Failures, "index_multiplier_budget")
	}
	if firstScreen > maximumFirstScreen {
		report.Failures = append(report.Failures, "first_screen_slo")
	}
	if percentile(warm, 0.95) > maximumWarmP95 {
		report.Failures = append(report.Failures, "warm_query_p95_slo")
	}
	if percentile(incremental, 0.95) > maximumIncrementalP95 {
		report.Failures = append(report.Failures, "incremental_p95_slo")
	}
	if err := writeReport(config.ReportPath, report); err != nil {
		return err
	}
	if len(report.Failures) != 0 {
		return fmt.Errorf(
			"workbench qualification failed: %v; rebuild=%s (budget %s), warmP95=%s, firstScreen=%s, incrementalP95=%s, peakRSS=%d",
			qualificationFailureMessage(report.Failures, corpusResult.Extraction.PDFStatusMismatchDetails),
			rebuildDuration.Round(time.Millisecond),
			maximumRebuildDuration,
			percentile(warm, 0.95).Round(time.Millisecond),
			firstScreen.Round(time.Millisecond),
			percentile(incremental, 0.95).Round(time.Millisecond),
			peakRSS,
		)
	}
	return nil
}

func qualificationFailureMessage(failures, pdfMismatchDetails []string) string {
	if len(pdfMismatchDetails) == 0 {
		return fmt.Sprint(failures)
	}
	return fmt.Sprintf("%v; pdfMismatchDetails=%v", failures, pdfMismatchDetails)
}

type extractionSummary struct {
	Documents                int
	Indexed                  int
	Truncated                int
	Failures                 int
	PDFDocuments             int
	PDFIndexed               int
	PDFNoText                int
	PDFResourceLimited       int
	PDFStatusMismatches      int
	PDFStatusMismatchDetails []string
}

func appendExtractionFailures(
	report *qualificationReport, extraction extractionSummary, fileCount int,
) {
	if extraction.Documents != fileCount ||
		extraction.Indexed+extraction.Truncated+
			extraction.PDFNoText+extraction.PDFResourceLimited != fileCount ||
		extraction.Failures != 0 {
		report.Failures = append(report.Failures, "extractor_corpus_incomplete")
	}
	if extraction.PDFDocuments != qualificationPDFCount ||
		extraction.PDFIndexed != 2 || extraction.PDFNoText != 1 ||
		extraction.PDFResourceLimited != 1 || extraction.PDFStatusMismatches != 0 {
		report.Failures = append(report.Failures, "pdf_fixture_status_mismatch")
	}
}

func recordPDFExtraction(summary *extractionSummary, fixtureIndex int,
	expected workspacesearch.ExtractionStatus, actual workspacesearch.ExtractionResult,
) {
	summary.PDFDocuments++
	switch actual.Status {
	case workspacesearch.ExtractionIndexed:
		summary.PDFIndexed++
	case workspacesearch.ExtractionNoTextLayer:
		summary.PDFNoText++
	case workspacesearch.ExtractionResourceLimited:
		summary.PDFResourceLimited++
	}
	if actual.Status == expected {
		return
	}
	summary.Failures++
	summary.PDFStatusMismatches++
	errorCode := "<nil>"
	if actual.ErrorCode != nil {
		errorCode = *actual.ErrorCode
	}
	if len(summary.PDFStatusMismatchDetails) < qualificationPDFCount {
		summary.PDFStatusMismatchDetails = append(summary.PDFStatusMismatchDetails, fmt.Sprintf(
			"fixture=%d expected=%s actual=%s errorCode=%s",
			fixtureIndex, expected, actual.Status, errorCode,
		))
	}
}

type corpusSummary struct {
	LogicalBytes            int64
	MaterializedBytes       int64
	ExtractedInputBytes     int64
	Digest                  string
	Extraction              extractionSummary
	MaterializationDuration time.Duration
}

type qualificationPDFFixture struct {
	Payload          []byte
	Expected         workspacesearch.ExtractionStatus
	MaximumPartBytes int64
}

func corpus(
	ctx context.Context,
	fileRoot string,
	recordCount, fileCount int,
	logicalCorpusBytes int64,
) ([]workspacesearch.SourceDocument, int64, corpusSummary, error) {
	if err := os.MkdirAll(fileRoot, 0o755); err != nil {
		return nil, 0, corpusSummary{}, err
	}
	pdfFixtures, err := qualificationPDFFixtures(recordCount)
	if err != nil {
		return nil, 0, corpusSummary{}, err
	}
	var pdfBytes int64
	for _, fixture := range pdfFixtures {
		pdfBytes += int64(len(fixture.Payload))
	}
	textFileCount := fileCount - len(pdfFixtures)
	textBytes := logicalCorpusBytes - pdfBytes
	if textFileCount < 1 || textBytes < int64(textFileCount)*1024 {
		return nil, 0, corpusSummary{}, fmt.Errorf("qualification corpus is too small for PDF fixtures")
	}
	result := make([]workspacesearch.SourceDocument, 0, recordCount+fileCount)
	combinedDigest := sha256.New()
	var sourceBytes int64
	var summary corpusSummary
	for index := 0; index < recordCount+fileCount; index++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, summary, err
		}
		kind := "record"
		canonicalID := fmt.Sprintf("table-1:record-%06d", index)
		open := contracts.SearchOpenTarget{
			Kind: "record", TableId: stringPointer("table-1"),
			RecordId: stringPointer(fmt.Sprintf("record-%06d", index)),
		}
		title := fmt.Sprintf("needle-%06d quarterly report", index)
		body := "offline workbench 数据工作台 alpha"
		var size *int64
		if index >= recordCount {
			kind = "file"
			fileIndex := index - recordCount
			canonicalID = fmt.Sprintf("document-%06d", fileIndex)
			open = contracts.SearchOpenTarget{Kind: "file", DocumentId: stringPointer(canonicalID)}
			logical := int64(0)
			path := ""
			var expected workspacesearch.ExtractionStatus
			maximumPartBytes := workspacesearch.DefaultExtractionLimits.MaximumPartBytes
			materializeStarted := time.Now()
			var materializedDigest string
			var materializeErr error
			if fileIndex < len(pdfFixtures) {
				fixture := pdfFixtures[fileIndex]
				logical = int64(len(fixture.Payload))
				path = filepath.Join(fileRoot, fmt.Sprintf("document-%06d.pdf", fileIndex))
				expected = fixture.Expected
				maximumPartBytes = fixture.MaximumPartBytes
				materializedDigest, materializeErr = materializePDFCorpusFile(path, fixture.Payload)
			} else {
				textIndex := fileIndex - len(pdfFixtures)
				logical = textBytes / int64(textFileCount)
				if textIndex == textFileCount-1 {
					logical += textBytes % int64(textFileCount)
				}
				path = filepath.Join(fileRoot, fmt.Sprintf("document-%06d.txt", fileIndex))
				materializedDigest, materializeErr = materializeCorpusFile(
					ctx, path, logical, index, fileIndex,
				)
			}
			summary.MaterializationDuration += time.Since(materializeStarted)
			if materializeErr != nil {
				return nil, 0, summary, fmt.Errorf("materialize corpus file: %w", materializeErr)
			}
			summary.LogicalBytes += logical
			summary.MaterializedBytes += logical
			extraction, readBytes, extractedDigest, extractErr := extractCorpusFileWithPartLimit(
				ctx, path, maximumPartBytes,
			)
			status := extraction.Status
			summary.Extraction.Documents++
			if extractErr != nil || materializedDigest != extractedDigest {
				summary.Extraction.Failures++
				if extractErr == nil {
					extractErr = fmt.Errorf("materialized/extracted digest mismatch")
				}
				return nil, 0, summary, extractErr
			}
			summary.ExtractedInputBytes += readBytes
			switch status {
			case workspacesearch.ExtractionIndexed:
				summary.Extraction.Indexed++
			case workspacesearch.ExtractionTruncated:
				summary.Extraction.Truncated++
			}
			if fileIndex < len(pdfFixtures) {
				recordPDFExtraction(&summary.Extraction, fileIndex, expected, extraction)
			} else if status != workspacesearch.ExtractionIndexed &&
				status != workspacesearch.ExtractionTruncated {
				summary.Extraction.Failures++
			}
			_, _ = io.WriteString(combinedDigest, extractedDigest)
			body = extraction.Text
			size = &logical
		}
		sourceBytes += int64(len(title) + len(body))
		result = append(result, workspacesearch.SourceDocument{
			Kind: kind, CanonicalID: canonicalID, Title: title, Body: body,
			SourceRevision: fmt.Sprintf("revision-%06d", index),
			RevisionTime:   "2026-08-12T00:00:00Z", SizeBytes: size,
			Status: "active", Current: true,
			Metadata: []contracts.SearchMetadataItem{}, OpenTarget: open,
		})
	}
	summary.Digest = hex.EncodeToString(combinedDigest.Sum(nil))
	return result, sourceBytes, summary, nil
}

func qualificationPDFFixtures(recordCount int) ([]qualificationPDFFixture, error) {
	definitions := []struct {
		content          string
		flate            bool
		expected         workspacesearch.ExtractionStatus
		maximumPartBytes int64
	}{
		{fmt.Sprintf("BT /F1 12 Tf (needle-%06d native PDF) Tj ET", recordCount), false, workspacesearch.ExtractionIndexed, workspacesearch.DefaultExtractionLimits.MaximumPartBytes},
		{fmt.Sprintf("BT /F1 12 Tf (needle-%06d compressed PDF) Tj ET", recordCount+1), true, workspacesearch.ExtractionIndexed, workspacesearch.DefaultExtractionLimits.MaximumPartBytes},
		{"q 1 0 0 1 0 0 cm Q", false, workspacesearch.ExtractionNoTextLayer, workspacesearch.DefaultExtractionLimits.MaximumPartBytes},
		{"BT /F1 12 Tf (resource limited PDF) Tj ET", true, workspacesearch.ExtractionResourceLimited, 4},
	}
	fixtures := make([]qualificationPDFFixture, 0, len(definitions))
	for _, definition := range definitions {
		payload, err := pdfQualificationDocument([]byte(definition.content), definition.flate)
		if err != nil {
			return nil, err
		}
		fixtures = append(fixtures, qualificationPDFFixture{
			Payload: payload, Expected: definition.expected,
			MaximumPartBytes: definition.maximumPartBytes,
		})
	}
	return fixtures, nil
}

func materializePDFCorpusFile(path string, payload []byte) (string, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func materializeCorpusFile(
	ctx context.Context,
	path string,
	logicalBytes int64,
	globalIndex, fileIndex int,
) (string, error) {
	prefix := []byte(fmt.Sprintf(
		"needle-%06d quarterly report\noffline workbench 数据工作台 alpha\n",
		globalIndex,
	))
	tail := []byte(fmt.Sprintf("\nqualification-tail-%06d\n", fileIndex))
	if logicalBytes < int64(len(prefix)+len(tail)) {
		return "", fmt.Errorf("corpus file size %d is too small", logicalBytes)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	digest := sha256.New()
	writer := io.MultiWriter(file, digest)
	if _, err := writer.Write(prefix); err != nil {
		return "", err
	}
	remaining := logicalBytes - int64(len(prefix)+len(tail))
	chunk := make([]byte, streamBufferBytes)
	filler := []byte("qualification corpus alpha 0123456789\n")
	for index := range chunk {
		chunk[index] = filler[index%len(filler)]
	}
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count := min(remaining, int64(len(chunk)))
		if _, err := writer.Write(chunk[:count]); err != nil {
			return "", err
		}
		remaining -= count
	}
	if _, err := writer.Write(tail); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	closed = true
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type digestingReader struct {
	reader io.Reader
	digest hash.Hash
	bytes  int64
}

func (reader *digestingReader) Read(target []byte) (int, error) {
	count, err := reader.reader.Read(target)
	if count > 0 {
		reader.bytes += int64(count)
		_, _ = reader.digest.Write(target[:count])
	}
	return count, err
}

func extractCorpusFile(
	ctx context.Context,
	path string,
) (workspacesearch.ExtractionResult, int64, string, error) {
	result, readBytes, digest, err := extractCorpusFileWithPartLimit(
		ctx, path, workspacesearch.DefaultExtractionLimits.MaximumPartBytes,
	)
	if err == nil && result.Status != workspacesearch.ExtractionIndexed &&
		result.Status != workspacesearch.ExtractionTruncated {
		err = fmt.Errorf("extract corpus file: unexpected status %s", result.Status)
	}
	return result, readBytes, digest, err
}

func extractCorpusFileWithPartLimit(
	ctx context.Context,
	path string,
	maximumPartBytes int64,
) (workspacesearch.ExtractionResult, int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return workspacesearch.ExtractionResult{}, 0, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return workspacesearch.ExtractionResult{}, 0, "", err
	}
	tracked := &digestingReader{reader: file, digest: sha256.New()}
	limits := workspacesearch.DefaultExtractionLimits
	limits.MaximumInputBytes = info.Size()
	limits.MaximumPartBytes = maximumPartBytes
	limits.MaximumTextCodePoints = searchableCodePoints
	mimeType := "text/plain"
	if filepath.Ext(path) == ".pdf" {
		mimeType = "application/pdf"
	}
	result := workspacesearch.Extract(ctx, filepath.Base(path), mimeType, tracked, limits)
	if tracked.bytes != info.Size() {
		return result, tracked.bytes, "",
			fmt.Errorf("extractor read %d of %d bytes", tracked.bytes, info.Size())
	}
	return result, tracked.bytes, hex.EncodeToString(tracked.digest.Sum(nil)), nil
}

func searchRequest(query string) contracts.SearchRequest {
	return contracts.SearchRequest{
		ContractVersion: "1.0", Query: query, Logic: "and",
		Filters: []contracts.SearchFilter{}, Sorts: []contracts.SearchSort{},
		Scope: "current", Limit: 20,
	}
}

func percentile(values []time.Duration, quantile float64) time.Duration {
	copyValues := append([]time.Duration(nil), values...)
	sort.Slice(copyValues, func(left, right int) bool { return copyValues[left] < copyValues[right] })
	index := int(float64(len(copyValues)-1) * quantile)
	return copyValues[index]
}

func databaseBytes(path string) (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

func writeReport(path string, report qualificationReport) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	// The QA lane report retains stdout on both success and budget failure.
	// Emit the existing report so its precise measurements survive CI upload.
	_, err = os.Stdout.Write(raw)
	return err
}

func milliseconds(value time.Duration) float64 { return float64(value.Microseconds()) / 1000 }
func indexRebuildDuration(extraction, projection time.Duration) time.Duration {
	return extraction + projection
}
func stringPointer(value string) *string { return &value }
