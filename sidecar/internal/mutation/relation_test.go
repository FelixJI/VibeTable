package mutation

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeRelationIDs(t *testing.T) {
	for _, testCase := range []struct {
		value any
		want  []string
	}{
		{value: nil, want: []string{}},
		{value: "record-1", want: []string{"record-1"}},
		{value: []string{"record-1", "record-2"}, want: []string{"record-1", "record-2"}},
		{value: []any{"record-1", "record-2"}, want: []string{"record-1", "record-2"}},
	} {
		got, err := normalizeRelationIDs(testCase.value)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("normalizeRelationIDs(%#v) = %#v", testCase.value, got)
		}
	}
}

func TestNormalizeRelationIDsRejectsNonStringTargets(t *testing.T) {
	for _, value := range []any{42, []any{"record-1", 42}, []string{"record-1", ""}} {
		_, err := normalizeRelationIDs(value)
		var productErr *ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "mutation.relation.invalid_value" {
			t.Fatalf("normalizeRelationIDs(%#v) error = %#v", value, err)
		}
	}
}
