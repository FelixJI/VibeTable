package pluginmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

// progressSequenceApp serves the sequence range of a progress read like the
// plugin event index does: only rows with a higher sequence are candidates.
type progressSequenceApp struct {
	*progressHistoryApp
	ranged int
}

func (app *progressSequenceApp) FindRecordsByFilter(_ any, filter, _ string, limit, offset int, params ...dbx.Params) ([]*core.Record, error) {
	records := app.records
	if strings.Contains(filter, "seq>{:after}") {
		app.ranged++
		after, _ := params[0]["after"].(int64)
		records = nil
		for _, record := range app.records {
			if int64(record.GetInt("seq")) > after {
				records = append(records, record)
			}
		}
	}
	end := min(offset+limit, len(records))
	return records[offset:end], nil
}

func TestHistoryAndStatesAfterValidateEveryRecordExactlyOnce(t *testing.T) {
	collection := core.NewBaseCollection("vibetable_plugin_records")
	collection.Fields.Add(&core.JSONField{Name: "payload_json"})
	collection.Fields.Add(&core.NumberField{Name: "seq"})
	base := &progressHistoryApp{collection: collection}
	app := &progressSequenceApp{progressHistoryApp: base}
	append := func(seq int, key string, status string) {
		state := State{Contract: Contract, RequestID: fmt.Sprint(seq), Status: status, Bindings: map[string]Binding{"items": {TableID: "table_" + fmt.Sprint(seq), Fields: map[string]string{"name": "field"}}}}
		raw, err := json.Marshal(map[string]any{"details": state})
		if err != nil {
			t.Fatal(err)
		}
		row := core.NewRecord(collection)
		row.SetRaw("payload_json", types.JSONRaw(raw))
		row.Set("seq", seq)
		app.records = append(app.records, row)
	}
	// Step keys, a failed attempt and a completed state whose key predates the
	// ":completed" convention: none of them may depend on key spelling.
	for seq := 1; seq <= 300; seq++ {
		switch {
		case seq == 7:
			append(seq, "legacy-request:step:6", "partial")
		case seq == 150:
			append(seq, "legacy-request:failed:old", "failed")
		case seq == 300:
			append(seq, "legacy-request", "completed")
		default:
			append(seq, fmt.Sprintf("legacy-request:step:%d", seq-1), "partial")
		}
	}
	history, err := History(t.Context(), app, "project", "plugin")
	if err != nil || len(history) != 300 {
		t.Fatalf("history lost pages: count=%d err=%v", len(history), err)
	}
	for i, progress := range history {
		if progress.Seq != int64(i+1) || progress.State.RequestID != fmt.Sprint(i+1) {
			t.Fatalf("history order or sequence changed at %d: %+v", i, progress)
		}
	}
	states, err := States(t.Context(), app, "project", "plugin")
	if err != nil || len(states) != 300 || states[299].Status != "completed" {
		t.Fatalf("states differ from history: count=%d err=%v", len(states), err)
	}
	if app.ranged != 0 {
		t.Fatalf("full reads used the sequence range %d times", app.ranged)
	}

	// A cursor that validated everything up to sequence 294 only reads 295..300
	// and still sees the completed state, whatever its key looks like.
	newer, err := StatesAfter(t.Context(), app, "project", "plugin", 294)
	if err != nil || len(newer) != 6 || newer[0].Seq != 295 || newer[5].Seq != 300 || newer[5].State.Status != "completed" {
		t.Fatalf("range read differs: %+v err=%v", newer, err)
	}
	if newer, err := StatesAfter(t.Context(), app, "project", "plugin", 300); err != nil || len(newer) != 0 || newer == nil {
		t.Fatalf("empty range must stay an empty, non-nil slice: %+v err=%v", newer, err)
	}
	var protectedError *schemaerror.ProductError
	if err := ProtectField(t.Context(), app, "table_1", "field", &v2.FieldDefinition{}, nil); !errors.As(err, &protectedError) {
		t.Fatalf("historical binding lost protection: %v", err)
	}

	// Corruption inside the unread range fails the read exactly like History.
	app.records[297].SetRaw("payload_json", types.JSONRaw(`{"details":{"contract":"invalid"}}`))
	if progress, err := StatesAfter(t.Context(), app, "project", "plugin", 294); err == nil || progress != nil {
		t.Fatal("range read skipped a corrupt newer record")
	}
	if _, err := History(t.Context(), app, "project", "plugin"); err == nil {
		t.Fatal("history read ignored corruption")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := StatesAfter(cancelled, app, "project", "plugin", 294); !errors.Is(context.Canceled, err) {
		t.Fatalf("cancelled range read returned %v", err)
	}
}
