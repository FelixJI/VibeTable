package formula

import (
	"fmt"
	"math"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/interpreter"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

// Uppercase spreadsheet-style formula functions. IF/IFS/AND/OR/IFERROR need
// lazy left-to-right evaluation and MIN/MAX/ABS need dyn-friendly runtime
// dispatch, so their call nodes are replaced by custom interpretables through
// commonFunctionDecorator. The remaining functions run as guarded cel
// bindings on the standard eager evaluation path and share their value
// helpers with the existing lowercase functions.
//
// Branch typing follows the frozen contract: IF/IFS/IFERROR branches must
// agree on one type, except that int and double branches unify to double.
// Every other combination is rejected at check time by the declared
// overloads, so the whitelist and overload set stay closed.

const (
	commonVariadicMaxArgs = 8
	// Four condition/value pairs, matching the 2-8 argument convention used
	// by AND/OR/CONCATENATE.
	commonIfsMaxArgs = 8
)

// commonBranchTypes are the result types accepted for IF/IFS/IFERROR
// branches. list(dyn) covers homogeneous multi-select style values.
var commonBranchTypes = []*cel.Type{
	cel.IntType, cel.DoubleType, cel.StringType, cel.BoolType,
	cel.TimestampType, cel.ListType(cel.DynType),
}

func commonBranchTypeName(valueType *cel.Type) string {
	switch valueType {
	case cel.IntType:
		return "int"
	case cel.DoubleType:
		return "double"
	case cel.StringType:
		return "string"
	case cel.BoolType:
		return "bool"
	case cel.TimestampType:
		return "timestamp"
	default:
		return "list"
	}
}

// commonFunctionOptions declares the uppercase function signatures shared by
// Compile and InferExecutionSource. Control-flow and dispatch functions are
// declaration-only; their semantics live in commonFunctionDecorator.
func commonFunctionOptions() []cel.EnvOption {
	var options []cel.EnvOption
	// Same-type branches: (bool, T, ..., T) -> T for IF and (T, T) -> T for
	// IFERROR; IFS repeats condition/value pairs of one type.
	addBranchOverloads := func(name string, branchCount int, idForType func(*cel.Type) string) {
		for _, valueType := range commonBranchTypes {
			args := make([]*cel.Type, branchCount+1)
			args[0] = cel.BoolType
			for index := 1; index <= branchCount; index++ {
				args[index] = valueType
			}
			options = append(options, cel.Function(name,
				cel.Overload(fmt.Sprintf("vibetable_%s_%s", strings.ToLower(name), idForType(valueType)), args, valueType)))
		}
	}
	// Mixed int/double branch combinations unify to double. Every such
	// pattern contains at least one branch of each numeric type.
	addMixedBranchOverloads := func(name string, branchCount int) {
		for mask := 1; mask < 1<<uint(branchCount)-1; mask++ {
			args := make([]*cel.Type, branchCount+1)
			args[0] = cel.BoolType
			id := fmt.Sprintf("vibetable_%s", strings.ToLower(name))
			for index := 0; index < branchCount; index++ {
				if mask&(1<<uint(index)) != 0 {
					args[index+1] = cel.IntType
					id += "_int"
				} else {
					args[index+1] = cel.DoubleType
					id += "_double"
				}
			}
			options = append(options, cel.Function(name, cel.Overload(id, args, cel.DoubleType)))
		}
	}
	addBranchOverloads("IF", 2, commonBranchTypeName)
	addMixedBranchOverloads("IF", 2)
	for _, valueType := range commonBranchTypes {
		options = append(options, cel.Function("IFERROR",
			cel.Overload(fmt.Sprintf("vibetable_iferror_%s", commonBranchTypeName(valueType)),
				[]*cel.Type{valueType, valueType}, valueType)))
	}
	addIfErrorMixedOverloads(&options)
	for pairs := 1; pairs <= commonIfsMaxArgs/2; pairs++ {
		for _, valueType := range commonBranchTypes {
			args := make([]*cel.Type, 0, pairs*2)
			id := fmt.Sprintf("vibetable_ifs_p%d_%s", pairs, commonBranchTypeName(valueType))
			for index := 0; index < pairs; index++ {
				args = append(args, cel.BoolType, valueType)
			}
			options = append(options, cel.Function("IFS", cel.Overload(id, args, valueType)))
		}
		if pairs < 2 {
			continue
		}
		for mask := 1; mask < 1<<uint(pairs)-1; mask++ {
			args := make([]*cel.Type, 0, pairs*2)
			id := fmt.Sprintf("vibetable_ifs_p%d", pairs)
			for index := 0; index < pairs; index++ {
				id += "_"
				if mask&(1<<uint(index)) != 0 {
					args = append(args, cel.BoolType, cel.IntType)
					id += "int"
				} else {
					args = append(args, cel.BoolType, cel.DoubleType)
					id += "double"
				}
			}
			options = append(options, cel.Function("IFS", cel.Overload(id, args, cel.DoubleType)))
		}
	}
	for arity := 2; arity <= commonVariadicMaxArgs; arity++ {
		args := make([]*cel.Type, arity)
		for index := range args {
			args[index] = cel.BoolType
		}
		options = append(options,
			cel.Function("AND", cel.Overload(fmt.Sprintf("vibetable_and_%d", arity), args, cel.BoolType)),
			cel.Function("OR", cel.Overload(fmt.Sprintf("vibetable_or_%d", arity), args, cel.BoolType)),
		)
	}
	for _, name := range []string{"MIN", "MAX"} {
		options = append(options,
			cel.Function(name,
				cel.Overload(fmt.Sprintf("vibetable_%s_common_int_int", strings.ToLower(name)),
					[]*cel.Type{cel.IntType, cel.IntType}, cel.IntType),
				cel.Overload(fmt.Sprintf("vibetable_%s_common_int_double", strings.ToLower(name)),
					[]*cel.Type{cel.IntType, cel.DoubleType}, cel.DoubleType),
				cel.Overload(fmt.Sprintf("vibetable_%s_common_double_int", strings.ToLower(name)),
					[]*cel.Type{cel.DoubleType, cel.IntType}, cel.DoubleType),
				cel.Overload(fmt.Sprintf("vibetable_%s_common_double_double", strings.ToLower(name)),
					[]*cel.Type{cel.DoubleType, cel.DoubleType}, cel.DoubleType),
			),
		)
	}
	options = append(options,
		cel.Function("ABS",
			cel.Overload("vibetable_abs_common_int", []*cel.Type{cel.IntType}, cel.IntType),
			cel.Overload("vibetable_abs_common_double", []*cel.Type{cel.DoubleType}, cel.DoubleType)),
	)
	options = append(options, commonBindingOptions()...)
	return options
}

// addIfErrorMixedOverloads declares the int/double IFERROR combinations that
// unify to double.
func addIfErrorMixedOverloads(options *[]cel.EnvOption) {
	*options = append(*options,
		cel.Function("IFERROR",
			cel.Overload("vibetable_iferror_int_double",
				[]*cel.Type{cel.IntType, cel.DoubleType}, cel.DoubleType),
			cel.Overload("vibetable_iferror_double_int",
				[]*cel.Type{cel.DoubleType, cel.IntType}, cel.DoubleType)),
	)
}

// commonBindingOptions attaches guarded runtime bindings to the eager
// uppercase functions. Every value helper is shared with the lowercase
// function set so both spellings keep identical semantics.
func commonBindingOptions() []cel.EnvOption {
	options := []cel.EnvOption{
		cel.Function("NOT",
			cel.Overload("vibetable_not_bool", []*cel.Type{cel.BoolType}, cel.BoolType,
				cel.UnaryBinding(notBoolValue))),
		cel.Function("ISBLANK",
			cel.Overload("vibetable_isblank_any", []*cel.Type{cel.DynType}, cel.BoolType,
				cel.UnaryBinding(isBlankValue))),
		// ISERROR observes its argument's runtime error, so the overload must
		// stay non-strict. Only divide-by-zero and numeric overflow errors
		// are detected; every other error keeps propagating.
		cel.Function("ISERROR",
			cel.Overload("vibetable_iserror_any", []*cel.Type{cel.DynType}, cel.BoolType,
				cel.OverloadIsNonStrict(),
				cel.UnaryBinding(isErrorValue))),
		cel.Function("LEN",
			cel.Overload("vibetable_len_string", []*cel.Type{cel.StringType}, cel.IntType,
				cel.UnaryBinding(textLengthValue))),
		cel.Function("LEFT",
			cel.Overload("vibetable_left_string_int", []*cel.Type{cel.StringType, cel.IntType}, cel.StringType,
				cel.BinaryBinding(func(text, count ref.Val) ref.Val {
					source, sourceOK := text.(types.String)
					length, lengthOK := count.(types.Int)
					if !sourceOK || !lengthOK {
						return types.NewErr("no such overload: LEFT")
					}
					return leftTextRunes(string(source), int64(length))
				}))),
		cel.Function("RIGHT",
			cel.Overload("vibetable_right_string_int", []*cel.Type{cel.StringType, cel.IntType}, cel.StringType,
				cel.BinaryBinding(func(text, count ref.Val) ref.Val {
					source, sourceOK := text.(types.String)
					length, lengthOK := count.(types.Int)
					if !sourceOK || !lengthOK {
						return types.NewErr("no such overload: RIGHT")
					}
					return rightTextRunes(string(source), int64(length))
				}))),
		cel.Function("MID",
			cel.Overload("vibetable_mid_string_int_int",
				[]*cel.Type{cel.StringType, cel.IntType, cel.IntType}, cel.StringType,
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					if len(values) != 3 {
						return types.NewErr("no such overload: MID")
					}
					source, sourceOK := values[0].(types.String)
					start, startOK := values[1].(types.Int)
					count, countOK := values[2].(types.Int)
					if !sourceOK || !startOK || !countOK {
						return types.NewErr("no such overload: MID")
					}
					return midTextRunes(string(source), int64(start), int64(count))
				}))),
		cel.Function("TRIM",
			cel.Overload("vibetable_trim_common_string", []*cel.Type{cel.StringType}, cel.StringType,
				cel.UnaryBinding(trimTextValue))),
		cel.Function("UPPER",
			cel.Overload("vibetable_upper_common_string", []*cel.Type{cel.StringType}, cel.StringType,
				cel.UnaryBinding(upperTextValue))),
		cel.Function("LOWER",
			cel.Overload("vibetable_lower_common_string", []*cel.Type{cel.StringType}, cel.StringType,
				cel.UnaryBinding(lowerTextValue))),
		cel.Function("ROUND",
			cel.Overload("vibetable_round_common_double_int",
				[]*cel.Type{cel.DoubleType, cel.IntType}, cel.DoubleType,
				cel.BinaryBinding(func(number, digits ref.Val) ref.Val {
					value, valueOK := number.(types.Double)
					precision, precisionOK := digits.(types.Int)
					if !valueOK || !precisionOK {
						return types.NewErr("no such overload: ROUND")
					}
					return roundDoubleValue(value, precision)
				})),
			cel.Overload("vibetable_round_common_int_int",
				[]*cel.Type{cel.IntType, cel.IntType}, cel.IntType,
				cel.BinaryBinding(func(number, digits ref.Val) ref.Val {
					value, valueOK := number.(types.Int)
					precision, precisionOK := digits.(types.Int)
					if !valueOK || !precisionOK {
						return types.NewErr("no such overload: ROUND")
					}
					return roundIntValue(value, precision)
				}))),
	}
	for arity := 2; arity <= commonVariadicMaxArgs; arity++ {
		arguments := make([]*cel.Type, arity)
		for index := range arguments {
			arguments[index] = cel.DynType
		}
		options = append(options,
			cel.Function("CONCATENATE",
				cel.Overload(fmt.Sprintf("vibetable_concatenate_%d", arity), arguments, cel.StringType,
					cel.FunctionBinding(concatenateTextValues))),
		)
	}
	return options
}

