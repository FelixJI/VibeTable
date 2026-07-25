// Package contracts provides the storage-neutral JSON boundary used by the
// sidecar. Product contract ownership remains in the repository-level
// contracts/v1 bundle.
package contracts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const Version = "1.0"

// Document deliberately preserves unknown v1 properties so compatible
// additions can round-trip through the Go sidecar without data loss.
type Document map[string]any

func Decode(reader io.Reader) (Document, error) {
	if reader == nil {
		return nil, errors.New("contract reader is required")
	}

	decoder := json.NewDecoder(reader)
	decoder.UseNumber()

	var document Document
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode contract document: %w", err)
	}
	if document == nil {
		return nil, errors.New("contract document must be an object")
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("contract document contains trailing JSON")
		}
		return nil, fmt.Errorf("decode trailing contract data: %w", err)
	}

	version, ok := document["contractVersion"].(string)
	if !ok || version != Version {
		return nil, fmt.Errorf("contractVersion must be %q", Version)
	}
	return document, nil
}

func Encode(writer io.Writer, document Document) error {
	if writer == nil {
		return errors.New("contract writer is required")
	}
	if document == nil {
		return errors.New("contract document is required")
	}
	version, ok := document["contractVersion"].(string)
	if !ok || version != Version {
		return fmt.Errorf("contractVersion must be %q", Version)
	}
	if err := json.NewEncoder(writer).Encode(document); err != nil {
		return fmt.Errorf("encode contract document: %w", err)
	}
	return nil
}
