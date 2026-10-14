package pluginstore

import (
	"encoding/json"
	"path"
	"strings"
)

func invalidPayload() *Error {
	return requestError("plugin.request_invalid", "plugin payload is invalid", nil)
}

func validateObject(object map[string]any, allowed, texts string) error {
	fields := " " + allowed + " "
	for key := range object {
		if !strings.Contains(fields, " "+key+" ") {
			return invalidPayload()
		}
	}
	for _, key := range strings.Fields(texts) {
		if textValue(object[key]) == "" {
			return invalidPayload()
		}
	}
	return nil
}

func validateManifest(value any, pluginID, version string) error {
	manifest, ok := value.(map[string]any)
	if !ok || manifest["pluginId"] != pluginID || manifest["version"] != version {
		return invalidPayload()
	}
	compatibility, valid := manifest["compatibility"].(map[string]any)
	if !valid {
		return invalidPayload()
	}
	native := manifest["$schema"] == "vibetable.plugin-manifest.v2" && compatibility["pluginApi"] == "3.x"
	if !native && (manifest["$schema"] != "vibetable.plugin-manifest.v1" || compatibility["pluginApi"] == "3.x") {
		return invalidPayload()
	}
	if err := validateObject(manifest, "$schema pluginId version displayName description compatibility permissions actions ui", "pluginId version"); err != nil {
		return err
	}
	for _, key := range []string{"displayName", "description", "compatibility", "permissions", "ui"} {
		if _, ok := manifest[key].(map[string]any); !ok {
			return invalidPayload()
		}
	}
	actions, ok := manifest["actions"].([]any)
	if !ok {
		return invalidPayload()
	}
	for _, item := range actions {
		action, ok := item.(map[string]any)
		if !ok || action["mode"] != "local" {
			return invalidPayload()
		}
		required := "actionId workerEntry"
		if native {
			required = "actionId"
			if action["workerEntry"] != nil {
				return invalidPayload()
			}
		}
		if err := validateObject(action, "actionId displayName description mode risk invocation placements requires workerEntry formSchema inputSchema outputSchema presentation entry", required); err != nil {
			return err
		}
		if risk := action["risk"]; risk != "read" && risk != "write" && risk != "destructive" {
			return invalidPayload()
		}
		if invocation := action["invocation"]; invocation != "manual" && invocation != "webhook" {
			return invalidPayload()
		}
		presentation, hasPresentation := action["presentation"].(string)
		if !hasPresentation {
			presentation = "surface"
		} else if presentation != "surface" && presentation != "application" && presentation != "headless" {
			return invalidPayload()
		}
		entry, _ := action["entry"].(string)
		if presentation == "surface" {
			if action["entry"] != nil {
				return invalidPayload()
			}
		} else if !native || entry == "" {
			return invalidPayload()
		} else if suffix := strings.ToLower(path.Ext(entry)); (presentation == "application" && suffix != ".html") || (presentation == "headless" && suffix != ".js" && suffix != ".mjs") {
			return invalidPayload()
		} else if action["formSchema"] != nil || action["inputSchema"] != nil || action["outputSchema"] != nil {
			return invalidPayload()
		}
		if native {
			ui := manifest["ui"].(map[string]any)
			views, _ := ui["customViews"].([]any)
			found := false
			for _, value := range views {
				view, ok := value.(map[string]any)
				if ok && view["actionId"] == action["actionId"] && textValue(view["viewId"]) != "" && textValue(view["entry"]) != "" {
					found = true
				}
			}
			if found && presentation != "surface" {
				return invalidPayload()
			}
			if !found && presentation == "surface" {
				return invalidPayload()
			}
		}
	}
	return nil
}

func validateSnapshotFields(object map[string]any) error {
	if err := validateObject(object, "projectKey pluginId version packageHash sourceType sourceLocation developmentSourceLocation sourceChanged manifest schemas status disabledReason blockingReasons revision", "projectKey pluginId version packageHash sourceType sourceLocation status"); err != nil {
		return err
	}
	if source := object["sourceType"]; source != "package" && source != "local-folder" {
		return invalidPayload()
	}
	if status := object["status"]; status != "disabled" && status != "enabled" && status != "error" {
		return invalidPayload()
	}
	if _, ok := object["sourceChanged"].(bool); !ok {
		return invalidPayload()
	}
	if _, ok := object["schemas"].(map[string]any); !ok {
		return invalidPayload()
	}
	for _, key := range []string{"developmentSourceLocation", "disabledReason"} {
		if object[key] != nil {
			if _, ok := object[key].(string); !ok {
				return invalidPayload()
			}
		}
	}
	reasons, ok := object["blockingReasons"].([]any)
	if !ok {
		return invalidPayload()
	}
	for _, reason := range reasons {
		if _, ok := reason.(string); !ok {
			return invalidPayload()
		}
	}
	return validateManifest(object["manifest"], textValue(object["pluginId"]), textValue(object["version"]))
}

// ValidateCatalogEvent also prevents legacy/local paths from escaping the Go
// outbox. Host supplies the only local resource projection after epoch checks.
func ValidateCatalogEvent(event CatalogChangedEvent) bool {
	if event.ContractVersion != "2.0" || event.Topic != "plugin.catalog.changed" || event.EventType != event.Topic || event.Contract != "vibetable.plugin-event.v1" || event.EventID == "" || event.Revision < 1 {
		return false
	}
	identity, err := validateSnapshot(event.Snapshot)
	if err != nil || identity.projectKey != event.ProjectKey || identity.pluginID != event.EntityID || jsonInt(event.Snapshot, "revision") != event.Revision {
		return false
	}
	var snapshot map[string]any
	return json.Unmarshal(event.Snapshot, &snapshot) == nil && snapshot["sourceLocation"] == "host-managed" && snapshot["developmentSourceLocation"] == nil
}
