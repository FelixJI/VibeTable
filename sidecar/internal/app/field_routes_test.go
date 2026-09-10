package app

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func TestClassifyFieldErrorPreservesComputationCycle(t *testing.T) {
	details := map[string]any{"cycle": []map[string]string{{"tableId": "table", "fieldId": "formula"}}}
	domain := &schemaerror.ProductError{
		Code: "schema.computation.cycle", Path: "definition.fields",
		Message: "computed field dependency cycle detected", Details: details,
	}
	result := classifyFieldError(fmt.Errorf("apply computed metadata: %w", domain))
	if result.status != http.StatusUnprocessableEntity || result.code != domain.Code ||
		result.path != domain.Path || result.message != domain.Message ||
		!reflect.DeepEqual(result.details, details) || result.retryable {
		t.Fatalf("computation cycle was hidden or changed: %#v", result)
	}
}
