package productrpc

import (
	"context"
	"errors"
	"testing"
)

func TestAttachmentListRegistrationHandlerRejectsInvalidParams(t *testing.T) {
	registration := AttachmentListRegistration(nil, nil)

	_, err := registration.Handler(context.Background(), []byte(`{}`))
	if err == nil || err.Error() != "file.list requires tableId, recordId, and fieldId" {
		t.Fatalf("invalid params error = %v", err)
	}
}

func TestAttachmentProductErrorPreservesPrivateErrors(t *testing.T) {
	want := errors.New("private attachment failure")

	if got := attachmentProductError(want); !errors.Is(got, want) {
		t.Fatalf("attachment error = %v, want %v", got, want)
	}
}
