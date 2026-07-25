package formula

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

const (
	DefaultSourceLimit    = 4096
	DefaultASTNodeLimit   = 512
	DefaultRecursionLimit = 64
	DefaultCollectionMax  = 1024
	DefaultCostLimit      = 10_000
	DefaultEvalTimeout    = 50 * time.Millisecond
)

type Limits struct {
	SourceBytes    int
	ASTNodes       int
	RecursionDepth int
	CollectionSize int
	Cost           uint64
	EvalTimeout    time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		SourceBytes:    DefaultSourceLimit,
		ASTNodes:       DefaultASTNodeLimit,
		RecursionDepth: DefaultRecursionLimit,
		CollectionSize: DefaultCollectionMax,
		Cost:           DefaultCostLimit,
		EvalTimeout:    DefaultEvalTimeout,
	}
}

type Compiler struct {
	limits Limits
}

func NewCompiler(limits Limits) *Compiler {
	defaults := DefaultLimits()
	if limits.SourceBytes <= 0 {
		limits.SourceBytes = defaults.SourceBytes
	}
	if limits.ASTNodes <= 0 {
		limits.ASTNodes = defaults.ASTNodes
	}
	if limits.RecursionDepth <= 0 {
		limits.RecursionDepth = defaults.RecursionDepth
	}
	if limits.CollectionSize <= 0 {
		limits.CollectionSize = defaults.CollectionSize
	}
	if limits.Cost == 0 {
		limits.Cost = defaults.Cost
	}
	if limits.EvalTimeout <= 0 {
		limits.EvalTimeout = defaults.EvalTimeout
	}
	return &Compiler{limits: limits}
}

type CompiledFormula struct {
	FieldID         string
	PhysicalName    string
	ResultType      schema.DataType
	Nullable        bool
	Source          string
	CanonicalSource string
	ASTHash         string
	Dependencies    []string
	ReferencePaths  []string
	dependencyNames []string
	program         cel.Program
	limits          Limits
	hasDivision     bool
}

