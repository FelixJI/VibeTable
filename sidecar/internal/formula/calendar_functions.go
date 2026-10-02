package formula

// Uppercase calendar functions: DATE, YEAR, MONTH, DAY, DATEADD,
// DATEDIFF and TEXT. They are pure cel bindings on the standard eager
// evaluation path: strict typed overloads, runtime type guards that surface
// null arguments as "no such overload" so the plan keeps its frozen
// nullable/error mapping, and no access to the current time (TODAY/NOW and
// the clock chain use the batch evaluation context). The old lowercase
// dateAdd/dateSubtract/formatDate keep their UTC duration contract.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

	// Embed the standard zone database so time.LoadLocation resolves IANA
	// zones on offline Windows builds without the OS timezone data.
	_ "time/tzdata"
)

const (
	calendarMinYear = 1
	calendarMaxYear = 9999
	// textNumberMaxDecimals is the largest supported fraction of "0.0...".
	textNumberMaxDecimals = 15
	// Bounds for the DATEADD amount per unit. Any amount beyond these can
	// never land inside years 1-9999, so rejecting it up front keeps the
	// intermediate calendar arithmetic free of int64 overflow.
	calendarMaxYearAdd  = 10000
	calendarMaxMonthAdd = 120000
	calendarMaxDayAdd   = 4000000
)

// calendarFunctionOptions declares the calendar function signatures shared
// by Compile and InferExecutionSource.
func calendarFunctionOptions() []cel.EnvOption {
	options := []cel.EnvOption{
		cel.Function("DATE",
			cel.Overload("vibetable_date_int_int_int",
				[]*cel.Type{cel.IntType, cel.IntType, cel.IntType}, cel.TimestampType,
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					if len(values) != 3 {
						return types.NewErr("no such overload: DATE")
					}
					year, yearOK := values[0].(types.Int)
					month, monthOK := values[1].(types.Int)
					day, dayOK := values[2].(types.Int)
					if !yearOK || !monthOK || !dayOK {
						return types.NewErr("no such overload: DATE")
					}
					return calendarDateValue(year, month, day)
				}))),
	}
	calendarField := func(name string, extract func(time.Time) int64) []cel.EnvOption {
		return []cel.EnvOption{
			cel.Function(name,
				cel.Overload("vibetable_"+strings.ToLower(name)+"_timestamp",
					[]*cel.Type{cel.TimestampType}, cel.IntType,
					cel.UnaryBinding(func(value ref.Val) ref.Val {
						return calendarExtractField(name, value, nil, extract)
					})),
				cel.Overload("vibetable_"+strings.ToLower(name)+"_timestamp_string",
					[]*cel.Type{cel.TimestampType, cel.StringType}, cel.IntType,
					cel.BinaryBinding(func(value, zone ref.Val) ref.Val {
						return calendarExtractField(name, value, zone, extract)
					}))),
		}
	}
	options = append(options, calendarField("YEAR", func(local time.Time) int64 {
		return int64(local.Year())
	})...)
	options = append(options, calendarField("MONTH", func(local time.Time) int64 {
		return int64(local.Month())
	})...)
	options = append(options, calendarField("DAY", func(local time.Time) int64 {
		return int64(local.Day())
	})...)
	options = append(options,
		cel.Function("DATEADD",
			cel.Overload("vibetable_dateadd_timestamp_int_string",
				[]*cel.Type{cel.TimestampType, cel.IntType, cel.StringType}, cel.TimestampType,
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					return calendarAddValue(values, nil)
				})),
			cel.Overload("vibetable_dateadd_timestamp_int_string_string",
				[]*cel.Type{cel.TimestampType, cel.IntType, cel.StringType, cel.StringType}, cel.TimestampType,
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					return calendarAddValue(values, values[3])
				}))),
		cel.Function("DATEDIFF",
			cel.Overload("vibetable_datediff_timestamp_timestamp_string",
				[]*cel.Type{cel.TimestampType, cel.TimestampType, cel.StringType}, cel.IntType,
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					return calendarDifferenceValue(values, nil)
				})),
			cel.Overload("vibetable_datediff_timestamp_timestamp_string_string",
				[]*cel.Type{cel.TimestampType, cel.TimestampType, cel.StringType, cel.StringType}, cel.IntType,
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					return calendarDifferenceValue(values, values[3])
				}))),
		cel.Function("TEXT",
			cel.Overload("vibetable_text_double_string",
				[]*cel.Type{cel.DoubleType, cel.StringType}, cel.StringType,
				cel.BinaryBinding(func(number, format ref.Val) ref.Val {
					value, valueOK := number.(types.Double)
					pattern, patternOK := format.(types.String)
					if !valueOK || !patternOK {
						return types.NewErr("no such overload: TEXT")
					}
					return calendarTextNumberValue(float64(value), string(pattern))
				})),
			cel.Overload("vibetable_text_int_string",
				[]*cel.Type{cel.IntType, cel.StringType}, cel.StringType,
				cel.BinaryBinding(func(number, format ref.Val) ref.Val {
					value, valueOK := number.(types.Int)
					pattern, patternOK := format.(types.String)
					if !valueOK || !patternOK {
						return types.NewErr("no such overload: TEXT")
					}
					return calendarTextIntValue(int64(value), string(pattern))
				})),
			cel.Overload("vibetable_text_timestamp_string",
				[]*cel.Type{cel.TimestampType, cel.StringType}, cel.StringType,
				cel.BinaryBinding(func(value, format ref.Val) ref.Val {
					return calendarTextTimestampValue(value, format, nil)
				})),
			cel.Overload("vibetable_text_timestamp_string_string",
				[]*cel.Type{cel.TimestampType, cel.StringType, cel.StringType}, cel.StringType,
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					return calendarTextTimestampValue(values[0], values[1], values[2])
				}))),
	)
	return options
}

