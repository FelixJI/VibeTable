package v2

import "testing"

func TestSchemaRevisionCodecPreservesWireContract(t *testing.T) {
	for _, test := range []struct {
		revision int64
		wire     string
	}{
		{revision: 0, wire: "schema_0000"},
		{revision: 1, wire: "schema_0001"},
		{revision: 7, wire: "schema_0007"},
		{revision: 10_000, wire: "schema_10000"},
	} {
		if wire := FormatSchemaRevision(test.revision); wire != test.wire {
			t.Fatalf("FormatSchemaRevision(%d) = %q", test.revision, wire)
		}
		parsed, err := ParseSchemaRevision(test.wire)
		if err != nil || parsed != test.revision {
			t.Fatalf("ParseSchemaRevision(%q) = %d, %v", test.wire, parsed, err)
		}
	}
	parsed, err := ParseSchemaRevision("schema_7")
	if err != nil || parsed != 7 {
		t.Fatalf("unpadded revision = %d, %v", parsed, err)
	}
}

func TestSchemaRevisionCodecPreservesErrors(t *testing.T) {
	for _, value := range []string{"", "schema_", "schema_-1", "data_0001", "schema_1x"} {
		if _, err := ParseSchemaRevision(value); err == nil ||
			err.Error() != "schema revision must use schema_<number> format" {
			t.Fatalf("ParseSchemaRevision(%q) error = %v", value, err)
		}
	}
	if _, err := ParseSchemaRevision("schema_999999999999999999999999999"); err == nil ||
		err.Error() != "invalid schema revision" {
		t.Fatalf("overflow error = %v", err)
	}
}
