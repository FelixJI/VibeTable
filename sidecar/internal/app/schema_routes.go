package app

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

const maxSchemaRequestBytes = 1 << 20

func registerSchemaRoutes(
	r *router.Router[*core.RequestEvent],
	catalog schemaapi.SchemaCatalog,
	jobService *jobs.Service,
	gates ...businessWriteGate,
) {
	r.GET("/api/vibetable/v1/schema/tables", func(request *core.RequestEvent) error {
		definitions, err := catalog.List(request.Request.Context())
		if err != nil {
			return writeSchemaError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{"tables": definitions})
	})

	r.GET("/api/vibetable/v1/schema/tables/{id}", func(request *core.RequestEvent) error {
		definition, err := catalog.Describe(
			request.Request.Context(),
			request.Request.PathValue("id"),
		)
		if err != nil {
			return writeSchemaError(request, err)
		}
		return request.JSON(http.StatusOK, definition)
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

	r.POST("/api/vibetable/v1/schema/validate", func(request *core.RequestEvent) error {
		var change schemaapi.Change
		if err := decodeSchemaRequest(request.Request.Body, &change); err != nil {
			return writeSchemaError(request, err)
		}
		result, err := catalog.ValidateChange(request.Request.Context(), change)
		if err != nil {
			return writeSchemaError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})

	r.POST("/api/vibetable/v1/schema/apply", func(request *core.RequestEvent) error {
		var change schemaapi.Change
		if err := decodeSchemaRequest(request.Request.Body, &change); err != nil {
			return writeSchemaError(request, err)
		}
		var definition schema.TableDefinition
		var backfill jobs.Snapshot
		err := runBusinessWrite(
			request.Request.Context(),
			gates,
			"schema.apply",
			schemaChangeIdentity(change),
			func(ctx context.Context) error {
				var applyErr error
				definition, applyErr = catalog.ApplyChange(ctx, change)
				if applyErr != nil || !definitionNeedsBackfill(definition) {
					return applyErr
				}
				backfill, applyErr = jobService.StartFormulaBackfill(
					ctx,
					definition.TableID,
					definition.SchemaRevision,
				)
				return applyErr
			},
		)
		if err != nil {
			if _, ok := err.(*jobs.JobError); ok {
				return writeJobError(request, err)
			}
			return writeSchemaError(request, err)
		}
		if backfill.State == "queued" {
			jobService.Start(backfill.JobID)
		}
		return request.JSON(http.StatusOK, definition)
	})

	r.POST("/api/vibetable/v1/schema/delete", func(request *core.RequestEvent) error {
		var body struct {
			TableID          string `json:"tableId"`
			ExpectedRevision string `json:"expectedRevision"`
		}
		if err := decodeSchemaRequest(request.Request.Body, &body); err != nil {
			return writeSchemaError(request, err)
		}
		expectedRevision, err := schema.ParseSchemaRevision(body.ExpectedRevision)
		if err != nil {
			return writeSchemaError(request, &schema.ProductError{
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

func schemaChangeIdentity(change schemaapi.Change) string {
	if change.OperationID != "" {
		return change.OperationID
	}
	raw, _ := json.Marshal(struct {
		Definition       schema.TableDefinition `json:"definition"`
		ExpectedRevision int64                  `json:"expectedRevision"`
	}{
		Definition:       change.Definition,
		ExpectedRevision: change.ExpectedRevision,
	})
	return fmt.Sprintf("derived:%x", sha256.Sum256(raw))
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

func definitionNeedsBackfill(definition schema.TableDefinition) bool {
	for _, field := range definition.Fields {
		if field.Kind == schema.FieldKindFormula &&
			field.Formula != nil &&
			field.Formula.Status == "backfilling" {
			return true
		}
	}
	return false
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

func invalidSchemaRequest(cause error) *schema.ProductError {
	return &schema.ProductError{
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
	var productError *schema.ProductError
	if !errors.As(err, &productError) {
		productError = &schema.ProductError{
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
