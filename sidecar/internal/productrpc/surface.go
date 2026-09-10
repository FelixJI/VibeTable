package productrpc

import (
	"errors"
	"strings"
)

const CodeSurface = -32170

// SurfaceError projects the existing Interface error envelope only at its four methods.
type SurfaceError struct {
	Code    string
	Message string
	Path    *string
}

func (e *SurfaceError) Error() string { return e.Message }
func surfaceErrorData(method string, err error) (map[string]any, bool) {
	switch method {
	case "interface.list", "interface.load", "interface.commit", "interface.delete":
	default:
		return nil, false
	}
	var e *SurfaceError
	if !errors.As(err, &e) || e == nil || strings.TrimSpace(e.Message) == "" {
		return nil, false
	}
	switch e.Code {
	case "surface.action_duplicate",
		"surface.action_invalid",
		"surface.action_missing",
		"surface.binding_duplicate",
		"surface.binding_field_duplicate",
		"surface.binding_fields_required",
		"surface.binding_missing",
		"surface.binding_source_required",
		"surface.binding_variable_cycle",
		"surface.binding_variable_duplicate",
		"surface.binding_variable_source_field_invalid",
		"surface.binding_variable_source_invalid",
		"surface.binding_variable_source_missing",
		"surface.binding_variable_source_required",
		"surface.binding_variable_target_invalid",
		"surface.children_invalid",
		"surface.edit_conflict",
		"surface.element_depth",
		"surface.element_duplicate",
		"surface.element_id_required",
		"surface.element_limit",
		"surface.form_action_invalid",
		"surface.id_required",
		"surface.idempotency_conflict",
		"surface.idempotency_key_required",
		"surface.interface_id_invalid",
		"surface.name_required",
		"surface.navigation_action_invalid",
		"surface.not_found",
		"surface.page_duplicate",
		"surface.page_missing",
		"surface.page_title_required",
		"surface.pages_required",
		"surface.persistence_failed",
		"surface.plugin_action_invalid",
		"surface.revision_required",
		"surface.storage_invalid",
		"surface.structure_invalid":
	default:
		return nil, false
	}
	data := map[string]any{"kind": "surface_error", "message": e.Message, "code": e.Code}
	if e.Path != nil {
		data["path"] = *e.Path
	}
	return data, true
}
