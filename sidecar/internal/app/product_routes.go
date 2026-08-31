package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

const (
	productRPCPath             = "/api/vibetable/v2/product/rpc"
	productCapabilitiesPath    = "/api/vibetable/v2/product/capabilities"
	maxProductRPCRequestBytes  = 1 << 20
	maxProductRPCResponseBytes = 4 << 20
	productJSONContentType     = "application/json; charset=utf-8"
)

type productRPCDispatcher interface {
	Dispatch(ctx context.Context, raw []byte) productrpc.ResponseEnvelope
	Capabilities() productrpc.CapabilityDocument
}

func registerProductRoutes(
	r *router.Router[*core.RequestEvent],
	dispatcher productRPCDispatcher,
) {
	r.GET(productCapabilitiesPath, func(request *core.RequestEvent) error {
		return request.JSON(http.StatusOK, dispatcher.Capabilities())
	})
	r.POST(productRPCPath, func(request *core.RequestEvent) error {
		mediaType, _, err := mime.ParseMediaType(
			request.Request.Header.Get("Content-Type"),
		)
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			return writeProductEnvelope(
				request,
				http.StatusUnsupportedMediaType,
				invalidProductRequestEnvelope(),
			)
		}
		raw, err := io.ReadAll(io.LimitReader(
			request.Request.Body,
			maxProductRPCRequestBytes+1,
		))
		if err != nil || len(raw) == 0 || len(raw) > maxProductRPCRequestBytes {
			return writeProductEnvelope(
				request,
				http.StatusBadRequest,
				invalidProductRequestEnvelope(),
			)
		}
		response := dispatcher.Dispatch(request.Request.Context(), raw)
		status := http.StatusOK
		if response.Error != nil && response.Error.Code == productrpc.CodeInvalidRequest {
			status = http.StatusBadRequest
		}
		return writeProductEnvelope(request, status, response)
	})
}

func invalidProductRequestEnvelope() productrpc.ResponseEnvelope {
	return productrpc.ResponseEnvelope{
		JSONRPC: "2.0",
		ID:      json.RawMessage("null"),
		Wire:    json.RawMessage("null"),
		Error: &productrpc.ErrorObject{
			Code:    productrpc.CodeInvalidRequest,
			Message: "Invalid Request",
		},
	}
}

func writeProductEnvelope(
	request *core.RequestEvent,
	status int,
	envelope productrpc.ResponseEnvelope,
) error {
	raw, err := json.Marshal(envelope)
	if err != nil || len(raw) > maxProductRPCResponseBytes {
		envelope = productrpc.ResponseEnvelope{
			JSONRPC: "2.0",
			ID:      normalizedProductID(envelope.ID),
			Wire:    normalizedProductWire(envelope.Wire),
			Error: &productrpc.ErrorObject{
				Code:    productrpc.CodeInternalError,
				Message: "Internal error",
			},
		}
		raw, err = json.Marshal(envelope)
		status = http.StatusOK
	}
	if err != nil || len(raw) > maxProductRPCResponseBytes {
		return errors.New("encode Product RPC response")
	}
	request.Response.Header().Set("Content-Type", productJSONContentType)
	request.Response.Header().Set("X-Content-Type-Options", "nosniff")
	request.Response.WriteHeader(status)
	_, err = request.Response.Write(raw)
	return err
}

func normalizedProductID(value json.RawMessage) json.RawMessage {
	var id string
	if json.Unmarshal(value, &id) == nil && id != "" {
		return value
	}
	return json.RawMessage("null")
}

func normalizedProductWire(value json.RawMessage) json.RawMessage {
	var wire map[string]json.RawMessage
	if json.Unmarshal(value, &wire) == nil && wire != nil {
		return value
	}
	return json.RawMessage("null")
}
