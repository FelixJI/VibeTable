package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
)

type corpusCase struct {
	ExpectedRepeatedCharacter string                             `json:"expectedRepeatedCharacter"`
	File                      string                             `json:"file"`
	Tier                      string                             `json:"tier"`
	ExpectedStatuses          []workspacesearch.ExtractionStatus `json:"expectedStatuses"`
	RequiredTokens            []string                           `json:"requiredTokensWhenIndexed"`
	ForbiddenTokens           []string                           `json:"forbiddenTokens"`
	EmptyTextOnRejection      bool                               `json:"emptyTextOnRejection"`
	ExpectedCodePoints        *int                               `json:"expectedCodePoints"`
	ExpectedErrorCode         *string                            `json:"expectedErrorCode"`
}

type corpusBudgets struct {
	InputBytes               int64 `json:"inputBytes"`
	SingleDecodedStreamBytes int64 `json:"singleDecodedStreamBytes"`
	CumulativeDecodedBytes   int64 `json:"cumulativeDecodedBytes"`
	OutputCodePoints         int   `json:"outputCodePoints"`
	DeadlineSeconds          int64 `json:"deadlineSeconds"`
}

type corpus struct {
	Budgets corpusBudgets `json:"budgets"`
	Version int           `json:"corpusVersion"`
	Cases   []corpusCase  `json:"cases"`
}

type observation struct {
	File                      string                           `json:"file"`
	Tier                      string                           `json:"tier"`
	Status                    workspacesearch.ExtractionStatus `json:"status"`
	ErrorCode                 *string                          `json:"errorCode"`
	CodePoints                int                              `json:"codePoints"`
	ElapsedMilliseconds       float64                          `json:"elapsedMilliseconds"`
	CPUMilliseconds           *float64                         `json:"cpuMilliseconds,omitempty"`
	PeakJobMemoryBytes        *int64                           `json:"peakJobMemoryBytes,omitempty"`
	PeakWorkerWorkingSetBytes *int64                           `json:"peakWorkerWorkingSetBytes,omitempty"`
	AllProcessesExited        *bool                            `json:"allProcessesExited,omitempty"`
	WorkerReason              *string                          `json:"workerReason,omitempty"`
	Mismatches                []string                         `json:"mismatches"`
}

type externalResult struct {
	Status    workspacesearch.ExtractionStatus `json:"status"`
	Text      string                           `json:"text"`
	ErrorCode *string                          `json:"errorCode"`
}

type externalObservation struct {
	File                      string         `json:"file"`
	Result                    externalResult `json:"result"`
	ElapsedMilliseconds       *float64       `json:"elapsedMilliseconds"`
	CPUMilliseconds           *float64       `json:"cpuMilliseconds"`
	PeakJobMemoryBytes        *int64         `json:"peakJobMemoryBytes"`
	PeakWorkerWorkingSetBytes *int64         `json:"peakWorkerWorkingSetBytes"`
	AllProcessesExited        bool           `json:"allProcessesExited"`
	WorkerReason              *string        `json:"workerReason"`
}

type externalObservations struct {
	Version       int                   `json:"corpusVersion"`
	Budgets       corpusBudgets         `json:"budgets"`
	ProcessLimits processLimits         `json:"processLimits"`
	Observations  []externalObservation `json:"observations"`
}

type processLimits struct {
	MemoryBytes     int64 `json:"memoryBytes"`
	CPUSeconds      int64 `json:"cpuSeconds"`
	DeadlineSeconds int64 `json:"deadlineSeconds"`
}

