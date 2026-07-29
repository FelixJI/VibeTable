package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

type batchContextKey struct{}

type recordingBatchKernel struct {
	calls       int
	sawBoundCtx bool
	request     mutation.Request
	err         error
}

func (kernel *recordingBatchKernel) Apply(
	ctx context.Context,
	request mutation.Request,
) (mutation.Receipt, error) {
	kernel.calls++
	kernel.sawBoundCtx, _ = ctx.Value(batchContextKey{}).(bool)
	kernel.request = request
	return mutation.Receipt{}, kernel.err
}

func TestApplyKernelBatchUsesConfiguredBusinessWriteGate(t *testing.T) {
	kernel := &recordingBatchKernel{}
	service := New(nil, kernel)
	gateCalls := 0
	service.SetBusinessWriteGate(func(
		ctx context.Context,
		kind string,
		identity string,
		apply func(context.Context) error,
	) error {
		gateCalls++
		if kind != "formula.backfill.batch" ||
			identity != "job-1:rows-1-100" {
			t.Fatalf("gate identity = %q %q", kind, identity)
		}
		return apply(context.WithValue(ctx, batchContextKey{}, true))
	})
	request := mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "job-1:rows-1-100",
		IdempotencyKey:  "job-1:rows-1-100",
		TableID:         "orders",
		SchemaRevision:  "schema_0001",
	}

	if _, err := service.applyKernelBatch(
		context.Background(),
		"formula.backfill.batch",
		"job-1:rows-1-100",
		request,
	); err != nil {
		t.Fatal(err)
	}
	if gateCalls != 1 || kernel.calls != 1 || !kernel.sawBoundCtx {
		t.Fatalf(
			"gate=%d kernel=%d bound=%v",
			gateCalls,
			kernel.calls,
			kernel.sawBoundCtx,
		)
	}
	if kernel.request.IdempotencyKey != request.IdempotencyKey {
		t.Fatalf("request = %#v", kernel.request)
	}
}

func TestApplyKernelBatchPropagatesGateFailureWithoutCallingKernel(
	t *testing.T,
) {
	kernel := &recordingBatchKernel{}
	service := New(nil, kernel)
	expected := errors.New("workspace.write_rejected")
	service.SetBusinessWriteGate(func(
		context.Context,
		string,
		string,
		func(context.Context) error,
	) error {
		return expected
	})

	_, err := service.applyKernelBatch(
		context.Background(),
		"formula.fanout.batch",
		"fanout-1",
		mutation.Request{},
	)
	if !errors.Is(err, expected) || kernel.calls != 0 {
		t.Fatalf("err=%v kernel calls=%d", err, kernel.calls)
	}
}
