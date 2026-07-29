package app

import (
	"context"
	"reflect"
	"testing"
)

func TestRunIdempotentBusinessWriteSelectsDedicatedGate(t *testing.T) {
	var calls []string
	gates := []businessWriteGate{
		func(
			context.Context,
			string,
			string,
			func(context.Context) error,
		) error {
			calls = append(calls, "regular")
			return nil
		},
		func(
			ctx context.Context,
			kind string,
			identity string,
			apply func(context.Context) error,
		) error {
			calls = append(calls, kind+":"+identity)
			return apply(ctx)
		},
	}
	err := runIdempotentBusinessWrite(
		context.Background(),
		gates,
		"formula.backfill.enqueue",
		"orders:schema_0001",
		func(context.Context) error {
			calls = append(calls, "apply")
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"formula.backfill.enqueue:orders:schema_0001",
		"apply",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}
