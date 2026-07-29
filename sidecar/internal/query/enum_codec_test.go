package query

import "testing"

func TestDecodeMultipleEnumKeepsExplicitEmptyValueTypedAsCollection(t *testing.T) {
	t.Parallel()
	descriptor := &EnumDescriptor{Multiple: true}
	for _, stored := range []any{"", []byte{}} {
		value, ok := decodeEnumValue(descriptor, stored).([]any)
		if !ok || len(value) != 0 {
			t.Fatalf("decode empty multi-enum %#v = %#v", stored, value)
		}
	}
}
