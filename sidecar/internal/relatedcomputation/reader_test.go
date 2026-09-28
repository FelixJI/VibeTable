package relatedcomputation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestSourceReaderUnwrapsFreshAndRejectsStaleCells(t *testing.T) {
	app := computationTestApp(t)
	minute := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_minute", PhysicalName: "minute"},
		LogicalType: v2.LogicalFormula,
		Formula:     &v2.FormulaSpec{Source: "NOW()", Language: "cel-v1", ResultType: v2.LogicalDateTime},
	}
	fields := []v2.FieldDefinition{minute}
	saveInternalRecord(t, app, "vibetable_formulas", map[string]any{
		"table_id": "tbl_clock", "field_id": "fld_minute", "source": "NOW()",
		"language": "cel-v1", "result_type": "dateTime", "version": 1, "status": "ready",
	})
	t0 := time.Date(2024, 3, 10, 6, 59, 0, 0, time.UTC)
	ctxAt := func(at time.Time) context.Context {
		return WithClockCache(formula.WithEvaluationTime(context.Background(), at))
	}
	expectationAt := func(at time.Time) Expectation {
		expectation, err := ExpectationFor(ctxAt(at), app, "tbl_clock", fields, "fld_minute", 5)
		if err != nil {
			t.Fatalf("expectation at %v: %v", at, err)
		}
		return expectation
	}
	record := core.NewRecord(core.NewBaseCollection("rows"))
	record.Set(RowRevisionField, 5)
	storeEnvelope := func(value string, expectation Expectation) {
		raw, err := json.Marshal(Ready(value, CellVersion{
			DefinitionVersion:   expectation.DefinitionVersion,
			SourceDataRevision:  expectation.SourceDataRevision,
			DependencyWatermark: expectation.DependencyWatermark,
		}))
		if err != nil {
			t.Fatal(err)
		}
		var stored map[string]any
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		record.Set("minute", stored)
	}
	staleRejection := func(name string, err error) {
		t.Helper()
		var formulaErr *formula.Error
		if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.dependency" {
			t.Fatalf("%s error = %#v, want formula.dependency", name, err)
		}
	}
	reader := NewSourceReader()

	storeEnvelope("06:59", expectationAt(t0))
	if value, err := reader.Read(ctxAt(t0), app, "tbl_clock", fields, minute, record); err != nil || value != "06:59" {
		t.Fatalf("fresh source = %#v, %v", value, err)
	}
	// One batch reuses its graph and source expectation; cancellation still
	// wins over the cached value.
	batch := EnsureClockCache(formula.WithEvaluationTime(context.Background(), t0))
	if EnsureClockCache(batch) != batch {
		t.Fatal("existing batch cache was replaced")
	}
	if value, err := reader.Read(batch, app, "tbl_clock", fields, minute, record); err != nil || value != "06:59" {
		t.Fatalf("cached batch source = %#v, %v", value, err)
	}
	cancelled, cancel := context.WithCancel(batch)
	cancel()
	if _, err := reader.Read(cancelled, app, "tbl_clock", fields, minute, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cached read = %v", err)
	}
	// The same clock period keeps serving the stored scalar.
	if value, err := reader.Read(ctxAt(t0.Add(30*time.Second)), app, "tbl_clock", fields, minute, record); err != nil || value != "06:59" {
		t.Fatalf("same-period source = %#v, %v", value, err)
	}
	// The next minute rejects the unrefreshed source explicitly.
	_, err := reader.Read(ctxAt(t0.Add(time.Minute)), app, "tbl_clock", fields, minute, record)
	staleRejection("next period", err)
	// A newer downstream row revision must not bless an older envelope.
	record.Set(RowRevisionField, 6)
	_, err = reader.Read(ctxAt(t0), app, "tbl_clock", fields, minute, record)
	staleRejection("row revision drift", err)
	record.Set(RowRevisionField, 5)
	// A cell without a readable envelope fails closed instead of yielding null.
	record.Set("minute", "plain stored text")
	_, err = reader.Read(ctxAt(t0), app, "tbl_clock", fields, minute, record)
	staleRejection("missing envelope", err)
	// Once the source is refreshed for the new period, its scalar is served.
	next := expectationAt(t0.Add(time.Minute))
	storeEnvelope("07:00", Expectation{
		DefinitionVersion:   next.DefinitionVersion,
		SourceDataRevision:  5,
		DependencyWatermark: next.DependencyWatermark,
	})
	if value, err := reader.Read(ctxAt(t0.Add(time.Minute)), app, "tbl_clock", fields, minute, record); err != nil || value != "07:00" {
		t.Fatalf("refreshed source = %#v, %v", value, err)
	}

	// Schema-declared non-computed fields keep their stored value untouched,
	// even when the stored JSON is shaped exactly like an envelope.
	payload := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_payload", PhysicalName: "payload"},
		LogicalType: v2.LogicalJSON,
	}
	lookalike := map[string]any{
		"state": "ready", "value": "imposter",
		"version": map[string]any{
			"definitionVersion":   1,
			"sourceDataRevision":  1,
			"dependencyWatermark": "sha256:00",
		},
	}
	if _, ok := Decode(lookalike); !ok {
		t.Fatal("fixture must decode as an envelope lookalike")
	}
	record.Set("payload", lookalike)
	value, err := reader.Read(ctxAt(t0), app, "tbl_clock", []v2.FieldDefinition{payload}, payload, record)
	if err != nil || !reflect.DeepEqual(value, lookalike) {
		t.Fatalf("user JSON lookalike = %#v, %v", value, err)
	}
}
