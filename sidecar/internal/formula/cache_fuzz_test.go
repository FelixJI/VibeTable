package formula

import (
	"context"
	"reflect"
	"testing"
	"unicode/utf8"
)

// Compare cached results against pure compilation while changing the source at
// the same schema revision, then revisiting the first draft.
func FuzzPlanCachePreservesDraftSemantics(f *testing.F) {
	f.Add("value + 1.0", "value + 2.0", int16(7))
	f.Add("missing", "value * 2.0", int16(-3))
	f.Add("value / 0.0", "value", int16(0))
	f.Add("true ? value : 1.0", "value +", int16(10))
	f.Fuzz(func(t *testing.T, first, second string, input int16) {
		if len(first) > 256 || len(second) > 256 || !utf8.ValidString(first) || !utf8.ValidString(second) {
			return
		}
		compiler := NewCompiler(DefaultLimits())
		for _, source := range []string{first, second, first} {
			definition := formulaTable(
				scalarField("value_id", "value", numberType),
				formulaField("result_id", "result", numberType, source),
			)
			expected, expectedErr := compiler.compileExecutionTable(definition)
			actual, actualErr := compiler.CompileExecutionTable(definition)
			if !reflect.DeepEqual(actualErr, expectedErr) {
				t.Fatalf("source %q: cached error %#v, pure error %#v", source, actualErr, expectedErr)
			}
			if expectedErr != nil {
				continue
			}
			bindings := map[string]any{"value": float64(input)}
			expectedValues, expectedEvalErr := expected.Evaluate(context.Background(), bindings, nil)
			actualValues, actualEvalErr := actual.Evaluate(context.Background(), bindings, nil)
			if !reflect.DeepEqual(actualValues, expectedValues) || !reflect.DeepEqual(actualEvalErr, expectedEvalErr) {
				t.Fatalf("source %q: cached (%#v, %#v), pure (%#v, %#v)", source,
					actualValues, actualEvalErr, expectedValues, expectedEvalErr)
			}
		}
	})
}
