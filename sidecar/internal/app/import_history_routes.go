package app

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/importhistory"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// Import history ports are internal Host->sidecar routes like the import plan
// lifecycle ports: they inherit the global vibetableSessionAuth hook and the
// loopback-only listener, use strict decoding and bounded bodies, and never
// expose the backing locked collection through the standard PocketBase REST
// surface.
const (
	importHistoryBase           = "/api/vibetable/v2/import-history"
	maxImportHistoryRequestBody = 64 << 10
)

type importHistoryStartBody struct {
	TaskID         string `json:"taskId"`
	Collection     string `json:"collection"`
	SourceType     string `json:"sourceType"`
	SourceName     string `json:"sourceName"`
	IdempotencyKey string `json:"idempotencyKey"`
	SessionEpoch   uint64 `json:"sessionEpoch"`
}

type importHistoryFinishBody struct {
	TaskID string `json:"taskId"`
	State  string `json:"state"`
}

func registerImportHistoryRoutes(
	r *router.Router[*core.RequestEvent],
	app core.App,
	store *importhistory.Store,
	gates ...businessWriteGate,
) {
	// Start is called by the Host before the import worker executes and the
	// sidecar records startedAt itself. The write stays coordinated so the
	// workspace fencing rules govern every projection mutation.
	r.POST(importHistoryBase+"/start", func(request *core.RequestEvent) error {
		var body importHistoryStartBody
		if err := decodeImportHistoryBody(request.Request.Body, &body); err != nil {
			return writeImportHistoryError(request, err)
		}
		sourceType, ok := importhistory.NormalizeSourceType(body.SourceType)
		if !ok {
			return writeImportHistoryError(request, &importhistory.Error{
				Code:    "import_history.request.invalid",
				Path:    "sourceType",
				Message: "sourceType must be csv or xlsx",
			})
		}
		input := importhistory.StartInput{
			TaskID: body.TaskID, Collection: body.Collection,
			SourceType: sourceType, SourceName: body.SourceName,
			IdempotencyKey: body.IdempotencyKey,
			SessionEpoch:   body.SessionEpoch,
		}
		var entry importhistory.Entry
		err := runIdempotentBusinessWrite(
			request.Request.Context(),
			gates,
			"import_history.start",
			body.TaskID,
			func(ctx context.Context) error {
				var applyErr error
				// ctx carries the coordinated workspace write intent; the store
				// persists the business receipt from it inside its transaction.
				entry, applyErr = store.Start(ctx, app, input)
				return applyErr
			},
		)
		if err != nil {
			return writeImportHistoryError(request, err)
		}
		if entry.TaskID == "" {
			// A coordinated replay may skip the callback; re-read the
			// idempotent projection instead of fabricating an entry.
			entry, err = store.Get(request.Request.Context(), app, body.TaskID)
			if err != nil {
				return writeImportHistoryError(request, err)
			}
		}
		return request.JSON(http.StatusOK, entry)
	})
	// Finish only accepts Host-reported terminal execution outcomes
	// (failed/cancelled/aborted); success is minted exclusively by the Go
	// mutation transaction and can never be overwritten here.
	r.POST(importHistoryBase+"/finish", func(request *core.RequestEvent) error {
		var body importHistoryFinishBody
		if err := decodeImportHistoryBody(request.Request.Body, &body); err != nil {
			return writeImportHistoryError(request, err)
		}
		var entry importhistory.Entry
		err := runIdempotentBusinessWrite(
			request.Request.Context(),
			gates,
			"import_history.finish",
			body.TaskID,
			func(ctx context.Context) error {
				var applyErr error
				entry, applyErr = store.Finish(ctx, app, body.TaskID, body.State)
				return applyErr
			},
		)
		if err != nil {
			return writeImportHistoryError(request, err)
		}
		if entry.TaskID == "" {
			entry, err = store.Get(request.Request.Context(), app, body.TaskID)
			if err != nil {
				return writeImportHistoryError(request, err)
			}
		}
		return request.JSON(http.StatusOK, entry)
	})
	r.GET(importHistoryBase, func(request *core.RequestEvent) error {
		items, err := store.List(request.Request.Context(), app)
		if err != nil {
			return writeImportHistoryError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{"items": items})
	})
}

func decodeImportHistoryBody(body io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxImportHistoryRequestBody+1))
	if err != nil || len(raw) == 0 || len(raw) > maxImportHistoryRequestBody {
		return &importhistory.Error{
			Code:    "import_history.request.invalid",
			Message: "import history request body is invalid",
		}
	}
	if err := v2.StrictDecode(raw, target); err != nil {
		return &importhistory.Error{
			Code:    "import_history.request.invalid",
			Message: "import history request body is invalid",
		}
	}
	return nil
}

func writeImportHistoryError(request *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	productErr := &importhistory.Error{
		Code:    "import_history.internal.failed",
		Message: "import history operation failed",
	}
	var typed *importhistory.Error
	if errors.As(err, &typed) {
		productErr = typed
	}
	if writeErr := request.JSON(
		importHistoryHTTPStatus(productErr), productErr,
	); writeErr != nil {
		return writeErr
	}
	return nil
}

func importHistoryHTTPStatus(err *importhistory.Error) int {
	switch err.Code {
	case "import_history.request.invalid":
		return http.StatusBadRequest
	case "import_history.task_not_found":
		return http.StatusNotFound
	case "import_history.task_conflict",
		"import_history.idempotency_conflict":
		return http.StatusConflict
	case "import_history.storage.failed",
		"import_history.internal.failed":
		return http.StatusInternalServerError
	default:
		return http.StatusUnprocessableEntity
	}
}
