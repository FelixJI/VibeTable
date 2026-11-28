package fieldprotection

import (
	"context"
	"fmt"
	"sort"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

// SchemaEraseCode is the error code of the schema-layer guard. A structural
// change that would erase protected values is refused with it until the
// workspace manager releases the protection.
const SchemaEraseCode = "schema.field_protection_active"

// InTable lists the protected fields of one table in a stable order.
func (r *Registry) InTable(tableID string) []Entry {
	result := []Entry{}
	if r == nil {
		return result
	}
	for _, entry := range r.byField {
		if entry.TableID == tableID {
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].FieldID < result[j].FieldID })
	return result
}

// RefuseTableErase refuses deleting a table that holds protected fields until
// the workspace manager releases the protection. A corrupt registry fails closed.
//
// The plugin model binding is an independent second guard (pluginmodel.
// ProtectTable) checked by the caller afterwards: every protectable field is a
// model-bound field, so a released table that is still bound stays undeletable.
// This guard runs first only so the refusal names the protection and the
// plugins that own it.
func RefuseTableErase(ctx context.Context, app core.App, tableID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registry, err := Load(ctx, app)
	if err != nil {
		return err
	}
	entries := registry.InTable(tableID)
	if len(entries) == 0 {
		return nil
	}
	owners := make([]string, 0, len(entries))
	for _, entry := range entries {
		owners = append(owners, entry.PluginID)
	}
	return &schemaerror.ProductError{
		Code: SchemaEraseCode, Path: "tableId",
		Message: "table holds fields under persistent protection; release the protection in the workspace manager first",
		Details: map[string]any{
			"tableId": tableID, "fieldIds": fieldIDs(entries), "pluginIds": owners,
		},
	}
}

// RefuseSchemaErase is the field-level guard of persistent field protection.
// Deleting a protected field or changing it beyond its display would erase its
// values without a plugin transaction, so it is refused until the manager
// releases the protection. Display renames and new or user extension fields
// (fieldID == "") are never affected; a corrupt registry fails closed.
//
// The plugin model binding is an independent second guard (pluginmodel.
// ProtectField) checked by the caller afterwards: every protectable field is a
// model-bound field, so a released field that is still bound stays undeletable.
// This guard runs first only so the refusal names the protection and the plugin
// that owns it.
func RefuseSchemaErase(
	ctx context.Context,
	app core.App,
	tableID, fieldID string,
	before, after *v2.FieldDefinition,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fieldID == "" || (before == nil && after != nil) || (before == nil && after == nil) {
		return nil
	}
	registry, err := Load(ctx, app)
	if err != nil {
		return err
	}
	entry, protected := registry.Protected(tableID, fieldID)
	if !protected {
		return nil
	}
	if after != nil && pluginmodel.DisplayOnlyChange(before, after) {
		return nil
	}
	return &schemaerror.ProductError{
		Code: SchemaEraseCode, Path: "fieldId",
		Message: fmt.Sprintf("field is under persistent protection by plugin %q; release the protection in the workspace manager first", entry.PluginID),
		Details: map[string]any{
			"tableId": tableID, "fieldId": fieldID, "field": entry.Field, "entity": entry.Entity,
			"pluginId": entry.PluginID, "installationId": entry.InstallationID,
		},
	}
}

func fieldIDs(entries []Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.FieldID)
	}
	return ids
}
