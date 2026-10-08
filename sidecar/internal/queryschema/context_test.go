package queryschema

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// Inject a storage failure at the PocketBase boundary, keeping the production
// descriptor and relatedcomputation expectation path intact.
type versionFailureApp struct {
	core.App
	cause error
}

func (app versionFailureApp) FindFirstRecordByFilter(collection any, filter string, params ...dbx.Params) (*core.Record, error) {
	if collection == "vibetable_formulas" {
		return nil, fmt.Errorf("formula metadata query: %w", app.cause)
	}
	return app.App.FindFirstRecordByFilter(collection, filter, params...)
}

func TestResolvedDescriptorPreservesComputedVersionContextFailures(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Retained logs are auxiliary asynchronous writes with no cleanup join point.
	app.Settings().Logs.MaxDays = 0
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	formulas := core.NewBaseCollection("vibetable_formulas")
	formulas.Fields.Add(&core.TextField{Name: "table_id"}, &core.TextField{Name: "field_id"}, &core.NumberField{Name: "version"})
	if err := app.Save(formulas); err != nil {
		t.Fatal(err)
	}
	source, err := New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "fld_total", PhysicalName: "f_total"},
		LogicalType: v2.LogicalFormula,
		Formula:     &v2.FormulaSpec{Language: "cel-v1", Source: "1.0", ResultType: v2.LogicalNumber},
	}
	table := schemaexecution.Table{
		Snapshot:       v2.SchemaSnapshot{TableID: "tbl_orders", Fields: []v2.FieldDefinition{field}},
		FormulaRuntime: map[string]schemaexecution.FormulaRuntime{"fld_total": {Status: "ready", Version: 1}},
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			_, _, err := source.DescribeResolvedSelectionTable(context.Background(), versionFailureApp{App: app, cause: cause}, table)
			if !errors.Is(err, cause) {
				t.Fatalf("computed metadata context error = %v; want %v", err, cause)
			}
		})
	}
	t.Run("missing version stays a closed metadata failure", func(t *testing.T) {
		_, _, err := source.DescribeResolvedSelectionTable(context.Background(), app, table)
		var productErr *query.ProductError
		if !errors.As(err, &productErr) || productErr.Code != "query.computed.version_unavailable" || productErr.Path != "fields.f_total" {
			t.Fatalf("missing computed version error = %#v", err)
		}
	})
}
