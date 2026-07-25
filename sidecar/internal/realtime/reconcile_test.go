package realtime

import (
	"context"
	"errors"
	"testing"
)

type fakeRevisionSource struct {
	schemaRevision int64
	dataRevision   int64
	err            error
}

func (source fakeRevisionSource) GetRevision(
	context.Context, string,
) (int64, error) {
	return source.schemaRevision, source.err
}

func (source fakeRevisionSource) GetDataRevision(
	context.Context, string,
) (int64, error) {
	return source.dataRevision, source.err
}

func TestReconcileSelectsDeterministicRefreshAction(t *testing.T) {
	source := fakeRevisionSource{schemaRevision: 3, dataRevision: 9}
	for name, input := range map[string]struct {
		schema string
		data   string
		action string
	}{
		"current":      {"schema_0003", "data_0009", "none"},
		"stale data":   {"schema_0003", "data_0008", "refresh-data"},
		"ahead data":   {"schema_0003", "data_0010", "refresh-data"},
		"stale schema": {"schema_0002", "data_0009", "reload-schema"},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := Reconcile(
				context.Background(), source,
				ReconcileRequest{
					TableID:        "orders",
					SchemaRevision: input.schema,
					DataRevision:   input.data,
				},
			)
			if err != nil || result.Action != input.action ||
				result.CurrentSchemaRevision != "schema_0003" ||
				result.CurrentDataRevision != "data_0009" {
				t.Fatalf("result = %#v, err=%v", result, err)
			}
		})
	}
}

func TestReconcileRejectsInvalidAndFailClosedStorage(t *testing.T) {
	for _, input := range []ReconcileRequest{
		{},
		{TableID: "orders", SchemaRevision: "3", DataRevision: "data_0001"},
		{TableID: "orders", SchemaRevision: "schema_0001", DataRevision: "1"},
	} {
		_, err := Reconcile(
			context.Background(), fakeRevisionSource{}, input,
		)
		var productErr *Error
		if !errors.As(err, &productErr) ||
			productErr.Code != "realtime.request.invalid" {
			t.Fatalf("invalid request error = %#v", err)
		}
	}
	_, err := Reconcile(
		context.Background(),
		fakeRevisionSource{err: errors.New("disk failed")},
		ReconcileRequest{
			TableID:        "orders",
			SchemaRevision: "schema_0001",
			DataRevision:   "data_0001",
		},
	)
	var productErr *Error
	if !errors.As(err, &productErr) ||
		productErr.Code != "realtime.storage_failed" ||
		!productErr.Retryable {
		t.Fatalf("storage error = %#v", err)
	}
}