func (compiler *Compiler) Compile(
	definition schema.TableDefinition,
	field schema.FieldDefinition,
) (*CompiledFormula, *Error) {
	if field.Kind != schema.FieldKindFormula || field.Formula == nil {
		return nil, formulaError("formula.type", "field is not a formula field", nil)
	}
	if field.Formula.Language != "cel-v1" {
		return nil, formulaError("formula.type", "unsupported formula language", map[string]any{
			"language": field.Formula.Language,
		})
	}
	source := strings.TrimSpace(field.Formula.Source)
	if source == "" {
		return nil, formulaError("formula.syntax", "formula source is empty", nil)
	}
	if len(source) > compiler.limits.SourceBytes {
		return nil, formulaError("formula.resource_limit", "formula source exceeds the size limit", map[string]any{
			"limit": compiler.limits.SourceBytes,
		})
	}

	fieldsByName := make(map[string]schema.FieldDefinition, len(definition.Fields))
	fieldsByID := make(map[string]schema.FieldDefinition, len(definition.Fields))
	options := []cel.EnvOption{
		cel.ParserExpressionSizeLimit(compiler.limits.SourceBytes),
		cel.ParserRecursionLimit(compiler.limits.RecursionDepth),
		cel.DefaultUTCTimeZone(true),
		cel.HomogeneousAggregateLiterals(),
	}
	for _, candidate := range definition.Fields {
		if candidate.DataType == schema.DataTypeSecret ||
			candidate.DataType == schema.DataTypeHash ||
			candidate.Kind == schema.FieldKindAttachment ||
			candidate.Kind == schema.FieldKindSystem {
			continue
		}
		fieldType, err := celTypeForField(candidate)
		if err != nil {
			continue
		}
		fieldsByName[candidate.PhysicalName] = candidate
		fieldsByID[candidate.FieldID] = candidate
		options = append(options, cel.Variable(candidate.PhysicalName, fieldType))
	}
	options = append(options, functionOptions()...)

	env, err := cel.NewEnv(options...)
	if err != nil {
		return nil, formulaError("formula.runtime", "formula environment initialization failed", nil)
	}
	parsed, issues := env.Parse(source)
	if issues != nil && issues.Err() != nil {
		return nil, formulaError("formula.syntax", "formula could not be parsed", map[string]any{
			"reason": issues.Err().Error(),
		})
	}
	ast, issues := env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		code := "formula.type"
		message := "formula could not be type checked"
		if strings.Contains(issues.Err().Error(), "undeclared reference") {
			code = "formula.dependency"
			message = "formula references an unknown field or function"
		}
		return nil, formulaError(code, message, map[string]any{
			"reason": issues.Err().Error(),
		})
	}
	checked, err := cel.AstToCheckedExpr(ast)
	if err != nil {
		return nil, formulaError("formula.runtime", "checked formula AST is unavailable", nil)
	}
	nodeCount, dependencyNames, referencePaths, hasDivision, validationErr := inspectExpression(
		checked.GetExpr(), fieldsByName, compiler.limits,
	)
	if validationErr != nil {
		return nil, validationErr
	}
	if nodeCount > compiler.limits.ASTNodes {
		return nil, formulaError("formula.resource_limit", "formula AST exceeds the node limit", map[string]any{
			"limit": compiler.limits.ASTNodes,
		})
	}

	expected, err := celTypeForDataType(field.Formula.ResultType)
	if err != nil {
		return nil, formulaError("formula.type", "formula result type is unsupported", map[string]any{
			"resultType": field.Formula.ResultType,
		})
	}
	if output := ast.OutputType(); output != cel.DynType && !expected.IsAssignableType(output) {
		return nil, formulaError("formula.type", "formula result type does not match the field declaration", map[string]any{
			"expected": expected.String(),
			"actual":   output.String(),
		})
	}

	canonical, err := cel.AstToString(ast)
	if err != nil {
		return nil, formulaError("formula.runtime", "formula AST could not be normalized", nil)
	}
	hashInput := "cel-v1\x00" + canonical + "\x00" + string(field.Formula.ResultType)
	sum := sha256.Sum256([]byte(hashInput))
	dependencies := make([]string, 0, len(dependencyNames))
	for _, name := range dependencyNames {
		dependencies = append(dependencies, fieldsByName[name].FieldID)
	}
	sort.Strings(dependencies)

	program, err := env.Program(
		ast,
		cel.CostLimit(compiler.limits.Cost),
		cel.InterruptCheckFrequency(32),
	)
	if err != nil {
		return nil, formulaError("formula.runtime", "formula program could not be created", nil)
	}
	_ = fieldsByID
	return &CompiledFormula{
		FieldID:         field.FieldID,
		PhysicalName:    field.PhysicalName,
		ResultType:      field.Formula.ResultType,
		Nullable:        field.Nullable,
		Source:          source,
		CanonicalSource: canonical,
		ASTHash:         hex.EncodeToString(sum[:]),
		Dependencies:    dependencies,
		ReferencePaths:  referencePaths,
		dependencyNames: dependencyNames,
		program:         program,
		limits:          compiler.limits,
		hasDivision:     hasDivision,
	}, nil
}