// calendarLocation resolves the timezone argument shared by the calendar
// functions. Only the explicit spellings "UTC" and "system" and valid IANA
// location names are accepted; the empty name and the stdlib pseudo-name
// "Local" are rejected so callers always state their intent.
func calendarLocation(name string) (*time.Location, error) {
	switch name {
	case "UTC":
		return time.UTC, nil
	case "system":
		return time.Local, nil
	case "", "Local":
		return nil, fmt.Errorf("timezone %q must be UTC, system or an IANA location", name)
	default:
		return time.LoadLocation(name)
	}
}

// calendarZone resolves a ref.Val timezone argument, returning a
// "no such overload" error for null or mistyped values so the plan keeps the
// frozen null classification.
func calendarZone(zone ref.Val) (*time.Location, ref.Val) {
	name, ok := zone.(types.String)
	if !ok {
		return nil, types.NewErr("no such overload")
	}
	location, err := calendarLocation(string(name))
	if err != nil {
		return nil, types.NewErr("unknown timezone: %s", string(name))
	}
	return location, nil
}

// calendarDaysInMonth returns the month length for a proleptic Gregorian
// year, or 0 for an invalid month number.
func calendarDaysInMonth(year, month int64) int64 {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	default:
		return 0
	}
}

// calendarDateValue implements DATE(year, month, day): strict valid
// Gregorian calendar dates in years 1-9999 only, produced as UTC midnight.
// Out-of-range components never carry into the next month or year.
func calendarDateValue(year, month, day types.Int) ref.Val {
	yearNumber, monthNumber, dayNumber := int64(year), int64(month), int64(day)
	if yearNumber < calendarMinYear || yearNumber > calendarMaxYear {
		return types.NewErr("DATE year overflows the supported range (1-9999)")
	}
	if monthNumber < 1 || monthNumber > 12 {
		return types.NewErr("DATE month is invalid")
	}
	if dayNumber < 1 || dayNumber > calendarDaysInMonth(yearNumber, monthNumber) {
		return types.NewErr("DATE day is invalid")
	}
	return types.Timestamp{Time: time.Date(
		int(yearNumber), time.Month(monthNumber), int(dayNumber), 0, 0, 0, 0, time.UTC,
	)}
}

// calendarExtractField implements YEAR/MONTH/DAY in the resolved timezone.
func calendarExtractField(
	name string, value, zone ref.Val, extract func(time.Time) int64,
) ref.Val {
	timestamp, ok := value.(types.Timestamp)
	if !ok {
		return types.NewErr("no such overload: %s", name)
	}
	location := time.UTC
	if zone != nil {
		resolved, failure := calendarZone(zone)
		if failure != nil {
			return failure
		}
		location = resolved
	}
	return types.Int(extract(timestamp.Time.In(location)))
}

