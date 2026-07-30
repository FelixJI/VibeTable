package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

const (
	workspaceV2RPCPath          = "/api/vibetable/v2/rpc"
	workspaceV2CapabilitiesPath = "/api/vibetable/v2/capabilities"
	workspaceV2DrainPath        = "/api/vibetable/v2/workspace/drain"
	maxWorkspaceV2RequestBytes  = 1 << 20
)

func registerWorkspaceV2Routes(
	r *router.Router[*core.RequestEvent],
	runtime *workspacev2.Runtime,
) {
	r.GET(workspaceV2CapabilitiesPath, func(
		request *core.RequestEvent,
	) error {
		return request.JSON(http.StatusOK, runtime.Capabilities())
	})
	r.POST(workspaceV2RPCPath, func(request *core.RequestEvent) error {
		mediaType, _, err := mime.ParseMediaType(
			request.Request.Header.Get("Content-Type"),
		)
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			return request.JSON(
				http.StatusUnsupportedMediaType,
				workspaceV2ErrorEnvelope(
					"application/json is required",
				),
			)
		}
		raw, err := io.ReadAll(io.LimitReader(
			request.Request.Body,
			maxWorkspaceV2RequestBytes+1,
		))
		if err != nil || len(raw) == 0 ||
			len(raw) > maxWorkspaceV2RequestBytes {
			return request.JSON(
				http.StatusBadRequest,
				workspaceV2ErrorEnvelope(
					"workspace v2 request is invalid",
				),
			)
		}
		response := runtime.Dispatcher().DispatchEnvelope(
			workspacev2.WithPathGrantHeader(
				request.Request.Context(),
				request.Request.Header.Get("X-VibeTable-Path-Grant"),
			),
			raw,
		)
		status := http.StatusOK
		if response.Error != nil &&
			response.Error.Code == "workspace.request_invalid" {
			status = http.StatusBadRequest
		}
		return request.JSON(status, response)
	})
	r.POST(workspaceV2DrainPath, func(request *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(request.Request.Body, 257))
		if err != nil || len(raw) == 0 || len(raw) > 256 {
			return request.JSON(http.StatusBadRequest, map[string]any{
				"code": "workspace.drain_request_invalid",
			})
		}
		var params struct {
			DeadlineMs uint64 `json:"deadlineMs"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&params) != nil ||
			params.DeadlineMs == 0 ||
			params.DeadlineMs > 60_000 ||
			!errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
			return request.JSON(http.StatusBadRequest, map[string]any{
				"code": "workspace.drain_request_invalid",
			})
		}
		highWatermark, err := runtime.Drain(
			request.Request.Context(),
			time.Now().UTC().Add(
				time.Duration(params.DeadlineMs)*time.Millisecond,
			),
		)
		if err != nil {
			return request.JSON(http.StatusConflict, map[string]any{
				"code":      err.Error(),
				"retryable": true,
			})
		}
		return request.JSON(http.StatusOK, map[string]any{
			"sourceEpoch":    highWatermark.SourceEpoch,
			"sourceSequence": highWatermark.SourceSequence,
			"chainHash":      highWatermark.ChainHash,
		})
	})
}

func workspaceV2ErrorEnvelope(message string) protocolv2.ResponseEnvelope {
	return protocolv2.ResponseEnvelope{
		JSONRPC: "2.0",
		ID:      "",
		Wire:    json.RawMessage("null"),
		Error: &protocolv2.ErrorBody{
			Code:      "workspace.request_invalid",
			Message:   message,
			Details:   map[string]any{},
			Retryable: false,
		},
	}
}
