package app

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

var productDescribeInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

type schemaDescribeParams struct {
	Collection string
	Generation json.Number
	Accepts    []json.RawMessage
}

func decodeSchemaDescribeParams(raw json.RawMessage) (schemaDescribeParams, error) {
	var params schemaDescribeParams
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 3 {
		return params, errors.New("schema.describe requires collection, requestGeneration and accepts")
	}
	if err := json.Unmarshal(object["collection"], &params.Collection); err != nil || params.Collection == "" {
		return params, errors.New("schema.describe collection must be a non-empty string")
	}
	generation := object["requestGeneration"]
	if !productDescribeInteger.Match(generation) {
		return params, errors.New("schema.describe requestGeneration must be an integer")
	}
	params.Generation = json.Number(generation)
	if params.Generation == "-0" {
		params.Generation = "0"
	}
	if err := json.Unmarshal(object["accepts"], &params.Accepts); err != nil || params.Accepts == nil {
		return params, errors.New("schema.describe accepts must be an array")
	}
	return params, nil
}

func schemaDescribeRegistration(app core.App, relations *relation.Service) productrpc.Registration {
	return productrpc.Registration{
		Method: "schema.describe", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			_, err := decodeSchemaDescribeParams(raw)
			return err
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodeSchemaDescribeParams(raw)
			if err != nil {
				return nil, err
			}
			if !productSchemaTableID.MatchString(params.Collection) {
				return nil, errors.New("table id is invalid")
			}
			if len(params.Accepts) != 2 {
				return nil, errors.New("schema.describe accepts is invalid")
			}
			for index, expected := range []string{"vibetable.relation-capabilities.v1", "vibetable.lookup-query.v1"} {
				var accepted string
				if json.Unmarshal(params.Accepts[index], &accepted) != nil || accepted != expected {
					return nil, errors.New("schema.describe accepts is invalid")
				}
			}
			load := func(tableID string) (v2.SchemaSnapshot, error) {
				if err := ctx.Err(); err != nil {
					return v2.SchemaSnapshot{}, err
				}
				definition, err := schemaexecution.Describe(ctx, app, tableID)
				if err != nil {
					return v2.SchemaSnapshot{}, publicSchemaGetTableError(err)
				}
				return definition.Snapshot, nil
			}
			snapshot, err := load(params.Collection)
			if err != nil {
				return nil, err
			}
			catalog, err := relations.Describe(ctx, params.Collection)
			if err != nil {
				return nil, publicSchemaDescribeCatalogError(err)
			}
			result, err := projectSchemaDescribe(snapshot, catalog, params.Generation, load)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return result, nil
		},
	}
}

// Preserve the former relation REST adapter's explicit public error shape.
func publicSchemaDescribeCatalogError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var formulaErr *formula.Error
	if errors.As(err, &formulaErr) {
		return &productrpc.PublicError{Code: formulaErr.Code, Path: formulaErr.Path,
			Message: formulaErr.Message, Details: formulaErr.Details}
	}
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) {
		return &productrpc.PublicError{Code: "mutation.internal.failed", Message: "mutation operation failed", Details: map[string]any{}, Retryable: true}
	}
	return &productrpc.PublicError{Code: productErr.Code, Path: productErr.Path,
		Message: productErr.Message, Details: productErr.Details, Retryable: productErr.Retryable}
}
