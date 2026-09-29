package formula

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
	"google.golang.org/protobuf/proto"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// Collection expressions are compiled into closed, typed plans. Their lazy
// activation bindings use the same CEL runtime for scalar predicates; user
// comprehensions and dynamic indexing never enter the executable plan.
type collectionBinding struct {
	name string
	node *collectionNode
}
type collectionNode struct {
	kind            string
	valueType       ValueType
	records         *v2.SchemaSnapshot
	tableID         string
	localName       string
	relationFieldID string
	child           *collectionNode
	program         *CompiledFormula
	predicate       *CompiledFormula
	separator       *CompiledFormula
	projection      v2.FieldDefinition
	dependencies    []string
	inputs          map[string]v2.FieldDefinition
	filtered        bool
	sourceFields    []v2.FieldDefinition
	queryPlan       *collectionQuery
}

func collectionCall(call *exprpb.Expr_Call) bool {
	if call == nil || call.Target != nil {
		return false
	}
	switch call.Function {
	case "TABLE", "FILTER", "PROJECT", "SUMIF", "COUNTIF", "SUM", "AVERAGE",
		"COUNT", "COUNTA", "UNIQUE", "ARRAYJOIN":
		return true
	case "MIN", "MAX":
		return len(call.Args) == 1
	}
	return false
}

