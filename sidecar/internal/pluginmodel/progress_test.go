package pluginmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func legacyProgressDecode(payload any) (State, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return State{}, err
	}
	var event struct {
		Details State `json:"details"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return State{}, err
	}
	return event.Details, nil
}

func TestProgressDecodePreservesLegacyValuesAndErrors(t *testing.T) {
	valid := `{"details":{"contract":"vibetable.plugin-model.v1","requestId":"模型","installationRevision":9007199254740991,"model":{"entities":[{"key":"items","fields":[{"key":"name","type":"text"}]}]},"bindings":{"items":{"tableId":"old","fields":{"name":"field"}}},"diagnostics":[{"code":"x","details":{"number":1.5,"null":null,"list":[true,"é"]}}]}}`
	tests := []struct {
		name    string
		payload any
	}{
		{"record", types.JSONRaw(valid)},
		{"whitespace", types.JSONRaw(" \n" + valid + "\n ")},
		{"case", types.JSONRaw(`{"DETAILS":{"CONTRACT":"vibetable.plugin-model.v1","REQUESTID":"case"}}`)},
		{"duplicates", types.JSONRaw(`{"details":{"contract":"vibetable.plugin-model.v1","bindings":{"first":{"tableId":"a"}}},"details":{"requestId":"last","bindings":{"second":{"tableId":"b"}}}}`)},
		{"null_merge", types.JSONRaw(`{"details":{"contract":"vibetable.plugin-model.v1","completed":3},"details":{"completed":null}}`)},
		{"surrogate", types.JSONRaw(`{"details":{"contract":"vibetable.plugin-model.v1","requestId":"\ud800"}}`)},
		{"invalid_utf8", types.JSONRaw(append(append([]byte(`{"details":{"contract":"vibetable.plugin-model.v1","requestId":"`), 0xff), []byte(`"}}`)...))},
		{"unknown", types.JSONRaw(`{"unused":{"nested":[{},[1,true,null]]},"details":{"contract":"vibetable.plugin-model.v1"}}`)},
		{"wrong_type", types.JSONRaw(`{"details":{"contract":"vibetable.plugin-model.v1","completed":"bad"}}`)},
		{"early_type_error", types.JSONRaw(`{"details":{"completed":"bad","requestId":"partial"},"details":{"completed":3,"contract":"vibetable.plugin-model.v1"}}`)},
		{"overflow", types.JSONRaw(`{"details":{"contract":"vibetable.plugin-model.v1","completed":1e100}}`)},
		{"fractional_integer", types.JSONRaw(`{"details":{"contract":"vibetable.plugin-model.v1","completed":1.5}}`)},
		{"trailing", types.JSONRaw(valid + ` {}`)},
		{"truncated", types.JSONRaw(valid[:len(valid)-1])},
		{"empty", types.JSONRaw(nil)},
		{"message", json.RawMessage(valid)},
		{"map", map[string]any{"details": map[string]any{"contract": Contract, "completed": 3}}},
		{"nil", nil},
		{"string", valid},
		{"bytes", []byte(valid)},
		{"unsupported", make(chan int)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, wantErr := legacyProgressDecode(tt.payload)
			got, gotErr := decodeProgress(tt.payload)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("decoded value differs from legacy: got=%+v want=%+v", got, want)
			}
			if reflect.TypeOf(gotErr) != reflect.TypeOf(wantErr) {
				t.Fatalf("error type differs: got=%T want=%T", gotErr, wantErr)
			}
			if gotErr != nil && gotErr.Error() != wantErr.Error() {
				t.Fatalf("error differs: got=%v want=%v", gotErr, wantErr)
			}
		})
	}
}

type progressHistoryApp struct {
	core.App
	collection *core.Collection
	records    []*core.Record
}

func (app *progressHistoryApp) FindCollectionByNameOrId(string) (*core.Collection, error) {
	return app.collection, nil
}

func (app *progressHistoryApp) FindRecordsByFilter(_ any, _ string, _ string, limit, offset int, _ ...dbx.Params) ([]*core.Record, error) {
	end := min(offset+limit, len(app.records))
	return app.records[offset:end], nil
}

func TestStatesPreservesAllPagesAndHistoricalProtection(t *testing.T) {
	collection := core.NewBaseCollection("vibetable_plugin_records")
	collection.Fields.Add(&core.JSONField{Name: "payload_json"})
	app := &progressHistoryApp{collection: collection}
	for i := range 258 {
		state := State{Contract: Contract, RequestID: fmt.Sprint(i), Status: "completed", Bindings: map[string]Binding{"items": {TableID: "current", Fields: map[string]string{"name": "current_field"}}}}
		if i == 0 {
			state.Bindings["items"] = Binding{TableID: "historical", Fields: map[string]string{"name": "historical_field"}}
		}
		raw, err := json.Marshal(map[string]any{"details": state})
		if err != nil {
			t.Fatal(err)
		}
		row := core.NewRecord(collection)
		row.SetRaw("payload_json", types.JSONRaw(raw))
		app.records = append(app.records, row)
	}
	states, err := States(t.Context(), app, "project", "plugin")
	if err != nil || len(states) != len(app.records) {
		t.Fatalf("history lost pages: count=%d err=%v", len(states), err)
	}
	for i, state := range states {
		if state.RequestID != fmt.Sprint(i) {
			t.Fatalf("history order changed at %d", i)
		}
	}
	var protectedError *schemaerror.ProductError
	if err := ProtectTable(t.Context(), app, "historical"); !errors.As(err, &protectedError) || protectedError.Code != "plugin.model.core_protected" {
		t.Fatalf("old table binding lost protection: %v", err)
	}
	if err := ProtectField(t.Context(), app, "historical", "historical_field", &v2.FieldDefinition{}, nil); !errors.As(err, &protectedError) {
		t.Fatalf("old field binding lost protection: %v", err)
	}
	app.records[0].SetRaw("payload_json", types.JSONRaw(`{"details":{"contract":"invalid"}}`))
	if states, err := States(t.Context(), app, "project", "plugin"); err == nil || states != nil {
		t.Fatal("valid newer events hid invalid older history")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := States(cancelled, app, "project", "plugin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled history read returned %v", err)
	}
}
