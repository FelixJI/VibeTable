// Package computationplan validates mixed Formula/Lookup dependencies without
// owning values, persistence, or the execution runtime.
package computationplan

import (
	"context"
	"sort"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// Validate overlays candidate on a caller-owned consistent schema snapshot. Only
// reachable tables are resolved, once each. Callers must rebuild inside the
// authority transaction before applying a previously inspected candidate.
func Validate(ctx context.Context, candidate schemaexecution.Table, resolve func(context.Context, string) (schemaexecution.Table, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	candidate = activeTable(candidate)
	compiler := formula.NewCompiler(formula.DefaultLimits())
	formulas, err := compiler.CompileExecutionTable(candidate)
	if err != nil {
		return err
	}
	builder := graphBuilder{
		ctx: ctx, resolve: resolve, compiler: compiler,
		tables:   map[string]schemaexecution.Table{candidate.Snapshot.TableID: candidate},
		compiled: map[node]*formula.CompiledFormula{}, state: map[node]uint8{},
	}
	for _, compiled := range formulas.Formulas {
		builder.compiled[node{candidate.Snapshot.TableID, compiled.FieldID}] = compiled
	}
	fields := append([]v2.FieldDefinition(nil), candidate.Snapshot.Fields...)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Identity.FieldID < fields[j].Identity.FieldID })
	for _, field := range fields {
		if err := builder.visit(node{candidate.Snapshot.TableID, field.Identity.FieldID}); err != nil {
			return err
		}
	}
	return nil
}

type node struct{ tableID, fieldID string }
type graphBuilder struct {
	ctx      context.Context
	resolve  func(context.Context, string) (schemaexecution.Table, error)
	compiler *formula.Compiler
	tables   map[string]schemaexecution.Table
	compiled map[node]*formula.CompiledFormula
	state    map[node]uint8
	stack    []node
}

func activeTable(table schemaexecution.Table) schemaexecution.Table {
	fields := make([]v2.FieldDefinition, 0, len(table.Snapshot.Fields))
	for _, field := range table.Snapshot.Fields {
		if field.Lifecycle.State != v2.LifecycleRetired {
			fields = append(fields, field)
		}
	}
	table.Snapshot.Fields = fields
	return table
}

func (builder *graphBuilder) table(id string) (schemaexecution.Table, error) {
	if err := builder.ctx.Err(); err != nil {
		return schemaexecution.Table{}, err
	}
	if table, found := builder.tables[id]; found {
		return table, nil
	}
	table, err := builder.resolve(builder.ctx, id)
	if err != nil {
		return schemaexecution.Table{}, err
	}
	if table.Snapshot.TableID != id {
		return schemaexecution.Table{}, graphError("schema.computation.schema_invalid", "resolved schema table identity does not match", nil)
	}
	table = activeTable(table)
	builder.tables[id] = table
	return table, nil
}

func (builder *graphBuilder) visit(key node) error {
	if err := builder.ctx.Err(); err != nil {
		return err
	}
	if builder.state[key] == 2 {
		return nil
	}
	if builder.state[key] == 1 {
		start := 0
		for builder.stack[start] != key {
			start++
		}
		cycle := make([]map[string]string, 0, len(builder.stack)-start+1)
		for _, item := range append(append([]node(nil), builder.stack[start:]...), key) {
			cycle = append(cycle, map[string]string{"tableId": item.tableID, "fieldId": item.fieldID})
		}
		return graphError("schema.computation.cycle", "computed field dependency cycle detected", map[string]any{"cycle": cycle})
	}
	table, err := builder.table(key.tableID)
	if err != nil {
		return err
	}
	field, found := table.Field(key.fieldID)
	if !found {
		return graphError("schema.computation.target_not_found", "computed dependency field is unavailable", map[string]any{"tableId": key.tableID, "fieldId": key.fieldID})
	}
	if field.LogicalType != v2.LogicalFormula && field.LogicalType != v2.LogicalLookup {
		return nil
	}
	builder.state[key] = 1
	builder.stack = append(builder.stack, key)
	var dependencies []node
	if field.LogicalType == v2.LogicalFormula {
		dependencies, err = builder.formulaDependencies(key, table, field)
	} else {
		dependencies, err = builder.lookupDependencies(key, table, field)
	}
	if err != nil {
		return err
	}
	for _, dependency := range dependencies {
		if err := builder.visit(dependency); err != nil {
			return err
		}
	}
	builder.stack = builder.stack[:len(builder.stack)-1]
	builder.state[key] = 2
	return nil
}

func (builder *graphBuilder) formulaDependencies(key node, table schemaexecution.Table, field v2.FieldDefinition) ([]node, error) {
	compiled := builder.compiled[key]
	if compiled == nil {
		var err *formula.Error
		compiled, err = builder.compiler.Compile(table, field)
		if err != nil {
			return nil, err
		}
		builder.compiled[key] = compiled
	}
	dependencies := make([]node, 0, len(compiled.Dependencies))
	for _, id := range compiled.Dependencies {
		dependencies = append(dependencies, node{key.tableID, id})
	}
	// COUNT(relation) depends on relation membership only, not on any target value.
	references := append(append([]string(nil), compiled.ReferencePaths...), compiled.RelationAggregatePaths...)
	seen := map[string]bool{}
	for _, reference := range references {
		if seen[reference] {
			continue
		}
		seen[reference] = true
		parts := strings.Split(reference, ".")
		if len(parts) != 2 {
			return nil, graphError("schema.formula.relation_path_invalid", "relation formula paths must select one target field", map[string]any{"reference": reference})
		}
		relation, found := table.Field(parts[0])
		if !found || relation.Relation == nil || relation.LogicalType != v2.LogicalRelation {
			continue
		}
		target, err := builder.table(relation.Relation.TargetTableID)
		if err != nil {
			return nil, err
		}
		targetField, found := target.Field(parts[1])
		if !found || targetField.LogicalType == v2.LogicalRelation {
			return nil, graphError("schema.formula.target_field_not_found", "formula relation target field was not found", map[string]any{"reference": reference})
		}
		dependencies = append(dependencies, node{target.Snapshot.TableID, targetField.Identity.FieldID})
	}
	return dependencies, nil
}

func (builder *graphBuilder) lookupDependencies(key node, table schemaexecution.Table, field v2.FieldDefinition) ([]node, error) {
	if field.Lookup == nil || len(field.Lookup.Path) == 0 {
		return nil, graphError("schema.lookup.path_invalid", "lookup dependency path is unavailable", nil)
	}
	current := table
	for index, step := range field.Lookup.Path {
		if err := builder.ctx.Err(); err != nil {
			return nil, err
		}
		relation, found := current.Field(step.RelationFieldID)
		if !found || relation.LogicalType != v2.LogicalRelation || relation.Relation == nil {
			return nil, graphError("schema.lookup.relation_not_found", "lookup path relation field was not found", map[string]any{"pathIndex": index})
		}
		target, err := builder.table(relation.Relation.TargetTableID)
		if err != nil {
			return nil, err
		}
		current = target
	}
	target, found := current.Field(field.Lookup.TargetFieldID)
	if !found || target.LogicalType == v2.LogicalRelation {
		return nil, graphError("schema.lookup.target_field_not_found", "lookup target field was not found", nil)
	}
	return []node{{current.Snapshot.TableID, target.Identity.FieldID}}, nil
}

func graphError(code, message string, details map[string]any) *schemaerror.ProductError {
	return &schemaerror.ProductError{Code: code, Path: "definition.fields", Message: message, Details: details}
}
