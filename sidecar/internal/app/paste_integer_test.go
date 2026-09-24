package app

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"
)

func TestPasteIntegerMachineBounds(t *testing.T) {
	upper := math.Ldexp(1, strconv.IntSize-1)
	for _, raw := range []string{
		fmt.Sprintf("%.0f", upper),
		fmt.Sprintf("%.1f", upper),
		fmt.Sprintf("%.17e", upper),
		fmt.Sprintf("\"%.0f\"", upper),
		"18446744073709551616",
		"-18446744073709551616",
		"-9223372036854775809",
	} {
		t.Run(raw, func(t *testing.T) {
			if value, err := pasteInteger(json.RawMessage(raw)); err == nil {
				t.Fatalf("out-of-range index %s accepted as %d", raw, value)
			}
		})
	}
	for _, value := range []int{math.MinInt, 0, 1, math.MaxInt} {
		for _, raw := range []string{strconv.Itoa(value), strconv.Quote(strconv.Itoa(value))} {
			actual, err := pasteInteger(json.RawMessage(raw))
			if err != nil || actual != value {
				t.Fatalf("index %s = %d, %v; want %d", raw, actual, err, value)
			}
		}
	}
	for _, raw := range []string{"1.0", "1e2"} {
		expected := 1
		if raw == "1e2" {
			expected = 100
		}
		actual, err := pasteInteger(json.RawMessage(raw))
		if err != nil || actual != expected {
			t.Fatalf("integral coordinate %s = %d, %v", raw, actual, err)
		}
	}
}
