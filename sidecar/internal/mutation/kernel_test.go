package mutation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type staticSchemaSource struct {
	definition schemaexecution.Table
}

func (source staticSchemaSource) Describe(
	_ context.Context,
	_ core.App,
	_ string,
) (schemaexecution.Table, error) {
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
	definition.Kind = "view"
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

func TestPreviewRejectsCrossContainerAliasesBeforeValueNormalization(t *testing.T) {
	definition := mutationTestDefinition()
	kernel := mutation.New(nil, staticSchemaSource{definition: definition})
	recordID := "rec_00000000001"
	request := mutationTestRequest(mutation.Operation{
		Kind: mutation.OperationUpdate, RecordID: &recordID,
		Values:    map[string]any{"fld_title": "typed"},
		RawValues: map[string]any{"title": 42},
	})

	_, err := kernel.Preview(context.Background(), request)

	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.field.duplicate" ||
		productErr.Path == nil ||
		*productErr.Path != "operations[0].rawValues.title" {
		t.Fatalf("duplicate alias error = %#v", err)
	}
}

func mutationTestDefinition() schemaexecution.Table {
	return schemaexecution.Table{
		Snapshot: v2.SchemaSnapshot{
			Contract: v2.Contract, TableID: "tbl_notes", DisplayName: "Notes",
			SchemaRevision: "schema_0001",
			Fields: []v2.FieldDefinition{
				{
					Contract:    v2.Contract,
					Identity:    v2.FieldIdentity{FieldID: "fld_title", PhysicalName: "title"},
					DisplayName: "Title", LogicalType: v2.LogicalText,
					Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
					Value:     v2.ValueSpec{Default: v2.DefaultSpec{Source: v2.DefaultRecommended}},
				},
				{
					Contract:    v2.Contract,
					Identity:    v2.FieldIdentity{FieldID: "fld_computed", PhysicalName: "computed"},
					DisplayName: "Computed", LogicalType: v2.LogicalFormula,
					Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
					Value:     v2.ValueSpec{Default: v2.DefaultSpec{Source: v2.DefaultRecommended}},
					Formula: &v2.FormulaSpec{
						Language: "cel-v1", Source: "title", ResultType: v2.LogicalText,
					},
				},
			},
		},
		PhysicalName: "notes", Kind: "base",
		ArchivePolicy: v2.ArchivePolicy{Mode: "none"},
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