// Value helpers shared with the lowercase function set declared in
// functionOptions. Keeping one implementation per behavior guarantees the
// uppercase spellings never drift from the frozen lowercase semantics.

func notBoolValue(value ref.Val) ref.Val {
	if condition, ok := value.(types.Bool); ok {
		return !condition
	}
	return types.NewErr("no such overload: NOT")
}

// isBlankValue is true only for null and the empty string; zero, false and
// whitespace-only strings are values.
func isBlankValue(value ref.Val) ref.Val {
	if value == types.NullValue {
		return types.True
	}
	if text, ok := value.(types.String); ok && text == "" {
		return types.True
	}
	return types.False
}

func isErrorValue(value ref.Val) ref.Val {
	if err, ok := value.(*types.Err); ok {
		if isAbsorbableRuntimeError(err.Error()) {
			return types.True
		}
		return value
	}
	return types.False
}

func upperTextValue(value ref.Val) ref.Val {
	if text, ok := value.(types.String); ok {
		return types.String(strings.ToUpper(string(text)))
	}
	return types.NewErr("no such overload")
}

func lowerTextValue(value ref.Val) ref.Val {
	if text, ok := value.(types.String); ok {
		return types.String(strings.ToLower(string(text)))
	}
	return types.NewErr("no such overload")
}

