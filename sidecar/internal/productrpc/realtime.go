package productrpc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
)

func ReconcileRegistration(source realtime.RevisionSource) Registration {
	return Registration{
		Method: "events.reconcile", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: validateReconcileParams,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request realtime.ReconcileRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, errors.New("events.reconcile params are invalid")
			}
			result, err := realtime.Reconcile(ctx, source, request)
			if err == nil {
				return result, nil
			}
			return nil, reconcileProductError(err)
		},
	}
}

func reconcileProductError(err error) error {
	var realtimeError *realtime.Error
	if !errors.As(err, &realtimeError) {
		return err
	}
	return &PublicError{
		Code: realtimeError.Code, Message: realtimeError.Message,
		Retryable: realtimeError.Retryable,
	}
}

func validateReconcileParams(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 3 {
		return errors.New("events.reconcile requires three string parameters")
	}
	for _, name := range []string{"tableId", "schemaRevision", "dataRevision"} {
		value, ok := fields[name]
		if !ok {
			return errors.New("events.reconcile parameters are incomplete")
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil || text == "" {
			return errors.New("events.reconcile parameters must be non-empty strings")
		}
	}
	return nil
}
