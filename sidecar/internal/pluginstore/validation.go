package pluginstore

import (
	"encoding/json"
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
	if !ok || manifest["pluginId"] != pluginID || manifest["version"] != version || manifest["$schema"] != "vibetable.plugin-manifest.v1" {
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
		if err := validateObject(action, "actionId displayName description mode risk invocation placements requires workerEntry formSchema inputSchema outputSchema", "actionId workerEntry"); err != nil {
			return err
		}
		if risk := action["risk"]; risk != "read" && risk != "write" && risk != "destructive" {
			return invalidPayload()
		}
		if invocation := action["invocation"]; invocation != "manual" && invocation != "webhook" {
			return invalidPayload()
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
