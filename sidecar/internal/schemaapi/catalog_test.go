package schemaapi

import (
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestValidateStoredDataRevisionRejectsMissingNegativeAndFractionalValues(t *testing.T) {
	for _, value := range []any{
		nil, -1.0, 1.5, "0", float64(1<<53) + 2, uint64(1 << 53),
	} {
		err := validateStoredDataRevision(value)
		var productErr *schema.ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "schema.metadata.invalid_data_revision" {
			t.Fatalf("validateStoredDataRevision(%#v) = %#v", value, err)
		}
	}
	for _, value := range []any{0.0, 1.0, int64(2)} {
		if err := validateStoredDataRevision(value); err != nil {
			t.Fatalf("validateStoredDataRevision(%#v): %v", value, err)
		}
	}
}
