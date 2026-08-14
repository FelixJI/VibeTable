package v2

import (
	"fmt"
	"regexp"
	"strconv"
)

var schemaRevisionPattern = regexp.MustCompile(`^schema_([0-9]+)$`)

// FormatSchemaRevision formats the stable wire identity used by schema-v2
// intents, receipts, snapshots and durable execution state.
func FormatSchemaRevision(revision int64) string {
	return fmt.Sprintf("schema_%04d", revision)
}

// ParseSchemaRevision parses the stable schema_<number> wire identity.
func ParseSchemaRevision(value string) (int64, error) {
	matches := schemaRevisionPattern.FindStringSubmatch(value)
	if matches == nil {
		return 0, fmt.Errorf("schema revision must use schema_<number> format")
	}
	revision, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || revision < 0 {
		return 0, fmt.Errorf("invalid schema revision")
	}
	return revision, nil
}
