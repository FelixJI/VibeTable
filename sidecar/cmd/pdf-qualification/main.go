package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibetable/vibetable/sidecar/internal/workspacesearch"
)

type corpusCase struct {
	File                 string                             `json:"file"`
	Tier                 string                             `json:"tier"`
	ExpectedStatuses     []workspacesearch.ExtractionStatus `json:"expectedStatuses"`
	RequiredTokens       []string                           `json:"requiredTokensWhenIndexed"`
	ForbiddenTokens      []string                           `json:"forbiddenTokens"`
	EmptyTextOnRejection bool                               `json:"emptyTextOnRejection"`
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
	File                string                           `json:"file"`
	Tier                string                           `json:"tier"`
	Status              workspacesearch.ExtractionStatus `json:"status"`
	ErrorCode           *string                          `json:"errorCode"`
	CodePoints          int                              `json:"codePoints"`
	ElapsedMilliseconds int64                            `json:"elapsedMilliseconds"`
	Mismatches          []string                         `json:"mismatches"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pdf-qualification <manifest.json> <generated-pdf-directory>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(manifestPath, directory string, output io.Writer) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var definition corpus
	if err := json.Unmarshal(data, &definition); err != nil {
		return err
	}
	if definition.Version != 1 || len(definition.Cases) == 0 {
		return errors.New("unsupported or empty PDF qualification corpus")
	}
	limits := workspacesearch.DefaultExtractionLimits
	actualBudgets := corpusBudgets{limits.MaximumInputBytes, limits.MaximumPartBytes, limits.MaximumUncompressed, limits.MaximumTextCodePoints, int64(limits.MaximumDuration / time.Second)}
	if definition.Budgets != actualBudgets || limits.MaximumDuration != time.Duration(definition.Budgets.DeadlineSeconds)*time.Second {
		return errors.New("PDF corpus budgets differ from actual extractor limits")
	}
	seen := make(map[string]bool)
	for _, item := range definition.Cases {
		if item.File == "." || filepath.Base(item.File) != item.File || filepath.Ext(item.File) != ".pdf" || seen[item.File] || len(item.ExpectedStatuses) == 0 {
			return errors.New("invalid or duplicate PDF corpus case")
		}
		seen[item.File] = true
	}
	rows := make([]observation, 0, len(definition.Cases))
	failed := 0
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
		row := observation{File: item.File, Tier: item.Tier, Status: result.Status, ErrorCode: result.ErrorCode, CodePoints: utf8.RuneCountInString(result.Text), ElapsedMilliseconds: time.Since(started).Milliseconds(), Mismatches: []string{}}
		if !slices.Contains(item.ExpectedStatuses, result.Status) {
			row.Mismatches = append(row.Mismatches, "status")
		}
		if result.Status == workspacesearch.ExtractionIndexed {
			for _, token := range item.RequiredTokens {
				if !strings.Contains(result.Text, token) {
					row.Mismatches = append(row.Mismatches, "missing token: "+token)
				}
			}
		} else if item.EmptyTextOnRejection && result.Text != "" {
			row.Mismatches = append(row.Mismatches, "partial text on rejection")
		}
		for _, token := range item.ForbiddenTokens {
			if strings.Contains(result.Text, token) {
				row.Mismatches = append(row.Mismatches, "forbidden token: "+token)
			}
		}
		if len(row.Mismatches) != 0 {
			failed++
		}
		rows = append(rows, row)
	}
	report := struct {
		CorpusVersion int                              `json:"corpusVersion"`
		Limits        workspacesearch.ExtractionLimits `json:"limits"`
		Failed        int                              `json:"failed"`
		Cases         []observation                    `json:"cases"`
	}{definition.Version, workspacesearch.DefaultExtractionLimits, failed, rows}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return err
	}
	if failed != 0 {
		return fmt.Errorf("PDF corpus has %d mismatched cases", failed)
	}
	return nil
}
