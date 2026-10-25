package v2

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxAutoNumber = int64(1<<53 - 1)

// AutoNumberDateFormat is the only supported date segment format.
const AutoNumberDateFormat = "yyyyMMdd"

// autoNumberDateLayouts maps the contract enum to Go layouts.
var autoNumberDateLayouts = map[string]string{
	AutoNumberDateFormat: "20060102",
}

// AutoNumberOrderTokens are the concatenation tokens; Order must be a
// permutation of exactly these three.
var AutoNumberOrderTokens = []string{"prefix", "date", "number"}

func ValidateAutoNumberSpec(spec *AutoNumberSpec) error {
	if spec == nil {
		return invalid("autoNumber", "numbering settings are required")
	}
	if !utf8.ValidString(spec.Prefix) || utf8.RuneCountInString(spec.Prefix) > 64 || strings.ContainsAny(spec.Prefix, "\x00\r\n") {
		return invalid("autoNumber.prefix", "prefix must contain at most 64 characters without NUL or line breaks")
	}
	if spec.Start < 1 || spec.Start > MaxAutoNumber {
		return invalid("autoNumber.start", "start must be a positive safe integer")
	}
	if spec.Width < 1 || spec.Width > 16 {
		return invalid("autoNumber.width", "minimum width must be between 1 and 16")
	}
	if spec.DateFormat != "" && spec.DateFormat != AutoNumberDateFormat {
		return invalid("autoNumber.dateFormat", "dateFormat must be yyyyMMdd")
	}
	if err := ValidateAutoNumberOrder(spec.Order); err != nil {
		return err
	}
	return nil
}

// ValidateAutoNumberOrder accepts an absent order (default concatenation) or
// a permutation containing each of prefix/date/number exactly once.
func ValidateAutoNumberOrder(order []string) error {
	if len(order) == 0 {
		return nil
	}
	if len(order) != len(AutoNumberOrderTokens) {
		return invalid("autoNumber.order", "order must contain each of prefix, date and number exactly once")
	}
	seen := make(map[string]struct{}, len(order))
	for _, token := range order {
		allowed := false
		for _, candidate := range AutoNumberOrderTokens {
			if token == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return invalid("autoNumber.order", "order must contain each of prefix, date and number exactly once")
		}
		if _, duplicate := seen[token]; duplicate {
			return invalid("autoNumber.order", "order must contain each of prefix, date and number exactly once")
		}
		seen[token] = struct{}{}
	}
	return nil
}

func (spec AutoNumberSpec) orderTokens() []string {
	if len(spec.Order) == len(AutoNumberOrderTokens) {
		return spec.Order
	}
	return AutoNumberOrderTokens
}

// AutoNumberDateEnabled reports whether the value carries a date segment.
func AutoNumberDateEnabled(spec AutoNumberSpec) bool {
	_, ok := autoNumberDateLayouts[spec.DateFormat]
	return ok
}

// AutoNumberDateText renders the record creation instant as the date segment.
func AutoNumberDateText(spec AutoNumberSpec, createdAt time.Time) string {
	layout, ok := autoNumberDateLayouts[spec.DateFormat]
	if !ok {
		return ""
	}
	return createdAt.UTC().Format(layout)
}

// AutoNumberValue concatenates prefix/date/number per the spec order. The
// date segment is empty when dateFormat is disabled.
func AutoNumberValue(spec AutoNumberSpec, sequence int64, createdAt time.Time) string {
	number := fmt.Sprintf("%0*d", spec.Width, sequence)
	date := AutoNumberDateText(spec, createdAt)
	var value strings.Builder
	for _, token := range spec.orderTokens() {
		switch token {
		case "prefix":
			value.WriteString(spec.Prefix)
		case "date":
			value.WriteString(date)
		case "number":
			value.WriteString(number)
		}
	}
	return value.String()
}

// ParseAutoNumberValue checks stored business values without guessing
// allocator state. Segments are located by the known prefix and date lengths;
// the number takes the remaining characters, so concatenated digits cannot
// blur. The date segment must be a legal date and the round-trip must
// reproduce the exact value.
func ParseAutoNumberValue(spec AutoNumberSpec, value string) (int64, error) {
	layout, dated := autoNumberDateLayouts[spec.DateFormat]
	dateLength := 0
	if dated {
		dateLength = len(time.Date(2006, 1, 2, 0, 0, 0, 0, time.UTC).Format(layout))
	}
	fixed := len(spec.Prefix) + dateLength
	if len(value) <= fixed {
		return 0, fmt.Errorf("number is outside its fixed format or range")
	}
	numberLength := len(value) - fixed
	// Pass 1: locate the segments and validate the date before the number's
	// round-trip needs it (order may place the number first).
	consumed := 0
	var parsedDate time.Time
	numberText := ""
	for _, token := range spec.orderTokens() {
		switch token {
		case "prefix":
			if !strings.HasPrefix(value[consumed:], spec.Prefix) {
				return 0, fmt.Errorf("number prefix does not match")
			}
			consumed += len(spec.Prefix)
		case "date":
			if !dated {
				continue
			}
			date, err := time.Parse(layout, value[consumed:consumed+dateLength])
			if err != nil {
				return 0, fmt.Errorf("number date segment is not a legal %s date", spec.DateFormat)
			}
			parsedDate = date
			consumed += dateLength
		case "number":
			numberText = value[consumed : consumed+numberLength]
			consumed += numberLength
		}
	}
	sequence, err := strconv.ParseInt(numberText, 10, 64)
	if err != nil || sequence < spec.Start || sequence > MaxAutoNumber ||
		AutoNumberValue(spec, sequence, parsedDate) != value {
		return 0, fmt.Errorf("number is outside its fixed format or range")
	}
	return sequence, nil
}

// AutoNumberDateSegment returns the date slice of a stored value. It assumes
// ParseAutoNumberValue already accepted the value.
func AutoNumberDateSegment(spec AutoNumberSpec, value string) string {
	layout, dated := autoNumberDateLayouts[spec.DateFormat]
	if !dated {
		return ""
	}
	dateLength := len(time.Date(2006, 1, 2, 0, 0, 0, 0, time.UTC).Format(layout))
	consumed := 0
	for _, token := range spec.orderTokens() {
		switch token {
		case "prefix":
			consumed += len(spec.Prefix)
		case "date":
			return value[consumed : consumed+dateLength]
		case "number":
			consumed += len(value) - len(spec.Prefix) - dateLength
		}
	}
	return ""
}
