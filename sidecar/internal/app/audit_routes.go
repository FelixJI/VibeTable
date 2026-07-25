package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
)

type restorePreviewRequest struct {
	Collection     string  `json:"collection"`
	ItemID         string  `json:"itemId"`
	TargetRevision string  `json:"targetRevision"`
	Scope          string  `json:"scope"`
	Field          *string `json:"field,omitempty"`
}

type restoreApplyRequest struct {
	Collection string `json:"collection"`
	ItemID     string `json:"itemId"`
	Token      string `json:"token"`
}

func registerAuditRoutes(
	r *router.Router[*core.RequestEvent],
	service *audit.Service,
) {
	r.GET("/api/vibetable/v1/history/change-sets", func(request *core.RequestEvent) error {
		params, err := decodeHistoryQuery(request.Request)
		if err != nil {
			return writeAuditError(request, err)
		}
		page, err := service.ReadChangeSets(request.Request.Context(), params)
		if err != nil {
			return writeAuditError(request, err)
		}
		return request.JSON(http.StatusOK, page)
	})
	r.POST("/api/vibetable/v1/history/restore-preview", func(request *core.RequestEvent) error {
		var input restorePreviewRequest
		if err := decodeAuditBody(request.Request.Body, &input); err != nil {
			return writeAuditError(request, err)
		}
		preview, err := service.PreviewRestore(request.Request.Context(), audit.PreviewParams{
			TableID: input.Collection, ItemID: input.ItemID,
			TargetRevision: input.TargetRevision, Scope: input.Scope, Field: input.Field,
		})
		if err != nil {
			return writeAuditError(request, err)
		}
		return request.JSON(http.StatusOK, preview)
	})
	r.POST("/api/vibetable/v1/history/restore-apply", func(request *core.RequestEvent) error {
		var input restoreApplyRequest
		if err := decodeAuditBody(request.Request.Body, &input); err != nil {
			return writeAuditError(request, err)
		}
		result, err := service.ApplyRestore(request.Request.Context(), audit.ApplyParams{
			TableID: input.Collection, ItemID: input.ItemID, Token: input.Token,
		})
		if err != nil {
			return writeAuditError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
}

func decodeHistoryQuery(request *http.Request) (audit.ReadParams, error) {
	query := request.URL.Query()
	allowed := map[string]struct{}{
		"collection": {}, "itemId": {}, "field": {}, "search": {},
		"actorId": {}, "action": {}, "dateFrom": {}, "dateTo": {},
		"limit": {}, "offset": {}, "scope": {}, "recordId": {},
	}
	for name := range query {
		if _, ok := allowed[name]; !ok {
			return audit.ReadParams{}, auditRequestError("unknown history query parameter")
		}
	}
	for _, name := range []string{
		"collection", "itemId", "field", "search", "actorId",
		"dateFrom", "dateTo", "limit", "offset", "scope", "recordId",
	} {
		if len(query[name]) > 1 {
			return audit.ReadParams{}, auditRequestError(
				"history query parameter must appear at most once",
			)
		}
	}
	if len(query["collection"]) != 1 || query.Get("collection") == "" {
		return audit.ReadParams{}, auditRequestError("collection is required")
	}
	limit, offset := 50, 0
	var err error
	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil {
			return audit.ReadParams{}, auditRequestError("history limit is invalid")
		}
	}
	if value := query.Get("offset"); value != "" {
		offset, err = strconv.Atoi(value)
		if err != nil {
			return audit.ReadParams{}, auditRequestError("history offset is invalid")
		}
	}
	scope := query.Get("scope")
	if scope == "" {
		scope = "row"
	}
	return audit.ReadParams{
		TableID: query.Get("collection"), ItemID: optionalQuery(query.Get("itemId")),
		Field: optionalQuery(query.Get("field")), Search: query.Get("search"),
		ActorID: optionalQuery(query.Get("actorId")), Actions: query["action"],
		DateFrom: optionalQuery(query.Get("dateFrom")), DateTo: optionalQuery(query.Get("dateTo")),
		Limit: limit, Offset: offset, Scope: scope,
		RecordID: optionalQuery(query.Get("recordId")),
	}, nil
}

func decodeAuditBody(reader io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, maxSchemaRequestBytes+1))
	if err != nil || len(raw) > maxSchemaRequestBytes {
		return auditRequestError("history request body is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return auditRequestError("history request body is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return auditRequestError("history request must contain one JSON value")
	}
	return nil
}

func auditRequestError(message string) *audit.Error {
	return &audit.Error{
		Code: "history.request_invalid", Message: message,
		Details: map[string]any{},
	}
}

func writeAuditError(request *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	var formulaErr *formula.Error
	if errors.As(err, &formulaErr) {
		return writeFormulaError(request, formulaErr)
	}
	var historyErr *audit.Error
	if !errors.As(err, &historyErr) {
		historyErr = &audit.Error{
			Code: "history.internal_failed", Message: "history operation failed",
			Details: map[string]any{}, Retryable: true,
		}
	}
	status := auditHTTPStatus(historyErr)
	if writeErr := request.JSON(status, historyErr); writeErr != nil {
		return fmt.Errorf("write history error response: %w", writeErr)
	}
	return nil
}

func auditHTTPStatus(err *audit.Error) int {
	switch err.Code {
	case "history.request_invalid", "restore.request_invalid", "restore.scope_invalid":
		return http.StatusBadRequest
	case "history.table_not_found", "history.field_not_found",
		"target_revision_invalid", "restore_token_unknown":
		return http.StatusNotFound
	case "restore_token_expired":
		return http.StatusGone
	case "restore_conflict", "restore_scope_mismatch", "schema_drift":
		return http.StatusConflict
	case "history.storage_failed", "history.storage_corrupt",
		"history.internal_failed", "restore.token_failed", "revision_not_created":
		return http.StatusInternalServerError
	case "restore.capacity_exhausted":
		return http.StatusServiceUnavailable
	case "restore_attachment_missing", "restore_attachment_corrupt",
		"restore_validation_failed", "restore_no_fields",
		"restore.resource_limit":
		return http.StatusUnprocessableEntity
	default:
		return http.StatusUnprocessableEntity
	}
}

func optionalQuery(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
