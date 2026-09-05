package productrpc

import (
	"context"
	"errors"
	"testing"
)

func TestReconcileRegistrationHandlerRejectsMalformedParams(t *testing.T) {
	registration := ReconcileRegistration(nil)

	_, err := registration.Handler(context.Background(), []byte(`{`))
	if err == nil || err.Error() != "events.reconcile params are invalid" {
		t.Fatalf("invalid params error = %v", err)
	}
}

func TestValidateReconcileParamsRequiresAllNamedFields(t *testing.T) {
	err := validateReconcileParams([]byte(
		`{"tableId":"orders","schemaRevision":"schema_0001","extra":"data_0000"}`,
	))
	if err == nil || err.Error() != "events.reconcile parameters are incomplete" {
		t.Fatalf("missing param error = %v", err)
	}
}

func TestReconcileProductErrorPreservesPrivateErrors(t *testing.T) {
	want := errors.New("private reconcile failure")

	if got := reconcileProductError(want); !errors.Is(got, want) {
		t.Fatalf("reconcile error = %v, want %v", got, want)
	}
}
