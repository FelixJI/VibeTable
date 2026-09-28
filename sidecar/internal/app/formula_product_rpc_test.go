package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

// The frozen Formula corpus froze the retired Python adapter boundary. Go must
// reject and accept the same inputs at the transport and DTO stages; scripted
// forwarding acceptance never implies the Go domain accepts those payloads.
func TestFormulaProductFrozenBoundaryMatchesPython(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/formula-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Cases []struct {
			Name    string `json:"name"`
			Request struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			} `json:"request"`
			Response struct {
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			} `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	dtoFailures := map[string]bool{
		"incomplete-field-handler-error":    true,
		"nested-unknown-handler-error":      true,
		"nested-language-handler-error":     true,
		"nested-empty-source-handler-error": true,
		"empty-changed-id-handler-error":    true,
		"numeric-changed-id-handler-error":  true,
	}
	checked := 0
	for _, item := range frozen.Cases {
		t.Run(item.Name, func(t *testing.T) {
			kind := formulaCaseKind(item.Name)
			paramsRejected := item.Response.Error != nil &&
				(item.Response.Error.Code == -32602 || item.Response.Error.Code == -32600)
			_, err := decodeFormulaProductParams(item.Request.Method, item.Request.Params)
			if (err != nil) != paramsRejected {
				t.Fatalf("transport stage: Python rejected=%v Go=%v", paramsRejected, err)
			}
			if paramsRejected {
				return
			}
			var object map[string]any
			decoder := json.NewDecoder(bytes.NewReader(item.Request.Params))
			decoder.UseNumber()
			if err := decoder.Decode(&object); err != nil {
				t.Fatal(err)
			}
			dtoRejected := item.Response.Error != nil && item.Response.Error.Code == -32603 &&
				dtoFailures[kind]
			err = validateFormulaNestedParams(item.Request.Method, object)
			if (err != nil) != dtoRejected {
				t.Fatalf("DTO stage: Python rejected=%v Go=%v", dtoRejected, err)
			}
		})
		checked++
	}
	if checked != 63 {
		t.Fatalf("frozen formula corpus changed: %d", checked)
	}
}

// Real PocketBase store and the real app-scoped compiler execute the three
// methods; no transport reply or domain result is scripted.
func TestFormulaProductRealProducerLifecycle(t *testing.T) {
	pb := schemaProductStore(t)
	domain := formulaDomain{app: pb, compiler: formula.NewAppCompiler(pb)}
	invoke := formulaTestInvoker(t, domain)
	table, amount, formulaField := createFormulaProductTable(t, pb)

	inspected, err := invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": "{" + amount.DisplayName + "} * 2",
	})
	if err != nil {
		t.Fatal(err)
	}
	draft := inspected.(fieldchange.FormulaDraftInspection)
	if draft.ResultType != v2.LogicalNumber || len(draft.Dependencies) != 1 ||
		draft.Dependencies[0] != amount.Identity.FieldID {
		t.Fatalf("draft inspection = %#v", draft)
	}

	// A table that does not exist keeps the former REST formula.runtime reply.
	_, err = invoke("formula.draft.validate", map[string]any{
		"tableId": "tbl_missing", "displaySource": "1",
	})
	assertFormulaPublicError(t, err, "formula.runtime")

	validated, err := invoke("formula.validate", map[string]any{
		"tableId": table.TableID, "field": formulaTestWireField(t, formulaField),
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := validated.(map[string]any)["formulas"].([]formulaMetadata)
	if len(metadata) != 1 || metadata[0].FieldID != formulaField.Identity.FieldID ||
		metadata[0].CanonicalSource == "" || metadata[0].ASTHash == "" {
		t.Fatalf("validate metadata = %#v", metadata)
	}

	// The retired Python DTO failures stay handler errors, not public errors.
	for _, field := range []map[string]any{
		{},
		func() map[string]any {
			broken := formulaTestWireField(t, formulaField)
			broken["unexpected"] = true
			return broken
		}(),
	} {
		_, err := invoke("formula.validate", map[string]any{
			"tableId": table.TableID, "field": field,
		})
		if err == nil {
			t.Fatalf("incomplete field accepted: %#v", field)
		}
		var public *productrpc.PublicError
		if errors.As(err, &public) {
			t.Fatalf("DTO failure became public: %v", err)
		}
	}

	// snake_case spellings the Python DTO tolerated must still reach the
	// domain decoder rejection instead of being normalized.
	aliased := formulaTestWireField(t, formulaField)
	aliased["display_name"] = aliased["displayName"]
	delete(aliased, "displayName")
	_, err = invoke("formula.validate", map[string]any{
		"tableId": table.TableID, "field": aliased,
	})
	assertFormulaPublicError(t, err, "formula.syntax")

	// An unknown dependency keeps the frozen formula dependency projection.
	broken := formulaTestWireField(t, formulaField)
	spec := broken["formula"].(map[string]any)
	spec["source"] = "f_missing_value * 2"
	_, err = invoke("formula.validate", map[string]any{
		"tableId": table.TableID, "field": broken,
	})
	var public *productrpc.PublicError
	if !errors.As(err, &public) || (public.Code != "formula.dependency" && public.Code != "formula.reference") {
		t.Fatalf("dependency error = %#v", err)
	}

	previewed, err := invoke("formula.preview", map[string]any{
		"tableId": table.TableID, "field": formulaTestWireField(t, formulaField),
		"row": map[string]any{
			amount.Identity.PhysicalName: json.Number("7"),
		},
		"changedFieldIds": []any{amount.Identity.FieldID},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := previewed.(map[string]any)["values"].(map[string]any)
	encoded, err := json.Marshal(values[formulaField.Identity.PhysicalName])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "14" {
		t.Fatalf("preview value = %s (%#v)", encoded, values)
	}

	// changedFieldIds items follow the retired non-empty string DTO contract.
	for _, changed := range []any{[]any{""}, []any{1}} {
		_, err = invoke("formula.preview", map[string]any{
			"tableId": table.TableID, "field": formulaTestWireField(t, formulaField),
			"row":             map[string]any{amount.Identity.PhysicalName: json.Number("1")},
			"changedFieldIds": changed,
		})
		var publicError *productrpc.PublicError
		if err == nil || errors.As(err, &publicError) {
			t.Fatalf("changedFieldIds %v accepted or public: %v", changed, err)
		}
	}

	// Read-only authority: validations and previews must not move revisions
	// or write business rows.
	revisions := formulaTestRevisions(t, pb, table.TableID)
	if _, err = invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": "1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke("formula.preview", map[string]any{
		"tableId": table.TableID, "field": formulaTestWireField(t, formulaField),
		"row":             map[string]any{amount.Identity.PhysicalName: json.Number("3")},
		"changedFieldIds": []any{},
	}); err != nil {
		t.Fatal(err)
	}
	if after := formulaTestRevisions(t, pb, table.TableID); after != revisions {
		t.Fatalf("formula validation wrote: %v -> %v", revisions, after)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	raw, err := json.Marshal(map[string]any{
		"tableId": table.TableID, "displaySource": "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	registrations := formulaProductRegistrations(domain)
	if err := formulaRegistrationFor(registrations, "formula.draft.validate").ValidateParams(raw); err != nil {
		t.Fatal(err)
	}
	if _, err = formulaRegistrationFor(registrations, "formula.draft.validate").Handler(
		cancelled, raw,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled draft validation = %v", err)
	}
}

func TestFormulaProductDraftSyntaxStaysPublic(t *testing.T) {
	pb := schemaProductStore(t)
	domain := formulaDomain{app: pb, compiler: formula.NewAppCompiler(pb)}
	invoke := formulaTestInvoker(t, domain)
	table, _, _ := createFormulaProductTable(t, pb)
	_, err := invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": "1 + (",
	})
	var public *productrpc.PublicError
	if !errors.As(err, &public) {
		t.Fatalf("syntax error = %#v", err)
	}
	if public.Code != "formula.syntax" {
		t.Fatalf("syntax code = %s (%s)", public.Code, public.Message)
	}
}

func TestFormulaProductPublicErrorProjection(t *testing.T) {
	for _, cancelled := range []error{context.Canceled, context.DeadlineExceeded} {
		if !errors.Is(publicFormulaError(cancelled), cancelled) {
			t.Fatal("cancellation became a domain failure")
		}
	}
	path := "field.formula.source"
	projected := publicFormulaError(formulaProductError(
		"formula.dependency", path, "unknown field", map[string]any{"fieldId": "missing"},
	))
	var public *productrpc.PublicError
	if !errors.As(projected, &public) || public.Code != "formula.dependency" ||
		public.Path == nil || *public.Path != path || public.Retryable ||
		public.Details["fieldId"] != "missing" {
		t.Fatalf("projection = %#v", public)
	}
	unknown := publicFormulaError(errors.New("boom"))
	if !errors.As(unknown, &public) || public.Code != "formula.runtime" ||
		public.Message != "formula operation failed" {
		t.Fatalf("unknown projection = %#v", public)
	}
}

func assertFormulaPublicError(t *testing.T, err error, code string) {
	t.Helper()
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != code {
		t.Fatalf("expected public %s: %#v", code, err)
	}
}

func formulaRegistrationFor(
	registrations []productrpc.Registration,
	method string,
) productrpc.Registration {
	for _, registration := range registrations {
		if registration.Method == method {
			return registration
		}
	}
	panic("missing formula registration: " + method)
}

func formulaTestInvoker(
	t *testing.T,
	domain formulaDomain,
) func(string, map[string]any) (any, error) {
	t.Helper()
	registrations := formulaProductRegistrations(domain)
	return func(method string, params map[string]any) (any, error) {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		registration := formulaRegistrationFor(registrations, method)
		if err := registration.ValidateParams(raw); err != nil {
			t.Fatalf("%s params: %v", method, err)
		}
		return registration.Handler(context.Background(), raw)
	}
}

func createFormulaProductTable(
	t *testing.T,
	pb *pocketbase.PocketBase,
) (v2.TableCreateReceipt, v2.FieldDefinition, v2.FieldDefinition) {
	t.Helper()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
		DisplayName: "Formula product", OperationID: "formula-product-table",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	amountReceipt := createSchemaProductField(
		t, pb, table.TableID, v2.LogicalNumber, "Amount", "formula-product-amount",
	)
	amount := *amountReceipt.Definition
	recommended, err := v2.RecommendedDefaults(v2.LogicalFormula)
	if err != nil {
		t.Fatal(err)
	}
	// Formula fields need the durable backfill scheduler the production app
	// wires; the table is empty so the enqueued backfill has no rows to write.
	backfill := jobs.New(pb, nil)
	t.Cleanup(backfill.Shutdown)
	catalog := fieldchange.NewCatalog(pb)
	revisions, err := catalog.Revisions(context.Background(), table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		ExpectedSchemaRev: revisions.Schema,
		Actor:             v2.Actor{ID: "local-user", Kind: "user"},
		Draft: &v2.FieldDraft{
			DisplayName: "Double", LogicalType: v2.LogicalFormula,
			Value: recommended.Value, Constraints: recommended.Constraints,
			Storage: recommended.Storage, Display: recommended.Display,
			Formula: &v2.FormulaDraftSpec{
				Language: "cel-v1", Source: amount.Identity.PhysicalName + " * 2",
			},
		},
	}
	store := fieldchange.NewPocketBasePlanStore(pb)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	plan, err := planner.Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	formulaReceipt, err := fieldchange.NewExecutor(
		pb, store, fieldchange.WithFormulaBackfillScheduler(backfill),
	).Apply(context.Background(), v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "formula-product-field", Actor: intent.Actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	return table, amount, *formulaReceipt.Definition
}

func formulaTestWireField(t *testing.T, definition v2.FieldDefinition) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var wire map[string]any
	if err := decoder.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	return wire
}

func formulaTestRevisions(t *testing.T, pb *pocketbase.PocketBase, tableID string) string {
	t.Helper()
	catalog := fieldchange.NewCatalog(pb)
	revisions, err := catalog.Revisions(context.Background(), tableID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(revisions)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func formulaCaseKind(name string) string {
	for index := len(name) - 1; index >= 0; index-- {
		if name[index] == ':' {
			return name[index+1:]
		}
	}
	return name
}

func TestFormulaProductSupplementalDTOBoundary(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/formula-dto-boundaries.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Path     []string `json:"path"`
		Value    any      `json:"value"`
		Accepted bool     `json:"accepted"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	frozen, err := os.ReadFile("../../../contracts/v2/formula-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []struct {
			Name    string `json:"name"`
			Request struct {
				Params json.RawMessage `json:"params"`
			} `json:"request"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(frozen, &corpus); err != nil {
		t.Fatal(err)
	}
	var baseline json.RawMessage
	for _, item := range corpus.Cases {
		if item.Name == "formula.validate:success" {
			baseline = item.Request.Params
		}
	}
	if baseline == nil {
		t.Fatal("missing baseline")
	}
	for index, item := range cases {
		t.Run(fmt.Sprintf("%d-%s", index, strings.Join(item.Path, ".")), func(t *testing.T) {
			object, err := decodeFormulaProductParams("formula.validate", baseline)
			if err != nil {
				t.Fatal(err)
			}
			parent := object["field"].(map[string]any)
			for _, key := range item.Path[:len(item.Path)-1] {
				parent = parent[key].(map[string]any)
			}
			parent[item.Path[len(item.Path)-1]] = item.Value
			err = validateFormulaNestedParams("formula.validate", object)
			if (err == nil) != item.Accepted {
				t.Fatalf("Python accepted=%v, Go error=%v", item.Accepted, err)
			}
		})
	}
}

func TestFormulaProductCompactsFormerRESTBodyBeforeDecode(t *testing.T) {
	pb := schemaProductStore(t)
	table, amount, formulaField := createFormulaProductTable(t, pb)
	textField := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Text", "formula-large-text")
	domain := formulaDomain{app: pb, compiler: formula.NewAppCompiler(pb)}
	registrations := formulaProductRegistrations(domain)
	for _, method := range []string{"formula.validate", "formula.preview"} {
		t.Run(method, func(t *testing.T) {
			params := map[string]any{"tableId": table.TableID, "field": formulaTestWireField(t, formulaField)}
			if method == "formula.preview" {
				params["row"] = map[string]any{
					amount.Identity.PhysicalName:               7,
					textField.Definition.Identity.PhysicalName: strings.Repeat("中", 200_000),
				}
				params["changedFieldIds"] = []any{}
			}
			compact, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			// Default Host JSON serialization escapes non-ASCII strings. The
			// retired Python HTTP transport forwarded compact UTF-8 instead.
			expanded := strings.ReplaceAll(string(compact), "中", `\u4e2d`)
			if method == "formula.validate" {
				expanded = strings.Repeat(" ", maxSchemaRequestBytes) + expanded
			}
			if len(compact) >= maxSchemaRequestBytes || len(expanded) <= maxSchemaRequestBytes {
				t.Fatalf("fixture does not cross raw-only size boundary: compact=%d expanded=%d", len(compact), len(expanded))
			}
			registration := formulaRegistrationFor(registrations, method)
			var baseline []byte
			for index, raw := range []json.RawMessage{compact, []byte(expanded)} {
				if err := registration.ValidateParams(raw); err != nil {
					t.Fatal(err)
				}
				result, err := registration.Handler(context.Background(), raw)
				if err != nil {
					t.Fatalf("wire %d failed: %v", index, err)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if index == 0 {
					baseline = encoded
				} else if !bytes.Equal(baseline, encoded) {
					t.Fatalf("same parameters produced different results: compact=%s expanded=%s", baseline, encoded)
				}
			}
		})
	}
}

func TestFormulaProductAuthorDocumentAndCatalog(t *testing.T) {
	pb := schemaProductStore(t)
	domain := formulaDomain{app: pb, compiler: formula.NewAppCompiler(pb)}
	invoke := formulaTestInvoker(t, domain)
	table, amount, _ := createFormulaProductTable(t, pb)
	before := formulaTestRevisions(t, pb, table.TableID)
	response, err := invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": amount.Identity.PhysicalName + " * 2", "restoreSource": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	restored := response.(fieldchange.FormulaDraftInspection)
	if restored.AuthorDocument == nil || len(restored.AuthorDocument.Tokens) != 1 || len(restored.Functions) == 0 {
		t.Fatalf("missing authoring contract: %#v", restored)
	}
	document := *restored.AuthorDocument
	if document.Tokens[0].FieldId != amount.Identity.FieldID {
		t.Fatal("lost stable field identity")
	}
	// Equal-width stale label proves bindings, rather than names, choose the field.
	document.DisplaySource = strings.Replace(document.DisplaySource, "Amount", "旧名甲乙丙丁", 1)
	document.DocumentRevision++
	response, err = invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": document.DisplaySource, "authorDocument": document,
	})
	if err != nil {
		t.Fatal(err)
	}
	authored := response.(fieldchange.FormulaDraftInspection)
	if authored.CanonicalSource != restored.CanonicalSource || authored.AuthorDocument.DocumentRevision != document.DocumentRevision {
		t.Fatalf("authoring changed stable source: %#v", authored)
	}
	_, err = invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": "1", "authorDocument": document,
	})
	assertFormulaPublicError(t, err, "formula.syntax")
	if after := formulaTestRevisions(t, pb, table.TableID); before != after {
		t.Fatal("authoring wrote data")
	}
}

func TestFormulaProductAuthoringRejectsUnknownNestedKeys(t *testing.T) {
	pb := schemaProductStore(t)
	invoke := formulaTestInvoker(t, formulaDomain{app: pb, compiler: formula.NewAppCompiler(pb)})
	table, _, _ := createFormulaProductTable(t, pb)
	_, err := invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": "1",
		"authorDocument": map[string]any{"displaySource": "1", "tokens": []any{}, "documentRevision": 1, "unexpected": true},
	})
	assertFormulaPublicError(t, err, "formula.syntax")
}

func TestFormulaProductAuthoringKeepsDiagnosticsAndMissingReferences(t *testing.T) {
	pb := schemaProductStore(t)
	invoke := formulaTestInvoker(t, formulaDomain{app: pb, compiler: formula.NewAppCompiler(pb)})
	table, _, _ := createFormulaProductTable(t, pb)
	_, err := invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": "f_missing + 1", "restoreSource": true,
	})
	assertFormulaPublicError(t, err, "formula.reference")
	var publicErr *productrpc.PublicError
	if !errors.As(err, &publicErr) {
		t.Fatal(err)
	}
	document, ok := publicErr.Details["authorDocument"].(workbench.FormulaAuthorDocument)
	if !ok || !strings.Contains(document.DisplaySource, "#REF!") {
		t.Fatalf("missing reference projection: %#v", publicErr.Details)
	}
	source := "'😀' == '😀' ? {Amount} : UNKNOWN(1)"
	_, err = invoke("formula.draft.validate", map[string]any{
		"tableId": table.TableID, "displaySource": source,
		"authorDocument": workbench.FormulaAuthorDocument{DisplaySource: source, DocumentRevision: 1},
	})
	if !errors.As(err, &publicErr) {
		t.Fatal(err)
	}
	span, ok := publicErr.Details["range"].(workbench.FormulaTextRange)
	// UTF-16 includes two surrogate pairs before UNKNOWN, while Amount is projected.
	if !ok || span.Start.Line != 0 || span.Start.Character != 26 || span.End.Character <= span.Start.Character {
		t.Fatalf("unknown call range: %#v", publicErr.Details)
	}
}

// Integer date-function drafts must carry their OnlyInt inference on the wire
// so the web preview builder declares the same storage shape as a saved field;
// a decimal-only preview field keeps failing the frozen compile type check.
func TestFormulaProductIntegerDraftInspectionFeedsPreviewStorage(t *testing.T) {
	pb := schemaProductStore(t)
	domain := formulaDomain{app: pb, compiler: formula.NewAppCompiler(pb)}
	invoke := formulaTestInvoker(t, domain)
	table, _, _ := createFormulaProductTable(t, pb)
	due := createSchemaProductField(
		t, pb, table.TableID, v2.LogicalDate, "Due", "formula-year-due",
	).Definition

	integer, err := invoke("formula.draft.validate", map[string]any{
		"tableId":       table.TableID,
		"displaySource": "YEAR(" + due.Identity.PhysicalName + ")",
	})
	if err != nil {
		t.Fatal(err)
	}
	integerInspection := integer.(fieldchange.FormulaDraftInspection)
	if integerInspection.ResultType != v2.LogicalNumber || !integerInspection.OnlyInt {
		t.Fatalf("integer draft inspection = %#v", integerInspection)
	}
	// The public wire must spell the flag exactly as the TS contract expects.
	encoded, err := json.Marshal(integerInspection)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"onlyInt":true`) {
		t.Fatalf("integer inspection wire = %s", encoded)
	}

	decimal, err := invoke("formula.draft.validate", map[string]any{
		"tableId":       table.TableID,
		"displaySource": "MAX(YEAR(" + due.Identity.PhysicalName + "), 2024.5)",
	})
	if err != nil {
		t.Fatal(err)
	}
	decimalInspection := decimal.(fieldchange.FormulaDraftInspection)
	if decimalInspection.ResultType != v2.LogicalNumber || decimalInspection.OnlyInt {
		t.Fatalf("decimal draft inspection = %#v", decimalInspection)
	}

	// Mirror the web buildFormulaPreviewField: recommended defaults with the
	// storage integer flag overridden by the fresh draft inference.
	previewField := func(inspection fieldchange.FormulaDraftInspection) map[string]any {
		recommended, err := v2.RecommendedDefaults(v2.LogicalFormula)
		if err != nil {
			t.Fatal(err)
		}
		field := v2.FieldDefinition{
			Contract: v2.Contract,
			Identity: v2.FieldIdentity{
				FieldID: "fld_formula_preview", PhysicalName: "f_formula_preview",
				ProviderFieldID: "pb_formula_preview",
			},
			DisplayName: "Formula preview", LogicalType: v2.LogicalFormula,
			Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
			Value:     recommended.Value, Constraints: recommended.Constraints,
			Storage: recommended.Storage, Display: recommended.Display,
			Formula: &v2.FormulaSpec{
				Language: "cel-v1", Source: inspection.CanonicalSource,
				ResultType: inspection.ResultType,
			},
		}
		field.Storage.Options.OnlyInt = inspection.OnlyInt
		return formulaTestWireField(t, field)
	}
	row := map[string]any{due.Identity.PhysicalName: "2024-05-06T00:00:00Z"}

	previewed, err := invoke("formula.preview", map[string]any{
		"tableId": table.TableID, "field": previewField(integerInspection),
		"row": row, "changedFieldIds": []any{due.Identity.FieldID},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := previewed.(map[string]any)["values"].(map[string]any)
	encoded, err = json.Marshal(values["f_formula_preview"])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "2024" {
		t.Fatalf("integer preview value = %s (%#v)", encoded, values)
	}

	// The shape the pre-fix web preview sent (recommended decimal storage)
	// stays a public type failure; onlyInt must not be silently dropped.
	decimalField := previewField(integerInspection)
	options := decimalField["storage"].(map[string]any)["options"].(map[string]any)
	options["onlyInt"] = false
	_, err = invoke("formula.preview", map[string]any{
		"tableId": table.TableID, "field": decimalField,
		"row": row, "changedFieldIds": []any{due.Identity.FieldID},
	})
	assertFormulaPublicError(t, err, "formula.type")

	// A genuinely decimal draft still previews through decimal storage.
	decimalPreview, err := invoke("formula.preview", map[string]any{
		"tableId": table.TableID, "field": previewField(decimalInspection),
		"row": row, "changedFieldIds": []any{due.Identity.FieldID},
	})
	if err != nil {
		t.Fatal(err)
	}
	decimalValues := decimalPreview.(map[string]any)["values"].(map[string]any)
	encoded, err = json.Marshal(decimalValues["f_formula_preview"])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "2024.5" {
		t.Fatalf("decimal preview value = %s (%#v)", encoded, decimalValues)
	}
}
