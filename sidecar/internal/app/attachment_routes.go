package app

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

func registerAttachmentRoutes(
	r *router.Router[*core.RequestEvent],
	manager *attachments.Manager,
) {
	r.GET("/api/vibetable/v1/attachments/refs", func(request *core.RequestEvent) error {
		query, err := strictAttachmentQuery(
			request.Request.URL.Query(),
			[]string{"tableId", "recordId", "fieldId"},
			nil,
		)
		if err != nil {
			return writeMutationError(
				request,
				attachmentRequestError("tableId, recordId, and fieldId are required"),
			)
		}
		refs, err := manager.RefsByID(
			request.Request.Context(), request.App,
			query["tableId"], query["recordId"], query["fieldId"],
		)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{"attachments": refs})
	})
	r.GET("/api/vibetable/v1/files/token", func(request *core.RequestEvent) error {
		query, err := strictAttachmentQuery(
			request.Request.URL.Query(),
			[]string{"tableId", "recordId", "fieldId", "storedName"},
			[]string{"variant"},
		)
		if err != nil {
			return writeMutationError(
				request,
				attachmentRequestError(
					"tableId, recordId, fieldId, and storedName are required",
				),
			)
		}
		capability, err := manager.Token(
			request.Request.Context(), request.App,
			query["tableId"], query["recordId"], query["fieldId"],
			query["storedName"], query["variant"],
		)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{
			"contractVersion":    attachments.ContractVersion,
			"downloadCapability": capability,
		})
	})
	r.GET("/api/vibetable/v1/attachments/download", func(request *core.RequestEvent) error {
		query, err := strictAttachmentQuery(
			request.Request.URL.Query(), []string{"capability"}, nil,
		)
		if err != nil {
			return writeMutationError(
				request, attachmentRequestError("download capability is required"),
			)
		}
		download, err := manager.Open(
			request.Request.Context(), request.App, query["capability"],
		)
		if err != nil {
			return writeMutationError(request, err)
		}
		defer download.Reader.Close()
		request.Response.Header().Set("Content-Type", download.ContentType)
		request.Response.Header().Set(
			"Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": download.Name}),
		)
		request.Response.Header().Set("Cache-Control", "private, no-store")
		request.Response.Header().Set("X-Content-Type-Options", "nosniff")
		if download.Size >= 0 {
			request.Response.Header().Set("Content-Length", strconv.FormatInt(download.Size, 10))
		}
		request.Response.WriteHeader(http.StatusOK)
		if _, err := io.Copy(request.Response, download.Reader); err != nil {
			return fmt.Errorf("stream managed attachment: %w", err)
		}
		return nil
	})
	r.GET("/api/vibetable/v1/attachments/integrity", func(request *core.RequestEvent) error {
		if request.Request.URL.RawQuery != "" {
			return writeMutationError(
				request, attachmentRequestError("integrity request has no query parameters"),
			)
		}
		report, err := manager.Integrity(request.Request.Context(), request.App)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, report)
	})
}

func strictAttachmentQuery(
	values url.Values,
	required []string,
	optional []string,
) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	result := make(map[string]string, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = struct{}{}
		raw, exists := values[key]
		if !exists || len(raw) != 1 || raw[0] == "" {
			return nil, attachmentRequestError("attachment query is invalid")
		}
		result[key] = raw[0]
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
		raw, exists := values[key]
		if !exists {
			continue
		}
		if len(raw) != 1 || raw[0] == "" {
			return nil, attachmentRequestError("attachment query is invalid")
		}
		result[key] = raw[0]
	}
	for key := range values {
		if _, exists := allowed[key]; !exists {
			return nil, attachmentRequestError("attachment query is invalid")
		}
	}
	return result, nil
}

func attachmentRequestError(message string) *mutation.ProductError {
	return &mutation.ProductError{
		ContractVersion: mutation.ContractVersion,
		Code:            "attachment.request.invalid", Message: message,
		Details: map[string]any{},
	}
}
