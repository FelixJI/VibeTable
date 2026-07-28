package filehistory

import (
	"context"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

// Publication is the complete authoritative state about to become visible at
// the file-history head CAS boundary.
type Publication struct {
	Head      CurrentHead
	Documents []Document
}

type OperationReceiptBuilder func(
	Publication,
) (protocolv2.OperationReceipt, error)

type operationReceiptBuilderKey struct{}

// WithOperationReceiptBuilder binds the RPC projection builder to a
// file-history mutation. Production SQLite head stores fail closed if a
// builder is present but cannot publish its receipt atomically with the head.
func WithOperationReceiptBuilder(
	ctx context.Context,
	builder OperationReceiptBuilder,
) (context.Context, error) {
	if builder == nil {
		return nil, errors.New("filehistory.operation_receipt_builder_required")
	}
	return context.WithValue(ctx, operationReceiptBuilderKey{}, builder), nil
}

func operationReceiptBuilder(
	ctx context.Context,
) (OperationReceiptBuilder, bool) {
	builder, ok := ctx.Value(operationReceiptBuilderKey{}).(OperationReceiptBuilder)
	return builder, ok
}
