package schemaapi

import (
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

func TestValidateStoredDataRevisionRejectsMissingNegativeAndFractionalValues(t *testing.T) {
	for _, value := range []any{
		nil, -1.0, 1.5, "0", float64(1<<53) + 2, uint64(1 << 53),
	} {
		err := validateStoredDataRevision(value)
		var productErr *schema.ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "schema.metadata.invalid_data_revision" {
			t.Fatalf("validateStoredDataRevision(%#v) = %#v", value, err)
		}
	}
	for _, value := range []any{0.0, 1.0, int64(2)} {
		if err := validateStoredDataRevision(value); err != nil {
			t.Fatalf("validateStoredDataRevision(%#v): %v", value, err)
		}
	}
}

func TestValidateCompatibleAlterRejectsAutoDateRoleChanges(t *testing.T) {
	previous := schema.TableDefinition{
		Kind: schema.TableKindBase,
		Fields: []schema.FieldDefinition{{
			FieldID: "timestamp", Kind: schema.FieldKindSystem,
			DataType: schema.DataTypeAutoDate, StorageType: schema.StorageAutodate,
			AutoDate: &schema.AutoDateSpec{Role: schema.AutoDateRoleCreatedAt},
		}},
	}
	next := previous
	next.Fields = append([]schema.FieldDefinition(nil), previous.Fields...)
	next.Fields[0].AutoDate = &schema.AutoDateSpec{Role: schema.AutoDateRoleUpdatedAt}

	err := validateCompatibleAlter(previous, next)
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.field.autodate_role_immutable" ||
		productErr.Path != "fields[0].autoDate.role" {
		t.Fatalf("validateCompatibleAlter() = %#v", err)
	}
}