// calendarAddValue implements the DATEADD overloads; zone is the optional
// timezone argument and nil when the overload has none.
func calendarAddValue(values []ref.Val, zone ref.Val) ref.Val {
	if len(values) != 3 && zone == nil {
		return types.NewErr("no such overload: DATEADD")
	}
	if len(values) != 4 && zone != nil {
		return types.NewErr("no such overload: DATEADD")
	}
	timestamp, timestampOK := values[0].(types.Timestamp)
	amount, amountOK := values[1].(types.Int)
	unit, unitOK := values[2].(types.String)
	if !timestampOK || !amountOK || !unitOK {
		return types.NewErr("no such overload: DATEADD")
	}
	location := time.UTC
	if zone != nil {
		resolved, failure := calendarZone(zone)
		if failure != nil {
			return failure
		}
		location = resolved
	}
	return calendarShift(timestamp.Time, int64(amount), string(unit), location)
}

// calendarDayNumberOf is the local calendar date as a pure UTC day number
// (days since 1970-01-01). Going through Unix seconds instead of
// time.Sub keeps year 1-9999 spans free of time.Duration, which overflows
// beyond roughly 292 years.
func calendarDayNumberOf(local time.Time) int64 {
	return time.Date(
		local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC,
	).Unix() / 86400
}

// calendarWallClockMatches reports whether the local wall clock of value
// equals the requested fields exactly.
func calendarWallClockMatches(
	value time.Time, year int, month time.Month, day, hour, minute, second, nanos int,
) bool {
	return value.Year() == year && value.Month() == month && value.Day() == day &&
		value.Hour() == hour && value.Minute() == minute &&
		value.Second() == second && value.Nanosecond() == nanos
}

// calendarResolveLocal returns the earliest instant in loc whose local wall
// clock equals the requested fields. A wall clock inside a DST gap has no
// instant at all (ok=false). A wall clock inside a DST overlap has two
// instants; the previous zone segment is probed through ZoneBounds and the
// earlier instant wins, without assuming any fixed transition size. This
// also applies to a zero DATEADD amount, so an input on the later side of an
// overlap snaps to the earlier occurrence.
func calendarResolveLocal(
	year int, month time.Month, day, hour, minute, second, nanos int, loc *time.Location,
) (time.Time, bool) {
	candidate := time.Date(year, month, day, hour, minute, second, nanos, loc)
	if !calendarWallClockMatches(candidate, year, month, day, hour, minute, second, nanos) {
		return time.Time{}, false
	}
	if segmentStart, _ := candidate.ZoneBounds(); !segmentStart.IsZero() {
		previous := time.Unix(segmentStart.Unix()-1, 0).In(loc)
		_, previousOffset := previous.Zone()
		naive := time.Date(year, month, day, hour, minute, second, nanos, time.UTC)
		earlier := time.Unix(naive.Unix()-int64(previousOffset), int64(nanos)).In(loc)
		if earlier.Before(candidate) &&
			calendarWallClockMatches(earlier, year, month, day, hour, minute, second, nanos) {
			return earlier, true
		}
	}
	return candidate, true
}

// calendarShift implements the DATEADD calendar arithmetic: year/month
// additions clamp to the target month end, day additions keep the local wall
// clock across calendar days, a result inside a DST gap is an explicit
// error, and a result inside a DST overlap resolves to the earlier instant.
func calendarShift(timestamp time.Time, amount int64, unit string, location *time.Location) ref.Val {
	local := timestamp.In(location)
	var year, month, day int64
	switch unit {
	case "year":
		if amount > calendarMaxYearAdd || amount < -calendarMaxYearAdd {
			return types.NewErr("DATEADD amount overflows the supported range")
		}
		year, month = int64(local.Year())+amount, int64(local.Month())
	case "month":
		if amount > calendarMaxMonthAdd || amount < -calendarMaxMonthAdd {
			return types.NewErr("DATEADD amount overflows the supported range")
		}
		totalMonths := int64(local.Year())*12 + int64(local.Month()) - 1 + amount
		year, month = totalMonths/12, totalMonths%12+1
	case "day":
		if amount > calendarMaxDayAdd || amount < -calendarMaxDayAdd {
			return types.NewErr("DATEADD amount overflows the supported range")
		}
		shifted := time.Unix((calendarDayNumberOf(local)+amount)*86400, 0).UTC()
		year, month, day = int64(shifted.Year()), int64(shifted.Month()), int64(shifted.Day())
	default:
		return types.NewErr("unsupported DATEADD unit: %q", unit)
	}
	if year < calendarMinYear || year > calendarMaxYear {
		return types.NewErr("DATEADD result year overflows the supported range (1-9999)")
	}
	if unit == "year" || unit == "month" {
		day = int64(local.Day())
		if monthLength := calendarDaysInMonth(year, month); day > monthLength {
			day = monthLength
		}
	}
	result, exists := calendarResolveLocal(int(year), time.Month(month), int(day),
		local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), location)
	if !exists {
		return types.NewErr(
			"DATEADD result does not exist in timezone %s", location.String(),
		)
	}
	return types.Timestamp{Time: result}
}

