package lookup

import (
	"math"
	"reflect"
	"testing"

	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// TestAggregateLookupValuesPinsFixedSampleOracle is the VibeTable contract
// sample: [10,20,0,null,10] -> countRecords 5, countNonEmpty 4,
// countDistinct 3, sum 40, average 10, min 0, max 20, with values keeping the
// historical shape and distinct keeping first-occurrence order including null.
func TestAggregateLookupValuesPinsFixedSampleOracle(t *testing.T) {
	t.Parallel()
	sample := []any{float64(10), float64(20), float64(0), nil, float64(10)}
	for _, testCase := range []struct {
		mode string
		want any
	}{
		{mode: v2.LookupAggregationValues, want: sample},
		{mode: v2.LookupAggregationDistinct, want: []any{float64(10), float64(20), float64(0), nil}},
		{mode: v2.LookupAggregationCountRecords, want: float64(5)},
		{mode: v2.LookupAggregationCountNonEmpty, want: float64(4)},
		{mode: v2.LookupAggregationCountDistinct, want: float64(3)},
		{mode: v2.LookupAggregationSum, want: float64(40)},
		{mode: v2.LookupAggregationAverage, want: float64(10)},
		{mode: v2.LookupAggregationMin, want: float64(0)},
		{mode: v2.LookupAggregationMax, want: float64(20)},
	} {
		got, err := aggregateLookupValues(testCase.mode, sample)
		if err != nil {
			t.Fatalf("%s: %v", testCase.mode, err)
		}
		if !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("%s = %#v, want %#v", testCase.mode, got, testCase.want)
		}
	}
}

// TestAggregateLookupValuesEmptySet keeps counts and sum at zero while
// average/min/max are null; zero and false stay non-empty values.
func TestAggregateLookupValuesEmptySet(t *testing.T) {
	t.Parallel()
	empty := []any{}
	for _, testCase := range []struct {
		mode string
		want any
	}{
		{mode: v2.LookupAggregationValues, want: nil},
		{mode: v2.LookupAggregationDistinct, want: []any{}},
		{mode: v2.LookupAggregationCountRecords, want: float64(0)},
		{mode: v2.LookupAggregationCountNonEmpty, want: float64(0)},
		{mode: v2.LookupAggregationCountDistinct, want: float64(0)},
		{mode: v2.LookupAggregationSum, want: float64(0)},
		{mode: v2.LookupAggregationAverage, want: nil},
		{mode: v2.LookupAggregationMin, want: nil},
		{mode: v2.LookupAggregationMax, want: nil},
	} {
		got, err := aggregateLookupValues(testCase.mode, empty)
		if err != nil {
			t.Fatalf("%s: %v", testCase.mode, err)
		}
		if !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("%s = %#v, want %#v", testCase.mode, got, testCase.want)
		}
	}
	if got, _ := aggregateLookupValues(v2.LookupAggregationCountNonEmpty, []any{float64(0), false, "", nil}); got != float64(2) {
		t.Fatalf("zero/false/empty counting = %#v", got)
	}
	if got, _ := aggregateLookupValues(v2.LookupAggregationCountDistinct, []any{"a", "", nil, "a"}); got != float64(1) {
		t.Fatalf("countDistinct with empties = %#v", got)
	}
}

// TestAggregateLookupNumericRejectsNonNumericValues pins the explicit error
// contract: text, empty strings, booleans and non-finite numbers are errors,
// never silent zeros. Null stays ignored.
func TestAggregateLookupNumericRejectsNonNumericValues(t *testing.T) {
	t.Parallel()
	for _, values := range [][]any{
		{"10"},
		{float64(1), ""},
		{true},
		{float64(1), false},
	} {
		if _, err := aggregateLookupValues(v2.LookupAggregationSum, values); err == nil {
			t.Fatalf("non-numeric values accepted: %#v", values)
		}
	}
	if _, err := aggregateLookupValues(v2.LookupAggregationMin, []any{inf()}); err == nil {
		t.Fatal("non-finite number accepted")
	}
	if got, err := aggregateLookupValues(v2.LookupAggregationSum, []any{nil}); err != nil || got != float64(0) {
		t.Fatalf("null-only sum = %#v, %v", got, err)
	}
}

// TestLookupNumericValueNeverUnwrapsEnvelopeLookalikes pins the reducer
// boundary: computed envelopes are unwrapped only at the schema-declared read
// seam, so a JSON payload that merely looks like an envelope is data — a
// number when numeric, an explicit type error otherwise — never a smuggled
// unpackage. Empty JSONRaw stays the storage null sentinel.
func TestLookupNumericValueNeverUnwrapsEnvelopeLookalikes(t *testing.T) {
	t.Parallel()
	lookalike := pbtypes.JSONRaw(`{"state":"ready","value":7.5,"version":{"definitionVersion":1,"sourceDataRevision":1,"dependencyWatermark":"sha256:00"}}`)
	if _, _, err := lookupNumericValue(lookalike); err == nil {
		t.Fatal("envelope lookalike was unpacked inside the reducer")
	}
	if got, present, err := lookupNumericValue(pbtypes.JSONRaw(`2.5`)); err != nil || !present || got != 2.5 {
		t.Fatalf("plain JSON number = %v, %v, %v", got, present, err)
	}
	if _, present, err := lookupNumericValue(pbtypes.JSONRaw{}); err != nil || present {
		t.Fatalf("empty JSONRaw sentinel = %v, %v", present, err)
	}
	if _, _, err := lookupNumericValue(pbtypes.JSONRaw(`"text"`)); err == nil {
		t.Fatal("JSON text accepted as number")
	}
}

// TestAggregateLookupNumericBoundsOverflowToSumAndAverageOnly pins that a
// finite-input overflow is a sum/average error while min/max stay valid:
// two MaxFloat64 values are legal extremes, not an aggregation failure.
func TestAggregateLookupNumericBoundsOverflowToSumAndAverageOnly(t *testing.T) {
	t.Parallel()
	extremes := []any{math.MaxFloat64, math.MaxFloat64}
	if got, err := aggregateLookupValues(v2.LookupAggregationMax, extremes); err != nil || got != math.MaxFloat64 {
		t.Fatalf("max of extremes = %#v, %v", got, err)
	}
	if got, err := aggregateLookupValues(v2.LookupAggregationMin, extremes); err != nil || got != math.MaxFloat64 {
		t.Fatalf("min of extremes = %#v, %v", got, err)
	}
	for _, mode := range []string{v2.LookupAggregationSum, v2.LookupAggregationAverage} {
		if _, err := aggregateLookupValues(mode, extremes); err == nil {
			t.Fatalf("%s of extremes was accepted", mode)
		}
	}
}

// TestLookupPageWindowSlicesFullSetPages pins the presentation-only window
// used after complete-set traversals.
func TestLookupPageWindowSlicesFullSetPages(t *testing.T) {
	t.Parallel()
	items := []int{0, 1, 2, 3, 4}
	if got := lookupPageWindow(items, 0, 2); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("first page = %#v", got)
	}
	if got := lookupPageWindow(items, 3, 10); !reflect.DeepEqual(got, []int{3, 4}) {
		t.Fatalf("last page = %#v", got)
	}
	if got := lookupPageWindow(items, 5, 10); !reflect.DeepEqual(got, []int{}) {
		t.Fatalf("past end = %#v", got)
	}
}

func inf() float64 {
	return math.Inf(1)
}
