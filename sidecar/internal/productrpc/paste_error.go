package productrpc

import (
	"errors"
	"strings"
)

const CodePaste = -32040

// PasteError preserves the pre-migration Python PasteError envelope.
// Only the two paste methods may expose this type to the renderer.
type PasteError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *PasteError) Error() string { return e.Message }

func pasteErrorData(method string, err error) (map[string]any, bool) {
	if method != "table.previewPaste" && method != "table.applyPaste" {
		return nil, false
	}
	var paste *PasteError
	if !errors.As(err, &paste) || paste == nil || paste.Code == "" || strings.TrimSpace(paste.Message) == "" {
		return nil, false
	}
	details, sanitizeErr := sanitizeDetails(paste.Details)
	if sanitizeErr != nil {
		return nil, false
	}
	data := map[string]any{"kind": "paste_error", "message": paste.Message, "code": paste.Code}
	for key, value := range details {
		if key == "kind" || key == "message" || key == "code" {
			continue
		}
		data[key] = value
	}
	return data, true
}
