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

func TestAttachmentTokenRegistrationHandlerRejectsBeforeAuthority(t *testing.T) {
	registration := AttachmentTokenRegistration(nil, nil)
	t.Run("invalid params", func(t *testing.T) {
		result, err := registration.Handler(context.Background(), []byte(`{}`))
		if result != nil || err == nil || err.Error() != "file.token requires attachment identity" {
			t.Fatalf("invalid token params: result=%v error=%v", result, err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := registration.Handler(ctx, []byte(`{"tableId":"t","recordId":"r","fieldId":"f","storedName":"s"}`))
		if result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled token request: result=%v error=%v", result, err)
		}
	})
}
