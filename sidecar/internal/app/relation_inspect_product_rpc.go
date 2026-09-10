package app

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relationpair"
)

func relationInspectPairRegistration(app core.App) productrpc.Registration {
	return relationInspectRegistration(func(ctx context.Context, input relationpair.Request) (relationpair.Report, error) {
		return relationpair.Inspect(ctx, app, input)
	})
}

func relationInspectRegistration(inspect func(context.Context, relationpair.Request) (relationpair.Report, error)) productrpc.Registration {
	return productrpc.Registration{
		Method: "relation.inspectPair", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeRelationInspectParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			input, err := decodeRelationInspectParams(raw)
			if err != nil {
				return nil, err
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			report, err := inspect(ctx, input)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				code, message := "relation.inspect.storage_failed", "无法读取关系数据，请重试检查。"
				switch {
				case errors.Is(err, relationpair.ErrRevisionChanged):
					code, message = "relation.inspect.revision_changed", "检查期间数据或字段已变化，请重新检查。"
				case errors.Is(err, relationpair.ErrInvalidRequest):
					code, message = "relation.inspect.invalid_request", "关系检查参数或续页位置无效。"
				}
				return nil, &productrpc.PublicError{Code: code, Message: message, Details: map[string]any{}}
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			return report, nil
		},
	}
}

func decodeRelationInspectParams(raw json.RawMessage) (relationpair.Request, error) {
	input := relationpair.Request{Limit: 100}
	invalid := errors.New("invalid relation inspection parameters")
	if len(raw) > 1<<20 || !json.Valid(raw) || !utf8.Valid(raw) {
		return input, invalid
	}
	// Go JSON decoding otherwise replaces unpaired surrogate escapes silently.
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
			return input, invalid
		}
	}
	object, err := relationInspectObject(raw, "tableId", "fieldId")
	if err != nil {
		return input, err
	}
	if limit, exists := object["limit"]; exists && string(limit) == "null" {
		return input, invalid
	}
	if err = mutation.DecodeStrict(raw, &input); err != nil {
		return input, invalid
	}
	if input.TableID == "" || utf8.RuneCountInString(input.TableID) > 128 || input.FieldID == "" || utf8.RuneCountInString(input.FieldID) > 128 || input.Limit < 1 || input.Limit > 200 {
		return input, invalid
	}
	if input.Cursor == nil {
		return input, nil
	}
	cursor, err := relationInspectObject(object["cursor"], "pairId", "endpoints", "after", "done", "incomplete")
	if err != nil {
		return input, err
	}
	if input.Cursor.PairID == "" || utf8.RuneCountInString(input.Cursor.PairID) > 128 {
		return input, invalid
	}
	for _, key := range []string{"endpoints", "after", "done"} {
		var values []json.RawMessage
		if json.Unmarshal(cursor[key], &values) != nil || len(values) != 2 {
			return input, invalid
		}
		for _, value := range values {
			if string(value) == "null" {
				return input, invalid
			}
			if key == "endpoints" {
				if _, err = relationInspectObject(value, "tableId", "fieldId", "schemaRevision", "dataRevision"); err != nil {
					return input, err
				}
			}
		}
	}
	for index, endpoint := range input.Cursor.Endpoints {
		if utf8.RuneCountInString(endpoint.TableID) > 128 || utf8.RuneCountInString(endpoint.FieldID) > 128 || utf8.RuneCountInString(endpoint.SchemaRevision) > 256 || endpoint.DataRevision < 0 || utf8.RuneCountInString(input.Cursor.After[index]) > 200 {
			return input, invalid
		}
	}
	return input, nil
}

func relationInspectObject(raw json.RawMessage, required ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, errors.New("relation inspection requires an object")
	}
	for _, key := range required {
		value, ok := object[key]
		if !ok || string(value) == "null" {
			return nil, errors.New("relation inspection omits a required field")
		}
	}
	return object, nil
}
