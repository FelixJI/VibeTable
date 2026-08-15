package schemaerror

import (
	"encoding/json"
	"fmt"
)

const ContractVersion = "2.0"

// ProductError is the stable public error envelope shared by Schema V2 routes.
// Domain field/table definitions live exclusively in internal/schema/v2.
type ProductError struct {
	Code      string         `json:"code"`
	Path      string         `json:"path"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool           `json:"-"`
	cause     error
}

func (err *ProductError) Error() string {
	return fmt.Sprintf("%s at %s: %s", err.Code, err.Path, err.Message)
}

func (err *ProductError) Unwrap() error { return err.cause }

func WithCause(productErr *ProductError, cause error) *ProductError {
	productErr.cause = cause
	return productErr
}

func (err ProductError) MarshalJSON() ([]byte, error) {
	details := err.Details
	if details == nil {
		details = map[string]any{}
	}
	return json.Marshal(struct {
		ContractVersion string         `json:"contractVersion"`
		Code            string         `json:"code"`
		Path            string         `json:"path"`
		Message         string         `json:"message"`
		Details         map[string]any `json:"details"`
		Retryable       bool           `json:"retryable"`
	}{
		ContractVersion: ContractVersion,
		Code:            err.Code,
		Path:            err.Path,
		Message:         err.Message,
		Details:         details,
		Retryable:       err.Retryable,
	})
}