// calendarCivilKey carries the local wall-clock fields of a timestamp.
type calendarCivilKey struct {
	year, month, day, hour, minute, second, nanos int64
}

func calendarCivilKeyOf(local time.Time) calendarCivilKey {
	return calendarCivilKey{
		year: int64(local.Year()), month: int64(local.Month()), day: int64(local.Day()),
		hour: int64(local.Hour()), minute: int64(local.Minute()),
		second: int64(local.Second()), nanos: int64(local.Nanosecond()),
	}
}

// calendarCivilLess orders two civil keys by their wall clock only, ignoring
// the instants they map to, so month counting stays symmetric around DST.
func calendarCivilLess(left, right calendarCivilKey) bool {
	leftFields := [...]int64{left.year, left.month, left.day, left.hour, left.minute, left.second, left.nanos}
	rightFields := [...]int64{right.year, right.month, right.day, right.hour, right.minute, right.second, right.nanos}
	for index := range leftFields {
		if leftFields[index] != rightFields[index] {
			return leftFields[index] < rightFields[index]
		}
	}
	return false
}

// calendarAddMonths applies the DATEADD month rule (month-end clamp, wall
// clock preserved) to a civil key without resolving it to an instant.
func calendarAddMonths(start calendarCivilKey, months int64) calendarCivilKey {
	totalMonths := start.year*12 + start.month - 1 + months
	year, month := totalMonths/12, totalMonths%12+1
	day := start.day
	if monthLength := calendarDaysInMonth(year, month); day > monthLength {
		day = monthLength
	}
	return calendarCivilKey{
		year: year, month: month, day: day,
		hour: start.hour, minute: start.minute, second: start.second, nanos: start.nanos,
	}
}

// calendarMonthSpan counts the full calendar months from start to end using
// the DATEADD month rule; start must not be after end by wall clock.
func calendarMonthSpan(start, end calendarCivilKey) int64 {
	months := (end.year-start.year)*12 + end.month - start.month
	if calendarCivilLess(end, calendarAddMonths(start, months)) {
		months--
	}
	return months
}

// calendarMonthDifference is the signed full-month difference; swapping the
// arguments exactly negates the result.
func calendarMonthDifference(start, end calendarCivilKey) int64 {
	if calendarCivilLess(end, start) {
		return -calendarMonthSpan(end, start)
	}
	return calendarMonthSpan(start, end)
}

// calendarDifferenceValue implements the DATEDIFF overloads; zone is the
// optional timezone argument and nil when the overload has none.
func calendarDifferenceValue(values []ref.Val, zone ref.Val) ref.Val {
	if len(values) != 3 && zone == nil {
		return types.NewErr("no such overload: DATEDIFF")
	}
	if len(values) != 4 && zone != nil {
		return types.NewErr("no such overload: DATEDIFF")
	}
	start, startOK := values[0].(types.Timestamp)
	end, endOK := values[1].(types.Timestamp)
	unit, unitOK := values[2].(types.String)
	if !startOK || !endOK || !unitOK {
		return types.NewErr("no such overload: DATEDIFF")
	}
	location := time.UTC
	if zone != nil {
		resolved, failure := calendarZone(zone)
		if failure != nil {
			return failure
		}
		location = resolved
	}
	localStart, localEnd := start.Time.In(location), end.Time.In(location)
	switch string(unit) {
	case "day":
		return types.Int(calendarDayNumberOf(localEnd) - calendarDayNumberOf(localStart))
	case "month":
		return types.Int(calendarMonthDifference(
			calendarCivilKeyOf(localStart), calendarCivilKeyOf(localEnd),
		))
	case "year":
		return types.Int(calendarMonthDifference(
			calendarCivilKeyOf(localStart), calendarCivilKeyOf(localEnd),
		) / 12)
	default:
		return types.NewErr("unsupported DATEDIFF unit: %q", string(unit))
	}
}

