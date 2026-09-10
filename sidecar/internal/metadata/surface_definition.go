package metadata

import (
	"fmt"
	wb "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var surfaceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func requireSurfaceID(id string) error {
	if !surfaceID.MatchString(id) {
		return surfaceError("surface.interface_id_invalid", "Interface ID must use 1-128 safe identifier characters.", "interfaceId")
	}
	return nil
}

// Python str.strip also treats the four ASCII information separators as whitespace.
func surfaceBlank(text string) bool {
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }) == ""
}
func surfaceUnique(values []string, path, code string) (map[string]bool, error) {
	result := map[string]bool{}
	for i, value := range values {
		if value == "" {
			return nil, surfaceError("surface.id_required", "ID is required.", fmt.Sprintf("%s.%d", path, i))
		}
		if result[value] {
			return nil, surfaceError(code, "IDs must be unique.", fmt.Sprintf("%s.%d", path, i))
		}
		result[value] = true
	}
	return result, nil
}
func present(value *string) bool { return value != nil && *value != "" }
func validateSurfaceDefinition(d wb.InterfaceDefinition) error {
	if err := requireSurfaceID(d.InterfaceId); err != nil {
		return err
	}
	if surfaceBlank(d.Name) {
		return surfaceError("surface.name_required", "Interface name is required.", "name")
	}
	if len(d.Pages) == 0 {
		return surfaceError("surface.pages_required", "An Interface requires at least one page.", "pages")
	}
	ids := make([]string, len(d.Bindings))
	bindings := map[string]wb.DataBinding{}
	for i, b := range d.Bindings {
		ids[i] = b.BindingId
		bindings[b.BindingId] = b
	}
	bindingIDs, err := surfaceUnique(ids, "bindings", "surface.binding_duplicate")
	if err != nil {
		return err
	}
	for i, b := range d.Bindings {
		path := fmt.Sprintf("bindings.%d", i)
		if b.Query.TableId == "" {
			return surfaceError("surface.binding_source_required", "Binding source is required.", path+".query.tableId")
		}
		if len(b.Query.Fields) == 0 {
			return surfaceError("surface.binding_fields_required", "Binding requires at least one field.", path+".query.fields")
		}
		fields := map[string]bool{}
		for _, f := range b.Query.Fields {
			if fields[f] {
				return surfaceError("surface.binding_field_duplicate", "Binding fields must be unique.", path+".query.fields")
			}
			fields[f] = true
		}
		vars := make([]string, len(b.Variables))
		for i, v := range b.Variables {
			vars[i] = v.VariableId
		}
		if _, err := surfaceUnique(vars, path+".variables", "surface.binding_variable_duplicate"); err != nil {
			return err
		}
	}
	dependencies := map[string]map[string]bool{}
	for i, b := range d.Bindings {
		dependencies[b.BindingId] = map[string]bool{}
		for j, v := range b.Variables {
			path := fmt.Sprintf("bindings.%d.variables.%d", i, j)
			if !slices.Contains(b.Query.Fields, v.TargetFieldId) {
				return surfaceError("surface.binding_variable_target_invalid", "Variable target must be a field selected by its binding query.", path+".targetFieldId")
			}
			if v.Source == "literal" {
				if v.SourceBindingId != nil || v.SourceFieldId != nil {
					return surfaceError("surface.binding_variable_source_invalid", "Literal variables cannot reference another binding.", path)
				}
				continue
			}
			if !present(v.SourceBindingId) || !present(v.SourceFieldId) {
				return surfaceError("surface.binding_variable_source_required", "Selected-record variables require a source binding and field.", path)
			}
			if *v.SourceBindingId == b.BindingId {
				return surfaceError("surface.binding_variable_cycle", "A binding cannot depend on its own selected record.", path+".sourceBindingId")
			}
			source, ok := bindings[*v.SourceBindingId]
			if !ok {
				return surfaceError("surface.binding_variable_source_missing", "Variable source binding does not exist.", path+".sourceBindingId")
			}
			if !slices.Contains(source.Query.Fields, *v.SourceFieldId) {
				return surfaceError("surface.binding_variable_source_field_invalid", "Variable source field is not selected by its source binding.", path+".sourceFieldId")
			}
			dependencies[b.BindingId][*v.SourceBindingId] = true
		}
	}
	for len(dependencies) > 0 {
		ready := []string{}
		for id, sources := range dependencies {
			if len(sources) == 0 {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			return surfaceError("surface.binding_variable_cycle", "Binding variables must form an acyclic dependency graph.", "bindings")
		}
		for _, id := range ready {
			delete(dependencies, id)
		}
		for _, sources := range dependencies {
			for _, id := range ready {
				delete(sources, id)
			}
		}
	}
	ids = make([]string, len(d.Pages))
	for i, p := range d.Pages {
		ids[i] = p.PageId
	}
	pageIDs, err := surfaceUnique(ids, "pages", "surface.page_duplicate")
	if err != nil {
		return err
	}
	ids = make([]string, len(d.Actions))
	actions := map[string]wb.InterfaceAction{}
	for i, a := range d.Actions {
		ids[i] = a.ActionId
		actions[a.ActionId] = a
	}
	actionIDs, err := surfaceUnique(ids, "actions", "surface.action_duplicate")
	if err != nil {
		return err
	}
	for i, a := range d.Actions {
		path := fmt.Sprintf("actions.%d", i)
		switch a.Kind {
		case "record.create", "record.update", "binding.refresh":
			if a.BindingId == nil || !bindingIDs[*a.BindingId] {
				return surfaceError("surface.binding_missing", "Action binding does not exist.", path+".bindingId")
			}
			if present(a.TargetPageId) || present(a.PluginId) || present(a.PluginActionId) {
				return surfaceError("surface.action_invalid", "Record and refresh actions contain unrelated targets.", path)
			}
		case "navigate":
			if a.TargetPageId == nil || !pageIDs[*a.TargetPageId] {
				return surfaceError("surface.page_missing", "Navigation target page does not exist.", path+".targetPageId")
			}
			if present(a.BindingId) || present(a.PluginId) || present(a.PluginActionId) {
				return surfaceError("surface.action_invalid", "Navigate action contains unrelated targets.", path)
			}
		case "plugin":
			if a.PluginId == nil || a.PluginActionId == nil {
				return surfaceError("surface.plugin_action_invalid", "Plugin action identity is incomplete.", path)
			}
			if a.BindingId != nil || a.TargetPageId != nil {
				return surfaceError("surface.action_invalid", "Plugin action contains unrelated targets.", path)
			}
		}
	}
	seen := map[string]bool{}
	count := 0
	var visit func(wb.InterfaceElement, string, int) error
	visit = func(e wb.InterfaceElement, path string, depth int) error {
		count++
		if count > 200 {
			return surfaceError("surface.element_limit", "An Interface can contain at most 200 elements.", path)
		}
		if depth > 8 {
			return surfaceError("surface.element_depth", "Element nesting cannot exceed 8 levels.", path)
		}
		if e.ElementId == "" {
			return surfaceError("surface.element_id_required", "Element ID is required.", path+".elementId")
		}
		if seen[e.ElementId] {
			return surfaceError("surface.element_duplicate", "Element IDs must be unique.", path+".elementId")
		}
		seen[e.ElementId] = true
		if e.BindingId != nil && !bindingIDs[*e.BindingId] {
			return surfaceError("surface.binding_missing", "Element binding does not exist.", path+".bindingId")
		}
		if e.ActionId != nil && !actionIDs[*e.ActionId] {
			return surfaceError("surface.action_missing", "Element action does not exist.", path+".actionId")
		}
		if slices.Contains([]string{"metric", "chart", "record-list", "record-detail", "form"}, e.Kind) && e.BindingId == nil {
			return surfaceError("surface.binding_missing", "Element requires a binding.", path+".bindingId")
		}
		if slices.Contains([]string{"form", "button", "navigation"}, e.Kind) && e.ActionId == nil {
			return surfaceError("surface.action_missing", "Element requires an action.", path+".actionId")
		}
		if slices.Contains([]string{"section", "columns", "tabs"}, e.Kind) {
			if e.BindingId != nil || e.ActionId != nil {
				return surfaceError("surface.structure_invalid", "Structural elements cannot bind data or actions.", path)
			}
		} else if len(e.Children) > 0 {
			return surfaceError("surface.children_invalid", "Only structural elements can contain children.", path+".children")
		}
		if e.Kind == "navigation" && e.ActionId != nil && actions[*e.ActionId].Kind != "navigate" {
			return surfaceError("surface.navigation_action_invalid", "Navigation elements require a navigate action.", path+".actionId")
		}
		if e.Kind == "form" && e.ActionId != nil && !slices.Contains([]string{"record.create", "record.update"}, actions[*e.ActionId].Kind) {
			return surfaceError("surface.form_action_invalid", "Form elements require a record create or update action.", path+".actionId")
		}
		for i, c := range e.Children {
			if err := visit(c, fmt.Sprintf("%s.children.%d", path, i), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for i, p := range d.Pages {
		if surfaceBlank(p.Title) {
			return surfaceError("surface.page_title_required", "Page title is required.", fmt.Sprintf("pages.%d.title", i))
		}
		for j, e := range p.Elements {
			if err := visit(e, fmt.Sprintf("pages.%d.elements.%d", i, j), 1); err != nil {
				return err
			}
		}
	}
	return nil
}
