package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// LegacyPreservationService is the #485 read-only preservation reader for the
// retired visual layout builder (Goal #480). It reads the three original
// collections (vibetable_interfaces, vibetable_dashboards, vibetable_panels)
// as raw payloads with their stable logical ids and payload-derived
// revisions, and diagnoses every item individually. Nothing is validated
// against the retired closed-set DTOs, nothing is executed, and unknown or
// corrupt content is preserved verbatim with a diagnostic instead of failing
// the whole snapshot. The service performs no writes.
type LegacyPreservationService struct{ metadata *Service }

func NewLegacyPreservation(app core.App) *LegacyPreservationService {
	return &LegacyPreservationService{metadata: New(app)}
}

type LegacyLayoutItem struct {
	Namespace   Namespace                `json:"namespace"`
	RecordID    string                   `json:"recordId"`
	LogicalId   string                   `json:"logicalId"`
	ParentID    string                   `json:"parentId"`
	RawPayload  string                   `json:"rawPayload"`
	Revision    string                   `json:"revision"`
	Diagnostics []LegacyLayoutDiagnostic `json:"diagnostics,omitempty"`
}

type LegacyLayoutDiagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
}

type LegacyLayoutSnapshot struct {
	ContractVersion string             `json:"contractVersion"`
	Interfaces      []LegacyLayoutItem `json:"interfaces"`
	Dashboards      []LegacyLayoutItem `json:"dashboards"`
	Panels          []LegacyLayoutItem `json:"panels"`
	Diagnostics     []string           `json:"diagnostics"`
	CollectedAt     string             `json:"collectedAt"`
}

// recordManagerElementKinds are the #485 support-matrix Interface element
// kinds a record-manager code plugin can represent without manual rewriting.
var recordManagerElementKinds = map[string]bool{
	"text": true, "record-list": true, "record-detail": true,
	"form": true, "button": true, "navigation": true,
}

const LegacyChunkBytes = 256 * 1024
const LegacyDiagnosticPreviewLimit = 100

type LegacyLayoutPageRequest struct {
	Collection    Namespace `json:"collection"`
	AfterRecordID string    `json:"afterRecordId"`
	RecordOffset  int       `json:"recordOffset"`
}
type LegacyLayoutPage struct {
	PreviewTruncated  bool                     `json:"previewTruncated"`
	ContractVersion   string                   `json:"contractVersion"`
	Collection        Namespace                `json:"collection"`
	RecordID          string                   `json:"recordId"`
	LogicalID         string                   `json:"logicalId"`
	Revision          string                   `json:"revision"`
	Chunk             []byte                   `json:"chunk"`
	RecordDone        bool                     `json:"recordDone"`
	CollectionDone    bool                     `json:"collectionDone"`
	NextAfterRecordID string                   `json:"nextAfterRecordId"`
	NextRecordOffset  int                      `json:"nextRecordOffset"`
	DiagnosticCount   int                      `json:"diagnosticCount"`
	Diagnostics       []LegacyLayoutDiagnostic `json:"diagnostics"`
	CollectedAt       string                   `json:"collectedAt"`
}

