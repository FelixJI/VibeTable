package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
)

const (
	DiffHistoricalFileName = "historical.content"
	DiffEffectiveFileName  = "effective.content"
	diffHistoricalPartial  = "historical.content.partial"
	diffEffectivePartial   = "effective.content.partial"
)

type materializeDiffPairParams struct {
	DocumentID                  string `json:"documentId"`
	HistoricalRevisionID        string `json:"historicalRevisionId"`
	ExpectedEffectiveRevisionID string `json:"expectedEffectiveRevisionId"`
	PathGrant                   string `json:"pathGrant"`
}

type materializeDiffPairResult struct {
	DocumentID           string `json:"documentId"`
	HistoricalRevisionID string `json:"historicalRevisionId"`
	EffectiveRevisionID  string `json:"effectiveRevisionId"`
	HistoricalMimeType   string `json:"historicalMimeType"`
	EffectiveMimeType    string `json:"effectiveMimeType"`
}

func (runtime *Runtime) materializeDiffPair(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, params, err := decodeFileHistoryRequest[materializeDiffPairParams](
		wireRaw,
		paramsRaw,
	)
	if err != nil ||
		!validUUID(params.DocumentID) ||
		!validUUID(params.HistoricalRevisionID) ||
		!validUUID(params.ExpectedEffectiveRevisionID) ||
		params.PathGrant == "" {
		return nil, errors.New("file_history.request_invalid")
	}
	destination, err := consumePathGrant(
		ctx,
		params.PathGrant,
		"fileHistory.materializeDiffPair",
		wire.OperationID,
		"document-diff-materialize",
	)
	if err != nil {
		return nil, err
	}
	if err := validateEmptyDiffDestination(destination); err != nil {
		return nil, err
	}

	pair, err := runtime.history.OpenDiffPair(
		ctx,
		params.DocumentID,
		params.HistoricalRevisionID,
		params.ExpectedEffectiveRevisionID,
	)
	if err != nil {
		return nil, err
	}
	defer pair.Close()
	cleanup := true
	defer func() {
		if cleanup {
			cleanupDiffMaterialization(destination)
		}
	}()
	if err := materializeDiffFile(
		ctx,
		destination,
		diffHistoricalPartial,
		DiffHistoricalFileName,
		pair.HistoricalContent,
		pair.HistoricalRevision.Size,
	); err != nil {
		return nil, err
	}
	if err := materializeDiffFile(
		ctx,
		destination,
		diffEffectivePartial,
		DiffEffectiveFileName,
		pair.EffectiveContent,
		pair.EffectiveRevision.Size,
	); err != nil {
		return nil, err
	}
	if err := runtime.history.AssertEffectiveRevision(
		params.DocumentID,
		params.ExpectedEffectiveRevisionID,
	); err != nil {
		return nil, err
	}
	cleanup = false
	return materializeDiffPairResult{
		DocumentID:           params.DocumentID,
		HistoricalRevisionID: pair.HistoricalRevision.RevisionID,
		EffectiveRevisionID:  pair.EffectiveRevision.RevisionID,
		HistoricalMimeType:   pair.HistoricalRevision.MimeType,
		EffectiveMimeType:    pair.EffectiveRevision.MimeType,
	}, nil
}

type assertEffectiveRevisionParams struct {
	DocumentID                  string `json:"documentId"`
	ExpectedEffectiveRevisionID string `json:"expectedEffectiveRevisionId"`
}

func (runtime *Runtime) assertEffectiveRevision(
	_ context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	_, params, err := decodeFileHistoryRequest[assertEffectiveRevisionParams](
		wireRaw,
		paramsRaw,
	)
	if err != nil ||
		!validUUID(params.DocumentID) ||
		!validUUID(params.ExpectedEffectiveRevisionID) {
		return nil, errors.New("file_history.request_invalid")
	}
	if err := runtime.history.AssertEffectiveRevision(
		params.DocumentID,
		params.ExpectedEffectiveRevisionID,
	); err != nil {
		return nil, err
	}
	return map[string]any{
		"documentId":          params.DocumentID,
		"effectiveRevisionId": params.ExpectedEffectiveRevisionID,
		"stable":              true,
	}, nil
}

func validateEmptyDiffDestination(destination string) error {
	info, err := os.Lstat(destination)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("file_history.diff_destination_invalid"), err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		return errors.Join(errors.New("file_history.diff_destination_not_empty"), err)
	}
	return nil
}

func materializeDiffFile(
	ctx context.Context,
	destination string,
	partialName string,
	finalName string,
	source io.Reader,
	expectedSize int64,
) error {
	if expectedSize < 0 {
		return filehistory.ErrStateCorrupt
	}
	partialPath := filepath.Join(destination, partialName)
	finalPath := filepath.Join(destination, finalName)
	target, err := os.OpenFile(
		partialPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return err
	}
	written, copyErr := copyDiffContent(ctx, target, source, expectedSize)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil || written != expectedSize {
		return errors.Join(
			errors.New("file_history.diff_materialization_failed"),
			copyErr,
			closeErr,
		)
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		return err
	}
	return nil
}

func copyDiffContent(
	ctx context.Context,
	target io.Writer,
	source io.Reader,
	expectedSize int64,
) (int64, error) {
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			total += int64(read)
			if total > expectedSize {
				return total, filehistory.ErrStateCorrupt
			}
			if _, err := target.Write(buffer[:read]); err != nil {
				return total, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
		if read == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func cleanupDiffMaterialization(destination string) {
	for _, name := range []string{
		diffHistoricalPartial,
		diffEffectivePartial,
		DiffHistoricalFileName,
		DiffEffectiveFileName,
	} {
		_ = os.Remove(filepath.Join(destination, name))
	}
}
