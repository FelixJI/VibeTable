package productrpc

import "errors"

type PresetError struct{ Code, Message, Field string }

func (e *PresetError) Error() string { return e.Message }
func presetErrorData(method string, err error) (map[string]any, bool) {
	if method != "preset.save" && method != "preset.delete" {
		return nil, false
	}
	var domain *PresetError
	if !errors.As(err, &domain) || domain == nil {
		return nil, false
	}
	if (domain.Code != "preset_edit_conflict" || domain.Field != "expectedRevision") && (domain.Code != "preset_idempotency_conflict" || domain.Field != "operationId") {
		return nil, false
	}
	message := "Preset changed elsewhere."
	if domain.Code == "preset_idempotency_conflict" {
		message = "Operation was used for another Preset request."
	}
	return map[string]any{"kind": "insights_error", "message": message, "code": domain.Code, "field": domain.Field}, true
}
