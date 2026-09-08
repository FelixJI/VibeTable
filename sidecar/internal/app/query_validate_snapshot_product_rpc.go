package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
)

func decodeQueryValidateSnapshotParams(raw json.RawMessage) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, errors.New("invalid query.validateSnapshot JSON")
	}
	// encoding/json replaces malformed UTF-8 and lone surrogate escapes unless checked first.
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
			return nil, errors.New("query.validateSnapshot requires Unicode scalar values")
		}
	}
	if err := validateQueryPageValue(value, 0); err != nil {
		return nil, err
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxQueryRequestBytes {
		return nil, errors.New("query.validateSnapshot parameters exceed 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("query.validateSnapshot parameters must be an object")
	}
	for key := range object {
		if key != "snapshot" && key != "currentQuery" {
			return nil, errors.New("unknown query.validateSnapshot parameter")
		}
	}
	if _, ok := object["snapshot"].(map[string]any); !ok {
		return nil, errors.New("snapshot must be an object")
	}
	if current, exists := object["currentQuery"]; exists {
		if _, ok := current.(map[string]any); !ok {
			return nil, errors.New("currentQuery must be an object")
		}
	}
	return object, nil
}

// The caller supplies the existing application query port and its authority configuration.
func queryValidateSnapshotRegistration(port interface {
	ValidateSnapshot(context.Context, query.QuerySnapshot, *query.TableQuery) (query.SnapshotValidation, error)
}) productrpc.Registration {
	return productrpc.Registration{
		Method: "query.validateSnapshot", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeQueryValidateSnapshotParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			object, err := decodeQueryValidateSnapshotParams(raw)
			if err != nil {
				return nil, err
			}
			var body strings.Builder
			if err := appendDescribeRevision(&body, object); err != nil {
				return nil, err
			}
			// Retain the former REST typed decoding boundary after the permissive object DTO.
			var input snapshotValidationRequest
			if err := decodeQueryRequest(strings.NewReader(body.String()), &input); err != nil {
				return nil, publicQueryPageError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result, err := port.ValidateSnapshot(ctx, input.Snapshot, input.CurrentQuery)
			if err != nil {
				return nil, publicQueryPageError(err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return result, nil
		},
	}
}
