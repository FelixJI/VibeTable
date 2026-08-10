package mutation_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

func TestProductMutationFixturesDecodeAndRoundTripExactly(t *testing.T) {
	cases := []struct {
		name string
		file string
		new  func() any
	}{
		{"request", "mutation-request.json", func() any { return &mutation.Request{} }},
		{"receipt", "mutation-receipt.json", func() any { return &mutation.Receipt{} }},
		{"event", "data-changed-event.json", func() any { return &mutation.DataChangedEvent{} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", test.file))
			if err != nil {
				t.Fatal(err)
			}
			value := test.new()
			if err := mutation.DecodeStrict(raw, value); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			_ = json.Unmarshal(raw, &before)
			_ = json.Unmarshal(encoded, &after)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("semantic roundtrip changed\nbefore=%s\nafter=%s", raw, encoded)
			}
		})
	}
}

func TestMutationRequestRejectsUnknownFieldsAndOperationShapes(t *testing.T) {
	base := `{
		"contractVersion":"2.0","requestId":"req","idempotencyKey":"idem",
		"tableId":"table","schemaRevision":"schema_0001",
		"operations":[{"kind":"update","recordId":"rec","values":{}}],
		"actor":{"type":"user","id":"local","displayName":null},
		"expectedRevision":null,"expectedDigest":null
	}`
	for _, malformed := range []string{
		strings.Replace(base, `"requestId":"req"`, `"requestId":"req","provider":"pb"`, 1),
		strings.Replace(base, `"values":{}`, `"values":{},"uploadHandles":[]`, 1),
		strings.Replace(base, `"kind":"update"`, `"kind":"upsert"`, 1),
	} {
		var request mutation.Request
		if err := mutation.DecodeStrict([]byte(malformed), &request); err == nil {
			t.Fatalf("malformed request decoded: %s", malformed)
		}
	}
}

func TestMutationOperationAcceptsOptionalRawValuesAndRoundTripsThem(t *testing.T) {
	raw := []byte(`{
		"kind":"update","recordId":"rec","values":{},
		"rawValues":{"amount":"$1,234.50","approved":"否"}
	}`)
	var operation mutation.Operation
	if err := mutation.DecodeStrict(raw, &operation); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(operation.RawValues, map[string]any{
		"amount": "$1,234.50", "approved": "否",
	}) {
		t.Fatalf("raw values = %#v", operation.RawValues)
	}
	encoded, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	_ = json.Unmarshal(raw, &before)
	_ = json.Unmarshal(encoded, &after)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("semantic roundtrip changed\nbefore=%s\nafter=%s", raw, encoded)
	}
}

func TestMutationOperationRejectsExplicitNullRawValues(t *testing.T) {
	raw := []byte(`{
		"kind":"insert","recordId":null,"values":{},"rawValues":null
	}`)
	var operation mutation.Operation
	if err := mutation.DecodeStrict(raw, &operation); err == nil {
		t.Fatal("explicit null rawValues decoded")
	}
}
