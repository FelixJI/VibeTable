package v2

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxAutoNumber = int64(1<<53 - 1)

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
	return nil
}

func AutoNumberValue(spec AutoNumberSpec, sequence int64) string {
	return spec.Prefix + fmt.Sprintf("%0*d", spec.Width, sequence)
}

// ParseAutoNumberValue checks stored business values without guessing allocator state.
func ParseAutoNumberValue(spec AutoNumberSpec, value string) (int64, error) {
	if !strings.HasPrefix(value, spec.Prefix) {
		return 0, fmt.Errorf("number prefix does not match")
	}
	sequence, err := strconv.ParseInt(strings.TrimPrefix(value, spec.Prefix), 10, 64)
	if err != nil || sequence < spec.Start || sequence > MaxAutoNumber || AutoNumberValue(spec, sequence) != value {
		return 0, fmt.Errorf("number is outside its fixed format or range")
	}
	return sequence, nil
}
