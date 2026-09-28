package formula

import (
	"context"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestCalendarFormulaSemanticGoldens covers the frozen calendar semantics
// through the production compiler and plan evaluation boundary: strict DATE
// validity, timezone-aware YEAR/MONTH/DAY, DATEADD month-end clamp and DST
// handling, symmetric full-unit DATEDIFF without duration overflow, and the
// closed TEXT pattern sets with half-away-from-zero rounding.
func TestCalendarFormulaSemanticGoldens(t *testing.T) {
	tests := []struct {
		name       string
		inputType  ValueType
		resultType ValueType
		source     string
		input      any
		nullable   bool
		want       any
		wantCode   string
	}{
		// DATE: strict Gregorian validity, no carry, years 1-9999.
		{name: "DATE builds UTC midnight", resultType: dateTimeType, source: "DATE(2024, 2, 29)", want: "2024-02-29T00:00:00Z"},
		{name: "DATE lower year bound", resultType: dateTimeType, source: "DATE(1, 1, 1)", want: "0001-01-01T00:00:00Z"},
		{name: "DATE upper year bound", resultType: dateTimeType, source: "DATE(9999, 12, 31)", want: "9999-12-31T00:00:00Z"},
		{name: "DATE rejects non-leap February 29", resultType: dateTimeType, source: "DATE(2023, 2, 29)", wantCode: "formula.runtime"},
		{name: "DATE rejects century non-leap", resultType: dateTimeType, source: "DATE(1900, 2, 29)", wantCode: "formula.runtime"},
		{name: "DATE accepts 400-year leap", resultType: dateTimeType, source: "DATE(2000, 2, 29)", want: "2000-02-29T00:00:00Z"},
		{name: "DATE rejects month 13 without carry", resultType: dateTimeType, source: "DATE(2024, 13, 1)", wantCode: "formula.runtime"},
		{name: "DATE rejects day 32 without carry", resultType: dateTimeType, source: "DATE(2024, 1, 32)", wantCode: "formula.runtime"},
		{name: "DATE rejects April 31", resultType: dateTimeType, source: "DATE(2024, 4, 31)", wantCode: "formula.runtime"},
		{name: "DATE rejects year zero", resultType: dateTimeType, source: "DATE(0, 1, 1)", wantCode: "formula.overflow"},
		{name: "DATE rejects year 10000", resultType: dateTimeType, source: "DATE(10000, 1, 1)", wantCode: "formula.overflow"},
		{name: "DATE rejects month zero", resultType: dateTimeType, source: "DATE(2024, 0, 1)", wantCode: "formula.runtime"},
		{name: "DATE rejects negative day", resultType: dateTimeType, source: "DATE(2024, 1, -1)", wantCode: "formula.runtime"},
		{name: "DATE rejects double year", resultType: dateTimeType, source: "DATE(2024.0, 1, 1)", wantCode: "formula.type"},
		{name: "DATE rejects two arguments", resultType: dateTimeType, source: "DATE(2024, 1)", wantCode: "formula.type"},

		// YEAR/MONTH/DAY: default UTC, explicit UTC, IANA overrides.
		{name: "YEAR extracts calendar year", resultType: integerType, source: "YEAR(DATE(2024, 5, 6))", want: int64(2024)},
		{name: "MONTH extracts calendar month", resultType: integerType, source: "MONTH(DATE(2024, 5, 6))", want: int64(5)},
		{name: "DAY extracts calendar day", resultType: integerType, source: "DAY(DATE(2024, 5, 6))", want: int64(6)},
		{name: "YEAR Shanghai enters new year first", inputType: dateTimeType, resultType: integerType, source: `YEAR(value, "Asia/Shanghai")`, input: "2023-12-31T23:30:00Z", want: int64(2024)},
		{name: "YEAR default UTC stays in old year", inputType: dateTimeType, resultType: integerType, source: "YEAR(value)", input: "2023-12-31T23:30:00Z", want: int64(2023)},
		{name: "MONTH New York falls back a day", inputType: dateTimeType, resultType: integerType, source: `MONTH(value, "America/New_York")`, input: "2024-03-01T00:30:00Z", want: int64(2)},
		{name: "DAY Tokyo advances a day", inputType: dateTimeType, resultType: integerType, source: `DAY(value, "Asia/Tokyo")`, input: "2024-01-01T20:00:00Z", want: int64(2)},
		{name: "DAY default UTC keeps day", inputType: dateTimeType, resultType: integerType, source: "DAY(value)", input: "2024-01-01T20:00:00Z", want: int64(1)},
		{name: "MONTH explicit UTC matches default", inputType: dateTimeType, resultType: integerType, source: `MONTH(value, "UTC")`, input: "2024-03-01T00:30:00Z", want: int64(3)},
		{name: "YEAR unknown timezone errors", inputType: dateTimeType, resultType: integerType, source: `YEAR(value, "Not/AZone")`, input: "2024-03-01T00:30:00Z", wantCode: "formula.runtime"},
		{name: "YEAR rejects double argument", resultType: integerType, source: "YEAR(1.5)", wantCode: "formula.type"},
		{name: "DAY rejects non-string timezone", resultType: integerType, source: "DAY(DATE(2024, 1, 1), 5)", wantCode: "formula.type"},
		{name: "MONTH rejects three arguments", resultType: integerType, source: `MONTH(DATE(2024, 1, 1), "UTC", "UTC")`, wantCode: "formula.type"},

		// DATEADD: month-end clamp, wall-clock day shifts, DST gap errors.
		{name: "DATEADD month clamps to month end", resultType: dateTimeType, source: `DATEADD(DATE(2024, 1, 31), 1, "month")`, want: "2024-02-29T00:00:00Z"},
		{name: "DATEADD year clamps February 29", resultType: dateTimeType, source: `DATEADD(DATE(2024, 2, 29), 1, "year")`, want: "2025-02-28T00:00:00Z"},
		{name: "DATEADD year keeps leap day in leap year", resultType: dateTimeType, source: `DATEADD(DATE(2024, 2, 29), 4, "year")`, want: "2028-02-29T00:00:00Z"},
		{name: "DATEADD negative month clamps", resultType: dateTimeType, source: `DATEADD(DATE(2024, 3, 31), -1, "month")`, want: "2024-02-29T00:00:00Z"},
		{name: "DATEADD month rolls into next year", resultType: dateTimeType, source: `DATEADD(DATE(2024, 12, 15), 2, "month")`, want: "2025-02-15T00:00:00Z"},
		{name: "DATEADD day default UTC", resultType: dateTimeType, source: `DATEADD(timestamp("2024-01-01T00:00:00Z"), 90, "day")`, want: "2024-03-31T00:00:00Z"},
		{name: "DATEADD day keeps wall clock across DST end", resultType: dateTimeType, source: `DATEADD(timestamp("2024-11-02T06:30:00Z"), 2, "day", "America/New_York")`, want: "2024-11-04T07:30:00Z"},
		{name: "DATEADD negative day keeps wall clock across DST start", resultType: dateTimeType, source: `DATEADD(timestamp("2024-03-10T07:30:00Z"), -1, "day", "America/New_York")`, want: "2024-03-09T08:30:00Z"},
		{name: "DATEADD day into DST gap errors", resultType: dateTimeType, source: `DATEADD(timestamp("2024-03-09T07:30:00Z"), 1, "day", "America/New_York")`, wantCode: "formula.runtime"},
		{name: "DATEADD half-hour DST gap errors", resultType: dateTimeType, source: `DATEADD(timestamp("2024-10-04T15:45:00Z"), 1, "day", "Australia/Lord_Howe")`, wantCode: "formula.runtime"},
		{name: "DATEADD zero snaps overlap to earlier instant", resultType: dateTimeType, source: `DATEADD(timestamp("2024-11-03T06:30:00Z"), 0, "day", "America/New_York")`, want: "2024-11-03T05:30:00Z"},
		{name: "DATEADD half-hour fall-back picks earlier instant", resultType: dateTimeType, source: `DATEADD(timestamp("2024-04-06T15:15:00Z"), 0, "day", "Australia/Lord_Howe")`, want: "2024-04-06T14:45:00Z"},
		{name: "DATEADD Shanghai has no DST", resultType: dateTimeType, source: `DATEADD(timestamp("2024-03-09T18:30:00Z"), 1, "day", "Asia/Shanghai")`, want: "2024-03-10T18:30:00Z"},
		{name: "DATEADD zero keeps time of day", resultType: dateTimeType, source: `DATEADD(timestamp("2024-06-01T12:34:56Z"), 0, "day")`, want: "2024-06-01T12:34:56Z"},
		{name: "DATEADD beyond year 9999 errors", resultType: dateTimeType, source: `DATEADD(DATE(9999, 12, 31), 1, "day")`, wantCode: "formula.overflow"},
		{name: "DATEADD before year 1 errors", resultType: dateTimeType, source: `DATEADD(DATE(1, 1, 1), -1, "day")`, wantCode: "formula.overflow"},
		{name: "DATEADD month beyond year 9999 errors", resultType: dateTimeType, source: `DATEADD(DATE(9999, 1, 1), 12, "month")`, wantCode: "formula.overflow"},
		{name: "DATEADD unknown unit errors", resultType: dateTimeType, source: `DATEADD(DATE(2024, 1, 1), 1, "week")`, wantCode: "formula.runtime"},
		{name: "DATEADD unit case is significant", resultType: dateTimeType, source: `DATEADD(DATE(2024, 1, 1), 1, "Month")`, wantCode: "formula.runtime"},
		{name: "DATEADD amount bound errors", resultType: dateTimeType, source: `DATEADD(DATE(2024, 1, 1), 5000000, "day")`, wantCode: "formula.overflow"},
		{name: "DATEADD missing unit is a type error", resultType: dateTimeType, source: "DATEADD(DATE(2024, 1, 1), 1)", wantCode: "formula.type"},
		{name: "DATEADD rejects double amount", resultType: dateTimeType, source: `DATEADD(DATE(2024, 1, 1), 1.5, "day")`, wantCode: "formula.type"},
		{name: "DATEADD rejects duration mix", resultType: dateTimeType, source: `DATEADD(DATE(2024, 1, 1), duration("24h"))`, wantCode: "formula.type"},

		// DATEDIFF: calendar boundaries, symmetric signed full units.
		{name: "DATEDIFF day counts local calendar boundaries", resultType: integerType, source: `DATEDIFF(timestamp("2024-01-01T23:00:00Z"), timestamp("2024-01-02T01:00:00Z"), "day")`, want: int64(1)},
		{name: "DATEDIFF day reversed negates", resultType: integerType, source: `DATEDIFF(timestamp("2024-01-02T01:00:00Z"), timestamp("2024-01-01T23:00:00Z"), "day")`, want: int64(-1)},
		{name: "DATEDIFF same instant is zero", resultType: integerType, source: `DATEDIFF(DATE(2024, 5, 6), DATE(2024, 5, 6), "day")`, want: int64(0)},
		{name: "DATEDIFF day respects timezone", resultType: integerType, source: `DATEDIFF(timestamp("2024-01-01T14:00:00Z"), timestamp("2024-01-01T16:00:00Z"), "day", "Asia/Tokyo")`, want: int64(1)},
		{name: "DATEDIFF day default UTC stays same day", resultType: integerType, source: `DATEDIFF(timestamp("2024-01-01T14:00:00Z"), timestamp("2024-01-01T16:00:00Z"), "day")`, want: int64(0)},
		{name: "DATEDIFF full 1-9999 day span avoids duration overflow", resultType: integerType, source: `DATEDIFF(DATE(1, 1, 1), DATE(9999, 12, 31), "day")`, want: int64(3652058)},
		{name: "DATEDIFF month full units", resultType: integerType, source: `DATEDIFF(DATE(2024, 1, 15), DATE(2024, 3, 14), "month")`, want: int64(1)},
		{name: "DATEDIFF month complete on same day", resultType: integerType, source: `DATEDIFF(DATE(2024, 1, 15), DATE(2024, 3, 15), "month")`, want: int64(2)},
		{name: "DATEDIFF month honors month-end clamp", resultType: integerType, source: `DATEDIFF(DATE(2024, 1, 31), DATE(2024, 3, 1), "month")`, want: int64(1)},
		{name: "DATEDIFF month clamped candidate equals end", resultType: integerType, source: `DATEDIFF(DATE(2024, 1, 30), DATE(2024, 2, 29), "month")`, want: int64(1)},
		{name: "DATEDIFF month negative is symmetric", resultType: integerType, source: `DATEDIFF(DATE(2024, 3, 1), DATE(2024, 1, 31), "month")`, want: int64(-1)},
		{name: "DATEDIFF month timezone uses local months", resultType: integerType, source: `DATEDIFF(timestamp("2023-12-31T23:30:00Z"), timestamp("2024-01-01T00:30:00Z"), "month", "Asia/Shanghai")`, want: int64(0)},
		{name: "DATEDIFF year full units", resultType: integerType, source: `DATEDIFF(DATE(2023, 6, 15), DATE(2025, 6, 14), "year")`, want: int64(1)},
		{name: "DATEDIFF year completes on anniversary", resultType: integerType, source: `DATEDIFF(DATE(2023, 6, 15), DATE(2025, 6, 15), "year")`, want: int64(2)},
		{name: "DATEDIFF year reversed is symmetric", resultType: integerType, source: `DATEDIFF(DATE(2025, 6, 14), DATE(2023, 6, 15), "year")`, want: int64(-1)},
		{name: "DATEDIFF year honors February 29 clamp", resultType: integerType, source: `DATEDIFF(DATE(2024, 2, 29), DATE(2025, 2, 28), "year")`, want: int64(1)},
		{name: "DATEDIFF full 1-9999 month span", resultType: integerType, source: `DATEDIFF(DATE(1, 1, 1), DATE(9999, 12, 31), "month")`, want: int64(119987)},
		{name: "DATEDIFF unknown unit errors", resultType: integerType, source: `DATEDIFF(DATE(2024, 1, 1), DATE(2024, 2, 1), "week")`, wantCode: "formula.runtime"},
		{name: "DATEDIFF missing unit is a type error", resultType: integerType, source: "DATEDIFF(DATE(2024, 1, 1), DATE(2024, 2, 1))", wantCode: "formula.type"},
		{name: "DATEDIFF rejects non-string timezone", resultType: integerType, source: `DATEDIFF(DATE(2024, 1, 1), DATE(2024, 2, 1), "day", 5)`, wantCode: "formula.type"},

		// TEXT numbers: closed pattern set, half-away-from-zero rounding.
		{name: "TEXT two decimals", resultType: textType, source: `TEXT(3.14159, "0.00")`, want: "3.14"},
		{name: "TEXT integer pattern rounds half away up", resultType: textType, source: `TEXT(2.5, "0")`, want: "3"},
		{name: "TEXT integer pattern rounds half away down", resultType: textType, source: `TEXT(-2.5, "0")`, want: "-3"},
		{name: "TEXT matches ROUND not banker rounding", resultType: textType, source: `TEXT(2.675, "0.00")`, want: "2.68"},
		{name: "TEXT exact half rounds away from zero", resultType: textType, source: `TEXT(0.125, "0.00")`, want: "0.13"},
		{name: "TEXT keeps trailing fraction zeros", resultType: textType, source: `TEXT(5, "0.0")`, want: "5.0"},
		{name: "TEXT negative integer", resultType: textType, source: `TEXT(-42, "0")`, want: "-42"},
		{name: "TEXT pads leading zero", resultType: textType, source: `TEXT(0.5, "0.00")`, want: "0.50"},
		{name: "TEXT simple percent", resultType: textType, source: `TEXT(0.5, "0%")`, want: "50%"},
		{name: "TEXT percent with fraction", resultType: textType, source: `TEXT(0.125, "0.0%")`, want: "12.5%"},
		{name: "TEXT integer percent", resultType: textType, source: `TEXT(1, "0%")`, want: "100%"},
		{name: "TEXT zero percent keeps single zero", resultType: textType, source: `TEXT(0, "0%")`, want: "0%"},
		{name: "TEXT max int64 stays exact", resultType: textType, source: `TEXT(9223372036854775807, "0")`, want: "9223372036854775807"},
		{name: "TEXT max int64 fraction stays exact", resultType: textType, source: `TEXT(9223372036854775807, "0.000000000000000")`, want: "9223372036854775807.000000000000000"},
		{name: "TEXT min int64 percent stays exact", inputType: integerType, resultType: textType, source: `TEXT(value, "0.0%")`, input: int64(math.MinInt64), want: "-922337203685477580800.0%"},
		{name: "TEXT percent rounds up", resultType: textType, source: `TEXT(0.9999, "0%")`, want: "100%"},
		{name: "TEXT fifteen fraction digits", resultType: textType, source: `TEXT(1.5, "0.000000000000000")`, want: "1.500000000000000"},
		{name: "TEXT rejects sixteen fraction digits", resultType: textType, source: `TEXT(1.5, "0.0000000000000000")`, wantCode: "formula.runtime"},
		{name: "TEXT rejects grouping", resultType: textType, source: `TEXT(1234.5, "#,##0.0")`, wantCode: "formula.runtime"},
		{name: "TEXT rejects currency", resultType: textType, source: `TEXT(1234.5, "$0.00")`, wantCode: "formula.runtime"},
		{name: "TEXT rejects scientific code", resultType: textType, source: `TEXT(1234.5, "0.0E+00")`, wantCode: "formula.runtime"},
		{name: "TEXT rejects stray percent", resultType: textType, source: `TEXT(1.5, "0.0%%")`, wantCode: "formula.runtime"},
		{name: "TEXT percent overflow errors", resultType: textType, source: `TEXT(1e308, "0%")`, wantCode: "formula.overflow"},
		{name: "TEXT number rejects timestamp pattern", resultType: textType, source: `TEXT(3.5, "YYYY-MM-DD")`, wantCode: "formula.runtime"},
		{name: "TEXT missing format is a type error", resultType: textType, source: "TEXT(1.5)", wantCode: "formula.type"},
		{name: "TEXT rejects boolean", resultType: textType, source: `TEXT(true, "0")`, wantCode: "formula.type"},
		{name: "TEXT rejects non-string format", resultType: textType, source: "TEXT(1.5, 0)", wantCode: "formula.type"},

		// TEXT timestamps: fixed pattern set, MM month vs mm minute.
		{name: "TEXT timestamp date", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "YYYY-MM-DD")`, want: "2024-05-06"},
		{name: "TEXT timestamp slashed date", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "YYYY/MM/DD")`, want: "2024/05/06"},
		{name: "TEXT timestamp year month", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "YYYY-MM")`, want: "2024-05"},
		{name: "TEXT timestamp year only", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "YYYY")`, want: "2024"},
		{name: "TEXT timestamp month only", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "MM")`, want: "05"},
		{name: "TEXT timestamp day only", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "DD")`, want: "06"},
		{name: "TEXT distinguishes MM month from mm minute", resultType: textType, source: `TEXT(timestamp("2024-12-05T23:59:59Z"), "YYYY-MM-DD HH:mm:ss")`, want: "2024-12-05 23:59:59"},
		{name: "TEXT minute pattern", resultType: textType, source: `TEXT(timestamp("2024-05-06T18:09:08Z"), "HH:mm")`, want: "18:09"},
		{name: "TEXT second pattern", resultType: textType, source: `TEXT(timestamp("2024-05-06T18:09:08Z"), "HH:mm:ss")`, want: "18:09:08"},
		{name: "TEXT datetime minute pattern", resultType: textType, source: `TEXT(timestamp("2024-05-06T18:09:08Z"), "YYYY-MM-DD HH:mm")`, want: "2024-05-06 18:09"},
		{name: "TEXT timestamp timezone override", resultType: textType, source: `TEXT(timestamp("2024-05-06T18:09:08Z"), "YYYY-MM-DD HH:mm:ss", "Asia/Shanghai")`, want: "2024-05-07 02:09:08"},
		{name: "TEXT timestamp default is UTC", resultType: textType, source: `TEXT(timestamp("2024-05-06T18:09:08Z"), "YYYY-MM-DD")`, want: "2024-05-06"},
		{name: "TEXT rejects lowercase pattern", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "yyyy-MM-dd")`, wantCode: "formula.runtime"},
		{name: "TEXT rejects unknown timestamp pattern", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "YYYY-MM-DD HH")`, wantCode: "formula.runtime"},
		{name: "TEXT timestamp rejects number pattern", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "0.0")`, wantCode: "formula.runtime"},
		{name: "TEXT timestamp unknown timezone errors", resultType: textType, source: `TEXT(DATE(2024, 5, 6), "YYYY", "Not/AZone")`, wantCode: "formula.runtime"},

		// Null propagation through typed calendar arguments.
		{name: "YEAR null timestamp stays null", inputType: dateTimeType, resultType: integerType, source: "YEAR(value)", input: nil, nullable: true, want: nil},
		{name: "YEAR null timestamp requires nullable", inputType: dateTimeType, resultType: integerType, source: "YEAR(value)", input: nil, wantCode: "formula.null"},
		{name: "DATEADD null timestamp stays null", inputType: dateTimeType, resultType: dateTimeType, source: `DATEADD(value, 1, "day")`, input: nil, nullable: true, want: nil},
		{name: "DATEDIFF null start stays null", inputType: dateTimeType, resultType: integerType, source: `DATEDIFF(value, DATE(2024, 1, 1), "day")`, input: nil, nullable: true, want: nil},
		{name: "TEXT null timestamp stays null", inputType: dateTimeType, resultType: textType, source: `TEXT(value, "YYYY")`, input: nil, nullable: true, want: nil},
		{name: "DATE null day argument stays null", inputType: integerType, resultType: dateTimeType, source: "DATE(2024, 1, value)", input: nil, nullable: true, want: nil},
	}
	compiler := NewCompiler(DefaultLimits())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := formulaField("result_id", "result", test.resultType, test.source)
			output.Value.Required = !test.nullable
			inputs := map[string]any{}
			if test.inputType.LogicalType != "" {
				inputs["value"] = test.input
			}
			plan, err := compiler.CompileExecutionTable(formulaTable(
				scalarField("value_id", "value", test.inputType), output,
			))
			if err != nil {
				if test.wantCode == "" {
					t.Fatalf("compile %q: %v", test.source, err)
				}
				assertFormulaCode(t, err, test.wantCode)
				return
			}
			if test.wantCode != "" && strings.HasPrefix(test.wantCode, "formula.type") {
				t.Fatalf("compile %q unexpectedly succeeded", test.source)
			}
			values, err := plan.Evaluate(context.Background(), inputs, nil)
			if test.wantCode != "" {
				assertFormulaCode(t, err, test.wantCode)
				return
			}
			if err != nil || !reflect.DeepEqual(values, map[string]any{"result": test.want}) {
				t.Fatalf("%s with %#v: got %#v, error %v; want %#v", test.source, test.input, values, err, test.want)
			}
		})
	}
}

// TestCalendarFormulaCompileErrorGoldens keeps the function set closed: no
// clock functions ship from this pure-function slice.
func TestCalendarFormulaCompileErrorGoldens(t *testing.T) {
	tests := []struct {
		name   string
		source string
		code   string
	}{
		{"unknown calendar helper", `DATEPART(DATE(2024, 1, 1), "year")`, "formula.dependency"},
		{"lowercase date rejected", "date(2024, 1, 1)", "formula.dependency"},
		{"lowercase year rejected", `year(timestamp("2024-01-01T00:00:00Z"))`, "formula.dependency"},
	}
	compiler := NewCompiler(DefaultLimits())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := compiler.CompileExecutionTable(formulaTable(
				formulaField("result_id", "result", dateTimeType, test.source),
			))
			assertFormulaCode(t, err, test.code)
		})
	}
}

// TestCalendarFormulaInferExecutionSourceGoldens proves the inference path
// agrees with the declared overload results.
func TestCalendarFormulaInferExecutionSourceGoldens(t *testing.T) {
	tests := []struct {
		source string
		want   ValueType
	}{
		{"DATE(2024, 1, 1)", dateTimeType},
		{`DATE(2024, 1, 31) + duration("24h")`, dateTimeType},
		{"YEAR(DATE(2024, 1, 1))", integerType},
		{`MONTH(DATE(2024, 1, 1), "UTC")`, integerType},
		{`DAY(DATE(2024, 1, 1), "Asia/Tokyo")`, integerType},
		{`DATEADD(DATE(2024, 1, 1), 1, "day")`, dateTimeType},
		{`DATEADD(DATE(2024, 1, 1), -2, "month", "system")`, dateTimeType},
		{`DATEDIFF(DATE(2024, 1, 1), DATE(2024, 2, 1), "month")`, integerType},
		{`DATEDIFF(DATE(2024, 1, 1), DATE(2024, 2, 1), "year", "UTC")`, integerType},
		{`TEXT(1.5, "0.0")`, textType},
		{`TEXT(5, "0")`, textType},
		{`TEXT(DATE(2024, 1, 1), "YYYY")`, textType},
		{`TEXT(DATE(2024, 1, 1), "YYYY-MM-DD HH:mm", "system")`, textType},
		{`CONCATENATE(TEXT(0.5, "0%"), TEXT(DATE(2024, 1, 1), "YYYY"))`, textType},
	}
	compiler := NewCompiler(DefaultLimits())
	for _, test := range tests {
		result, err := compiler.InferExecutionSource(formulaTable(), test.source)
		if err != nil {
			t.Fatalf("infer %q: %v", test.source, err)
		}
		if result != test.want {
			t.Fatalf("infer %q = %#v; want %#v", test.source, result, test.want)
		}
	}
}

// TestCalendarLocationResolution pins the timezone name resolution rules:
// only UTC, system and valid IANA locations resolve, the empty name and the
// stdlib pseudo-name "Local" are rejected, and the embedded tzdata database
// resolves IANA zones on Windows.
func TestCalendarLocationResolution(t *testing.T) {
	if location, err := calendarLocation("UTC"); err != nil || location != time.UTC {
		t.Fatalf("UTC = %v, %v; want UTC", location, err)
	}
	if location, err := calendarLocation("system"); err != nil || location != time.Local {
		t.Fatalf("system = %v, %v; want local zone", location, err)
	}
	for _, name := range []string{"Asia/Shanghai", "America/New_York", "Asia/Tokyo", "Australia/Lord_Howe"} {
		location, err := calendarLocation(name)
		if err != nil || location.String() != name {
			t.Fatalf("calendarLocation(%q) = %v, %v", name, location, err)
		}
	}
	for _, name := range []string{"", "Local", "Not/AZone"} {
		if _, err := calendarLocation(name); err == nil {
			t.Fatalf("calendarLocation(%q) resolved unexpectedly", name)
		}
	}
}

// TestCalendarTextNumberPatternBoundaries pins the closed numeric pattern
// grammar directly, including every rejected shape.
func TestCalendarTextNumberPatternBoundaries(t *testing.T) {
	tests := []struct {
		format   string
		decimals int
		percent  bool
		ok       bool
	}{
		{"0", 0, false, true},
		{"0%", 0, true, true},
		{"0.0", 1, false, true},
		{"0.000000000000000", 15, false, true},
		{"0.000000000000000%", 15, true, true},
		{"0.0000000000000000", 0, false, false},
		{"0.0000000000000000%", 0, false, false},
		{"0.", 0, false, false},
		{"00", 0, false, false},
		{"0.00.0", 0, false, false},
		{"0.5", 0, false, false},
		{"#,##0.0", 0, false, false},
		{"$0.00", 0, false, false},
		{"0.0E+00", 0, false, false},
		{"0%%", 0, false, false},
		{"%", 0, false, false},
		{"", 0, false, false},
	}
	for _, test := range tests {
		decimals, percent, ok := calendarTextNumberPattern(test.format)
		if ok != test.ok || decimals != test.decimals || percent != test.percent {
			t.Fatalf("pattern %q = %d, %t, %t; want %d, %t, %t",
				test.format, decimals, percent, ok, test.decimals, test.percent, test.ok)
		}
	}
}

// TestCalendarMonthDifferenceProperties proves the frozen DATEDIFF month
// contract on deterministic pseudo-random civil pairs: exact sign symmetry
// and counting consistent with the DATEADD month-end clamp rule.
func TestCalendarMonthDifferenceProperties(t *testing.T) {
	source := rand.New(rand.NewSource(394))
	randomCivil := func() calendarCivilKey {
		year := int64(1600 + source.Intn(400))
		month := int64(1 + source.Intn(12))
		day := int64(1 + source.Intn(int(calendarDaysInMonth(year, month))))
		return calendarCivilKey{
			year: year, month: month, day: day,
			hour: int64(source.Intn(24)), minute: int64(source.Intn(60)),
			second: int64(source.Intn(60)), nanos: int64(source.Intn(1000000000)),
		}
	}
	for index := 0; index < 500; index++ {
		start, end := randomCivil(), randomCivil()
		forward := calendarMonthDifference(start, end)
		if backward := calendarMonthDifference(end, start); forward != -backward {
			t.Fatalf("asymmetry: %v -> %v gives %d and %d", start, end, forward, backward)
		}
		if calendarCivilLess(end, start) {
			start, end = end, start
		}
		months := calendarMonthDifference(start, end)
		if months < 0 {
			t.Fatalf("negative span for ordered pair: %d", months)
		}
		added := calendarAddMonths(start, months)
		if calendarCivilLess(added, start) {
			t.Fatalf("candidate %v before start %v", added, start)
		}
		if calendarCivilLess(end, added) {
			t.Fatalf("candidate %v after end %v for %d months", added, end, months)
		}
		if next := calendarAddMonths(start, months+1); !calendarCivilLess(end, next) {
			t.Fatalf("end %v reaches start + %d months %v", end, months+1, next)
		}
	}
}

// TestCalendarLowercaseDateSemanticsUnchanged keeps the frozen lowercase UTC
// duration contract next to the new calendar functions.
func TestCalendarLowercaseDateSemanticsUnchanged(t *testing.T) {
	tests := []struct {
		source     string
		resultType ValueType
		want       any
	}{
		{`dateAdd(timestamp("2024-01-31T00:00:00Z"), duration("24h"))`, dateTimeType, "2024-02-01T00:00:00Z"},
		{`dateSubtract(timestamp("2024-03-01T00:00:00Z"), duration("24h"))`, dateTimeType, "2024-02-29T00:00:00Z"},
		{`formatDate(timestamp("2024-02-29T23:30:00Z"), "yyyy-MM-dd")`, textType, "2024-02-29"},
	}
	for _, test := range tests {
		plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(
			formulaField("result_id", "result", test.resultType, test.source),
		))
		if err != nil {
			t.Fatalf("compile %q: %v", test.source, err)
		}
		values, err := plan.Evaluate(context.Background(), map[string]any{}, nil)
		if err != nil || !reflect.DeepEqual(values["result"], test.want) {
			t.Fatalf("%s: got %#v, error %v; want %#v", test.source, values, err, test.want)
		}
	}
}