func main() {
	var err error
	switch {
	case len(os.Args) == 3:
		err = run(os.Args[1], os.Args[2], os.Stdout)
	case len(os.Args) == 4 && os.Args[1] == "--observations":
		err = runObservations(os.Args[2], os.Args[3], os.Stdout)
	default:
		fmt.Fprintln(os.Stderr, "usage: pdf-qualification <manifest.json> <generated-pdf-directory>\n       pdf-qualification --observations <manifest.json> <observations.json>")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(manifestPath, directory string, output io.Writer) error {
	definition, err := loadCorpus(manifestPath)
	if err != nil {
		return err
	}
	rows := make([]observation, 0, len(definition.Cases))
	for _, item := range definition.Cases {
		input, err := os.Open(filepath.Join(directory, item.File))
		if err != nil {
			return err
		}
		started := time.Now()
		result := workspacesearch.Extract(context.Background(), item.File, "application/pdf", input, workspacesearch.DefaultExtractionLimits)
		if err := input.Close(); err != nil {
			return err
		}
		rows = append(rows, qualify(item, result.Status, result.Text, result.ErrorCode, float64(time.Since(started).Milliseconds())))
	}
	return writeReport(definition, rows, nil, output)
}

func runObservations(manifestPath, observationsPath string, output io.Writer) error {
	definition, err := loadCorpus(manifestPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(observationsPath)
	if err != nil {
		return err
	}
	var source externalObservations
	if err := json.Unmarshal(data, &source); err != nil {
		return err
	}
	if source.Version != definition.Version || source.Budgets != definition.Budgets {
		return errors.New("PDF observations differ from corpus version or budgets")
	}
	if source.ProcessLimits.MemoryBytes <= 0 || source.ProcessLimits.CPUSeconds <= 0 || source.ProcessLimits.DeadlineSeconds <= 0 || source.ProcessLimits.DeadlineSeconds != definition.Budgets.DeadlineSeconds || source.ProcessLimits.CPUSeconds > source.ProcessLimits.DeadlineSeconds {
		return errors.New("PDF observation process limits are invalid")
	}
	items := make(map[string]externalObservation, len(source.Observations))
	for _, item := range source.Observations {
		if _, exists := items[item.File]; exists {
			return errors.New("duplicate PDF observation file")
		}
		if item.ElapsedMilliseconds == nil || item.CPUMilliseconds == nil || item.PeakJobMemoryBytes == nil || item.PeakWorkerWorkingSetBytes == nil || item.WorkerReason == nil {
			return errors.New("PDF observation metrics are required")
		}
		if !knownWorkerReason(*item.WorkerReason) {
			return errors.New("PDF observation has unknown worker reason")
		}
		if !finiteNonNegative(*item.ElapsedMilliseconds) || !finiteNonNegative(*item.CPUMilliseconds) || *item.PeakJobMemoryBytes < 0 || *item.PeakWorkerWorkingSetBytes < 0 {
			return errors.New("PDF observation metrics must be non-negative")
		}
		if !item.AllProcessesExited {
			return errors.New("PDF observation has unclosed processes")
		}
		if !knownStatus(item.Result.Status) {
			return errors.New("PDF observation has unknown extraction status")
		}
		if *item.WorkerReason != "Succeeded" && (item.Result.Text != "" || item.Result.Status == workspacesearch.ExtractionIndexed || item.Result.Status == workspacesearch.ExtractionTruncated) {
			return errors.New("PDF observation has invalid failed worker result")
		}
		items[item.File] = item
	}
	if len(items) != len(definition.Cases) {
		return errors.New("PDF observations do not match corpus file set")
	}
	rows := make([]observation, 0, len(definition.Cases))
	for _, expected := range definition.Cases {
		item, found := items[expected.File]
		if !found {
			return errors.New("PDF observations do not match corpus file set")
		}
		row := qualify(expected, item.Result.Status, item.Result.Text, item.Result.ErrorCode, *item.ElapsedMilliseconds)
		row.CPUMilliseconds = item.CPUMilliseconds
		row.PeakJobMemoryBytes = item.PeakJobMemoryBytes
		row.PeakWorkerWorkingSetBytes = item.PeakWorkerWorkingSetBytes
		row.AllProcessesExited = &item.AllProcessesExited
		row.WorkerReason = item.WorkerReason
		rows = append(rows, row)
	}
	return writeReport(definition, rows, &source.ProcessLimits, output)
}

func loadCorpus(manifestPath string) (corpus, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return corpus{}, err
	}
	var definition corpus
	if err := json.Unmarshal(data, &definition); err != nil {
		return corpus{}, err
	}
	if definition.Version != 1 || len(definition.Cases) == 0 {
		return corpus{}, errors.New("unsupported or empty PDF qualification corpus")
	}
	limits := workspacesearch.DefaultExtractionLimits
	actualBudgets := corpusBudgets{limits.MaximumInputBytes, limits.MaximumPartBytes, limits.MaximumUncompressed, limits.MaximumTextCodePoints, int64(limits.MaximumDuration / time.Second)}
	if definition.Budgets != actualBudgets || limits.MaximumDuration != time.Duration(definition.Budgets.DeadlineSeconds)*time.Second {
		return corpus{}, errors.New("PDF corpus budgets differ from actual extractor limits")
	}
	seen := make(map[string]bool, len(definition.Cases))
	for _, item := range definition.Cases {
		if item.File == "." || filepath.Base(item.File) != item.File || filepath.Ext(item.File) != ".pdf" || seen[item.File] || len(item.ExpectedStatuses) == 0 {
			return corpus{}, errors.New("invalid or duplicate PDF corpus case")
		}
		if item.ExpectedRepeatedCharacter != "" && utf8.RuneCountInString(item.ExpectedRepeatedCharacter) != 1 {
			return corpus{}, errors.New("expected repeated PDF character must be one code point")
		}
		seen[item.File] = true
	}
	return definition, nil
}

func qualify(item corpusCase, status workspacesearch.ExtractionStatus, text string, errorCode *string, elapsedMilliseconds float64) observation {
	row := observation{File: item.File, Tier: item.Tier, Status: status, ErrorCode: errorCode, CodePoints: utf8.RuneCountInString(text), ElapsedMilliseconds: elapsedMilliseconds, Mismatches: []string{}}
	if !slices.Contains(item.ExpectedStatuses, status) {
		row.Mismatches = append(row.Mismatches, "status")
	}
	if item.ExpectedRepeatedCharacter != "" && strings.Trim(text, item.ExpectedRepeatedCharacter) != "" {
		row.Mismatches = append(row.Mismatches, "repeated text character")
	}
	if item.ExpectedCodePoints != nil && row.CodePoints != *item.ExpectedCodePoints {
		row.Mismatches = append(row.Mismatches, "code points")
	}
	if item.ExpectedErrorCode != nil && (errorCode == nil || *errorCode != *item.ExpectedErrorCode) {
		row.Mismatches = append(row.Mismatches, "error code")
	}
	if status == workspacesearch.ExtractionIndexed {
		for _, token := range item.RequiredTokens {
			if !strings.Contains(text, token) {
				row.Mismatches = append(row.Mismatches, "missing token: "+token)
			}
		}
	} else if item.EmptyTextOnRejection && text != "" {
		row.Mismatches = append(row.Mismatches, "partial text on rejection")
	}
	for _, token := range item.ForbiddenTokens {
		if strings.Contains(text, token) {
			row.Mismatches = append(row.Mismatches, "forbidden token: "+token)
		}
	}
	return row
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func knownStatus(status workspacesearch.ExtractionStatus) bool {
	switch status {
	case workspacesearch.ExtractionIndexed, workspacesearch.ExtractionUnsupported, workspacesearch.ExtractionFailed, workspacesearch.ExtractionTruncated, workspacesearch.ExtractionPasswordProtected, workspacesearch.ExtractionNoTextLayer, workspacesearch.ExtractionResourceLimited, workspacesearch.ExtractionCancelled:
		return true
	default:
		return false
	}
}

func knownWorkerReason(reason string) bool {
	switch reason {
	case "Succeeded", "Cancelled", "DeadlineExceeded", "CpuLimitExceeded", "MemoryLimitExceeded", "OutputLimitExceeded", "WorkerFailed", "CleanupFailed":
		return true
	default:
		return false
	}
}

func writeReport(definition corpus, rows []observation, limits *processLimits, output io.Writer) error {
	failed := 0
	for _, row := range rows {
		if len(row.Mismatches) != 0 {
			failed++
		}
	}
	report := struct {
		CorpusVersion int                              `json:"corpusVersion"`
		Limits        workspacesearch.ExtractionLimits `json:"limits"`
		Failed        int                              `json:"failed"`
		ProcessLimits *processLimits                   `json:"processLimits,omitempty"`
		Cases         []observation                    `json:"cases"`
	}{definition.Version, workspacesearch.DefaultExtractionLimits, failed, limits, rows}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return err
	}
	if failed != 0 {
		return fmt.Errorf("PDF corpus has %d mismatched cases", failed)
	}
	return nil
}
