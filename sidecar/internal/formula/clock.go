package formula

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/interpreter"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

type evaluationTimeKey struct{}

const clockActivationName = "@vibetable.evaluationTime"

// WithEvaluationTime binds a whole logical batch, including its freshness
// metadata, to one instant. It does not alter cancellation or deadlines.
func WithEvaluationTime(ctx context.Context, instant time.Time) context.Context {
	return context.WithValue(ctx, evaluationTimeKey{}, instant.UTC())
}

func EnsureEvaluationTime(ctx context.Context) context.Context {
	return EnsureEvaluationTimeAt(ctx, time.Now())
}

func EnsureEvaluationTimeAt(ctx context.Context, instant time.Time) context.Context {
	if _, ok := ctx.Value(evaluationTimeKey{}).(time.Time); ok {
		return ctx
	}
	return WithEvaluationTime(ctx, instant)
}

func EvaluationTime(ctx context.Context) time.Time {
	if instant, ok := ctx.Value(evaluationTimeKey{}).(time.Time); ok {
		return instant
	}
	return time.Now().UTC()
}

type ClockReference struct{ Function, Zone string }

// ClockSignature is a plain, inspectable period key, not a second checksum.
// An empty signature keeps every existing nonvolatile envelope unchanged.
func ClockSignature(ctx context.Context, references []ClockReference) string {
	instant := EvaluationTime(ctx)
	periods := map[string]struct{}{}
	for _, reference := range references {
		if reference.Function == "NOW" {
			periods["minute:"+instant.Truncate(time.Minute).Format(time.RFC3339)] = struct{}{}
		} else {
			location, err := calendarLocation(reference.Zone)
			if err != nil {
				continue
			} // References are validated at compilation.
			periods["day:"+reference.Zone+":"+instant.In(location).Format("2006-01-02")] = struct{}{}
		}
	}
	keys := make([]string, 0, len(periods))
	for key := range periods {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, "|")
}

func clockFunctionOptions() []cel.EnvOption {
	binding := cel.FunctionBinding(func(...ref.Val) ref.Val { return types.NewErr("formula clock decorator is unavailable") })
	return []cel.EnvOption{
		cel.Function("TODAY",
			cel.Overload("vibetable_today", []*cel.Type{}, cel.TimestampType, binding),
			cel.Overload("vibetable_today_zone", []*cel.Type{cel.StringType}, cel.TimestampType, binding)),
		cel.Function("NOW", cel.Overload("vibetable_now", []*cel.Type{}, cel.TimestampType, binding)),
	}
}

func validateClockCall(call *exprpb.Expr_Call) *Error {
	if call.Function != "TODAY" || len(call.Args) == 0 {
		return nil
	}
	if len(call.Args) != 1 {
		return formulaError("formula.type", "TODAY accepts one constant time zone", nil)
	}
	constant, ok := call.Args[0].ExprKind.(*exprpb.Expr_ConstExpr)
	if !ok {
		return formulaError("formula.type", "TODAY time zone must be a string literal", nil)
	}
	zone, ok := constant.ConstExpr.ConstantKind.(*exprpb.Constant_StringValue)
	if !ok {
		return formulaError("formula.type", "TODAY time zone must be a string literal", nil)
	}
	if _, err := calendarLocation(zone.StringValue); err != nil {
		return formulaError("formula.timezone", "TODAY time zone is unavailable", map[string]any{"timezone": zone.StringValue})
	}
	return nil
}

func expressionClockReferences(expression *exprpb.Expr) []ClockReference {
	var references []ClockReference
	var walk func(*exprpb.Expr)
	walk = func(current *exprpb.Expr) {
		if current == nil {
			return
		}
		switch kind := current.ExprKind.(type) {
		case *exprpb.Expr_CallExpr:
			call := kind.CallExpr
			if call.Function == "NOW" || call.Function == "TODAY" {
				reference := ClockReference{Function: call.Function, Zone: "system"}
				if len(call.Args) == 1 {
					reference.Zone = call.Args[0].GetConstExpr().GetStringValue()
				}
				references = append(references, reference)
			}
			walk(call.Target)
			for _, arg := range call.Args {
				walk(arg)
			}
		case *exprpb.Expr_SelectExpr:
			walk(kind.SelectExpr.Operand)
		case *exprpb.Expr_ListExpr:
			for _, item := range kind.ListExpr.Elements {
				walk(item)
			}
		case *exprpb.Expr_StructExpr:
			for _, entry := range kind.StructExpr.Entries {
				walk(entry.GetMapKey())
				walk(entry.GetValue())
			}
		}
	}
	walk(expression)
	return references
}

// FieldClockReferences includes the transitive same-row formula dependencies.
// Cross-table references are extended by RelatedComputation's existing graph.
func (plan *Plan) FieldClockReferences(fieldID string) []ClockReference {
	var result []ClockReference
	seen := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		compiled := plan.byID[id]
		if compiled == nil {
			return
		}
		result = append(result, compiled.ClockReferences...)
		for _, dependency := range compiled.Dependencies {
			visit(dependency)
		}
	}
	visit(fieldID)
	return result
}

type clockCall struct{ interpreter.InterpretableCall }

