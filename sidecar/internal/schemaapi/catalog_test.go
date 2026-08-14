package schemaapi

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func TestDescribeReturnsCancellationBeforeTouchingStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(nil).Describe(ctx, "tbl_orders")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Describe() error = %#v", err)
	}
}

func TestValidateStoredDataRevisionRejectsMissingNegativeAndFractionalValues(t *testing.T) {
	for _, value := range []any{
		nil, -1.0, 1.5, "0", float64(1<<53) + 2, uint64(1 << 53),
	} {
		err := validateStoredDataRevision(value)
		var productErr *schemaerror.ProductError
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
