package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
)

const maxMetadataRequestBytes = 1 << 20

type metadataUpsertBody struct {
	LogicalID        string          `json:"logicalId"`
	Payload          json.RawMessage `json:"payload"`
	ExpectedRevision string          `json:"expectedRevision"`
	IdempotencyKey   string          `json:"idempotencyKey"`
}

type metadataDeleteBody struct {
	LogicalID        string `json:"logicalId"`
	ExpectedRevision string `json:"expectedRevision"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

func registerMetadataRoutes(
	r *router.Router[*core.RequestEvent],
	service *metadata.Service,
	gates ...businessWriteGate,
) {
	r.GET("/api/vibetable/v1/metadata/{namespace}", func(
		request *core.RequestEvent,
	) error {
		namespace := metadata.Namespace(
			request.Request.PathValue("namespace"),
		)
		items, err := service.List(
			request.Request.Context(), namespace,
		)
		if err != nil {
			return writeMetadataError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{
			"namespace": namespace,
			"items":     items,
		})
	})
	r.POST("/api/vibetable/v1/metadata/{namespace}/upsert", func(
		request *core.RequestEvent,
	) error {
		var body metadataUpsertBody
		if err := decodeMetadataBody(
			request.Request.Body, &body,
		); err != nil {
			return writeMetadataError(request, err)
		}
		receipt, err := metadataMutationWithGate(
			request.Request.Context(),
			metadata.UpsertRequest{
				Namespace: metadata.Namespace(
					request.Request.PathValue("namespace"),
				),
				LogicalID:        body.LogicalID,
				Payload:          body.Payload,
				ExpectedRevision: body.ExpectedRevision,
				IdempotencyKey:   body.IdempotencyKey,
			},
			gates,
			service.Upsert,
		)
		if err != nil {
			return writeMetadataError(request, err)
		}
		return request.JSON(http.StatusOK, receipt)
	})
	r.POST("/api/vibetable/v1/metadata/{namespace}/delete", func(
		request *core.RequestEvent,
	) error {
		var body metadataDeleteBody
		if err := decodeMetadataBody(
			request.Request.Body, &body,
		); err != nil {
			return writeMetadataError(request, err)
		}
		receipt, err := deleteMetadataWithGate(
			request.Request.Context(),
			metadata.DeleteRequest{
				Namespace: metadata.Namespace(
					request.Request.PathValue("namespace"),
				),
				LogicalID:        body.LogicalID,
				ExpectedRevision: body.ExpectedRevision,
				IdempotencyKey:   body.IdempotencyKey,
			},
			gates,
			service.Delete,
		)
		if err != nil {
			return writeMetadataError(request, err)
		}
		return request.JSON(http.StatusOK, receipt)
	})
	r.POST("/api/vibetable/v1/metadata/dashboards/commit", func(
		request *core.RequestEvent,
	) error {
		var body metadata.DashboardCommitRequest
		if err := decodeMetadataBody(
			request.Request.Body, &body,
		); err != nil {
			return writeMetadataError(request, err)
		}
		receipt, err := commitDashboardWithGate(
			request.Request.Context(),
			body,
			gates,
			service.CommitDashboard,
		)
		if err != nil {
			return writeMetadataError(request, err)
		}
		return request.JSON(http.StatusOK, receipt)
	})
}

func metadataMutationWithGate(
	ctx context.Context,
	request metadata.UpsertRequest,
	gates []businessWriteGate,
	apply func(context.Context, metadata.UpsertRequest) (metadata.MutationReceipt, error),
) (metadata.MutationReceipt, error) {
	var receipt metadata.MutationReceipt
	err := runIdempotentBusinessWrite(
		ctx,
		gates,
		"metadata."+string(request.Namespace)+".upsert",
		request.IdempotencyKey,
		func(writeContext context.Context) error {
			var applyErr error
			receipt, applyErr = apply(writeContext, request)
			return applyErr
		},
	)
	return receipt, err
}

func deleteMetadataWithGate(
	ctx context.Context,
	request metadata.DeleteRequest,
	gates []businessWriteGate,
	apply func(context.Context, metadata.DeleteRequest) (metadata.DeleteReceipt, error),
) (metadata.DeleteReceipt, error) {
	var receipt metadata.DeleteReceipt
	err := runIdempotentBusinessWrite(
		ctx,
		gates,
		"metadata."+string(request.Namespace)+".delete",
		request.IdempotencyKey,
		func(writeContext context.Context) error {
			var applyErr error
			receipt, applyErr = apply(writeContext, request)
			return applyErr
		},
	)
	return receipt, err
}

func commitDashboardWithGate(
	ctx context.Context,
	request metadata.DashboardCommitRequest,
	gates []businessWriteGate,
	apply func(context.Context, metadata.DashboardCommitRequest) (
		metadata.DashboardCommitReceipt, error,
	),
) (metadata.DashboardCommitReceipt, error) {
	var receipt metadata.DashboardCommitReceipt
	err := runBusinessWrite(
		ctx,
		gates,
		"metadata.dashboard.commit",
		request.IdempotencyKey,
		func(writeContext context.Context) error {
			var applyErr error
			receipt, applyErr = apply(writeContext, request)
			return applyErr
		},
	)
	return receipt, err
}

func decodeMetadataBody(body io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(
		body, maxMetadataRequestBytes+1,
	))
	if err != nil || len(raw) == 0 ||
		len(raw) > maxMetadataRequestBytes {
		return invalidMetadataBody()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalidMetadataBody()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return invalidMetadataBody()
	}
	return nil
}

func invalidMetadataBody() *metadata.Error {
	return &metadata.Error{
		Code:    "metadata.request.invalid",
		Message: "metadata request body is invalid",
	}
}

func writeMetadataError(
	request *core.RequestEvent,
	err error,
) error {
	var productErr *metadata.Error
	if !errors.As(err, &productErr) {
		productErr = &metadata.Error{
			Code:      "metadata.internal.failed",
			Message:   "metadata operation failed",
			Retryable: true,
		}
	}
	return request.JSON(metadataHTTPStatus(productErr), productErr)
}

func metadataHTTPStatus(err *metadata.Error) int {
	switch err.Code {
	case "metadata.namespace.invalid",
		"metadata.request.invalid",
		"metadata.dashboard.invalid":
		return http.StatusBadRequest
	case "metadata.not_found":
		return http.StatusNotFound
	case "metadata.revision_conflict",
		"metadata.idempotency_conflict":
		return http.StatusConflict
	case "metadata.storage.failed",
		"metadata.internal.failed":
		return http.StatusInternalServerError
	default:
		return http.StatusUnprocessableEntity
	}
}