func clockFunctionDecorator(node interpreter.InterpretableV2) (interpreter.InterpretableV2, error) {
	if call, ok := node.(interpreter.InterpretableCall); ok && (call.Function() == "TODAY" || call.Function() == "NOW") {
		return clockCall{call}, nil
	}
	return node, nil
}
func (call clockCall) Eval(activation interpreter.Activation) ref.Val {
	return call.evaluate(activation, func(arg interpreter.InterpretableV2) ref.Val { return arg.Eval(activation) })
}
func (call clockCall) Exec(frame *interpreter.ExecutionFrame) ref.Val {
	return call.evaluate(frame, func(arg interpreter.InterpretableV2) ref.Val { return arg.Exec(frame) })
}
func (call clockCall) evaluate(activation interpreter.Activation, run func(interpreter.InterpretableV2) ref.Val) ref.Val {
	raw, ok := activation.ResolveName(clockActivationName)
	instant, valid := raw.(time.Time)
	if !ok || !valid {
		return types.NewErr("formula evaluation time is unavailable")
	}
	if call.Function() == "NOW" {
		return types.Timestamp{Time: instant.UTC().Truncate(time.Minute)}
	}
	zone := "system"
	if len(call.Args()) == 1 {
		value := run(call.Args()[0])
		text, ok := value.(types.String)
		if !ok {
			return types.MaybeNoSuchOverloadErr(value)
		}
		zone = string(text)
	}
	location, err := calendarLocation(zone)
	if err != nil {
		return types.NewErr("formula time zone is unavailable")
	}
	local := instant.In(location)
	return types.Timestamp{Time: time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)}
}

// AnalyzeClock reuses the expression inspector to follow same-row inputs
// without depending on storage bindings or evaluating a formula.
func AnalyzeClock(source string, fields []v2.FieldDefinition) ([]ClockReference, []string, error) {
	if strings.TrimSpace(source) == "" {
		return nil, nil, nil
	}
	env, err := cel.NewEnv(cel.ParserExpressionSizeLimit(DefaultSourceLimit), cel.ParserRecursionLimit(DefaultRecursionLimit))
	if err != nil {
		return nil, nil, err
	}
	parsed, issues := env.Parse(source)
	if issues != nil && issues.Err() != nil {
		return nil, nil, issues.Err()
	}
	byName := make(map[string]v2.FieldDefinition, len(fields))
	for _, field := range fields {
		byName[field.Identity.PhysicalName] = field
	}
	_, names, _, _, _, failure := inspectExpression(parsed.Expr(), byName, DefaultLimits())
	if failure != nil {
		return nil, nil, failure
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		ids = append(ids, byName[name].Identity.FieldID)
	}
	return expressionClockReferences(parsed.Expr()), ids, nil
}

type evaluationFieldsKey struct{}

// WithEvaluationFields is internal-only selective materialization for clock
// refreshes. nil means a normal full calculation; an empty map means none.
func WithEvaluationFields(ctx context.Context, fields map[string]bool) context.Context {
	return context.WithValue(ctx, evaluationFieldsKey{}, fields)
}
func EvaluationFields(ctx context.Context) map[string]bool {
	fields, _ := ctx.Value(evaluationFieldsKey{}).(map[string]bool)
	return fields
}
func EvaluationIncludes(ctx context.Context, fieldID string) bool {
	fields := EvaluationFields(ctx)
	return fields == nil || fields[fieldID]
}

type sourceRecalculationKey struct{}

// WithSourceRecalculation is reserved for derived-cache materialization. Public
// reads and business mutations keep rejecting stale stored computed sources.
func WithSourceRecalculation(ctx context.Context) context.Context {
	return context.WithValue(ctx, sourceRecalculationKey{}, true)
}
func SourceRecalculationEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(sourceRecalculationKey{}).(bool)
	return enabled
}

// ChargeSourceEvaluation shares the collection evaluator's existing budget;
// recursion never starts a fresh allowance for each computed source cell.
func EnsureSourceEvaluationBudget(ctx context.Context) context.Context {
	_, ok := ctx.Value(collectionEvaluationKey{}).(*collectionEvaluation)
	if !ok {
		limits := DefaultLimits()
		evaluation := &collectionEvaluation{remaining: limits.Cost, limits: limits, bytes: collectionBudget{remaining: limits.CollectionBytes}}
		ctx = context.WithValue(ctx, collectionEvaluationKey{}, evaluation)
	}
	return ctx
}

func ChargeSourceEvaluation(ctx context.Context) (context.Context, error) {
	ctx = EnsureSourceEvaluationBudget(ctx)
	evaluation := ctx.Value(collectionEvaluationKey{}).(*collectionEvaluation)
	if err := evaluation.charge(ctx, 1); err != nil {
		return ctx, err
	}
	return ctx, nil
}

// RetainSourceValue charges recursive memoization to the same 32 MiB allowance
// as TABLE materialization, including every element of collection results.
func RetainSourceValue(ctx context.Context, value any) (any, error) {
	evaluation, ok := ctx.Value(collectionEvaluationKey{}).(*collectionEvaluation)
	if !ok {
		return nil, formulaError("formula.resource_limit", "computed source budget is unavailable", nil)
	}
	normalized, failure := normalizeDynamicInputBudget(value, evaluation.limits, 0, "", &evaluation.bytes)
	if failure != nil {
		return nil, failure
	}
	return normalized, nil
}
