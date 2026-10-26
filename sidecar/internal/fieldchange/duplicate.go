package fieldchange

import (
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// InstallCopiedField uses the same compiler and authoritative metadata writer
// as ordinary field changes. The caller owns the enclosing table transaction.
func InstallCopiedField(app core.App, collection *core.Collection, tableID string, definition v2.FieldDefinition) error {
	if err := addCompiledField(app, collection, definition); err != nil {
		return err
	}
	return saveDefinitionMetadata(app, tableID, definition)
}
