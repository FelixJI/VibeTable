package audit

import (
	"errors"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

func TestRestoreMutationErrorReturnsRetryableClaimToTokenPool(t *testing.T) {
	now := time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)
	state := restoreState{expiresAt: now.Add(time.Minute), size: 32}
	service := &Service{
		now:    func() time.Time { return now },
		tokens: map[string]restoreState{},
	}

	err := service.restoreMutationError("retry-token", state, &mutation.ProductError{
		Code:      "mutation.storage.failed",
		Message:   "mutation storage operation failed",
		Retryable: true,
	})
	var historyErr *Error
	if !errors.As(err, &historyErr) ||
		historyErr.Code != "history.storage_failed" ||
		!historyErr.Retryable ||
		historyErr.Details["mutationCode"] != "mutation.storage.failed" ||
		historyErr.Details["mutationMessage"] != "mutation storage operation failed" {
		t.Fatalf("retryable restore error = %#v", err)
	}
	if _, exists := service.tokens["retry-token"]; !exists || service.tokenBytes != state.size {
		t.Fatalf("retryable restore claim was not returned: tokens=%#v bytes=%d", service.tokens, service.tokenBytes)
	}
}

func TestRestoreMutationErrorKeepsValidationFailureConsumed(t *testing.T) {
	now := time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)
	state := restoreState{expiresAt: now.Add(time.Minute), size: 32}
	service := &Service{
		now:    func() time.Time { return now },
		tokens: map[string]restoreState{},
	}
	field := "documents"

	err := service.restoreMutationError("validation-token", state, &mutation.ProductError{
		Code:    "mutation.validation.failed",
		Path:    &field,
		Message: "file failed validation",
	})
	var historyErr *Error
	if !errors.As(err, &historyErr) ||
		historyErr.Code != "restore_validation_failed" ||
		historyErr.Retryable ||
		historyErr.Details["mutationField"] != field ||
		historyErr.Details["mutationMessage"] != "file failed validation" {
		t.Fatalf("validation restore error = %#v", err)
	}
	if _, exists := service.tokens["validation-token"]; exists || service.tokenBytes != 0 {
		t.Fatalf("validation restore claim was unexpectedly returned: tokens=%#v bytes=%d", service.tokens, service.tokenBytes)
	}
}
