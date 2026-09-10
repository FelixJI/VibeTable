package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

// Preserve the Python root DTO separately from the mutation domain decoder.
func decodeMutationProductParams(raw json.RawMessage) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid mutation parameters")
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		start := index
		for index++; index < len(raw); index++ {
			if raw[index] == '\\' {
				index++
				continue
			}
			if raw[index] == '"' {
				break
			}
		}
		if !lookupStringHasUnicodeScalars(raw[start : index+1]) {
			return nil, errors.New("mutation parameters require Unicode scalars")
		}
	}
	if err := validateQueryPageValue(value, 0); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("mutation parameters must be an object")
	}
	for key, item := range object {
		switch key {
		case "contractVersion", "requestId", "idempotencyKey", "tableId", "schemaRevision":
			if text, ok := item.(string); !ok || text == "" {
				return nil, errors.New("mutation identifier must be nonempty text")
			}
		case "expectedRevision", "expectedDigest":
			if item != nil {
				if text, ok := item.(string); !ok || text == "" {
					return nil, errors.New("mutation expectation must be null or nonempty text")
				}
			}
		case "operations":
			if _, ok := item.([]any); !ok {
				return nil, errors.New("mutation operations must be an array")
			}
		case "actor":
			if _, ok := item.(map[string]any); !ok {
				return nil, errors.New("mutation actor must be an object")
			}
		default:
			return nil, errors.New("unknown mutation parameter")
		}
	}
	for _, required := range []string{"contractVersion", "requestId", "idempotencyKey", "tableId", "schemaRevision", "operations", "actor"} {
		if _, exists := object[required]; !exists {
			return nil, errors.New("missing mutation parameter")
		}
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxMutationRequestBytes {
		return nil, errors.New("mutation parameters exceed 1 MiB")
	}
	return []byte(compact.String()), nil
}

func mutationPreviewRegistration(kernel mutationKernel) productrpc.Registration {
	return productrpc.Registration{
		Method: "mutation.preview", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeMutationProductParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			body, err := decodeMutationProductParams(raw)
			if err != nil {
				return nil, err
			}
			input, err := decodeMutationRequest(bytes.NewReader(body))
			if err != nil {
				return nil, publicMutationProductError(err)
			}
			result, err := kernel.Preview(ctx, input)
			if err != nil {
				return nil, publicMutationProductError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return result, nil
		},
	}
}

func mutationApplyRegistration(kernel mutationKernel, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{
		Method: "mutation.apply", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeMutationProductParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			body, err := decodeMutationProductParams(raw)
			if err != nil {
				return nil, err
			}
			input, err := decodeMutationRequest(bytes.NewReader(body))
			if err != nil {
				return nil, publicMutationProductError(err)
			}
			var result mutation.Receipt
			err = runBusinessWrite(ctx, gates, "mutation.apply", input.IdempotencyKey, func(writeCtx context.Context) error {
				if err := writeCtx.Err(); err != nil {
					return err
				}
				var applyErr error
				result, applyErr = kernel.Apply(writeCtx, input)
				return applyErr
			})
			if err != nil {
				return nil, publicMutationProductError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return result, nil
		},
	}
}

// Match writeMutationError: formula and mutation errors are public; all other
// failures become a stable retryable rejection without leaking provider text.
func publicMutationProductError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var formulaErr *formula.Error
	if errors.As(err, &formulaErr) {
		return &productrpc.PublicError{Code: formulaErr.Code, Path: formulaErr.Path, Message: formulaErr.Message, Details: formulaErr.Details}
	}
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) {
		productErr = mutationRequestError("mutation operation failed")
		productErr.Code, productErr.Retryable = "mutation.internal.failed", true
	}
	return &productrpc.PublicError{Code: productErr.Code, Path: productErr.Path, Message: productErr.Message, Details: productErr.Details, Retryable: productErr.Retryable}
}