func (compiler *Compiler) lowerCollections(
	definition schemaexecution.Table, env *cel.Env, parsed *cel.Ast,
	fields map[string]v2.FieldDefinition,
) (*cel.Env, *cel.Ast, []collectionBinding, *Error) {
	expression := parsed.Expr()
	if _, _, _, _, _, err := inspectExpression(expression, fields, compiler.limits); err != nil {
		if function, ok := err.Details["function"].(string); ok {
			for _, token := range authorSyntaxLexemes(parsed.Source().Content()) {
				if token.text == function {
					err.SourceSpan = &SourceSpan{Start: token.start, End: token.end}
					break
				}
			}
		}
		return nil, nil, nil, err
	}
	var bindings []collectionBinding
	var options []cel.EnvOption
	var walk func(*exprpb.Expr) *Error
	walk = func(current *exprpb.Expr) *Error {
		if current == nil {
			return nil
		}
		if identifier := current.GetIdentExpr(); identifier != nil &&
			strings.HasPrefix(identifier.Name, "__vt_collection_") {
			return formulaError("formula.dependency", "reserved collection binding", nil)
		}
		if call := current.GetCallExpr(); collectionCall(call) {
			node, err := compiler.collectionNode(definition, current)
			if err != nil {
				return collectionErrorAt(parsed, current, err)
			}
			if node.records != nil {
				return formulaError("formula.type", "record ranges require projection or aggregation", nil)
			}
			typed, typeErr := celTypeForValueType(node.valueType)
			if typeErr != nil {
				return formulaError("formula.type", typeErr.Error(), nil)
			}
			node.selectSourceFields()
			name := fmt.Sprintf("__vt_collection_%d", current.Id)
			options = append(options, cel.Variable(name, typed))
			bindings = append(bindings, collectionBinding{name: name, node: node})
			current.ExprKind = &exprpb.Expr_IdentExpr{IdentExpr: &exprpb.Expr_Ident{Name: name}}
			return nil
		}
		switch kind := current.ExprKind.(type) {
		case *exprpb.Expr_CallExpr:
			if err := walk(kind.CallExpr.Target); err != nil {
				return err
			}
			for _, arg := range kind.CallExpr.Args {
				if err := walk(arg); err != nil {
					return err
				}
			}
		case *exprpb.Expr_SelectExpr:
			return walk(kind.SelectExpr.Operand)
		case *exprpb.Expr_ListExpr:
			for _, element := range kind.ListExpr.Elements {
				if err := walk(element); err != nil {
					return err
				}
			}
		case *exprpb.Expr_StructExpr:
			for _, entry := range kind.StructExpr.Entries {
				if err := walk(entry.GetMapKey()); err != nil {
					return err
				}
				if err := walk(entry.Value); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(expression); err != nil {
		return nil, nil, nil, err
	}
	if len(options) > 0 {
		extended, err := env.Extend(options...)
		if err != nil {
			return nil, nil, nil, formulaError("formula.type", err.Error(), nil)
		}
		env = extended
	}
	return env, cel.ParsedExprToAst(&exprpb.ParsedExpr{Expr: expression, SourceInfo: parsed.SourceInfo()}), bindings, nil
}

func (compiler *Compiler) collectionNode(definition schemaexecution.Table, expression *exprpb.Expr) (*collectionNode, *Error) {
	call := expression.GetCallExpr()
	if !collectionCall(call) {
		if identifier := expression.GetIdentExpr(); identifier != nil {
			field, ok := definition.Field(identifier.Name)
			if ok && field.Lookup != nil && !v2.LookupAggregationNumeric(v2.ResolvedLookupAggregation(*field.Lookup)) {
				element, err := collectionLookupType(definition, *field.Lookup)
				if err != nil {
					return nil, err
				}
				return &collectionNode{kind: "lookup", localName: identifier.Name,
					valueType:    ValueType{LogicalType: v2.LogicalJSON, ElementType: element.LogicalType},
					dependencies: []string{identifier.Name}}, nil
			}
			if ok && field.Relation != nil {
				source, exists := definition.FormulaSources[field.Relation.TargetTableID]
				if !exists {
					return nil, formulaError("formula.dependency", "relation source schema is unavailable", nil)
				}
				return &collectionNode{kind: "local", localName: identifier.Name, records: &source, relationFieldID: field.Identity.FieldID, dependencies: []string{identifier.Name}}, nil
			}
		}
		program, err := compiler.collectionScalar(definition, expression, nil)
		if err != nil {
			return nil, err
		}
		if program.ResultType.ElementType == "" {
			return nil, formulaError("formula.type", "collection function requires a typed list", nil)
		}
		return &collectionNode{kind: "value", program: program, valueType: program.ResultType, dependencies: program.dependencyNames}, nil
	}
	arity := 1
	switch call.Function {
	case "FILTER", "PROJECT", "COUNTIF", "ARRAYJOIN":
		arity = 2
	case "SUMIF":
		arity = 3
	}
	if len(call.Args) != arity {
		return nil, formulaError("formula.syntax", "collection function argument count is invalid", map[string]any{"function": call.Function, "arguments": arity})
	}
	if call.Function == "TABLE" {
		tableID := call.Args[0].GetConstExpr().GetStringValue()
		source, ok := definition.FormulaSources[tableID]
		if tableID == "" || !ok {
			return nil, formulaError("formula.dependency", "TABLE requires an active static table identity", map[string]any{"tableId": tableID})
		}
		return &collectionNode{kind: "table", tableID: tableID, records: &source}, nil
	}
	if call.Function == "SUMIF" || call.Function == "COUNTIF" {
		filtered := collectionExpression("FILTER", call.Args[0], call.Args[1])
		if call.Function == "COUNTIF" {
			return compiler.collectionNode(definition, collectionExpression("COUNT", filtered))
		}
		projected := collectionExpression("PROJECT", filtered, call.Args[2])
		return compiler.collectionNode(definition, collectionExpression("SUM", projected))
	}
	child, err := compiler.collectionNode(definition, call.Args[0])
	if err != nil {
		return nil, err
	}
	node := &collectionNode{kind: call.Function, child: child, valueType: child.valueType,
		records: child.records, relationFieldID: child.relationFieldID, dependencies: append([]string(nil), child.dependencies...), filtered: child.filtered}
	switch call.Function {
	case "FILTER":
		if child.filtered {
			return nil, formulaError("formula.resource_limit", "nested FILTER is not supported", nil)
		}
		predicate, inputs, err := compiler.collectionPredicate(definition, child, call.Args[1])
		if err != nil {
			return nil, err
		}
		node.predicate, node.inputs, node.filtered = predicate, inputs, true
		if child.kind == "table" {
			child.queryPlan = compiler.collectionQuery(definition, *child.records, call.Args[1])
		}
		for _, name := range predicate.dependencyNames {
			if _, internal := inputs[name]; !internal {
				node.dependencies = append(node.dependencies, name)
			}
		}
	case "PROJECT":
		if child.records == nil {
			return nil, formulaError("formula.type", "PROJECT requires a record range", nil)
		}
		selected := call.Args[1].GetSelectExpr()
		if selected == nil || selected.Operand.GetIdentExpr().GetName() != "CurrentValue" {
			return nil, formulaError("formula.dependency", "PROJECT requires a static CurrentValue field", nil)
		}
		found := false
		for _, field := range child.records.Fields {
			if field.Identity.PhysicalName == selected.Field {
				element, err := collectionElementType(field)
				if err != nil {
					return nil, err
				}
				node.projection = field
				node.valueType = ValueType{LogicalType: v2.LogicalJSON, ElementType: element.LogicalType}
				node.records = nil
				found = true
				break
			}
		}
		if !found {
			return nil, formulaError("formula.dependency", "PROJECT field is unavailable", nil)
		}
	case "COUNT", "COUNTA":
		if call.Function == "COUNTA" && child.records != nil {
			return nil, formulaError("formula.type", "COUNTA requires scalar elements", nil)
		}
		node.records = nil
		node.valueType = ValueType{LogicalType: v2.LogicalNumber, OnlyInt: true}
	case "SUM", "AVERAGE", "MIN", "MAX":
		if child.records != nil || child.valueType.ElementType != v2.LogicalNumber {
			return nil, formulaError("formula.type", "numeric collection aggregate requires number elements", nil)
		}
		node.valueType = ValueType{LogicalType: v2.LogicalNumber}
	case "UNIQUE", "ARRAYJOIN":
		if child.records != nil {
			return nil, formulaError("formula.type", "collection function requires scalar elements", nil)
		}
		if call.Function == "ARRAYJOIN" {
			expected := ValueType{LogicalType: v2.LogicalText}
			separator, err := compiler.collectionScalar(definition, call.Args[1], &expected)
			if err != nil {
				return nil, err
			}
			node.separator = separator
			node.dependencies = append(node.dependencies, separator.dependencyNames...)
			node.valueType = expected
		}
	default:
		return nil, formulaError("formula.dependency", "unsupported collection operation", nil)
	}
	node.dependencies = uniqueSortedStrings(node.dependencies)
	return node, nil
}

func collectionExpression(name string, args ...*exprpb.Expr) *exprpb.Expr {
	return &exprpb.Expr{ExprKind: &exprpb.Expr_CallExpr{CallExpr: &exprpb.Expr_Call{Function: name, Args: args}}}
}

func collectionElementType(field v2.FieldDefinition) (ValueType, *Error) {
	typed, err := celTypeForField(field)
	if err != nil {
		return ValueType{}, formulaError("formula.type", "collection element type is unsupported", nil)
	}
	result, failure := valueTypeForCELType(typed)
	if failure != nil || result.ElementType != "" {
		return ValueType{}, formulaError("formula.type", "nested or untyped collection element", nil)
	}
	// Preserve the product calendar-date input shape until CEL normalization.
	if valueTypeForField(field).LogicalType == v2.LogicalDate {
		result.LogicalType = v2.LogicalDate
	}
	result.OnlyInt = false // One stable numeric list shape across JSON round trips.
	return result, nil
}

func (compiler *Compiler) collectionScalar(definition schemaexecution.Table, expression *exprpb.Expr, expected *ValueType) (*CompiledFormula, *Error) {
	ast := cel.ParsedExprToAst(&exprpb.ParsedExpr{Expr: expression})
	source, err := cel.AstToString(ast)
	if err != nil {
		return nil, formulaError("formula.syntax", "collection expression cannot be normalized", nil)
	}
	var valueType ValueType
	if expected == nil {
		inferred, failure := compiler.inferExecutionSource(definition, source, true)
		if failure != nil {
			return nil, failure
		}
		valueType = inferred
	} else {
		valueType = *expected
	}
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "__vt_collection_scalar", PhysicalName: "__vt_collection_scalar"},
		LogicalType: v2.LogicalFormula,
		Formula:     &v2.FormulaSpec{Language: "cel-v2", Source: source, ResultType: valueType.LogicalType, ResultElementType: valueType.ElementType},
	}
	field.Storage.Options.OnlyInt = valueType.OnlyInt
	return compiler.Compile(definition, field)
}

func (compiler *Compiler) collectionPredicate(definition schemaexecution.Table, child *collectionNode, expression *exprpb.Expr) (*CompiledFormula, map[string]v2.FieldDefinition, *Error) {
	expression = proto.Clone(expression).(*exprpb.Expr)
	definition.Snapshot.Fields = append([]v2.FieldDefinition(nil), definition.Snapshot.Fields...)
	inputs := map[string]v2.FieldDefinition{}
	bind := func(current *exprpb.Expr, field v2.FieldDefinition) {
		name := "__vt_element_" + field.Identity.PhysicalName
		if _, exists := inputs[name]; !exists {
			inputs[name] = field
			alias := field
			alias.Identity = v2.FieldIdentity{FieldID: name, PhysicalName: name}
			definition.Snapshot.Fields = append(definition.Snapshot.Fields, alias)
		}
		current.ExprKind = &exprpb.Expr_IdentExpr{IdentExpr: &exprpb.Expr_Ident{Name: name}}
	}
	var walk func(*exprpb.Expr) *Error
	walk = func(current *exprpb.Expr) *Error {
		if current == nil {
			return nil
		}
		if collectionCall(current.GetCallExpr()) {
			return formulaError("formula.resource_limit", "collection predicates cannot contain collection operations", nil)
		}
		if selected := current.GetSelectExpr(); selected != nil && selected.Operand.GetIdentExpr().GetName() == "CurrentValue" {
			if child.records == nil {
				return formulaError("formula.type", "scalar CurrentValue has no fields", nil)
			}
			for _, field := range child.records.Fields {
				if field.Identity.PhysicalName == selected.Field {
					bind(current, field)
					return nil
				}
			}
			return formulaError("formula.dependency", "CurrentValue field is unavailable", nil)
		}
		if current.GetIdentExpr().GetName() == "CurrentValue" {
			if child.records != nil {
				return formulaError("formula.type", "record CurrentValue must select a field", nil)
			}
			bind(current, v2.FieldDefinition{Identity: v2.FieldIdentity{PhysicalName: "value"}, LogicalType: child.valueType.ElementType})
			return nil
		}
		switch kind := current.ExprKind.(type) {
		case *exprpb.Expr_CallExpr:
			if err := walk(kind.CallExpr.Target); err != nil {
				return err
			}
			for _, arg := range kind.CallExpr.Args {
				if err := walk(arg); err != nil {
					return err
				}
			}
		case *exprpb.Expr_SelectExpr:
			return walk(kind.SelectExpr.Operand)
		case *exprpb.Expr_ListExpr:
			for _, arg := range kind.ListExpr.Elements {
				if err := walk(arg); err != nil {
					return err
				}
			}
		case *exprpb.Expr_ComprehensionExpr:
			return formulaError("formula.resource_limit", "formula comprehensions are not allowed", nil)
		}
		return nil
	}
	if err := walk(expression); err != nil {
		return nil, nil, err
	}
	expected := ValueType{LogicalType: v2.LogicalBool}
	predicate, err := compiler.collectionScalar(definition, expression, &expected)
	return predicate, inputs, err
}

func collectionErrorAt(parsed *cel.Ast, expression *exprpb.Expr, err *Error) *Error {
	if err.SourceSpan != nil {
		return err
	}
	position, ok := parsed.SourceInfo().Positions[expression.Id]
	if !ok {
		return err
	}
	source := parsed.Source().Content()
	scalar := int32(0)
	for offset := range source {
		if scalar == position {
			tokens := authorSyntaxLexemes(source)
			for index, token := range tokens {
				if token.start == offset {
					if token.text == "(" && index > 0 {
						token = tokens[index-1]
					}
					err.SourceSpan = &SourceSpan{Start: token.start, End: token.end}
					return err
				}
			}
			break
		}
		scalar++
	}
	return err
}
