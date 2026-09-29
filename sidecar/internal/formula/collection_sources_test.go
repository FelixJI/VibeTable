package formula

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestLoadCollectionSchemaPreservesCancellationAndResolverFailure(t *testing.T) {
	failure := errors.New("source metadata unavailable")
	calls := 0
	ctx := WithCollectionSchemaResolver(context.Background(), func(ctx context.Context, id string) (schemaexecution.Table, error) {
		calls++
		return schemaexecution.Table{}, failure
	})
	if _, err := LoadCollectionSchema(ctx, nil, "source"); !errors.Is(err, failure) {
		t.Fatalf("resolver failure = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := LoadCollectionSchema(canceled, nil, "source"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if calls != 1 {
		t.Fatalf("canceled load called resolver: %d calls", calls)
	}
}