func trimTextValue(value ref.Val) ref.Val {
	if text, ok := value.(types.String); ok {
		return types.String(strings.TrimSpace(string(text)))
	}
	return types.NewErr("no such overload")
}

func textLengthValue(value ref.Val) ref.Val {
	if text, ok := value.(types.String); ok {
		return types.Int(len([]rune(string(text))))
	}
	return types.NewErr("no such overload")
}

// concatenateTextValues mirrors the lowercase concat binding: nulls are
// skipped and every other value is stringified.
func concatenateTextValues(values ...ref.Val) ref.Val {
	var builder strings.Builder
	for _, value := range values {
		if value == types.NullValue {
			continue
		}
		builder.WriteString(fmt.Sprint(value.Value()))
	}
	return types.String(builder.String())
}

func absIntValue(number types.Int) ref.Val {
	if number == math.MinInt64 {
		return types.NewErr("integer overflow")
	}
	if number < 0 {
		return -number
	}
	return number
}

func absDoubleValue(number types.Double) ref.Val {
	return types.Double(math.Abs(float64(number)))
}

func minIntValues(left, right types.Int) types.Int {
	if left < right {
		return left
	}
	return right
}

func maxIntValues(left, right types.Int) types.Int {
	if left > right {
		return left
	}
	return right
}

