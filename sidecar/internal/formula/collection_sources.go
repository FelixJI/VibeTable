package formula

import (
	"context"
	"sort"

	"github.com/google/cel-go/cel"
	"github.com/pocketbase/pocketbase/core"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// ResolveCollectionSchemas enriches a caller-owned schema snapshot, including
// transaction-local candidate overlays. Only statically referenced tables are
// loaded; no data rows or process-global source-schema cache are introduced.
func ResolveCollectionSchemas(ctx context.Context, definition schemaexecution.Table,
	resolve func(context.Context, string) (schemaexecution.Table, error), extraSources ...string,
) (schemaexecution.Table, error) {
	sources := map[string]v2.SchemaSnapshot{}
	load := func(id string) (v2.SchemaSnapshot, error) {
		if id == definition.Snapshot.TableID {
			sources[id] = definition.Snapshot
			return definition.Snapshot, nil
		}
		if source, ok := sources[id]; ok {
			return source, nil
		}
		if err := ctx.Err(); err != nil {
			return v2.SchemaSnapshot{}, err
		}
		source, err := resolve(ctx, id)
		if err != nil {
			return v2.SchemaSnapshot{}, err
		}
		if source.Snapshot.TableID != id {
			return v2.SchemaSnapshot{}, formulaError("formula.dependency", "source table identity differs", nil)
		}
		sources[id] = source.Snapshot
		return source.Snapshot, nil
	}
	expressions := append([]string(nil), extraSources...)
	for _, field := range definition.Snapshot.Fields {
		if field.Formula != nil {
			expressions = append(expressions, field.Formula.Source)
		}
	}
	for _, source := range expressions {
		env, err := cel.NewEnv(cel.ParserExpressionSizeLimit(DefaultSourceLimit), cel.ParserRecursionLimit(DefaultRecursionLimit))
		if err != nil {
			return definition, err
		}
		parsed, issues := env.Parse(source)
		if issues != nil && issues.Err() != nil {
			return definition, formulaIssuesError("formula.syntax", "formula could not be parsed", source, issues)
		}
		tableIDs := map[string]bool{}
		names := map[string]bool{}
		hasCollections := false
		var visit func(*exprpb.Expr) error
		visit = func(expression *exprpb.Expr) error {
			if expression == nil {
				return nil
			}
			if identifier := expression.GetIdentExpr(); identifier != nil {
				names[identifier.Name] = true
			}
			if call := expression.GetCallExpr(); call != nil {
				hasCollections = hasCollections || collectionCall(call)
				if call.Function == "TABLE" {
					if len(call.Args) != 1 || call.Args[0].GetConstExpr().GetStringValue() == "" {
						return formulaError("formula.dependency", "TABLE requires a static table identity", nil)
					}
					tableIDs[call.Args[0].GetConstExpr().GetStringValue()] = true
				}
				if err := visit(call.Target); err != nil {
					return err
				}
				for _, arg := range call.Args {
					if err := visit(arg); err != nil {
						return err
					}
				}
			}
			if selected := expression.GetSelectExpr(); selected != nil {
				return visit(selected.Operand)
			}
			if list := expression.GetListExpr(); list != nil {
				for _, arg := range list.Elements {
					if err := visit(arg); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := visit(parsed.Expr()); err != nil {
			return definition, err
		}
		if !hasCollections {
			continue
		}
		for id := range tableIDs {
			if _, err := load(id); err != nil {
				return definition, err
			}
		}
		for _, field := range definition.Snapshot.Fields {
			if !names[field.Identity.PhysicalName] {
				continue
			}
			if field.Relation != nil {
				if _, err := load(field.Relation.TargetTableID); err != nil {
					return definition, err
				}
			}
			if field.Lookup != nil {
				if field.Lookup.Condition != nil {
					if _, err := load(field.Lookup.Condition.SourceTableID); err != nil {
						return definition, err
					}
					continue
				}
				current := definition.Snapshot
				for _, step := range field.Lookup.Path {
					relation, ok := schemaSnapshotField(current, step.RelationFieldID)
					if !ok || relation.Relation == nil {
						return definition, formulaError("formula.dependency", "lookup range path is unavailable", nil)
					}
					var err error
					current, err = load(relation.Relation.TargetTableID)
					if err != nil {
						return definition, err
					}
				}
			}
		}
	}
	if len(sources) > 0 {
		sources[definition.Snapshot.TableID] = definition.Snapshot
		definition.FormulaSources = sources
	} else {
		definition.FormulaSources = nil
	}
	return definition, nil
}

type collectionSchemaResolverKey struct{}
type collectionSchemaResolver func(context.Context, string) (schemaexecution.Table, error)

// WithCollectionSchemaResolver shares the caller's transaction-local schema
// snapshot between planning and recursive evaluation. It never caches row data.
func WithCollectionSchemaResolver(ctx context.Context, resolve func(context.Context, string) (schemaexecution.Table, error)) context.Context {
	return context.WithValue(ctx, collectionSchemaResolverKey{}, collectionSchemaResolver(resolve))
}

func LoadCollectionSchemas(ctx context.Context, app core.App, definition schemaexecution.Table, extraSources ...string) (schemaexecution.Table, error) {
	if resolve, ok := ctx.Value(collectionSchemaResolverKey{}).(collectionSchemaResolver); ok {
		return ResolveCollectionSchemas(ctx, definition, resolve, extraSources...)
	}
	return ResolveCollectionSchemas(ctx, definition, func(ctx context.Context, id string) (schemaexecution.Table, error) {
		return schemaexecution.Describe(ctx, app, id)
	}, extraSources...)
}

func schemaSnapshotField(snapshot v2.SchemaSnapshot, identity string) (v2.FieldDefinition, bool) {
	for _, field := range snapshot.Fields {
		if field.Identity.FieldID == identity || field.Identity.PhysicalName == identity {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
}

type CollectionDependency struct {
	TableID         string
	FieldID         string
	RelationFieldID string
}

func collectionDependencies(bindings []collectionBinding) []CollectionDependency {
	seen := map[CollectionDependency]bool{}
	var visit func(*collectionNode)
	visit = func(node *collectionNode) {
		if node == nil {
			return
		}
		if node.kind == "table" || node.kind == "local" {
			if node.records != nil {
				seen[CollectionDependency{node.records.TableID, "__path__", node.relationFieldID}] = true
			}
		}
		if node.child != nil && node.child.records != nil {
			table := node.child.records.TableID
			if node.kind == "PROJECT" {
				seen[CollectionDependency{table, node.projection.Identity.FieldID, node.relationFieldID}] = true
			}
			for _, field := range node.inputs {
				seen[CollectionDependency{table, field.Identity.FieldID, node.relationFieldID}] = true
			}
		}
		visit(node.child)
	}
	for _, binding := range bindings {
		visit(binding.node)
	}
	dependencies := make([]CollectionDependency, 0, len(seen))
	for dependency := range seen {
		dependencies = append(dependencies, dependency)
	}
	sort.Slice(dependencies, func(i, j int) bool {
		a, b := dependencies[i], dependencies[j]
		if a.TableID != b.TableID {
			return a.TableID < b.TableID
		}
		if a.FieldID != b.FieldID {
			return a.FieldID < b.FieldID
		}
		return a.RelationFieldID < b.RelationFieldID
	})
	return dependencies
}

func LoadV2CollectionSchemas(ctx context.Context, app core.App, definition V2Table, extraSources ...string) (V2Table, error) {
	execution, err := LoadCollectionSchemas(ctx, app, executionTable(definition), extraSources...)
	if err != nil {
		return definition, err
	}
	definition.Sources = execution.FormulaSources
	return definition, nil
}
func (compiler *Compiler) InferV2Value(definition V2Table, source string) (ValueType, *Error) {
	return compiler.InferExecutionSource(executionTable(definition), source)
}
func HasCollectionSyntax(source string) bool {
	env, err := cel.NewEnv(cel.ParserExpressionSizeLimit(DefaultSourceLimit), cel.ParserRecursionLimit(DefaultRecursionLimit))
	if err != nil {
		return false
	}
	parsed, issues := env.Parse(source)
	if issues != nil && issues.Err() != nil {
		return false
	}
	var visit func(*exprpb.Expr) bool
	visit = func(expression *exprpb.Expr) bool {
		if expression == nil {
			return false
		}
		if call := expression.GetCallExpr(); call != nil {
			if collectionCall(call) {
				return true
			}
			if visit(call.Target) {
				return true
			}
			for _, arg := range call.Args {
				if visit(arg) {
					return true
				}
			}
		}
		if selected := expression.GetSelectExpr(); selected != nil {
			return visit(selected.Operand)
		}
		if list := expression.GetListExpr(); list != nil {
			for _, arg := range list.Elements {
				if visit(arg) {
					return true
				}
			}
		}
		return false
	}
	return visit(parsed.Expr())
}

func collectionLookupType(definition schemaexecution.Table, spec v2.LookupSpec) (ValueType, *Error) {
	current := definition.Snapshot
	many := v2.ResolvedLookupAggregation(spec) == v2.LookupAggregationDistinct
	if spec.Condition != nil {
		var ok bool
		current, ok = definition.FormulaSources[spec.Condition.SourceTableID]
		if !ok {
			return ValueType{}, formulaError("formula.dependency", "lookup source schema is unavailable", nil)
		}
		many = true
	} else {
		for _, step := range spec.Path {
			relation, ok := schemaSnapshotField(current, step.RelationFieldID)
			if !ok || relation.Relation == nil {
				return ValueType{}, formulaError("formula.dependency", "lookup path is unavailable", nil)
			}
			many = many || relation.Relation.Cardinality == "many"
			current, ok = definition.FormulaSources[relation.Relation.TargetTableID]
			if !ok {
				return ValueType{}, formulaError("formula.dependency", "lookup source schema is unavailable", nil)
			}
		}
	}
	if !many {
		return ValueType{}, formulaError("formula.type", "scalar lookup is not a collection", nil)
	}
	target, ok := schemaSnapshotField(current, spec.TargetFieldID)
	if !ok {
		return ValueType{}, formulaError("formula.dependency", "lookup target is unavailable", nil)
	}
	return collectionElementType(target)
}
