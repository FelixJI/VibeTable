package productrpc

import "errors"

type VersionError struct{ Code, Message string }

func (e *VersionError) Error() string { return e.Message }
func versionErrorData(method string, err error) (map[string]any, bool) {
	switch method {
	case "version.list", "version.create", "version.save", "version.compare", "version.promote", "version.delete":
	default:
		return nil, false
	}
	var domain *VersionError
	if !errors.As(err, &domain) || domain == nil || domain.Message == "" {
		return nil, false
	}
	switch domain.Code {
	case "version_not_found", "version_record_unavailable", "version_values_not_allowed", "version_audit_missing", "version_audit_invalid", "version_revision_missing", "version_edit_conflict", "version_main_conflict", "version_not_restorable", "version_idempotency_conflict", "version_operation_expired", "version_storage_invalid", "version_persistence_failed":
	default:
		return nil, false
	}
	return map[string]any{"kind": "insights_error", "code": domain.Code, "message": domain.Message}, true
}