func minDoubleValues(left, right types.Double) types.Double {
	if left < right {
		return left
	}
	return right
}

func maxDoubleValues(left, right types.Double) types.Double {
	if left > right {
		return left
	}
	return right
}

// roundDoubleValue applies the frozen round(double, int) semantics shared by
// round and ROUND.
func roundDoubleValue(number types.Double, digits types.Int) ref.Val {
	if digits < -15 || digits > 15 {
		return types.NewErr("round precision out of range")
	}
	factor := math.Pow10(int(digits))
	return types.Double(math.Round(float64(number)*factor) / factor)
}

// roundIntValue rounds an integer with the same precision range and
// half-away-from-zero rule; positive precision keeps the exact integer.
func roundIntValue(number, digits types.Int) ref.Val {
	if digits < -15 || digits > 15 {
		return types.NewErr("round precision out of range")
	}
	if digits >= 0 {
		return number
	}
	factor := int64(1)
	for index := int64(0); index < -int64(digits); index++ {
		factor *= 10
	}
	quotient, remainder := int64(number)/factor, int64(number)%factor
	if remainder < 0 {
		remainder = -remainder
	}
	if remainder*2 >= factor {
		if number < 0 {
			quotient--
		} else {
			quotient++
		}
	}
	result := quotient * factor
	if quotient != 0 && result/quotient != factor {
		return types.NewErr("integer overflow")
	}
	return types.Int(result)
}

func leftTextRunes(text string, count int64) ref.Val {
	if count < 0 {
		return types.NewErr("LEFT count is negative")
	}
	runes := []rune(text)
	if count >= int64(len(runes)) {
		return types.String(text)
	}
	return types.String(string(runes[:count]))
}

func rightTextRunes(text string, count int64) ref.Val {
	if count < 0 {
		return types.NewErr("RIGHT count is negative")
	}
	runes := []rune(text)
	if count >= int64(len(runes)) {
		return types.String(text)
	}
	return types.String(string(runes[int64(len(runes))-count:]))
}

// midTextRunes clamps count to the remaining length before slicing so
// start-1+count can never overflow.
func midTextRunes(text string, start, count int64) ref.Val {
	if start < 1 {
		return types.NewErr("MID start must be at least 1")
	}
	if count < 0 {
		return types.NewErr("MID count is negative")
	}
	runes := []rune(text)
	if start > int64(len(runes)) {
		return types.String("")
	}
	remaining := int64(len(runes)) - (start - 1)
	if count > remaining {
		count = remaining
	}
	return types.String(string(runes[start-1 : start-1+count]))
}

