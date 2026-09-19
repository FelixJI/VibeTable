package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"strings"
)

var schemaFieldChangeMethods = []string{"schema.table.create", "schema.delete", "field.change.plan", "field.change.apply", "field.change.status", "field.change.cancel", "field.recycleBin.list"}

func schemaFieldChangeRegistrations(domain schemaFieldChangeDomain, catalog schemaapi.SchemaCatalog) []productrpc.Registration {
	registrations := make([]productrpc.Registration, 0, len(schemaFieldChangeMethods))
	for _, method := range schemaFieldChangeMethods {
		registrations = append(registrations, productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope,
			ValidateParams: func(raw json.RawMessage) error { _, _, err := decodeSchemaFieldChangeParams(method, raw); return err },
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				body, object, err := decodeSchemaFieldChangeParams(method, raw)
				if err != nil {
					return nil, err
				}
				var result any
				switch method {
				case "schema.table.create":
					var intent v2.TableCreateIntent
					if err = decodeFieldRequest(bytes.NewReader(body), &intent); err == nil {
						result, err = domain.CreateTable(ctx, intent)
					}
				case "schema.delete":
					result, err = deleteSchemaTable(ctx, catalog, object["tableId"].(string), object["expectedRevision"].(string), domain.gates)
					if err != nil {
						return nil, publicSchemaDeleteError(err)
					}
				case "field.change.plan":
					var intent v2.FieldChangeIntent
					if err = decodeFieldRequest(bytes.NewReader(body), &intent); err == nil {
						result, err = domain.Plan(ctx, intent)
					}
				case "field.change.apply":
					var request v2.ApplyRequest
					if err = decodeFieldRequest(bytes.NewReader(body), &request); err == nil {
						result, err = domain.Apply(ctx, request)
					}
				case "field.change.status", "field.change.cancel", "field.recycleBin.list":
					key := "jobId"
					if method == "field.recycleBin.list" {
						key = "tableId"
					}
					id := object[key].(string)
					// Python rejects path identifiers after DTO validation, as handler errors.
					if !productSchemaTableID.MatchString(id) {
						return nil, errors.New("invalid schema product path")
					}
					switch method {
					case "field.change.status":
						result, err = domain.Status(ctx, id)
					case "field.change.cancel":
						result, err = domain.Cancel(ctx, id)
					default:
						result, err = domain.RecycledFields(ctx, id)
					}
				}
				if err != nil {
					return nil, publicFieldChangeError(err)
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return result, nil
			},
		})
	}
	return registrations
}

func publicFieldChangeError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	failure := classifyFieldError(err)
	return &productrpc.PublicError{Code: failure.code, Path: &failure.path, Message: failure.message, Details: failure.details, Retryable: failure.retryable}
}

// Preserve writeSchemaError and the Python PocketBaseProductError projection.
func publicSchemaDeleteError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var formulaError *formula.Error
	if errors.As(err, &formulaError) {
		return &productrpc.PublicError{Code: formulaError.Code, Path: formulaError.Path, Message: formulaError.Message, Details: formulaError.Details}
	}
	var schemaError *schemaerror.ProductError
	if !errors.As(err, &schemaError) {
		schemaError = &schemaerror.ProductError{Code: "schema.internal.failed", Path: "", Message: "schema operation failed"}
	}
	return &productrpc.PublicError{Code: schemaError.Code, Path: &schemaError.Path, Message: schemaError.Message, Details: schemaError.Details, Retryable: schemaError.Retryable}
}

// Keep Python transport validation separate from the existing domain decoder.
func decodeSchemaFieldChangeParams(method string, raw json.RawMessage) ([]byte, map[string]any, error) {
	invalid := errors.New("invalid schema/field-change parameters")
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, nil, invalid
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
			return nil, nil, invalid
		}
	}
	if err := validateQueryPageValue(value, 0); err != nil {
		return nil, nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil, invalid
	}
	var required, optional []string
	switch method {
	case "schema.table.create":
		required = []string{"displayName", "operationId", "actor"}
	case "schema.delete":
		required = []string{"tableId", "expectedRevision"}
	case "field.change.plan":
		required = []string{"action", "tableId", "fieldId", "expectedSchemaRevision", "expectedDataRevision", "draft", "actor", "conversionRule", "confirmation", "backupReceipt"}
		optional = []string{"relationPair", "relationPairPatch"}
	case "field.change.apply":
		required = []string{"planId", "planHash", "operationId", "actor", "confirmations"}
		optional = []string{"protectionSnapshotId"}
	case "field.change.status", "field.change.cancel":
		required = []string{"jobId"}
	case "field.recycleBin.list":
		required = []string{"tableId"}
	default:
		return nil, nil, invalid
	}
	// ApplyRequest's Pydantic model also accepts snake_case aliases. The old
	// adapter forwarded them unchanged, so REST's domain decoder still rejects
	// those spellings; do not silently normalize them into successful writes.
	aliases := map[string]string{"planId": "plan_id", "planHash": "plan_hash", "operationId": "operation_id", "protectionSnapshotId": "protection_snapshot_id"}
	if method == "field.change.apply" {
		for canonical, alias := range aliases {
			_, hasCanonical := object[canonical]
			if _, hasAlias := object[alias]; hasAlias {
				if hasCanonical {
					return nil, nil, invalid
				}
				for index, key := range required {
					if key == canonical {
						required[index] = alias
					}
				}
				for index, key := range optional {
					if key == canonical {
						optional[index] = alias
					}
				}
			}
		}
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
		if _, ok := object[key]; !ok {
			return nil, nil, invalid
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key, item := range object {
		if !allowed[key] {
			return nil, nil, invalid
		}
		if method == "field.change.plan" {
			switch key {
			case "draft", "relationPair", "relationPairPatch":
				if item != nil {
					if _, ok := item.(map[string]any); !ok {
						return nil, nil, invalid
					}
				}
			case "actor":
				if _, ok := item.(map[string]any); !ok {
					return nil, nil, invalid
				}
			}
			continue
		}
		if key == "actor" {
			actor, ok := item.(map[string]any)
			if !ok {
				return nil, nil, invalid
			}
			if method == "field.change.apply" {
				if len(actor) != 2 {
					return nil, nil, invalid
				}
				for _, name := range []string{"id", "kind"} {
					text, ok := actor[name].(string)
					if !ok || text == "" {
						return nil, nil, invalid
					}
				}
			}
		} else if key == "confirmations" {
			confirmations, ok := item.([]any)
			if !ok {
				return nil, nil, invalid
			}
			for _, confirmation := range confirmations {
				if _, ok := confirmation.(string); !ok {
					return nil, nil, invalid
				}
			}
		} else if key == "protectionSnapshotId" || key == "protection_snapshot_id" {
			if item != nil {
				if _, ok := item.(string); !ok {
					return nil, nil, invalid
				}
			}
		} else {
			text, ok := item.(string)
			if !ok || text == "" {
				return nil, nil, invalid
			}
		}
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, nil, err
	}
	if compact.Len() > maxFieldRequestBytes {
		return nil, nil, invalid
	}
	return []byte(compact.String()), object, nil
}
