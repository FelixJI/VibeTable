package app

import (
	"errors"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/importvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const maxImportPreviewRequestBytes = 64 << 20

func registerImportRoutes(
	r *router.Router[*core.RequestEvent],
	service *importvalue.Service,
	plans *importPlanOwner,
) {
	// The plan lifecycle routes are internal BFF->sidecar routes like the
	// import-preview route above: they inherit the global vibetableSessionAuth
	// router hook and the loopback-only listener. They are not public/native
	// Product RPC methods and stay out of the productcapabilities registry;
	// workspace isolation is enforced inside the owner via the workspaceID
	// binding minted with every plan.
	importPlanBase := "/api/vibetable/v2/import-plans"
	r.POST(importPlanBase, func(
		request *core.RequestEvent,
	) error {
		var input importPlanMintRequest
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		reply, err := plans.mint(input)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, reply)
	})
	r.POST(importPlanBase+"/stage", func(
		request *core.RequestEvent,
	) error {
		var input importPlanStageRequest
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		reply, err := plans.stage(input)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, reply)
	})
	r.POST(importPlanBase+"/bind", func(
		request *core.RequestEvent,
	) error {
		var input importPlanBindRequest
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		idempotencyKey, err := plans.bind(input)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{"idempotencyKey": idempotencyKey})
	})
	r.POST(importPlanBase+"/settle", func(
		request *core.RequestEvent,
	) error {
		var input importPlanSettleRequest
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		reply, err := plans.settle(input)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, reply)
	})
	r.POST("/api/vibetable/v2/import-preview", func(
		request *core.RequestEvent,
	) error {
		raw, err := io.ReadAll(io.LimitReader(
			request.Request.Body, maxImportPreviewRequestBytes+1,
		))
		if err != nil {
			return writeFieldError(request, err)
		}
		if len(raw) > maxImportPreviewRequestBytes {
			return writeFieldError(request, errors.New("import preview request is too large"))
		}
		var input importvalue.Request
		if err := v2.StrictDecode(raw, &input); err != nil {
			return writeFieldError(request, err)
		}
		result, err := service.Preview(request.Request.Context(), input)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
}

func decodeImportPlanBody(request *core.RequestEvent, target any) error {
	raw, err := io.ReadAll(io.LimitReader(
		request.Request.Body, maxImportPreviewRequestBytes+1,
	))
	if err != nil {
		return writeImportPlanDecodeError("request body could not be read")
	}
	if len(raw) > maxImportPreviewRequestBytes {
		return writeImportPlanDecodeError("import plan request is too large")
	}
	if err := v2.StrictDecode(raw, target); err != nil {
		return writeImportPlanDecodeError("import plan request is invalid: " + err.Error())
	}
	return nil
}

func writeImportPlanDecodeError(message string) error {
	return &v2.ProductError{Code: "import_plan_invalid", Message: message}
}
