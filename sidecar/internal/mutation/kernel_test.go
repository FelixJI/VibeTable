package mutation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

type staticSchemaSource struct {
	definition schema.TableDefinition
}

func (source staticSchemaSource) Describe(
	_ context.Context,
	_ core.App,
	_ string,
) (schema.TableDefinition, error) {
	return source.definition, nil
}

func TestPreviewNormalizesFieldIDsAndRejectsNonWritableFields(t *testing.T) {
	definition := mutationTestDefinition()
	kernel := mutation.New(nil, staticSchemaSource{definition: definition})
	recordID := "rec_00000000001"
	request := mutationTestRequest(mutation.Operation{
		Kind: mutation.OperationUpdate, RecordID: &recordID,
		Values: map[string]any{"fld_title": "Hello"},
	})
	preview, err := kernel.Preview(context.Background(), request)
	if err != nil {
		t.Fatalf("Preview(): %v", err)
	}
	if got := preview.Operations[0].Values["title"]; got != "Hello" {
		t.Fatalf("normalized title = %#v", got)
	}
	for _, key := range []string{"missing", "computed"} {
		request.Operations[0].Values = map[string]any{key: "bad"}
		_, err := kernel.Preview(context.Background(), request)
		var productErr *mutation.ProductError
		if !errors.As(err, &productErr) {
			t.Fatalf("Preview(%s) = %#v", key, err)
		}
		want := "mutation.field.unknown"
		if key == "computed" {
			want = "mutation.field.read_only"
		}
		if productErr.Code != want {
			t.Fatalf("Preview(%s) code = %q, want %q", key, productErr.Code, want)
		}
	}
	request.Operations = []mutation.Operation{{
		Kind: mutation.OperationSetAttachments, RecordID: &recordID,
		FieldID: "fld_title", UploadHandles: []string{"upload"}, RemoveStoredNames: []string{},
	}}
	_, err = kernel.Preview(context.Background(), request)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "mutation.attachment.unavailable" {
		t.Fatalf("setAttachments error = %#v", err)
	}
}

func TestPreviewRejectsReadOnlyViewTables(t *testing.T) {
	definition := mutationTestDefinition()
	definition.Kind = schema.TableKindView
	kernel := mutation.New(nil, staticSchemaSource{definition: definition})
	recordID := "rec_00000000001"
	_, err := kernel.Preview(
		context.Background(),
		mutationTestRequest(mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{"title": "blocked"},
		}),
	)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.table.read_only" {
		t.Fatalf("view mutation error = %#v", err)
	}
}

func mutationTestDefinition() schema.TableDefinition {
	return schema.TableDefinition{
		ContractVersion: schema.ContractVersion,
		TableID:         "tbl_notes", PhysicalName: "notes", DisplayName: "Notes",
		Kind: schema.TableKindBase, SchemaRevision: "schema_0001",
		ArchivePolicy: schema.ArchivePolicy{Mode: schema.ArchiveModeNone},
		Fields: []schema.FieldDefinition{
			{
				FieldID: "fld_title", PhysicalName: "title", DisplayName: "Title",
				Kind: schema.FieldKindScalar, DataType: schema.DataTypeShortText,
				StorageType: schema.StorageText, Nullable: true,
				Constraints: []schema.FieldConstraint{},
				Editor:      schema.EditorDefinition{Kind: "text", Config: map[string]any{}},
			},
			{
				FieldID: "fld_computed", PhysicalName: "computed", DisplayName: "Computed",
				Kind: schema.FieldKindFormula, DataType: schema.DataTypeFormula,
				StorageType: schema.StorageText, Nullable: true, ReadOnly: true,
				Constraints: []schema.FieldConstraint{},
				Editor:      schema.EditorDefinition{Kind: "formula", Config: map[string]any{}},
				Formula: &schema.FormulaSpec{
					Language: "cel-v1", Source: "title", ResultType: schema.DataTypeShortText,
					Version: 1, Status: "ready",
				},
			},
		},
		Indexes: []schema.IndexDefinition{},
	}
}

func mutationTestRequest(operations ...mutation.Operation) mutation.Request {
	return mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "req_1", IdempotencyKey: "idem_1", TableID: "tbl_notes",
		SchemaRevision: "schema_0001", Operations: operations,
		Actor: mutation.Actor{Type: "user", ID: "local"},
	}
}