// calendarTextNumberPattern validates the frozen TEXT number patterns:
// "0", "0.0" up to fifteen fraction zeros, each optionally followed by one
// "%". Grouping, currency and any other Excel code are rejected.
func calendarTextNumberPattern(format string) (decimals int, percent bool, ok bool) {
	body := format
	if strings.HasSuffix(body, "%") {
		body, percent = body[:len(body)-1], true
	}
	if body == "0" {
		return 0, percent, true
	}
	if !strings.HasPrefix(body, "0.") {
		return 0, false, false
	}
	fraction := body[2:]
	if len(fraction) == 0 || len(fraction) > textNumberMaxDecimals {
		return 0, false, false
	}
	for _, digit := range fraction {
		if digit != '0' {
			return 0, false, false
		}
	}
	return len(fraction), percent, true
}

// calendarInsertDecimalPoint renders an exact decimal digit string with
// the requested fraction width; negative values keep a leading "-".
func calendarInsertDecimalPoint(digits string, decimals int) string {
	negative := strings.HasPrefix(digits, "-")
	if negative {
		digits = digits[1:]
	}
	for len(digits) <= decimals {
		digits = "0" + digits
	}
	result := digits
	if decimals > 0 {
		result = digits[:len(digits)-decimals] + "." + digits[len(digits)-decimals:]
	}
	if negative {
		result = "-" + result
	}
	return result
}

func calendarTextNumberValue(number float64, format string) ref.Val {
	decimals, percent, ok := calendarTextNumberPattern(format)
	if !ok {
		return types.NewErr("unsupported TEXT number format: %q", format)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return types.NewErr("TEXT number must be finite")
	}
	value := number
	if percent {
		value = number * 100
		if math.IsInf(value, 0) {
			return types.NewErr("TEXT percent value overflows")
		}
	}
	scaled := value * math.Pow10(decimals)
	if math.IsInf(scaled, 0) {
		return types.NewErr("TEXT number overflows the requested precision")
	}
	result := calendarInsertDecimalPoint(
		strconv.FormatFloat(math.Round(scaled), 'f', 0, 64), decimals,
	)
	if percent {
		result += "%"
	}
	return types.String(result)
}

// calendarTextIntValue renders integers exactly through digit-string
// arithmetic so endpoints like math.MinInt64 keep full precision: percent
// scaling multiplies by 100 by appending two digits and the fraction width
// appends zeros, never converting through double.
func calendarTextIntValue(number int64, format string) ref.Val {
	decimals, percent, ok := calendarTextNumberPattern(format)
	if !ok {
		return types.NewErr("unsupported TEXT number format: %q", format)
	}
	digits := strconv.FormatInt(number, 10)
	negative := strings.HasPrefix(digits, "-")
	if negative {
		digits = digits[1:]
	}
	if percent && digits != "0" {
		digits += "00"
	}
	if decimals > 0 {
		digits += strings.Repeat("0", decimals)
	}
	result := calendarInsertDecimalPoint(digits, decimals)
	if negative {
		result = "-" + result
	}
	if percent {
		result += "%"
	}
	return types.String(result)
}

// calendarTextTimestampLayouts is the closed set of timestamp patterns. The
// spelling is case sensitive: MM is the month and mm the minute.
var calendarTextTimestampLayouts = map[string]string{
	"YYYY-MM-DD":          "2006-01-02",
	"YYYY/MM/DD":          "2006/01/02",
	"YYYY-MM":             "2006-01",
	"YYYY":                "2006",
	"MM":                  "01",
	"DD":                  "02",
	"YYYY-MM-DD HH:mm":    "2006-01-02 15:04",
	"YYYY-MM-DD HH:mm:ss": "2006-01-02 15:04:05",
	"HH:mm":               "15:04",
	"HH:mm:ss":            "15:04:05",
}

func calendarTextTimestampValue(value, format, zone ref.Val) ref.Val {
	timestamp, timestampOK := value.(types.Timestamp)
	pattern, patternOK := format.(types.String)
	if !timestampOK || !patternOK {
		return types.NewErr("no such overload: TEXT")
	}
	location := time.UTC
	if zone != nil {
		resolved, failure := calendarZone(zone)
		if failure != nil {
			return failure
		}
		location = resolved
	}
	layout, known := calendarTextTimestampLayouts[string(pattern)]
	if !known {
		return types.NewErr("unsupported TEXT timestamp format: %q", string(pattern))
	}
	return types.String(timestamp.Time.In(location).Format(layout))
}
