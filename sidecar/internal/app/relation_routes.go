package app

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

const maxRelationRequestBytes = 1 << 20

func registerRelationRoutes(
	r *router.Router[*core.RequestEvent],
	service *relation.Service,
	gates ...businessWriteGate,
) {
	r.GET("/api/vibetable/v1/relations/describe", func(request *core.RequestEvent) error {
		query := request.Request.URL.Query()
		if len(query) != 1 || len(query["tableId"]) != 1 ||
			query.Get("tableId") == "" {
			return writeMutationError(
				request,
				relationRequestError("tableId is required"),
			)
		}
		result, err := service.Describe(
			request.Request.Context(), query.Get("tableId"),
		)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
	r.GET("/api/vibetable/v1/lookups/describe", func(request *core.RequestEvent) error {
		query := request.Request.URL.Query()
		if len(query) != 1 || len(query["tableId"]) != 1 ||
			query.Get("tableId") == "" {
			return writeMutationError(
				request,
				relationRequestError("tableId is required"),
			)
		}
		result, err := service.Describe(
			request.Request.Context(), query.Get("tableId"),
		)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{
			"tableId":        result.TableID,
			"schemaRevision": result.SchemaRevision,
			"lookups":        result.Lookups,
		})
	})
	r.POST("/api/vibetable/v1/relations/search-targets", func(request *core.RequestEvent) error {
		var input relation.SearchRequest
		if err := decodeRelationBody(request, &input); err != nil {
			return writeMutationError(request, err)
		}
		result, err := service.SearchTargets(request.Request.Context(), input)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
	r.POST("/api/vibetable/v1/relations/create-target", func(request *core.RequestEvent) error {
		var input relation.CreateTargetRequest
		if err := decodeRelationBody(request, &input); err != nil {
			return writeMutationError(request, err)
		}
		var result relation.CreateTargetResult
		err := runBusinessWrite(
			request.Request.Context(),
			gates,
			"relation.create-target",
			input.IdempotencyKey,
			func(ctx context.Context) error {
				var createErr error
				result, createErr = service.CreateTarget(ctx, input)
				return createErr
			},
		)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
	r.POST("/api/vibetable/v1/relations/preview-delta", func(request *core.RequestEvent) error {
		var input relation.DeltaRequest
		if err := decodeRelationBody(request, &input); err != nil {
			return writeMutationError(request, err)
		}
		result, err := service.PreviewDelta(request.Request.Context(), input)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
	r.POST("/api/vibetable/v1/relations/apply-delta", func(request *core.RequestEvent) error {
		var input relation.DeltaRequest
		if err := decodeRelationBody(request, &input); err != nil {
			return writeMutationError(request, err)
		}
		var result relation.DeltaResult
		err := runBusinessWrite(
			request.Request.Context(),
			gates,
			"relation.apply-delta",
			input.IdempotencyKey,
			func(ctx context.Context) error {
				var applyErr error
				result, applyErr = service.ApplyDelta(ctx, input)
				return applyErr
			},
		)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
	r.POST("/api/vibetable/v1/lookups/query", func(request *core.RequestEvent) error {
		var input relation.LookupQueryRequest
		if err := decodeRelationBody(request, &input); err != nil {
			return writeMutationError(request, err)
		}
		result, err := service.QueryLookups(request.Request.Context(), input)
		if err != nil {
			return writeRelationQueryError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
	r.POST("/api/vibetable/v1/lookups/preview", func(request *core.RequestEvent) error {
		var input relation.LookupPreviewRequest
		if err := decodeRelationBody(request, &input); err != nil {
			return writeMutationError(request, err)
		}
		result, err := service.PreviewLookups(request.Request.Context(), input)
		if err != nil {
			return writeRelationQueryError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
	r.POST("/api/vibetable/v1/lookups/value-page", func(request *core.RequestEvent) error {
		var input relation.LookupValuePageRequest
		if err := decodeRelationBody(request, &input); err != nil {
			return writeMutationError(request, err)
		}
		result, err := service.LookupValuePage(request.Request.Context(), input)
		if err != nil {
			return writeRelationQueryError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
}

func writeRelationQueryError(request *core.RequestEvent, err error) error {
	var productErr *mutation.ProductError
	if errors.As(err, &productErr) {
		return writeMutationError(request, err)
	}
	return writeQueryError(request, err)
}

func decodeRelationBody(
	request *core.RequestEvent,
	target any,
) error {
	raw, err := io.ReadAll(io.LimitReader(
		request.Request.Body,
		maxRelationRequestBytes+1,
	))
	if err != nil || len(raw) > maxRelationRequestBytes ||
		mutation.DecodeStrict(raw, target) != nil {
		return relationRequestError("relation request body is invalid")
	}
	return nil
}

func relationRequestError(message string) *mutation.ProductError {
	return &mutation.ProductError{
		ContractVersion: mutation.ContractVersion,
		Code:            "relation.request.invalid",
		Message:         message,
		Details:         map[string]any{},
	}
}
