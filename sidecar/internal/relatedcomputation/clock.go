package relatedcomputation

import (
	"context"
	"fmt"
	"sync"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type clockCacheKey struct{}
type clockFieldKey struct{ table, field string }
type clockCache struct {
	mu         sync.Mutex
	fields     map[string][]v2.FieldDefinition
	references map[clockFieldKey][]formula.ClockReference
	tables     map[clockFieldKey][]string
}

// The cache belongs to one authoritative transaction/batch, never a process
// or workspace singleton: a schema edit cannot reuse an old dependency graph.
func WithClockCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, clockCacheKey{}, newClockCache())
}
func newClockCache() *clockCache {
	return &clockCache{fields: map[string][]v2.FieldDefinition{}, references: map[clockFieldKey][]formula.ClockReference{}, tables: map[clockFieldKey][]string{}}
}
func ClockReferencesFor(ctx context.Context, app core.App, tableID string, fields []v2.FieldDefinition, fieldID string) ([]formula.ClockReference, error) {
	cache, ok := ctx.Value(clockCacheKey{}).(*clockCache)
	if !ok {
		cache = newClockCache()
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.fields[tableID] = fields
	return cache.visit(ctx, app, clockFieldKey{tableID, fieldID}, map[clockFieldKey]bool{})
}
func (cache *clockCache) visit(ctx context.Context, app core.App, key clockFieldKey, visiting map[clockFieldKey]bool) ([]formula.ClockReference, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if references, ok := cache.references[key]; ok {
		return references, nil
	}
	if visiting[key] {
		return nil, fmt.Errorf("computed clock dependency cycle at %s/%s", key.table, key.field)
	}
	if len(cache.references)+len(visiting) >= 4096 {
		return nil, fmt.Errorf("computed clock dependency graph exceeds 4096 fields")
	}
	visiting[key] = true
	defer delete(visiting, key)
	fields, ok := cache.fields[key.table]
	if !ok {
		var err error
		fields, err = describeTableFields(ctx, app, key.table)
		if err != nil {
			return nil, err
		}
		cache.fields[key.table] = fields
	}
	var current *v2.FieldDefinition
	for i := range fields {
		if fields[i].Identity.FieldID == key.field {
			current = &fields[i]
			break
		}
	}
	if current == nil || (current.Formula == nil && current.Lookup == nil) {
		cache.references[key] = nil
		return nil, nil
	}
	tables, err := directDependencyTables(ctx, app, key.table, fields, *current)
	if err != nil {
		return nil, err
	}
	seenTables := map[string]bool{}
	for _, table := range tables {
		seenTables[table] = true
	}
	addTables := func(nested clockFieldKey) {
		for _, table := range cache.tables[nested] {
			if !seenTables[table] {
				seenTables[table] = true
				tables = append(tables, table)
			}
		}
	}
	var result []formula.ClockReference
	seen := map[formula.ClockReference]bool{}
	add := func(references []formula.ClockReference) {
		for _, reference := range references {
			if !seen[reference] {
				seen[reference] = true
				result = append(result, reference)
			}
		}
	}
	if current.Formula != nil {
		direct, dependencies, err := formula.AnalyzeClock(current.Formula.Source, fields)
		if err != nil {
			return nil, err
		}
		add(direct)
		for _, fieldID := range dependencies {
			nested, err := cache.visit(ctx, app, clockFieldKey{key.table, fieldID}, visiting)
			if err != nil {
				return nil, err
			}
			add(nested)
			addTables(clockFieldKey{key.table, fieldID})
		}
	}
	dependencies, err := app.FindRecordsByFilter("vibetable_computation_dependencies",
		"source_table_id={:table} && computed_field_id={:field}", "+target_table_id,+target_field_id", 4097, 0,
		dbx.Params{"table": key.table, "field": key.field})
	if err != nil {
		return nil, err
	}
	if len(dependencies) > 4096 {
		return nil, fmt.Errorf("computed clock dependency graph exceeds 4096 fields")
	}
	for _, dependency := range dependencies {
		target := dependency.GetString("target_field_id")
		if target == "__path__" {
			continue
		}
		nested, err := cache.visit(ctx, app, clockFieldKey{dependency.GetString("target_table_id"), target}, visiting)
		if err != nil {
			return nil, err
		}
		add(nested)
		addTables(clockFieldKey{dependency.GetString("target_table_id"), target})
	}
	cache.tables[key] = tables
	cache.references[key] = result
	return result, nil
}
