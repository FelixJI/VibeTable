package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/autodateobs"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

const maxSchemaRequestBytes = 1 << 20

func registerSchemaRoutes(
	r *router.Router[*core.RequestEvent],
	catalog schemaapi.SchemaCatalog,
	jobService *jobs.Service,
	gates ...businessWriteGate,
) {
	r.GET("/api/vibetable/v2/schema/tables", func(request *core.RequestEvent) error {
		definitions, err := catalog.List(request.Request.Context())
		if err != nil {
			return writeSchemaError(request, err)
		}
		tables := make([]map[string]any, 0, len(definitions))
		for _, definition := range definitions {
			tables = append(tables, map[string]any{
				"tableId":     definition.Snapshot.TableID,
				"displayName": definition.Snapshot.DisplayName,
				"kind":        definition.Snapshot.Kind,
			})
		}
		return request.JSON(http.StatusOK, map[string]any{"tables": tables})
	})

	r.GET("/api/vibetable/v1/schema/autodate-diagnostics", func(request *core.RequestEvent) error {
		diagnostics, err := catalog.InspectAutoDates(request.Request.Context())
		if err != nil {
			return writeSchemaError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{
			"diagnostics": diagnostics,
			"metrics":     autodateobs.Snapshot(),
			"scanCounts":  autoDateScanCounts(diagnostics),
		})
	})

	r.POST("/api/vibetable/v1/schema/delete", func(request *core.RequestEvent) error {
		var body struct {
			TableID          string `json:"tableId"`
			ExpectedRevision string `json:"expectedRevision"`
		}
		if err := decodeSchemaRequest(request.Request.Body, &body); err != nil {
			return writeSchemaError(request, err)
		}
		expectedRevision, err := v2.ParseSchemaRevision(body.ExpectedRevision)
		if err != nil {
			return writeSchemaError(request, &schemaerror.ProductError{
				Code: "schema.revision.invalid", Path: "expectedRevision",
				Message: err.Error(),
			})
		}
		var result schemaapi.DeleteResult
		err = runBusinessWrite(
			request.Request.Context(),
			gates,
			"schema.delete",
			fmt.Sprintf("%s:%d", body.TableID, expectedRevision),
			func(ctx context.Context) error {
				var deleteErr error
				result, deleteErr = catalog.DeleteTable(
					ctx,
					body.TableID,
					expectedRevision,
				)
				return deleteErr
			},
		)
		if err != nil {
			return writeSchemaError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
}

func autoDateScanCounts(
	diagnostics []schemaapi.AutoDateDiagnostic,
) map[string]int {
	counts := map[string]int{}
	for _, diagnostic := range diagnostics {
		key := fmt.Sprintf(
			"onCreate_%t_onUpdate_%t",
			diagnostic.OnCreate,
			diagnostic.OnUpdate,
		)
		counts[key]++
	}
	return counts
}

func decodeSchemaRequest(reader io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, maxSchemaRequestBytes+1))
	if err != nil {
		return invalidSchemaRequest(err)
	}
	if len(raw) > maxSchemaRequestBytes {
		return invalidSchemaRequest(errors.New("request exceeds the 1 MiB limit"))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalidSchemaRequest(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalidSchemaRequest(errors.New("multiple JSON values are not allowed"))
		}
		return invalidSchemaRequest(err)
	}
	return nil
}

func invalidSchemaRequest(cause error) *schemaerror.ProductError {
	return &schemaerror.ProductError{
		Code:    "schema.request.invalid",
		Path:    "",
		Message: "schema request body is invalid",
		Details: map[string]any{"cause": cause.Error()},
	}
}

func writeSchemaError(request *core.RequestEvent, err error) error {
	var formulaError *formula.Error
	if errors.As(err, &formulaError) {
		if writeErr := request.JSON(http.StatusUnprocessableEntity, formulaError); writeErr != nil {
			return fmt.Errorf("write formula error response: %w", writeErr)
		}
		return nil
	}
	var productError *schemaerror.ProductError
	if !errors.As(err, &productError) {
		productError = &schemaerror.ProductError{
			Code:    "schema.internal.failed",
			Path:    "",
			Message: "schema operation failed",
		}
	}
	status := http.StatusUnprocessableEntity
	switch {
	case productError.Code == "schema.request.invalid":
		status = http.StatusBadRequest
	case strings.HasSuffix(productError.Code, ".not_found"):
		status = http.StatusNotFound
	case strings.HasSuffix(productError.Code, ".revision_conflict") ||
		strings.HasSuffix(productError.Code, ".idempotency_conflict"):
		status = http.StatusConflict
	case productError.Code == "schema.storage.failed" ||
		productError.Code == "schema.internal.failed":
		status = http.StatusInternalServerError
	}
	if err := request.JSON(status, productError); err != nil {
		return fmt.Errorf("write schema error response: %w", err)
	}
	return nil
}