func inspectExpression(
	expression *exprpb.Expr,
	fields map[string]schema.FieldDefinition,
	limits Limits,
) (int, []string, []string, bool, *Error) {
	dependencies := map[string]struct{}{}
	references := map[string]struct{}{}
	nodes := 0
	hasDivision := false
	var walk func(*exprpb.Expr) *Error
	walk = func(current *exprpb.Expr) *Error {
		if current == nil {
			return nil
		}
		nodes++
		if nodes > limits.ASTNodes {
			return formulaError("formula.resource_limit", "formula AST exceeds the node limit", map[string]any{
				"limit": limits.ASTNodes,
			})
		}
		switch kind := current.ExprKind.(type) {
		case *exprpb.Expr_IdentExpr:
			if _, exists := fields[kind.IdentExpr.Name]; exists {
				dependencies[kind.IdentExpr.Name] = struct{}{}
			}
		case *exprpb.Expr_SelectExpr:
			if path, ok := staticSelectPath(current); ok {
				references[path] = struct{}{}
			}
			return walk(kind.SelectExpr.Operand)
		case *exprpb.Expr_CallExpr:
			if _, allowed := allowedFunctions[kind.CallExpr.Function]; !allowed {
				return formulaError("formula.dependency", "formula function is not allowed", map[string]any{
					"function": kind.CallExpr.Function,
				})
			}
			if kind.CallExpr.Function == "_/_" {
				hasDivision = true
			}
			if kind.CallExpr.Function == "_[_]" && len(kind.CallExpr.Args) == 2 {
				if _, ok := kind.CallExpr.Args[1].ExprKind.(*exprpb.Expr_ConstExpr); !ok {
					return formulaError("formula.dependency", "dynamic field or JSON indexing is not allowed", nil)
				}
			}
			if err := walk(kind.CallExpr.Target); err != nil {
				return err
			}
			for _, argument := range kind.CallExpr.Args {
				if err := walk(argument); err != nil {
					return err
				}
			}
		case *exprpb.Expr_ListExpr:
			if len(kind.ListExpr.Elements) > limits.CollectionSize {
				return formulaError("formula.resource_limit", "formula list literal exceeds the collection limit", nil)
			}
			for _, element := range kind.ListExpr.Elements {
				if err := walk(element); err != nil {
					return err
				}
			}
		case *exprpb.Expr_StructExpr:
			if len(kind.StructExpr.Entries) > limits.CollectionSize {
				return formulaError("formula.resource_limit", "formula map literal exceeds the collection limit", nil)
			}
			for _, entry := range kind.StructExpr.Entries {
				if err := walk(entry.GetMapKey()); err != nil {
					return err
				}
				if err := walk(entry.GetValue()); err != nil {
					return err
				}
			}
		case *exprpb.Expr_ComprehensionExpr:
			return formulaError("formula.resource_limit", "formula comprehensions are not allowed", nil)
		}
		return nil
	}
	if err := walk(expression); err != nil {
		return nodes, nil, nil, hasDivision, err
	}
	names := make([]string, 0, len(dependencies))
	for name := range dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	paths := make([]string, 0, len(references))
	for path := range references {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return nodes, names, paths, hasDivision, nil
}

func staticSelectPath(expression *exprpb.Expr) (string, bool) {
	selectExpression, ok := expression.ExprKind.(*exprpb.Expr_SelectExpr)
	if !ok {
		return "", false
	}
	parts := []string{selectExpression.SelectExpr.Field}
	current := selectExpression.SelectExpr.Operand
	for current != nil {
		switch kind := current.ExprKind.(type) {
		case *exprpb.Expr_IdentExpr:
			parts = append(parts, kind.IdentExpr.Name)
			for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
				parts[left], parts[right] = parts[right], parts[left]
			}
			return strings.Join(parts, "."), true
		case *exprpb.Expr_SelectExpr:
			parts = append(parts, kind.SelectExpr.Field)
			current = kind.SelectExpr.Operand
		default:
			return "", false
		}
	}
	return "", false
}

var allowedFunctions = map[string]struct{}{
	"_+_": {}, "_-_": {}, "_*_": {}, "_/_": {}, "_%_": {},
	"_==_": {}, "_!=_": {}, "_<_": {}, "_<=_": {}, "_>_": {}, "_>=_": {},
	"_&&_": {}, "_||_": {}, "!_": {}, "-_": {}, "_?_:_": {},
	"_[_]": {}, "@in": {},
	"string": {}, "int": {}, "double": {}, "bool": {},
	"timestamp": {}, "duration": {}, "size": {},
	"upper": {}, "lower": {}, "trim": {}, "length": {},
	"abs": {}, "round": {}, "min": {}, "max": {},
	"concat": {}, "coalesce": {},
	"dateAdd": {}, "dateSubtract": {}, "formatDate": {},
}

func celTypeForField(field schema.FieldDefinition) (*cel.Type, error) {
	if field.Kind == schema.FieldKindFormula && field.Formula != nil {
		return celTypeForDataType(field.Formula.ResultType)
	}
	if field.Kind == schema.FieldKindRelation {
		// Relation values are resolved to immutable, schema-filtered objects by
		// Calculator before evaluation. The exact target object shape belongs to
		// the target table schema, so CEL must treat it as dynamic while the
		// schema catalog performs the static cross-table checks.
		return cel.DynType, nil
	}
	if field.Kind == schema.FieldKindLookup {
		return celTypeForStorageType(field.StorageType)
	}
	return celTypeForDataType(field.DataType)
}

