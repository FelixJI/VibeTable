package app

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

type restoreParityRequest struct {
	Method string         `json:"method"`
	Path   string         `json:"path"`
	Body   map[string]any `json:"body"`
}
type restoreParityOwner struct {
	preview  audit.Preview
	applied  workspacev2.BusinessHistoryRestoreResult
	err      *audit.Error
	requests []restoreParityRequest
}

func (o *restoreParityOwner) PreviewBusinessHistoryRestore(_ context.Context, p audit.PreviewParams) (audit.Preview, error) {
	body := map[string]any{"collection": p.TableID, "itemId": p.ItemID, "targetRevision": p.TargetRevision, "scope": p.Scope}
	if p.Field != nil {
		body["field"] = *p.Field
	}
	o.requests = append(o.requests, restoreParityRequest{"POST", "/api/vibetable/v1/history/restore-preview", body})
	if o.err != nil {
		return audit.Preview{}, o.err
	}
	return o.preview, nil
}
func (o *restoreParityOwner) ApplyBusinessHistoryRestore(_ context.Context, identity string, p audit.ApplyParams) (workspacev2.BusinessHistoryRestoreResult, error) {
	if identity != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		panic("submit identity was not forwarded")
	}
	o.requests = append(o.requests, restoreParityRequest{"POST", "/api/vibetable/v1/history/restore-apply", map[string]any{"collection": p.TableID, "itemId": p.ItemID, "token": p.Token}})
	if o.err != nil {
		return workspacev2.BusinessHistoryRestoreResult{}, o.err
	}
	return o.applied, nil
}

func TestHistoryRestoreMatchesFrozenPythonParityCorpus(t *testing.T) {
	var corpus struct {
		FormatVersion int                                      `json:"formatVersion"`
		Methods       []string                                 `json:"methods"`
		Preview       audit.Preview                            `json:"preview"`
		Applied       workspacev2.BusinessHistoryRestoreResult `json:"applied"`
		Producer      map[string]string                        `json:"producer"`
		Cases         []struct {
			Name           string                 `json:"name"`
			Method         string                 `json:"method"`
			ParamsJSON     string                 `json:"paramsJson"`
			SidecarStatus  int                    `json:"sidecarStatus"`
			SidecarError   *audit.Error           `json:"sidecarError"`
			PythonResponse map[string]any         `json:"pythonResponse"`
			PythonRequests []restoreParityRequest `json:"pythonRequests"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "history-restore-python-parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.FormatVersion != 1 || len(corpus.Methods) != 2 ||
		corpus.Producer["route"] != "pythonBff" || corpus.Producer["commit"] == "" || len(corpus.Cases) == 0 {
		t.Fatal("missing historical Python parity provenance or cases")
	}
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			reader := &restoreParityOwner{preview: corpus.Preview, applied: corpus.Applied, err: test.SidecarError}
			registrations := []productrpc.Registration{historyPreviewRestoreRegistration(reader), historyApplyRestoreRegistration(reader)}
			for _, descriptor := range productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar) {
				if descriptor.Method == "history.previewRestore" || descriptor.Method == "history.applyRestore" {
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
				`{"jsonrpc":"2.0","id":"parity","method":"`+test.Method+`","wire":`+schemaListWire+`,"params":`+test.ParamsJSON+`}`))
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
