package metadata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPresetFrozenPythonPublicDTO(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "preset-python-oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string
		Cases    []struct {
			Name, Method   string
			Params         json.RawMessage
			Responses      []struct{ Error *struct{ Code int } }
			TransportCalls []struct {
				Operation string
				Request   struct{ Payload map[string]any }
			}
		}
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "146a9c2cac5998ee013daebc78eedff0bd4a7ca5" || len(corpus.Cases) != 53 {
		t.Fatal("invalid frozen producer/corpus")
	}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			request, err := DecodePresetRequest(c.Method, c.Params)
			invalid := c.Responses[0].Error != nil && c.Responses[0].Error.Code == -32602
			if (err != nil) != invalid {
				t.Fatalf("decode error=%v, original invalid=%v", err, invalid)
			}
			if invalid {
				return
			}
			for _, call := range c.TransportCalls {
				if call.Operation == "upsert" {
					a, _ := json.Marshal(request.View)
					b, _ := json.Marshal(call.Request.Payload["view"])
					var got, want any
					_ = json.Unmarshal(a, &got)
					_ = json.Unmarshal(b, &want)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("normalized view=%s want original=%s", a, b)
					}
					break
				}
			}
		})
	}
}
