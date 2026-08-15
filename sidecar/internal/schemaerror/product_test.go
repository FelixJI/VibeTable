package schemaerror

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestProductErrorPreservesStableWireAndHidesCause(t *testing.T) {
	private := errors.New("private storage detail")
	productErr := WithCause(&ProductError{
		Code: "schema.storage.failed", Path: "", Message: "schema storage operation failed",
	}, private)
	if !errors.Is(productErr, private) {
		t.Fatal("wrapped cause is unavailable to internal callers")
	}
	raw, err := json.Marshal(productErr)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"contractVersion":"2.0","code":"schema.storage.failed","path":"","message":"schema storage operation failed","details":{},"retryable":false}`
	if string(raw) != want {
		t.Fatalf("ProductError wire = %s", raw)
	}
	if bytes.Contains(raw, []byte(private.Error())) {
		t.Fatal("private cause leaked into public wire")
	}
}