// isAbsorbableRuntimeError matches exactly the runtime failures the frozen
// contract lets IFERROR/ISERROR absorb: division by zero in both the custom
// double ("divide by zero") and native integer ("division by zero")
// spellings, plus numeric overflow ("numeric overflow", "integer overflow").
// Resource limits and cancellation never surface as argument errors (they
// abort the program as panics/errors outside the value pipeline), and
// syntax, reference and type failures use different messages, so they keep
// propagating untouched.
func isAbsorbableRuntimeError(message string) bool {
	switch message {
	case "divide by zero", "division by zero", "numeric overflow", "integer overflow":
		return true
	default:
		return false
	}
}

type commonCallKind int

const (
	commonKindIf commonCallKind = iota
	commonKindIfs
	commonKindAnd
	commonKindOr
	commonKindIfError
)

// lazyCommonCall replaces the eager call node for IF/IFS/AND/OR/IFERROR.
// Only the arguments required by the frozen short-circuit semantics are
// evaluated, strictly left to right, and any argument error propagates
// immediately. doubleResult comes from the checked AST type map: when the
// unified branch type is double, selected int branch values are widened so
// mixed int/double branches behave as one double result.
type lazyCommonCall struct {
	kind         commonCallKind
	id           int64
	function     string
	args         []interpreter.InterpretableV2
	doubleResult bool
}

func (call *lazyCommonCall) ID() int64          { return call.id }
func (call *lazyCommonCall) Function() string   { return call.function }
func (call *lazyCommonCall) OverloadID() string { return "" }
func (call *lazyCommonCall) Args() []interpreter.InterpretableV2 {
	return call.args
}

func (call *lazyCommonCall) Eval(activation interpreter.Activation) ref.Val {
	return call.eval(func(argument interpreter.InterpretableV2) ref.Val {
		return argument.Eval(activation)
	})
}

func (call *lazyCommonCall) Exec(frame *interpreter.ExecutionFrame) ref.Val {
	return call.eval(func(argument interpreter.InterpretableV2) ref.Val {
		return argument.Exec(frame)
	})
}

func (call *lazyCommonCall) branchResult(value ref.Val) ref.Val {
	if call.doubleResult {
		if number, ok := value.(types.Int); ok {
			return types.Double(float64(number))
		}
	}
	return value
}

func (call *lazyCommonCall) condition(value ref.Val) (types.Bool, ref.Val) {
	if types.IsUnknownOrError(value) {
		return false, value
	}
	condition, ok := value.(types.Bool)
	if !ok {
		return false, types.MaybeNoSuchOverloadErr(value)
	}
	return condition, nil
}

func (call *lazyCommonCall) eval(run func(interpreter.InterpretableV2) ref.Val) ref.Val {
	switch call.kind {
	case commonKindIf:
		condition, failure := call.condition(run(call.args[0]))
		if failure != nil {
			return failure
		}
		selected := call.args[2]
		if condition {
			selected = call.args[1]
		}
		return call.branchResult(run(selected))
	case commonKindIfs:
		for index := 0; index+1 < len(call.args); index += 2 {
			condition, failure := call.condition(run(call.args[index]))
			if failure != nil {
				return failure
			}
			if condition {
				return call.branchResult(run(call.args[index+1]))
			}
		}
		return types.NullValue
	case commonKindAnd, commonKindOr:
		// Strict left-to-right short-circuit: stop at the first definitive
		// value and propagate any error, unknown or non-boolean immediately.
		for _, term := range call.args {
			value := run(term)
			boolean, ok := value.(types.Bool)
			if !ok {
				return types.MaybeNoSuchOverloadErr(value)
			}
			if call.kind == commonKindAnd {
				if boolean == types.False {
					return types.False
				}
			} else if boolean == types.True {
				return types.True
			}
		}
		if call.kind == commonKindAnd {
			return types.True
		}
		return types.False
	case commonKindIfError:
		value := run(call.args[0])
		if err, ok := value.(*types.Err); ok && isAbsorbableRuntimeError(err.Error()) {
			return call.branchResult(run(call.args[1]))
		}
		return call.branchResult(value)
	}
	return types.NewErr("no such overload: %s", call.function)
}

// numericCommonCall replaces MIN/MAX/ABS nodes so dyn-typed arguments (JSON
// fields, relation values) dispatch on their runtime numeric types instead
// of failing overload resolution at plan time.
type numericCommonCall struct {
	id       int64
	function string
	args     []interpreter.InterpretableV2
}

func (call *numericCommonCall) ID() int64          { return call.id }
func (call *numericCommonCall) Function() string   { return call.function }
func (call *numericCommonCall) OverloadID() string { return "" }
func (call *numericCommonCall) Args() []interpreter.InterpretableV2 {
	return call.args
}

