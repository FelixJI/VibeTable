package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

type formulaRecalculateRequest struct {
	TableID        string `json:"tableId"`
	SchemaRevision string `json:"schemaRevision"`
}

func registerJobRoutes(
	r *router.Router[*core.RequestEvent],
	service *jobs.Service,
) {
	r.POST("/api/vibetable/v1/formulas/recalculate", func(request *core.RequestEvent) error {
		var input formulaRecalculateRequest
		if err := decodeRelationBody(request, &input); err != nil {
			return writeJobError(request, jobRequestError(
				"formula recalculation request is invalid",
			))
		}
		snapshot, err := service.StartFormulaBackfill(
			request.Request.Context(), input.TableID, input.SchemaRevision,
		)
		if err != nil {
			return writeJobError(request, err)
		}
		service.Start(snapshot.JobID)
		return request.JSON(http.StatusAccepted, snapshot)
	})
	r.GET("/api/vibetable/v1/jobs/{id}", func(request *core.RequestEvent) error {
		if request.Request.URL.RawQuery != "" ||
			request.Request.PathValue("id") == "" {
			return writeJobError(request, jobRequestError(
				"job id is required",
			))
		}
		snapshot, err := service.Get(
			request.Request.Context(), request.Request.PathValue("id"),
		)
		if err != nil {
			return writeJobError(request, err)
		}
		return request.JSON(http.StatusOK, snapshot)
	})
	r.POST("/api/vibetable/v1/jobs/{id}/cancel", func(request *core.RequestEvent) error {
		if request.Request.URL.RawQuery != "" ||
			request.Request.PathValue("id") == "" {
			return writeJobError(request, jobRequestError(
				"job id is required",
			))
		}
		snapshot, err := service.Cancel(
			request.Request.Context(), request.Request.PathValue("id"),
		)
		if err != nil {
			return writeJobError(request, err)
		}
		return request.JSON(http.StatusOK, snapshot)
	})
	r.POST("/api/vibetable/v1/jobs/{id}/resume", func(request *core.RequestEvent) error {
		if request.Request.URL.RawQuery != "" ||
			request.Request.PathValue("id") == "" {
			return writeJobError(request, jobRequestError(
				"job id is required",
			))
		}
		snapshot, err := service.Resume(
			request.Request.Context(), request.Request.PathValue("id"),
		)
		if err != nil {
			return writeJobError(request, err)
		}
		service.Start(snapshot.JobID)
		return request.JSON(http.StatusAccepted, snapshot)
	})
}

func jobRequestError(message string) *jobs.JobError {
	return &jobs.JobError{
		Code:    "job.request.invalid",
		Message: message,
	}
}

func writeJobError(request *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	var jobErr *jobs.JobError
	if !errors.As(err, &jobErr) {
		var productErr *mutation.ProductError
		if errors.As(err, &productErr) {
			jobErr = &jobs.JobError{
				Code:      productErr.Code,
				Message:   productErr.Message,
				Retryable: productErr.Retryable,
			}
		} else {
			jobErr = &jobs.JobError{
				Code:      "job.internal_failed",
				Message:   "job operation failed",
				Retryable: true,
			}
		}
	}
	status := http.StatusUnprocessableEntity
	switch jobErr.Code {
	case "job.request.invalid":
		status = http.StatusBadRequest
	case "job.not_found":
		status = http.StatusNotFound
	case "job.schema_revision_conflict":
		status = http.StatusConflict
	case "job.storage_failed", "job.internal_failed":
		status = http.StatusInternalServerError
	case "job.already_running":
		status = http.StatusConflict
	case "job.cancelled", "job.not_cancelled":
		status = http.StatusConflict
	}
	if writeErr := request.JSON(status, jobErr); writeErr != nil {
		return fmt.Errorf("write job error response: %w", writeErr)
	}
	return nil
}
