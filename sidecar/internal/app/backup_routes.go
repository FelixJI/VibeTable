package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/backup"
)

const maxBackupRequestBytes = 16 << 10

type backupRequest struct {
	Name string `json:"name"`
}

func registerBackupRoutes(
	r *router.Router[*core.RequestEvent],
	service *backup.Service,
) {
	r.GET("/api/vibetable/v1/backups", func(
		request *core.RequestEvent,
	) error {
		result, err := service.List(request.Request.Context())
		if err != nil {
			return writeBackupError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{
			"backups": result,
		})
	})
	r.POST("/api/vibetable/v1/backups", func(
		request *core.RequestEvent,
	) error {
		var input backupRequest
		if err := decodeBackupBody(
			request.Request.Body, &input,
		); err != nil {
			return writeBackupError(request, err)
		}
		result, err := service.Create(
			request.Request.Context(), input.Name,
		)
		if err != nil {
			return writeBackupError(request, err)
		}
		return request.JSON(http.StatusCreated, result)
	})
	r.POST("/api/vibetable/v1/backups/restore", func(
		request *core.RequestEvent,
	) error {
		var input backupRequest
		if err := decodeBackupBody(
			request.Request.Body, &input,
		); err != nil {
			return writeBackupError(request, err)
		}
		if err := service.Restore(
			request.Request.Context(), input.Name,
		); err != nil {
			return writeBackupError(request, err)
		}
		return request.JSON(http.StatusAccepted, map[string]any{
			"status": "restarting",
		})
	})
}

func decodeBackupBody(body io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(
		body, maxBackupRequestBytes+1,
	))
	if err != nil || len(raw) == 0 ||
		len(raw) > maxBackupRequestBytes {
		return &backup.Error{
			Code:    "backup.request.invalid",
			Message: "backup request is invalid",
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return &backup.Error{
			Code:    "backup.request.invalid",
			Message: "backup request is invalid",
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &backup.Error{
			Code:    "backup.request.invalid",
			Message: "backup request is invalid",
		}
	}
	return nil
}

func writeBackupError(
	request *core.RequestEvent,
	err error,
) error {
	var productErr *backup.Error
	if !errors.As(err, &productErr) {
		productErr = &backup.Error{
			Code:      "backup.internal_failed",
			Message:   "backup operation failed",
			Retryable: true,
		}
	}
	status := http.StatusUnprocessableEntity
	switch productErr.Code {
	case "backup.request.invalid", "backup.name_invalid":
		status = http.StatusBadRequest
	case "backup.not_found":
		status = http.StatusNotFound
	case "backup.storage_failed", "backup.create_failed",
		"backup.restore_failed", "backup.safety_copy_failed",
		"backup.internal_failed":
		status = http.StatusInternalServerError
	}
	return request.JSON(status, productErr)
}
