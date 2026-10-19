package app

import (
	"bytes"
	"encoding/json"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"net/http"
	"net/http/httptest"
	"testing"
)

func metadataHTTPRequest(t *testing.T, server *httptest.Server, method, params string) productrpc.ResponseEnvelope {
	t.Helper()
	raw := `{"jsonrpc":"2.0","id":"metadata-http","method":"` + method + `","wire":` + schemaListWire + `,"params":` + params + `}`
	response, err := server.Client().Post(server.URL+productRPCPath, "application/json", bytes.NewBufferString(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status %d", response.StatusCode)
	}
	var envelope productrpc.ResponseEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}
