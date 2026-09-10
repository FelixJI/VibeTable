package productrpc

import (
	"errors"
	"strings"
)

const CodeContentModel = -32180

// ContentModelError preserves the closed workbench content error envelope. It
// cannot select an arbitrary RPC code or another application's error domain.
type ContentModelError struct {
	Code, Message string
	Path          *string
}

func (err *ContentModelError) Error() string { return err.Message }

func contentModelErrorData(method string, err error) (map[string]any, bool) {
	var domain *ContentModelError
	if !errors.As(err, &domain) || domain == nil || !publicErrorCodePattern.MatchString(domain.Code) || strings.TrimSpace(domain.Message) == "" {
		return nil, false
	}
	switch method {
	case "contentProfile.load", "contentProfile.commit", "contentProfile.delete", "recordDocumentLink.list", "recordDocumentLink.commit", "recordDocumentLink.repair", "recordDocumentLink.delete":
	default:
		return nil, false
	}
	switch domain.Code {
	case "content_model.edit_conflict", "content_model.idempotency_conflict", "content_model.not_found", "content_model.persistence_failed", "content_model.storage_invalid", "content_profile.body_type_invalid", "content_profile.edit_conflict", "content_profile.field_missing", "content_profile.not_found", "content_profile.search_field_duplicate", "content_profile.search_field_invalid", "content_profile.summary_type_invalid", "content_profile.table_missing", "content_profile.title_type_invalid", "record_document_link.edit_conflict", "record_document_link.not_found", "record_document_link.record_lookup_failed", "record_document_link.record_missing":
	default:
		return nil, false
	}
	data := map[string]any{"kind": "content_model_error", "message": domain.Message, "code": domain.Code}
	if domain.Path != nil {
		data["path"] = *domain.Path
	}
	return data, true
}
