package schemaapi

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestDescribeReturnsCancellationBeforeTouchingStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(nil).Describe(ctx, "tbl_orders")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Describe() error = %#v", err)
	}
}

func TestParseStoredDataRevisionRejectsMissingNegativeAndFractionalValues(t *testing.T) {
	for _, value := range []any{
		nil, -1.0, 1.5, "0", float64(1<<53) + 2, uint64(1 << 53),
	} {
		_, err := schemaexecution.ParseStoredRevision(value, "schema.metadata.invalid_data_revision", "dataRevision")
		var productErr *schemaerror.ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "schema.metadata.invalid_data_revision" {
			t.Fatalf("ParseStoredRevision(%#v) = %#v", value, err)
		}
	}
	for _, value := range []any{0.0, 1.0, int64(2)} {
		if _, err := schemaexecution.ParseStoredRevision(value, "schema.metadata.invalid_data_revision", "dataRevision"); err != nil {
			t.Fatalf("ParseStoredRevision(%#v): %v", value, err)
		}
	}
}