func (call *numericCommonCall) Eval(activation interpreter.Activation) ref.Val {
	return call.eval(func(argument interpreter.InterpretableV2) ref.Val {
		return argument.Eval(activation)
	})
}

func (call *numericCommonCall) Exec(frame *interpreter.ExecutionFrame) ref.Val {
	return call.eval(func(argument interpreter.InterpretableV2) ref.Val {
		return argument.Exec(frame)
	})
}

func (call *numericCommonCall) eval(run func(interpreter.InterpretableV2) ref.Val) ref.Val {
	if call.function == "ABS" {
		value := run(call.args[0])
		if types.IsUnknownOrError(value) {
			return value
		}
		switch number := value.(type) {
		case types.Int:
			return absIntValue(number)
		case types.Double:
			return absDoubleValue(number)
		default:
			return types.NewErr("no such overload: ABS")
		}
	}
	left := run(call.args[0])
	if types.IsUnknownOrError(left) {
		return left
	}
	right := run(call.args[1])
	if types.IsUnknownOrError(right) {
		return right
	}
	pickMin := call.function == "MIN"
	switch leftValue := left.(type) {
	case types.Int:
		switch rightValue := right.(type) {
		case types.Int:
			if pickMin {
				return minIntValues(leftValue, rightValue)
			}
			return maxIntValues(leftValue, rightValue)
		case types.Double:
			if (float64(leftValue) < float64(rightValue)) == pickMin {
				return types.Double(float64(leftValue))
			}
			return rightValue
		}
	case types.Double:
		switch rightValue := right.(type) {
		case types.Int:
			if (float64(leftValue) < float64(rightValue)) == pickMin {
				return leftValue
			}
			return types.Double(float64(rightValue))
		case types.Double:
			if pickMin {
				return minDoubleValues(leftValue, rightValue)
			}
			return maxDoubleValues(leftValue, rightValue)
		}
	}
	return types.NewErr("no such overload: %s", call.function)
}

// commonFunctionDecorator replaces the control-flow and numeric dispatch
// call nodes. It must be registered before the finiteFormulaCall decorator so
// the latter keeps wrapping the replacement and the cost observer stays the
// outermost layer of every node.
func commonFunctionDecorator(resultTypes map[int64]*exprpb.Type) interpreter.InterpretableDecoratorV2 {
	return func(node interpreter.InterpretableV2) (interpreter.InterpretableV2, error) {
		call, ok := node.(interpreter.InterpretableCall)
		if !ok {
			return node, nil
		}
		args := call.Args()
		doubleResult := false
		if resultType := resultTypes[call.ID()]; resultType != nil {
			doubleResult = resultType.GetPrimitive() == exprpb.Type_DOUBLE
		}
		switch call.Function() {
		case "IF":
			if len(args) == 3 {
				return &lazyCommonCall{
					kind: commonKindIf, id: call.ID(), function: "IF",
					args: args, doubleResult: doubleResult,
				}, nil
			}
		case "IFS":
			if len(args) >= 2 && len(args)%2 == 0 && len(args) <= commonIfsMaxArgs {
				return &lazyCommonCall{
					kind: commonKindIfs, id: call.ID(), function: "IFS",
					args: args, doubleResult: doubleResult,
				}, nil
			}
		case "AND":
			if len(args) >= 2 && len(args) <= commonVariadicMaxArgs {
				return &lazyCommonCall{
					kind: commonKindAnd, id: call.ID(), function: "AND", args: args,
				}, nil
			}
		case "OR":
			if len(args) >= 2 && len(args) <= commonVariadicMaxArgs {
				return &lazyCommonCall{
					kind: commonKindOr, id: call.ID(), function: "OR", args: args,
				}, nil
			}
		case "IFERROR":
			if len(args) == 2 {
				return &lazyCommonCall{
					kind: commonKindIfError, id: call.ID(), function: "IFERROR",
					args: args, doubleResult: doubleResult,
				}, nil
			}
		case "MIN", "MAX":
			if len(args) == 2 {
				return &numericCommonCall{id: call.ID(), function: call.Function(), args: args}, nil
			}
		case "ABS":
			if len(args) == 1 {
				return &numericCommonCall{id: call.ID(), function: "ABS", args: args}, nil
			}
		}
		return node, nil
	}
}
