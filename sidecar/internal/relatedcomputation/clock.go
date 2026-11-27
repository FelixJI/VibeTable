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
type computedDefinitionReference struct {
	key   clockFieldKey
	field v2.FieldDefinition
}
type clockCache struct {
	mu          sync.Mutex
	sameRowOnly bool
	// full is the transaction-wide cache a same-row view stores its proven
	// entries into and reads stored-edge-free entries back from. Standalone
	// caches (every other caller) leave it nil.
	full        *clockCache
	fields      map[string][]v2.FieldDefinition
	references  map[clockFieldKey][]formula.ClockReference
	tables      map[clockFieldKey][]string
	inputs      map[clockFieldKey][]clockFieldKey
	definitions map[clockFieldKey][]computedDefinitionReference
}

// The cache belongs to one authoritative transaction/batch, never a process
// or workspace singleton: a schema edit cannot reuse an old dependency graph.
func WithClockCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, clockCacheKey{}, newClockCache())
}

// WithSameRowClockCache rejects stored relationship edges when the caller only
// authorized same-row inputs from the formula definitions.
func WithSameRowClockCache(ctx context.Context) context.Context {
	cache := newClockCache()
	cache.sameRowOnly = true
	return context.WithValue(ctx, clockCacheKey{}, cache)
}

// WithSameRowClockCacheView installs the restricted same-row cache as a view of
// one full dependency-graph cache shared by the whole transaction. The view
// keeps its own policy (stored relationship edges are still refused and
// definition versions are still read the same-row way), stores every entry it
// proves same-row into the full cache as well, and takes over a full-cache
// entry only when that entry carries no stored edge, neither directly nor
// through a nested operand. Refused keys are stored nowhere, so a refusal never
// crosses from one policy to the other. WithClockCacheAfterSameRow hands the
// full cache itself to the schema description: nothing is copied per source.
func WithSameRowClockCacheView(ctx context.Context) context.Context {
	full, _ := ctx.Value(clockCacheKey{}).(*clockCache)
	if full == nil || full.sameRowOnly {
		full = newClockCache()
	}
	cache := newClockCache()
	cache.sameRowOnly = true
	cache.full = full
	return context.WithValue(ctx, clockCacheKey{}, cache)
}

// WithClockCacheAfterSameRow is what a schema description calls: stored
// relationship edges stay allowed, so relation targets and their computed
// fields describe exactly as before. After a same-row view it installs the
// transaction's full cache itself; in every other case it is exactly
// WithClockCache.
func WithClockCacheAfterSameRow(ctx context.Context) context.Context {
	previous, ok := ctx.Value(clockCacheKey{}).(*clockCache)
	if !ok || previous.full == nil {
		return WithClockCache(ctx)
	}
	return context.WithValue(ctx, clockCacheKey{}, previous.full)
}

// reusable hands the view the full cache's entry for key when the same-row
// policy may take it over: the entry exists and carries no stored edge. A
// nested operand's stored edges propagate into the entry's inputs and tables,
// so an empty pair covers the whole closure.
func (cache *clockCache) reusable(key clockFieldKey) ([]formula.ClockReference, bool) {
	if cache.full == nil {
		return nil, false
	}
	cache.full.mu.Lock()
	defer cache.full.mu.Unlock()
	references, found := cache.full.references[key]
	if !found || len(cache.full.tables[key]) != 0 || len(cache.full.inputs[key]) != 0 {
		return nil, false
	}
	cache.fields[key.table] = cache.full.fields[key.table]
	cache.references[key] = references
	cache.tables[key] = cache.full.tables[key]
	cache.inputs[key] = cache.full.inputs[key]
	cache.definitions[key] = cache.full.definitions[key]
	return references, true
}

func newClockCache() *clockCache {
	return &clockCache{fields: map[string][]v2.FieldDefinition{}, references: map[clockFieldKey][]formula.ClockReference{}, tables: map[clockFieldKey][]string{}, inputs: map[clockFieldKey][]clockFieldKey{}, definitions: map[clockFieldKey][]computedDefinitionReference{}}
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
	if references, ok := cache.reusable(key); ok {
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
		if cache.full != nil {
			cache.full.mu.Lock()
			fields, ok = cache.full.fields[key.table]
			cache.full.mu.Unlock()
		}
		if !ok {
			var err error
			fields, err = describeTableFields(ctx, app, key.table)
			if err != nil {
				return nil, err
			}
			if cache.full != nil {
				cache.full.mu.Lock()
				cache.keepFields(key.table, fields)
				cache.full.mu.Unlock()
			}
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
		cache.keep(key)
		return nil, nil
	}
	tables, err := directDependencyTables(ctx, app, key.table, fields, *current)
	if err != nil {
		return nil, err
	}
	if cache.sameRowOnly && len(tables) != 0 {
		return nil, fmt.Errorf("computed dependency is not same-row")
	}
	seenTables := map[string]bool{}
	definitions := []computedDefinitionReference{{key: key, field: *current}}
	seenDefinitions := map[clockFieldKey]bool{key: true}
	var inputs []clockFieldKey
	seenInputs := map[clockFieldKey]bool{}
	addInput := func(input clockFieldKey) {
		if !seenInputs[input] {
			seenInputs[input] = true
			inputs = append(inputs, input)
		}
	}
	for _, table := range tables {
		seenTables[table] = true
	}
	addTables := func(nested clockFieldKey) {
		for _, definition := range cache.definitions[nested] {
			if !seenDefinitions[definition.key] {
				seenDefinitions[definition.key] = true
				definitions = append(definitions, definition)
			}
		}
		for _, input := range cache.inputs[nested] {
			addInput(input)
		}
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
	if cache.sameRowOnly && len(dependencies) != 0 {
		return nil, fmt.Errorf("computed dependency is not same-row")
	}
	if len(dependencies) > 4096 {
		return nil, fmt.Errorf("computed clock dependency graph exceeds 4096 fields")
	}
	if len(dependencies) != 0 {
		addInput(key)
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
	cache.inputs[key] = inputs
	cache.definitions[key] = definitions
	cache.references[key] = result
	cache.keep(key)
	return result, nil
}

// keep copies an entry this view resolved into the full cache. The same-row
// policy only ever resolves entries without a stored edge, which is exactly
// what the full cache would have computed for them.
func (cache *clockCache) keep(key clockFieldKey) {
	if cache.full == nil {
		return
	}
	cache.full.mu.Lock()
	defer cache.full.mu.Unlock()
	cache.keepFields(key.table, cache.fields[key.table])
	cache.full.tables[key] = cache.tables[key]
	cache.full.inputs[key] = cache.inputs[key]
	cache.full.definitions[key] = cache.definitions[key]
	cache.full.references[key] = cache.references[key]
}

// keepFields shares one table description with the full cache.
func (cache *clockCache) keepFields(table string, fields []v2.FieldDefinition) {
	if cache.full != nil && cache.full.fields[table] == nil {
		cache.full.fields[table] = fields
	}
}
