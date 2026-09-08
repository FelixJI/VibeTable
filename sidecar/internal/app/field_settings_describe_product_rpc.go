package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type fieldSettingsDescribeResult struct {
	Contract                   string              `json:"contract"`
	TableID                    string              `json:"tableId"`
	FieldID                    string              `json:"fieldId"`
	SchemaRevision             string              `json:"schemaRevision"`
	DataRevision               int64               `json:"dataRevision"`
	Definition                 *v2.FieldDefinition `json:"definition"`
	Capabilities               []v2.Capability     `json:"capabilities"`
	RecommendedDefaultsVersion int64               `json:"recommendedDefaultsVersion"`
}
type fieldSettingsDescribePort interface {
	DescribeFieldSettings(context.Context, string, string) (fieldSettingsDescribeResult, error)
}
type fieldSettingsDescribeDomain struct {
	schema interface {
		Describe(context.Context, string) (v2.SchemaSnapshot, error)
	}
	fields interface {
		Field(context.Context, string, string) (*v2.FieldDefinition, error)
	}
}

func (port fieldSettingsDescribeDomain) DescribeFieldSettings(ctx context.Context, tableID, fieldID string) (fieldSettingsDescribeResult, error) {
	snapshot, err := port.schema.Describe(ctx, tableID)
	if err != nil {
		return fieldSettingsDescribeResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return fieldSettingsDescribeResult{}, err
	}
	var definition *v2.FieldDefinition
	if fieldID != "" {
		definition, err = port.fields.Field(ctx, tableID, fieldID)
		if err != nil {
			return fieldSettingsDescribeResult{}, err
		}
	}
	return fieldSettingsDescribeResult{v2.Contract, tableID, fieldID, snapshot.SchemaRevision, snapshot.DataRevision, definition, snapshot.Capabilities, 1}, nil
}
func fieldSettingsDescribeRegistration(port fieldSettingsDescribePort) productrpc.Registration {
	return productrpc.Registration{Method: "field.settings.describe", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, _, err := decodeFieldSettingsDescribeParams(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			table, field, err := decodeFieldSettingsDescribeParams(raw)
			if err != nil {
				return nil, err
			}
			if !productSchemaTableID.MatchString(table) {
				return nil, errors.New("invalid field settings table path")
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			result, err := port.DescribeFieldSettings(ctx, table, field)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				failure := classifyFieldError(err)
				return nil, &productrpc.PublicError{Code: failure.code, Path: &failure.path, Message: failure.message, Details: failure.details, Retryable: failure.retryable}
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			return result, nil
		}}
}
func decodeFieldSettingsDescribeParams(raw json.RawMessage) (string, string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return "", "", errors.New("invalid field settings JSON")
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
			return "", "", errors.New("field settings requires Unicode scalars")
		}
	}
	if err := validateQueryViewValue(value, 0); err != nil {
		return "", "", err
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return "", "", err
	}
	if compact.Len() > maxFieldRequestBytes {
		return "", "", errors.New("field settings exceeds 1 MiB")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", "", errors.New("field settings requires an object")
	}
	table, ok := object["tableId"].(string)
	if !ok || table == "" {
		return "", "", errors.New("tableId must be non-empty text")
	}
	field := ""
	for key, item := range object {
		if key != "tableId" && key != "fieldId" {
			return "", "", errors.New("unknown field settings parameter")
		}
		text, ok := item.(string)
		if !ok || text == "" {
			return "", "", errors.New("field settings text must not be empty")
		}
		if key == "fieldId" {
			field = text
		}
	}
	return table, field, nil
}
