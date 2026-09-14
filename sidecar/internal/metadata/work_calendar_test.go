package metadata

import (
	"strings"
	"testing"
)

func TestWorkCalendarDateUnicodeAndBudget(t *testing.T) {
	for _, test := range []struct {
		name, date, kind, label string
		valid                   bool
	}{
		{"leap", "2024-02-29", "holiday", " 闰日 ", true},
		{"bad-leap", "2026-02-29", "holiday", "", false},
		{"small-year", "0099-01-01", "holiday", "", false},
		{"minimum", "0100-01-01", "holiday", "", true},
		{"maximum", "9999-12-31", "workday", "", true},
		{"bad-kind", "2026-09-10", "weekend", "", false},
		{"emoji-limit", "2026-09-10", "holiday", strings.Repeat("😀", 20), true},
		{"emoji-over", "2026-09-10", "holiday", strings.Repeat("😀", 21), false},
		{"ascii-over", "2026-09-10", "holiday", strings.Repeat("a", 41), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateWorkCalendar([]WorkCalendarOverride{{Date: test.date, Kind: test.kind, Name: test.label}})
			if (err == nil) != test.valid {
				t.Fatalf("err=%v", err)
			}
		})
	}
	for _, values := range [][]WorkCalendarOverride{nil, make([]WorkCalendarOverride, MaxWorkCalendarOverrides+1), {{Date: "2026-09-10", Kind: "holiday"}, {Date: "2026-09-10", Kind: "workday"}}} {
		if _, err := ValidateWorkCalendar(values); err == nil {
			t.Fatal("accepted invalid calendar")
		}
	}
}
