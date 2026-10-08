package relation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// DisplayFieldInfo is the read-only render contract for one relation label
// source. DataType uses the schema-describe column vocabulary so the web can
// reuse the merged #445 formatting contract (formatNumberDisplay with the
// canonical DisplaySpec) instead of a second numeric authority.
type DisplayFieldInfo struct {
	FieldID  string          `json:"fieldId"`
	DataType string          `json:"dataType"`
	Display  *v2.DisplaySpec `json:"display,omitempty"`
}

// targetDisplayProjection resolves the label sources of one relation field:
// the relation's own displayFieldId decides the primary label, while the
// target table's global primary display field is the auxiliary label and the
// explicit empty-label fallback. Record IDs remain the only identity.
type targetDisplayProjection struct {
	displayPhysical string
	primaryPhysical string
}

func resolveTargetDisplay(
	field v2.FieldDefinition,
	target schemaexecution.Table,
) targetDisplayProjection {
	projection := targetDisplayProjection{primaryPhysical: targetLabelField(target)}
	if field.Relation == nil || field.Relation.DisplayField == "" {
		return projection
	}
	for _, candidate := range target.Snapshot.Fields {
		if candidate.Identity.FieldID == field.Relation.DisplayField &&
			candidate.LogicalType != v2.LogicalRelation {
			projection.displayPhysical = candidate.Identity.PhysicalName
			break
		}
	}
	return projection
}

// displayFieldInfoFor projects the render contract of one candidate label
// field. Computed fields report their declared result type; number display
// metadata comes verbatim from the canonical DisplaySpec producer.
func (service *Service) displayFieldInfoFor(
	ctx context.Context,
	target schemaexecution.Table,
	field v2.FieldDefinition,
) *DisplayFieldInfo {
	dataType := lookupOutputStorage(outputTypeFor(field))
	if field.LogicalType == v2.LogicalLookup && field.Lookup != nil {
		if _, _, output, err := service.describeLookupPath(
			ctx, target, *field.Lookup,
		); err == nil {
			dataType = lookupOutputStorage(output)
		}
	}
	display := field.Display
	return &DisplayFieldInfo{
		FieldID:  field.Identity.FieldID,
		DataType: dataType,
		Display:  &display,
	}
}

// displayFieldInfoByID resolves a display field info by field ID, returning
// nil when the field is missing (retired or deleted) or is itself a relation.
func (service *Service) displayFieldInfoByID(
	ctx context.Context,
	target schemaexecution.Table,
	fieldID string,
) *DisplayFieldInfo {
	if fieldID == "" {
		return nil
	}
	for _, field := range target.Snapshot.Fields {
		if field.Identity.FieldID == fieldID {
			if field.LogicalType == v2.LogicalRelation {
				return nil
			}
			return service.displayFieldInfoFor(ctx, target, field)
		}
	}
	return nil
}

// fallbackDisplayFieldInfoFor exposes the target table's global primary
// display field render contract used as auxiliary label and fallback.
func (service *Service) fallbackDisplayFieldInfoFor(
	ctx context.Context,
	target schemaexecution.Table,
) *DisplayFieldInfo {
	if primaryPhysical := targetLabelField(target); primaryPhysical != "" {
		for _, field := range target.Snapshot.Fields {
			if field.Identity.PhysicalName == primaryPhysical {
				return service.displayFieldInfoFor(ctx, target, field)
			}
		}
	}
	return nil
}

// targetLabelValue renders one raw row value as a relation label scalar.
// Empty and whitespace-only text is missing, but 0 and false are valid
// values. Fresh computed envelopes contribute their ready scalar by the
// declared result type; non-fresh envelopes and JSON structures never
// surface stale results as labels.
func targetLabelValue(value any) (any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, false
		}
		return trimmed, true
	case bool, int64, float64, json.Number:
		return typed, true
	case int:
		return int64(typed), true
	case map[string]any:
		// A computation envelope that survived projection: only a fresh
		// ready/ok state may contribute its scalar value.
		state, _ := typed["state"].(string)
		if state != "ready" && state != "ok" {
			return nil, false
		}
		return targetLabelValue(typed["value"])
	default:
		return nil, false
	}
}

// labelText is the plain string form used for wire labels; typed display
// values travel separately so clients format through the shared contract.
func labelString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool, int64, float64, json.Number:
		return fmt.Sprint(typed)
	case int:
		return fmt.Sprint(int64(typed))
	default:
		return ""
	}
}

// projectTargetRef resolves the TargetRef labels of one target row through
// the frozen chain: valid relation display value -> valid target global
// primary display value -> record ID.
func (projection targetDisplayProjection) projectTargetRef(
	tableID string,
	recordID string,
	row map[string]any,
) TargetRef {
	ref := TargetRef{TableID: tableID, RecordID: recordID, Label: recordID}
	primaryValue, primaryValid := targetLabelValue(row[projection.primaryPhysical])
	separateDisplay := projection.displayPhysical != "" &&
		projection.displayPhysical != projection.primaryPhysical
	// A valid configured display field always carries its raw typed scalar —
	// including when it IS the target's global primary display field — so
	// every surface formats it through the same display contract.
	if projection.displayPhysical != "" {
		if displayValue, valid := targetLabelValue(row[projection.displayPhysical]); valid {
			ref.Label = labelString(displayValue)
			ref.DisplayValue = displayValue
			if separateDisplay && primaryValid && labelString(primaryValue) != ref.Label {
				ref.SecondaryLabel = labelString(primaryValue)
				ref.SecondaryValue = primaryValue
			}
			return ref
		}
	}
	if primaryValid {
		ref.Label = labelString(primaryValue)
		// Expose the raw scalar only when the label actually fell back from a
		// separate configured display field; clients then format it with the
		// primary field's own contract (SecondaryValue), not a duplicate.
		if projection.displayPhysical != projection.primaryPhysical {
			ref.SecondaryValue = primaryValue
		}
	}
	return ref
}
