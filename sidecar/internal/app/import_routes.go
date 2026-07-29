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
) {
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
