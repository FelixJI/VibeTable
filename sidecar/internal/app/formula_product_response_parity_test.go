package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

// Frozen authority replies exercise the same forwarding boundary the retired
// Python adapter froze. Actual domain execution is qualified separately by
// the real producer and HTTP lifecycle tests; scripted replies are not domain
// evidence.
func TestFormulaProductFrozenResponseParity(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/formula-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Cases []struct {
			Name      string                     `json:"name"`
			Request   map[string]json.RawMessage `json:"request"`
			Authority struct {
				Status           int             `json:"status"`
				Body             json.RawMessage `json:"body"`
				TransportFailure bool            `json:"transportFailure"`
			} `json:"authorityFixture"`
			Response map[string]any `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, item := range frozen.Cases {
		_, success := item.Response["result"]
		_, domainFailure := item.Response["error"]
		if !success && (!domainFailure || item.Authority.TransportFailure) {
			continue
		}
		if item.Authority.TransportFailure {
			continue
		}
		errorData, _ := item.Response["error"].(map[string]any)
		if code, isPublic := errorData["code"].(float64); errorData != nil && (!isPublic || code != -32150) {
			continue
		}
		t.Run(item.Name, func(t *testing.T) {
			var method string
			if err := json.Unmarshal(item.Request["method"], &method); err != nil {
				t.Fatal(err)
			}
			var result any
			var failure error
			if errorData != nil {
				var domain formula.Error
				if err := json.Unmarshal(item.Authority.Body, &domain); err != nil {
					t.Fatal(err)
				}
				failure = publicFormulaError(&domain)
			} else {
				decoder := json.NewDecoder(bytes.NewReader(item.Authority.Body))
				decoder.UseNumber()
				if err := decoder.Decode(&result); err != nil {
					t.Fatal(err)
				}
			}
			var registrations []productrpc.Registration
			for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
				registration := productrpc.Registration{Method: descriptor.Method, Scope: descriptor.Scope,
					ValidateParams: func(json.RawMessage) error { t.Fatal("unrelated validator invoked"); return nil },
					Handler: func(context.Context, json.RawMessage) (any, error) {
						t.Fatal("unrelated handler invoked")
						return nil, nil
					},
				}
				if descriptor.Method == method {
					registration.ValidateParams = func(raw json.RawMessage) error {
						_, err := decodeFormulaProductParams(method, raw)
						return err
					}
					registration.Handler = func(context.Context, json.RawMessage) (any, error) {
						return result, failure
					}
				}
				registrations = append(registrations, registration)
			}
			dispatcher, err := productrpc.New(productrpc.Identity{
				WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
				FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			}, registrations...)
			if err != nil {
				t.Fatal(err)
			}
			item.Request["wire"] = json.RawMessage(schemaListWire)
			request, err := json.Marshal(item.Request)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := json.Marshal(dispatcher.Dispatch(context.Background(), request))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(actual, &got); err != nil {
				t.Fatal(err)
			}
			// The Go transport adds the separately validated Host session envelope.
			// All original JSON-RPC fields, including complete result/error data,
			// remain exact.
			delete(got, "wire")
			if !reflect.DeepEqual(got, item.Response) {
				t.Fatalf("Python=%#v\nGo=%s", item.Response, actual)
			}
		})
		checked++
	}
	if checked < 9 {
		t.Fatalf("missing representative success/error cases: %d", checked)
	}
}
