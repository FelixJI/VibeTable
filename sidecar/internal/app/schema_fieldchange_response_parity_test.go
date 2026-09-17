package app

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

// Frozen authority replies test the same forwarding boundary as the original
// Python capture. Actual domain execution is tested separately by the HTTP
// lifecycle test: these scripted replies are not domain qualification evidence.
func TestSchemaFieldChangeFrozenResponseParity(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/schema-fieldchange-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Cases []struct {
			Name      string                     `json:"name"`
			Request   map[string]json.RawMessage `json:"request"`
			Authority struct {
				Body    json.RawMessage `json:"body"`
				Failure string          `json:"failure"`
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
		if !success && item.Authority.Failure != "domain" {
			continue
		}
		t.Run(item.Name, func(t *testing.T) {
			var method string
			if err := json.Unmarshal(item.Request["method"], &method); err != nil {
				t.Fatal(err)
			}
			var result any
			var failure error
			if item.Authority.Failure == "domain" {
				if method == "schema.delete" {
					var domain schemaerror.ProductError
					if err := json.Unmarshal(item.Authority.Body, &domain); err != nil {
						t.Fatal(err)
					}
					failure = publicSchemaDeleteError(&domain)
				} else {
					var domain fieldchange.ProductError
					if err := json.Unmarshal(item.Authority.Body, &domain); err != nil {
						t.Fatal(err)
					}
					failure = publicFieldChangeError(&domain)
				}
			} else {
				switch method {
				case "schema.table.create":
					result = &v2.TableCreateReceipt{}
				case "schema.delete":
					result = &schemaapi.DeleteResult{}
				case "field.change.plan":
					result = &v2.FieldChangePlan{}
				case "field.change.apply":
					result = &v2.ApplyReceipt{}
				case "field.change.status", "field.change.cancel":
					result = &v2.MigrationStatus{}
				case "field.recycleBin.list":
					result = &recycledFieldsResult{}
				default:
					t.Fatalf("unexpected method %s", method)
				}
				if err := json.Unmarshal(item.Authority.Body, result); err != nil {
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
					registration.ValidateParams = func(raw json.RawMessage) error { _, _, err := decodeSchemaFieldChangeParams(method, raw); return err }
					registration.Handler = func(context.Context, json.RawMessage) (any, error) { return result, failure }
				}
				registrations = append(registrations, registration)
			}
			dispatcher, err := productrpc.New(productrpc.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, registrations...)
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
			// All original JSON-RPC fields, including complete result/error data, remain exact.
			delete(got, "wire")
			if !reflect.DeepEqual(got, item.Response) {
				t.Fatalf("Python=%#v\nGo=%s", item.Response, actual)
			}
		})
		checked++
	}
	if checked < 11 {
		t.Fatalf("missing representative success/error cases: %d", checked)
	}
}
