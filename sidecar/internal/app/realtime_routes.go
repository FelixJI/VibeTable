package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/realtime"
)

const realtimeHeartbeat = 15 * time.Second
const maxRealtimeRequestBytes = 64 << 10

func registerRealtimeRoutes(
	r *router.Router[*core.RequestEvent],
	hub *realtime.Hub,
	revisions realtime.RevisionSource,
) {
	r.GET("/api/vibetable/v1/events", func(request *core.RequestEvent) error {
		query := request.Request.URL.Query()
		if len(query) > 1 || len(query["after"]) > 1 {
			return writeRealtimeError(request, &realtime.Error{
				Code:    "realtime.request.invalid",
				Message: "events accepts at most one after cursor",
			})
		}
		after := query.Get("after")
		headerCursor := request.Request.Header.Get("Last-Event-ID")
		if after != "" && headerCursor != "" && after != headerCursor {
			return writeRealtimeError(request, &realtime.Error{
				Code:    "realtime.request.invalid",
				Message: "realtime cursors do not match",
			})
		}
		if after == "" {
			after = headerCursor
		}
		subscription, err := hub.Subscribe(request.Request.Context(), after)
		if err != nil {
			return writeRealtimeError(request, err)
		}
		defer subscription.Close()

		request.Response.Header().Set("Content-Type", "text/event-stream")
		request.Response.Header().Set("Cache-Control", "no-cache, no-store")
		request.Response.Header().Set("Connection", "keep-alive")
		request.Response.Header().Set("X-Accel-Buffering", "no")
		request.Response.WriteHeader(http.StatusOK)
		if err := request.Flush(); err != nil {
			return nil
		}

		sent := map[string]struct{}{}
		writeEvent := func(event realtime.Event) error {
			if sent != nil {
				if _, duplicate := sent[event.ID]; duplicate {
					return nil
				}
				sent[event.ID] = struct{}{}
			}
			topic := strings.ReplaceAll(event.Topic, "\n", "")
			eventID := event.Cursor
			if eventID == "" {
				eventID = event.ID
			}
			eventID = strings.ReplaceAll(eventID, "\n", "")
			if _, err := fmt.Fprintf(
				request.Response,
				"id: %s\nevent: %s\ndata: %s\n\n",
				eventID,
				topic,
				event.Payload,
			); err != nil {
				return err
			}
			if err := request.Flush(); err != nil {
				return err
			}
			return nil
		}
		for _, event := range subscription.Backlog {
			if err := writeEvent(event); err != nil {
				return nil
			}
		}
		sent = nil
		heartbeat := time.NewTicker(realtimeHeartbeat)
		defer heartbeat.Stop()
		for {
			select {
			case <-request.Request.Context().Done():
				return nil
			case event, open := <-subscription.Events:
				if !open {
					return nil
				}
				if err := writeEvent(event); err != nil {
					return nil
				}
			case <-heartbeat.C:
				if _, err := fmt.Fprint(request.Response, ": heartbeat\n\n"); err != nil {
					return nil
				}
				if err := request.Flush(); err != nil {
					return nil
				}
			}
		}
	})
	r.POST(
		"/api/vibetable/v1/events/reconcile",
		func(request *core.RequestEvent) error {
			var input realtime.ReconcileRequest
			if err := decodeRealtimeBody(
				request.Request.Body, &input,
			); err != nil {
				return writeRealtimeError(request, err)
			}
			result, err := realtime.Reconcile(
				request.Request.Context(), revisions, input,
			)
			if err != nil {
				return writeRealtimeError(request, err)
			}
			return request.JSON(http.StatusOK, result)
		},
	)
}

func decodeRealtimeBody(body io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(
		body, maxRealtimeRequestBytes+1,
	))
	if err != nil || len(raw) == 0 ||
		len(raw) > maxRealtimeRequestBytes {
		return &realtime.Error{
			Code:    "realtime.request.invalid",
			Message: "realtime request is invalid",
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &realtime.Error{
			Code:    "realtime.request.invalid",
			Message: "realtime request is invalid",
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &realtime.Error{
			Code:    "realtime.request.invalid",
			Message: "realtime request is invalid",
		}
	}
	return nil
}

func writeRealtimeError(request *core.RequestEvent, err error) error {
	var realtimeErr *realtime.Error
	if !errors.As(err, &realtimeErr) {
		realtimeErr = &realtime.Error{
			Code:      "realtime.internal_failed",
			Message:   "realtime operation failed",
			Retryable: true,
		}
	}
	status := http.StatusUnprocessableEntity
	switch realtimeErr.Code {
	case "realtime.request.invalid":
		status = http.StatusBadRequest
	case "realtime.cursor_unknown":
		status = http.StatusNotFound
	case "realtime.capacity":
		status = http.StatusServiceUnavailable
	case "realtime.storage_failed", "realtime.unavailable",
		"realtime.streaming_unavailable", "realtime.internal_failed":
		status = http.StatusInternalServerError
	}
	if writeErr := request.JSON(status, realtimeErr); writeErr != nil {
		raw, _ := json.Marshal(realtimeErr)
		return fmt.Errorf(
			"write realtime error response %s: %w",
			raw,
			writeErr,
		)
	}
	return nil
}
