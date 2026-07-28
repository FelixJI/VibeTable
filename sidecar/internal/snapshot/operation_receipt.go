package snapshot

import (
	"context"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

type OperationReceiptBuilder func(
	Record,
) (protocolv2.OperationReceipt, error)

type OperationReceiptCatalog interface {
	PublishWithOperationReceipt(
		context.Context,
		Record,
		protocolv2.OperationReceipt,
	) error
}

type operationReceiptBuilderKey struct{}

// WithOperationReceiptBuilder requires Capture to publish the exact RPC
// receipt atomically with the snapshot catalog record.
func WithOperationReceiptBuilder(
	ctx context.Context,
	builder OperationReceiptBuilder,
) (context.Context, error) {
	if builder == nil {
		return nil, errors.New("snapshot.operation_receipt_builder_required")
	}
	return context.WithValue(ctx, operationReceiptBuilderKey{}, builder), nil
}

func operationReceiptBuilder(
	ctx context.Context,
) (OperationReceiptBuilder, bool) {
	builder, ok := ctx.Value(operationReceiptBuilderKey{}).(OperationReceiptBuilder)
	return builder, ok
}