func celTypeForStorageType(storage schema.StorageType) (*cel.Type, error) {
	switch storage {
	case schema.StorageBool:
		return cel.BoolType, nil
	case schema.StorageNumber:
		return cel.DoubleType, nil
	case schema.StorageDate, schema.StorageAutodate:
		return cel.TimestampType, nil
	case schema.StorageJSON, schema.StorageRelation, schema.StorageFile,
		schema.StorageGeoPoint, schema.StorageSelect:
		return cel.DynType, nil
	case schema.StorageText, schema.StorageEditor, schema.StorageEmail,
		schema.StorageURL:
		return cel.StringType, nil
	default:
		return nil, fmt.Errorf("unsupported formula storage type %s", storage)
	}
}

func celTypeForDataType(dataType schema.DataType) (*cel.Type, error) {
	switch dataType {
	case schema.DataTypeBoolean:
		return cel.BoolType, nil
	case schema.DataTypeInteger:
		return cel.IntType, nil
	case schema.DataTypeFloat, schema.DataTypeDecimal:
		return cel.DoubleType, nil
	case schema.DataTypeDate, schema.DataTypeDateTime, schema.DataTypeAutoDate:
		return cel.TimestampType, nil
	case schema.DataTypeMultiSelect, schema.DataTypeList:
		return cel.ListType(cel.DynType), nil
	case schema.DataTypeJSON, schema.DataTypeGeoPoint, schema.DataTypeGeoJSON:
		return cel.DynType, nil
	case schema.DataTypeShortText, schema.DataTypeLongText, schema.DataTypeRichText,
		schema.DataTypeTime, schema.DataTypeEmail, schema.DataTypeURL, schema.DataTypeUUID,
		schema.DataTypeSelect, schema.DataTypeHash:
		return cel.StringType, nil
	default:
		return nil, fmt.Errorf("unsupported formula data type %s", dataType)
	}
}

