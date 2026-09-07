package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

type historyParityRequest struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Query  map[string][]string `json:"query"`
}

type historyParityReader struct {
	page     audit.Page
	err      *audit.Error
	requests []historyParityRequest
}

func (reader *historyParityReader) ReadBusinessHistory(_ context.Context, params audit.ReadParams) (audit.Page, error) {
	query := map[string][]string{
		"collection": {params.TableID}, "scope": {params.Scope},
		"limit": {strconv.Itoa(params.Limit)}, "offset": {strconv.Itoa(params.Offset)},
	}
	for key, value := range map[string]*string{
		"itemId": params.ItemID, "field": params.Field, "actorId": params.ActorID,
		"dateFrom": params.DateFrom, "dateTo": params.DateTo, "recordId": params.RecordID,
	} {
		if value != nil {
			query[key] = []string{*value}
		}
	}
	if params.Search != "" {
		query["search"] = []string{params.Search}
	}
	if len(params.Actions) != 0 {
		query["action"] = params.Actions
	}
	reader.requests = append(reader.requests, historyParityRequest{
		Method: "GET", Path: "/api/vibetable/v1/history/change-sets", Query: query,
	})
	if reader.err != nil {
		return audit.Page{}, reader.err
	}
	return reader.page, nil
}

func TestHistoryReadMatchesFrozenPythonParityCorpus(t *testing.T) {
	var corpus struct {
		FormatVersion int               `json:"formatVersion"`
		Method        string            `json:"method"`
		Page          audit.Page        `json:"page"`
		Producer      map[string]string `json:"producer"`
		Cases         []struct {
			Name           string                 `json:"name"`
			ParamsJSON     string                 `json:"paramsJson"`
			SidecarStatus  int                    `json:"sidecarStatus"`
			SidecarError   *audit.Error           `json:"sidecarError"`
			PythonResponse map[string]any         `json:"pythonResponse"`
			PythonRequests []historyParityRequest `json:"pythonRequests"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "history-read-python-parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.FormatVersion != 1 || corpus.Method != "history.read" ||
		corpus.Producer["route"] != "pythonBff" || corpus.Producer["commit"] == "" || len(corpus.Cases) == 0 {
		t.Fatal("missing historical Python parity provenance or cases")
	}
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			reader := &historyParityReader{page: corpus.Page, err: test.SidecarError}
			registrations := []productrpc.Registration{historyReadRegistration(reader)}
			for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
				if descriptor.Method == "history.read" {
					continue
				}
				registrations = append(registrations, productrpc.Registration{
					Method: descriptor.Method, Scope: descriptor.Scope,
					ValidateParams: func(json.RawMessage) error { t.Fatal("unexpected unrelated validation"); return nil },
					Handler: func(context.Context, json.RawMessage) (any, error) {
						t.Fatal("unexpected unrelated method")
						return nil, nil
					},
				})
			}
			dispatcher, err := productrpc.New(productrpc.Identity{
				WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
				FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			}, registrations...)
			if err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(context.Background(), []byte(
				`{"jsonrpc":"2.0","id":"parity","method":"history.read","wire":`+schemaListWire+`,"params":`+test.ParamsJSON+`}`))
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var actual map[string]any
			if err := json.Unmarshal(encoded, &actual); err != nil {
				t.Fatal(err)
			}
			// Go carries the authenticated sidecar wire, which is not part of the Python result envelope.
			delete(actual, "wire")
			if !reflect.DeepEqual(actual, test.PythonResponse) {
				t.Fatalf("Python/Go response mismatch\nPython: %#v\nGo: %#v", test.PythonResponse, actual)
			}
			if len(reader.requests) != len(test.PythonRequests) ||
				(len(reader.requests) != 0 && !reflect.DeepEqual(reader.requests, test.PythonRequests)) {
				t.Fatalf("Python/Go authority request mismatch\nPython: %#v\nGo: %#v", test.PythonRequests, reader.requests)
			}
		})
	}
}