// Historical JSONField admission bounds a legitimate record to 1 MiB.
// Each request reads only the next immutable retired record; base64 byte
// fragments preserve JSON escapes and UTF-8 even across fragment boundaries.
func (service *LegacyPreservationService) Page(ctx context.Context, request LegacyLayoutPageRequest) (LegacyLayoutPage, error) {
	page := LegacyLayoutPage{ContractVersion: "2.0", Collection: request.Collection,
		NextAfterRecordID: request.AfterRecordID, Chunk: []byte{}, Diagnostics: []LegacyLayoutDiagnostic{},
		CollectedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	collection, err := resolveCollection(service.metadata.app, request.Collection)
	if err != nil {
		return page, err
	}
	var record struct {
		ID        string `db:"id"`
		LogicalID string `db:"logical_id"`
		ParentID  string `db:"parent_id"`
		Payload   string `db:"payload_json"`
	}
	parentColumn := "'' AS parent_id"
	if request.Collection == NamespacePanels {
		parentColumn = "parent_id"
	}
	err = service.metadata.app.DB().NewQuery("SELECT id, logical_id, " + parentColumn + ", payload_json FROM {{" + collection.Name + "}} WHERE id > {:after} ORDER BY id LIMIT 1").Bind(dbx.Params{"after": request.AfterRecordID}).WithContext(ctx).One(&record)
	if errors.Is(err, sql.ErrNoRows) {
		if request.RecordOffset != 0 {
			return page, fmt.Errorf("legacy preservation cursor beyond collection")
		}
		page.CollectionDone = true
		return page, nil
	}
	if err != nil {
		return page, err
	}
	item := LegacyLayoutItem{Namespace: request.Collection, RecordID: record.ID, LogicalId: record.LogicalID, ParentID: record.ParentID, RawPayload: record.Payload}
	if _, revision, err := canonicalPayload([]byte(record.Payload)); err == nil {
		item.Revision = revision
	}
	item.Diagnostics = diagnoseLegacyItem(request.Collection, record.LogicalID, record.ParentID, []byte(record.Payload))
	encoded, err := json.Marshal(item)
	if err != nil {
		return page, err
	}
	if request.RecordOffset >= len(encoded) {
		return page, fmt.Errorf("legacy preservation cursor beyond record")
	}
	end := min(request.RecordOffset+LegacyChunkBytes, len(encoded))
	page.RecordID = record.ID
	page.LogicalID = record.LogicalID
	page.Revision = item.Revision
	page.Chunk = encoded[request.RecordOffset:end]
	page.RecordDone = end == len(encoded)
	if page.RecordDone {
		page.NextAfterRecordID = record.ID
	} else {
		page.NextRecordOffset = end
	}
	if request.RecordOffset == 0 {
		page.DiagnosticCount = len(item.Diagnostics)
		page.PreviewTruncated = len(item.Diagnostics) > LegacyDiagnosticPreviewLimit
		for _, diagnostic := range item.Diagnostics[:min(len(item.Diagnostics), LegacyDiagnosticPreviewLimit)] {
			// Only UI preview is shortened; export retains all original diagnostics.
			page.PreviewTruncated = page.PreviewTruncated || len([]rune(diagnostic.Path)) > 512 || len([]rune(diagnostic.Message)) > 512
			diagnostic.Path = truncateLegacyPreview(diagnostic.Path)
			diagnostic.Message = truncateLegacyPreview(diagnostic.Message)
			page.Diagnostics = append(page.Diagnostics, diagnostic)
		}
	}
	return page, nil
}
func truncateLegacyPreview(value string) string {
	runes := []rune(value)
	if len(runes) > 512 {
		return string(runes[:512]) + "…"
	}
	return value
}

// diagnoseLegacyItem inspects one preserved item leniently — never through
// the retired closed-set DTO validator, which rejects exactly the complex
// definitions this channel must preserve. Unknown element kinds, versions,
// unparsable payloads and identity mismatches are reported per item; the raw
// payload always stays intact.
func diagnoseLegacyItem(namespace Namespace, logicalID, parentID string, payload json.RawMessage) []LegacyLayoutDiagnostic {
	diagnostics := []LegacyLayoutDiagnostic{}
	add := func(code, severity, message string) {
		diagnostics = append(diagnostics, LegacyLayoutDiagnostic{Code: code, Severity: severity, Message: message})
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return []LegacyLayoutDiagnostic{{
			Code:     "legacy.payload_unparseable",
			Severity: "warning",
			Message:  "Payload is not valid JSON; preserved verbatim.",
		}}
	}
	if _, isObject := value.(map[string]any); !isObject {
		return []LegacyLayoutDiagnostic{{
			Code:     "legacy.payload_not_object",
			Severity: "warning",
			Message:  "Payload is not a JSON object; preserved verbatim.",
		}}
	}
	inspectLegacyFields(value, "$", namespace, &diagnostics)
	switch namespace {
	case NamespaceInterfaces:
		var definition struct {
			ContractVersion string `json:"contractVersion"`
			InterfaceId     string `json:"interfaceId"`
			Pages           []struct {
				Elements []struct {
					Kind string `json:"kind"`
				} `json:"elements"`
			} `json:"pages"`
			Actions []struct {
				Kind string `json:"kind"`
			} `json:"actions"`
		}
		if err := json.Unmarshal(payload, &definition); err != nil {
			add("legacy.definition_unreadable", "warning", "Interface payload could not be read; preserved verbatim.")
			return diagnostics
		}
		if definition.InterfaceId == "" || definition.InterfaceId != logicalID {
			add("legacy.identity_mismatch", "error", "Stored Interface identity does not match its logical id; preserved verbatim.")
		}
		if definition.ContractVersion != "1.0" {
			add("legacy.version_unsupported", "warning",
				fmt.Sprintf("Interface contract version %q is not the retired 1.0 format; preserved verbatim.", definition.ContractVersion))
		}
		for _, action := range definition.Actions {
			if action.Kind == "plugin" {
				add("legacy.plugin_action_unsupported", "error",
					"Legacy plugin action was never executable and stays preserved read-only.")
			}
		}
	case NamespaceDashboards, NamespacePanels:
		// The retired dashboard writer stored payload.id == logical_id and,
		// for panels, payload.dashboardId == parent_id; a diverging record is
		// corrupt, not merely manual-rewrite material. Both identities stay
		// untouched in storage and in the verbatim payload.
		object := value.(map[string]any)
		item := "Dashboard"
		if namespace == NamespacePanels {
			item = "Panel"
		}
		if id, _ := object["id"].(string); id == "" || id != logicalID {
			add("legacy.identity_mismatch", "error",
				fmt.Sprintf("Stored %s identity does not match its logical id; preserved verbatim.", item))
		}
		if namespace == NamespacePanels {
			if dashboardID, _ := object["dashboardId"].(string); dashboardID == "" || dashboardID != parentID {
				add("legacy.identity_mismatch", "error",
					"Stored Panel parent identity does not match its parent dashboard; preserved verbatim.")
			}
		}
		// Dashboard-era payloads need a hand-written code plugin (#485
		// matrix); keep the item with the shared manual-rewrite guidance.
		add("legacy.dashboard_manual_rewrite", "warning",
			"Dashboard-era layout needs a hand-written code plugin; public query/aggregate and chart modules remain available.")
	}
	return diagnostics
}

// Diagnose the historical shape without projecting it through a closed DTO.
// Options/query data stays opaque; every original byte is in rawPayload.
func inspectLegacyFields(value any, path string, namespace Namespace, diagnostics *[]LegacyLayoutDiagnostic) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	allowed := ""
	switch {
	case path == "$" && namespace == NamespaceInterfaces:
		allowed = "contractVersion interfaceId name bindings actions pages"
	case path == "$" && namespace == NamespaceDashboards:
		allowed = "id name note icon color config configVersion"
	case path == "$" && namespace == NamespacePanels:
		allowed = "id dashboardId name note icon color showHeader type position options query"
	case namespace == NamespaceInterfaces && strings.HasSuffix(path, ".query"):
		// Only Interface binding queries are the retired ViewQuery shape;
		// dashboard/panel queries keep their own historical contract and
		// stay opaque below.
		allowed = "contractVersion tableId fields filters sorts cursor pageSize"
	case strings.Contains(path, ".elements[") || strings.Contains(path, ".children["):
		allowed = "elementId kind bindingId actionId text width children"
		kind, _ := object["kind"].(string)
		if !recordManagerElementKinds[kind] {
			*diagnostics = append(*diagnostics, LegacyLayoutDiagnostic{Code: "legacy.element_manual_rewrite", Severity: "warning", Path: path + ".kind", Message: fmt.Sprintf("Element %q needs manual code-plugin rewriting; no automatic conversion is performed.", kind)})
		}
	case strings.Contains(path, ".pages["):
		allowed = "pageId title elements"
	case strings.Contains(path, ".actions["):
		allowed = "actionId kind bindingId targetPageId pluginId pluginActionId requiresConfirmation"
		kind, _ := object["kind"].(string)
		if kind != "record.create" && kind != "record.update" && kind != "binding.refresh" && kind != "navigate" && kind != "plugin" {
			*diagnostics = append(*diagnostics, LegacyLayoutDiagnostic{Code: "legacy.action_unsupported", Severity: "warning", Path: path + ".kind", Message: fmt.Sprintf("Unknown action %q; rewrite manually.", kind)})
		}
	case strings.Contains(path, ".variables["):
		allowed = "variableId targetFieldId operator source sourceBindingId sourceFieldId value"
	case strings.Contains(path, ".bindings["):
		allowed = "bindingId query variables"
	default:
		return
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		childPath := path + "." + key
		if !strings.Contains(" "+allowed+" ", " "+key+" ") {
			*diagnostics = append(*diagnostics, LegacyLayoutDiagnostic{Code: "legacy.field_unknown", Severity: "warning", Path: childPath, Message: "Unknown field is preserved; inspect and rewrite manually."})
		}
		switch child := object[key].(type) {
		case map[string]any:
			inspectLegacyFields(child, childPath, namespace, diagnostics)
		case []any:
			for index, item := range child {
				inspectLegacyFields(item, fmt.Sprintf("%s[%d]", childPath, index), namespace, diagnostics)
			}
		}
	}
}
