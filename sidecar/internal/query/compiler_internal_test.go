package query

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestViewGroupDateBucketsBindSQLLiterals(t *testing.T) {
	tests := []struct {
		bucket GroupBucket
		values []string
	}{
		{bucket: GroupBucketYear, values: []string{"%Y"}},
		{bucket: GroupBucketQuarter, values: []string{"%Y", "%m", "-Q"}},
		{bucket: GroupBucketMonth, values: []string{"%Y-%m"}},
		{bucket: GroupBucketWeek, values: []string{"%Y-W%W"}},
		{bucket: GroupBucketDay, values: []string{"%Y-%m-%d"}},
		{bucket: GroupBucketHour, values: []string{"%Y-%m-%dT%H"}},
	}
	for _, test := range tests {
		t.Run(string(test.bucket), func(t *testing.T) {
			compiler := &compiler{params: make(map[string]any)}
			expression, err := compiler.viewGroupExpression(
				resolvedField{
					sql:        quote("created_at"),
					descriptor: FieldDescriptor{Type: FieldTypeDate},
				},
				test.bucket,
				0,
				"groups[0].bucket",
			)
			if err != nil {
				t.Fatalf("viewGroupExpression(%s): %v", test.bucket, err)
			}
			if strings.Contains(expression, "'") {
				t.Fatalf("date group SQL contains a quoted literal: %s", expression)
			}
			values := make([]string, 0, len(compiler.params))
			for _, value := range compiler.params {
				text, ok := value.(string)
				if !ok {
					t.Fatalf("bound date group value has type %T", value)
				}
				values = append(values, text)
			}
			sort.Strings(values)
			want := append([]string(nil), test.values...)
			sort.Strings(want)
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("bound values = %#v, want %#v", values, want)
			}
		})
	}
}