func functionOptions() []cel.EnvOption {
	options := []cel.EnvOption{
		cel.Function("upper",
			cel.Overload("vibetable_upper_string", []*cel.Type{cel.StringType}, cel.StringType,
				cel.UnaryBinding(func(value ref.Val) ref.Val {
					return types.String(strings.ToUpper(string(value.(types.String))))
				}))),
		cel.Function("lower",
			cel.Overload("vibetable_lower_string", []*cel.Type{cel.StringType}, cel.StringType,
				cel.UnaryBinding(func(value ref.Val) ref.Val {
					return types.String(strings.ToLower(string(value.(types.String))))
				}))),
		cel.Function("trim",
			cel.Overload("vibetable_trim_string", []*cel.Type{cel.StringType}, cel.StringType,
				cel.UnaryBinding(func(value ref.Val) ref.Val {
					return types.String(strings.TrimSpace(string(value.(types.String))))
				}))),
		cel.Function("length",
			cel.Overload("vibetable_length_string", []*cel.Type{cel.StringType}, cel.IntType,
				cel.UnaryBinding(func(value ref.Val) ref.Val {
					return types.Int(len([]rune(string(value.(types.String)))))
				})),
			cel.Overload("vibetable_length_list", []*cel.Type{cel.ListType(cel.DynType)}, cel.IntType,
				cel.UnaryBinding(func(value ref.Val) ref.Val {
					return value.(traits.Sizer).Size()
				}))),
		cel.Function("abs",
			cel.Overload("vibetable_abs_int", []*cel.Type{cel.IntType}, cel.IntType,
				cel.UnaryBinding(func(value ref.Val) ref.Val {
					number := int64(value.(types.Int))
					if number == math.MinInt64 {
						return types.NewErr("integer overflow")
					}
					if number < 0 {
						number = -number
					}
					return types.Int(number)
				})),
			cel.Overload("vibetable_abs_double", []*cel.Type{cel.DoubleType}, cel.DoubleType,
				cel.UnaryBinding(func(value ref.Val) ref.Val {
					return types.Double(math.Abs(float64(value.(types.Double))))
				}))),
		cel.Function("round",
			cel.Overload("vibetable_round_double_int", []*cel.Type{cel.DoubleType, cel.IntType}, cel.DoubleType,
				cel.BinaryBinding(func(left, right ref.Val) ref.Val {
					digits := int64(right.(types.Int))
					if digits < -15 || digits > 15 {
						return types.NewErr("round precision out of range")
					}
					factor := math.Pow10(int(digits))
					return types.Double(math.Round(float64(left.(types.Double))*factor) / factor)
				}))),
		cel.Function("min",
			cel.Overload("vibetable_min_int", []*cel.Type{cel.IntType, cel.IntType}, cel.IntType,
				cel.BinaryBinding(func(left, right ref.Val) ref.Val {
					if left.(types.Int) < right.(types.Int) {
						return left
					}
					return right
				})),
			cel.Overload("vibetable_min_double", []*cel.Type{cel.DoubleType, cel.DoubleType}, cel.DoubleType,
				cel.BinaryBinding(func(left, right ref.Val) ref.Val {
					if left.(types.Double) < right.(types.Double) {
						return left
					}
					return right
				}))),
		cel.Function("max",
			cel.Overload("vibetable_max_int", []*cel.Type{cel.IntType, cel.IntType}, cel.IntType,
				cel.BinaryBinding(func(left, right ref.Val) ref.Val {
					if left.(types.Int) > right.(types.Int) {
						return left
					}
					return right
				})),
			cel.Overload("vibetable_max_double", []*cel.Type{cel.DoubleType, cel.DoubleType}, cel.DoubleType,
				cel.BinaryBinding(func(left, right ref.Val) ref.Val {
					if left.(types.Double) > right.(types.Double) {
						return left
					}
					return right
				}))),
		cel.Function("dateAdd",
			cel.Overload(
				"vibetable_date_add_timestamp_duration",
				[]*cel.Type{cel.TimestampType, cel.DurationType},
				cel.TimestampType,
				cel.BinaryBinding(func(left, right ref.Val) ref.Val {
					timestamp := left.(types.Timestamp)
					duration := right.(types.Duration)
					return types.Timestamp{Time: timestamp.Time.Add(duration.Duration).UTC()}
				}),
			)),
		cel.Function("dateSubtract",
			cel.Overload(
				"vibetable_date_subtract_timestamp_duration",
				[]*cel.Type{cel.TimestampType, cel.DurationType},
				cel.TimestampType,
				cel.BinaryBinding(func(left, right ref.Val) ref.Val {
					timestamp := left.(types.Timestamp)
					duration := right.(types.Duration)
					return types.Timestamp{Time: timestamp.Time.Add(-duration.Duration).UTC()}
				}),
			)),
		cel.Function("formatDate",
			cel.Overload(
				"vibetable_format_date_timestamp_string",
				[]*cel.Type{cel.TimestampType, cel.StringType},
				cel.StringType,
				cel.BinaryBinding(func(left, right ref.Val) ref.Val {
					layout, ok := dateFormatLayouts[string(right.(types.String))]
					if !ok {
						return types.NewErr("unsupported date format")
					}
					return types.String(left.(types.Timestamp).Time.UTC().Format(layout))
				}),
			)),
	}
	for arity := 2; arity <= 8; arity++ {
		arguments := make([]*cel.Type, arity)
		for index := range arguments {
			arguments[index] = cel.DynType
		}
		options = append(options,
			cel.Function("concat",
				cel.Overload(fmt.Sprintf("vibetable_concat_%d", arity), arguments, cel.StringType,
					cel.FunctionBinding(func(values ...ref.Val) ref.Val {
						var builder strings.Builder
						for _, value := range values {
							if value == types.NullValue {
								continue
							}
							builder.WriteString(fmt.Sprint(value.Value()))
						}
						return types.String(builder.String())
					}))),
			cel.Function("coalesce",
				cel.Overload(fmt.Sprintf("vibetable_coalesce_%d", arity), arguments, cel.DynType,
					cel.FunctionBinding(func(values ...ref.Val) ref.Val {
						for _, value := range values {
							if value != types.NullValue {
								return value
							}
						}
						return types.NullValue
					}))),
		)
	}
	return options
}

var dateFormatLayouts = map[string]string{
	"yyyy-MM-dd":                   "2006-01-02",
	"yyyy-MM-dd HH:mm:ss":          "2006-01-02 15:04:05",
	"yyyy-MM-dd'T'HH:mm:ss'Z'":     "2006-01-02T15:04:05Z",
	"yyyy-MM-dd'T'HH:mm:ss.SSS'Z'": "2006-01-02T15:04:05.000Z",
}
