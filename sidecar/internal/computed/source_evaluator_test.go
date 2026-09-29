package computed

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestSourceEvaluatorSharesExpectationAcrossRows(t *testing.T) {
	app := computedTestApp(t)
	field := v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: "fld_minute", PhysicalName: "minute"}, LogicalType: v2.LogicalFormula,
		Formula: &v2.FormulaSpec{Source: "NOW()", Language: "cel-v1", ResultType: v2.LogicalDateTime}}
	fields := []v2.FieldDefinition{field}
	definition := schemaexecution.Table{Snapshot: v2.SchemaSnapshot{TableID: "tbl_clock", Fields: fields}}
	metadata, err := app.FindCollectionByNameOrId("vibetable_formulas")
	if err != nil {
		t.Fatal(err)
	}
	formulaRecord := core.NewRecord(metadata)
	for name, value := range map[string]any{"table_id": "tbl_clock", "field_id": "fld_minute", "source": "NOW()", "language": "cel-v1", "result_type": "dateTime", "version": 1, "status": "ready"} {
		formulaRecord.Set(name, value)
	}
	if err := app.Save(formulaRecord); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2024, 3, 10, 6, 59, 0, 0, time.UTC)
	at := func(instant time.Time) context.Context {
		return relatedcomputation.WithClockCache(formula.WithEvaluationTime(context.Background(), instant))
	}
	ctx := at(t0)
	rows := []*core.Record{core.NewRecord(core.NewBaseCollection("rows")), core.NewRecord(core.NewBaseCollection("rows"))}
	store := func(ctx context.Context) {
		t.Helper()
		for i, row := range rows {
			revision := int64(i + 1)
			row.Set(relatedcomputation.RowRevisionField, revision)
			expectation, err := relatedcomputation.ExpectationFor(ctx, app, "tbl_clock", fields, field.Identity.FieldID, revision)
			if err != nil {
				t.Fatal(err)
			}
			row.Set("minute", relatedcomputation.Ready("fresh", relatedcomputation.CellVersion{
				DefinitionVersion: expectation.DefinitionVersion, SourceDataRevision: revision, DependencyWatermark: expectation.DependencyWatermark}))
		}
	}
	store(ctx)
	var versionReads atomic.Int64
	seen := map[*dbx.DB]bool{}
	for _, database := range []*dbx.DB{app.ConcurrentDB().(*dbx.DB), app.NonconcurrentDB().(*dbx.DB)} {
		if seen[database] {
			continue
		}
		seen[database] = true
		previous := database.QueryLogFunc
		database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
			if strings.Contains(statement, "vibetable_formulas") {
				versionReads.Add(1)
			}
			if previous != nil {
				previous(ctx, elapsed, statement, rows, err)
			}
		}
		defer func() { database.QueryLogFunc = previous }()
	}
	readRows := func(ctx context.Context, evaluator *sourceEvaluator) {
		t.Helper()
		versionReads.Store(0)
		for _, row := range rows {
			value, err := evaluator.read(ctx, app, "tbl_clock", fields, field, row)
			if err != nil || value != "fresh" {
				t.Fatalf("source value=%v error=%v", value, err)
			}
		}
		if versionReads.Load() != 1 {
			t.Fatalf("one field in one batch read its definition %d times, want 1", versionReads.Load())
		}
	}
	evaluator := newSourceEvaluator(New(), definition)
	readRows(ctx, evaluator)
	// A different row revision must still be checked after the field cache hits.
	rows[1].Set(relatedcomputation.RowRevisionField, 3)
	if _, err := evaluator.reader.Read(ctx, app, "tbl_clock", fields, field, rows[1]); err == nil {
		t.Fatal("cached expectation ignored row revision")
	}
	// A changed instant cannot reuse the earlier clock signature.
	next := at(t0.Add(time.Minute))
	if _, err := evaluator.reader.Read(next, app, "tbl_clock", fields, field, rows[0]); err == nil {
		t.Fatal("cached expectation ignored clock period")
	}
	store(next)
	// Use another instant in that minute to exercise a new cache key once.
	readRows(at(t0.Add(90*time.Second)), evaluator)
	// A new batch sees a changed definition, even at the same evaluation instant.
	formulaRecord.Set("version", 2)
	if err := app.Save(formulaRecord); err != nil {
		t.Fatal(err)
	}
	freshBatch := newSourceEvaluator(New(), definition)
	if _, err := freshBatch.reader.Read(next, app, "tbl_clock", fields, field, rows[0]); err == nil {
		t.Fatal("new batch ignored changed definition")
	}
	store(next)
	readRows(next, newSourceEvaluator(New(), definition))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	versionReads.Store(0)
	if _, err := evaluator.read(cancelled, app, "tbl_clock", fields, field, rows[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled source=%v", err)
	}
	if _, err := evaluator.reader.Expectation(cancelled, app, "tbl_clock", fields, field, rows[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled expectation=%v", err)
	}
	if versionReads.Load() != 0 {
		t.Fatal("cancelled source queried metadata")
	}
}

func computedTestApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.OnTerminate().Trigger(&core.TerminateEvent{App: app}, func(event *core.TerminateEvent) error { return event.App.ResetBootstrapState() }); err != nil {
			t.Error(err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}
