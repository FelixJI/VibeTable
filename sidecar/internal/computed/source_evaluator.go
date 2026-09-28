package computed

import (
	"context"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/computationplan"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type exactSelectionKey struct{}

func exactSelection(ctx context.Context) bool {
	value, _ := ctx.Value(exactSelectionKey{}).(bool)
	return value
}

type sourceCell struct{ table, row, field string }

// One evaluator belongs to one authoritative calculation transaction. It only
// computes requested stale cells in memory; storage and user history stay with
// the outer materializer. Query and ordinary mutation readers never use it.
type sourceEvaluator struct {
	composite *Composite
	reader    *relatedcomputation.SourceReader
	visiting  map[sourceCell]bool
	values    map[sourceCell]any
	tables    map[string]schemaexecution.Table
	orders    map[computationplan.FieldReference][]computationplan.FieldReference
}

func newSourceEvaluator(composite *Composite) *sourceEvaluator {
	return &sourceEvaluator{composite: composite, reader: relatedcomputation.NewSourceReader(),
		visiting: map[sourceCell]bool{}, values: map[sourceCell]any{},
		tables: map[string]schemaexecution.Table{}, orders: map[computationplan.FieldReference][]computationplan.FieldReference{}}
}

func (evaluator *sourceEvaluator) read(ctx context.Context, app core.App, tableID string, fields []v2.FieldDefinition, field v2.FieldDefinition, record *core.Record) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Validate the authoritative expectation before deciding whether a missing
	// or stale cache can be recomputed. Metadata failures never become fallbacks.
	expectation, err := relatedcomputation.ExpectationFor(ctx, app, tableID, fields, field.Identity.FieldID, int64(record.GetInt(relatedcomputation.RowRevisionField)))
	if err != nil {
		return nil, err
	}
	if envelope, valid := relatedcomputation.Decode(record.GetRaw(field.Identity.PhysicalName)); valid && envelope.Fresh(expectation) {
		return evaluator.reader.Read(ctx, app, tableID, fields, field, record)
	}
	key := sourceCell{tableID, record.Id, field.Identity.FieldID}
	if value, found := evaluator.values[key]; found {
		return value, nil
	}
	if evaluator.visiting[key] {
		return nil, &formula.Error{ContractVersion: formula.ContractVersion, Code: "formula.dependency", Message: "computed source dependency cycle"}
	}
	if len(evaluator.visiting) >= formula.DefaultRecursionLimit {
		return nil, &formula.Error{ContractVersion: formula.ContractVersion, Code: "formula.resource_limit", Message: "computed source exceeds the recursion limit"}
	}
	ctx, err = formula.ChargeSourceEvaluation(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, formula.DefaultEvalTimeout)
	defer cancel()
	evaluator.visiting[key] = true
	defer delete(evaluator.visiting, key)
	resolve := func(ctx context.Context, id string) (schemaexecution.Table, error) {
		if table, found := evaluator.tables[id]; found {
			return table, nil
		}
		table, err := schemaexecution.Describe(ctx, app, id)
		if err == nil {
			evaluator.tables[id] = table
		}
		return table, err
	}
	table, err := resolve(ctx, tableID)
	if err != nil {
		return nil, err
	}
	root := computationplan.FieldReference{TableID: tableID, FieldID: field.Identity.FieldID}
	order, found := evaluator.orders[root]
	if !found {
		order, err = computationplan.OrderedDependencies(ctx, table, field.Identity.FieldID, resolve)
		if err != nil {
			return nil, err
		}
		evaluator.orders[root] = order
	}
	// TABLE readers project a subset of columns. Reload a complete row before
	// evaluating any of its formulas, always through the caller's transaction.
	loaded, err := app.FindRecordById(table.PhysicalName, record.Id)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{field.Identity.FieldID: true}
	for _, dependency := range order {
		if dependency.TableID != tableID || dependency.FieldID == field.Identity.FieldID {
			continue
		}
		input, found := table.Field(dependency.FieldID)
		if !found {
			return nil, fmt.Errorf("computed source field %s is unavailable", dependency.FieldID)
		}
		value, err := evaluator.read(ctx, app, tableID, table.Snapshot.Fields, input, loaded)
		if err != nil {
			return nil, err
		}
		loaded.Set(input.Identity.PhysicalName, value)
	}
	ctx = context.WithValue(ctx, exactSelectionKey{}, true)
	ctx = formula.WithEvaluationFields(ctx, selected)
	ctx = formula.WithComputedSourceReader(ctx, evaluator.read)
	values, err := evaluator.composite.Calculate(ctx, app, table, loaded)
	if err != nil {
		return nil, err
	}
	value, found := values[field.Identity.PhysicalName]
	if !found {
		return nil, fmt.Errorf("computed source field %s was not calculated", field.Identity.FieldID)
	}
	if _, err := formula.RetainSourceValue(ctx, key.table+"/"+key.row+"/"+key.field); err != nil {
		return nil, err
	}
	value, err = formula.RetainSourceValue(ctx, value)
	if err != nil {
		return nil, err
	}
	evaluator.values[key] = value
	return value, nil
}
