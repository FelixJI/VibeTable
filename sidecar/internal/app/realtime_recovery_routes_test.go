package app

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestRealtimeRecoveryHTTPStartsWithAnEmptyAuthoritativeProjection(t *testing.T) {
	server := newRealtimeRecoveryServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/vibetable/v2/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("recovery stream status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	fields := map[string]string{}
	for scanner.Scan() && scanner.Text() != "" {
		key, value, _ := strings.Cut(scanner.Text(), ": ")
		fields[key] = value
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if fields["id"] != "rt:0" || fields["event"] != "realtime.recovered" {
		t.Fatalf("empty-store recovery envelope=%v", fields)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(fields["data"]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["contractVersion"] != "2.0" || payload["topic"] != "realtime.recovered" {
		t.Fatalf("recovery payload=%v", payload)
	}
	for _, key := range []string{"activeFormulaTasks", "terminalNotifications"} {
		values, ok := payload[key].([]any)
		if !ok || len(values) != 0 {
			t.Fatalf("%s must be the complete empty projection, got %v", key, payload[key])
		}
	}
}

func TestRealtimeRecoveryHTTPRejectsAmbiguousOrMalformedCursors(t *testing.T) {
	server := newRealtimeRecoveryServer(t)
	for _, input := range []struct {
		name, query string
		headers     []string
	}{
		{name: "malformed escaping", query: "after=rt%3A%zz"},
		{name: "unknown parameter", query: "unknown=rt:0"},
		{name: "duplicate query", query: "after=rt:0&after=rt:0"},
		{name: "duplicate header", headers: []string{"rt:0", "rt:1"}},
		{name: "conflicting cursors", query: "after=rt:0", headers: []string{"rt:1"}},
		{name: "noncanonical cursor", query: "after=rt:00"},
	} {
		t.Run(input.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/vibetable/v2/events?"+input.query, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, header := range input.headers {
				request.Header.Add("Last-Event-ID", header)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest || response.Header.Get("Content-Type") == "text/event-stream" {
				t.Fatalf("invalid request opened stream: status=%d type=%q", response.StatusCode, response.Header.Get("Content-Type"))
			}
		})
	}
}

func newRealtimeRecoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	pb := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(pb)
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pb.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	if err := pb.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (
		*core.RequestEvent, router.EventCleanupFunc,
	) {
		return &core.RequestEvent{Event: router.Event{Response: writer, Request: request}}, nil
	})
	registerRealtimeRoutes(r, realtime.New(pb), nil)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}
