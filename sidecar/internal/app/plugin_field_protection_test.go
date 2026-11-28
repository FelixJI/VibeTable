package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/fieldprotection"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type protectionFixture struct {
	*transactionFixture
	headers       schemaexecution.Table
	lines         schemaexecution.Table
	registrations map[string]productrpc.Registration
	sequence      int
}

func newProtectionFixture(t *testing.T, fields ...fieldprotection.DeclaredField) *protectionFixture {
	t.Helper()
	return protectionFixtureFor(t, newTransactionFixture(t), fields...)
}

func protectionFixtureFor(t *testing.T, f *transactionFixture, fields ...fieldprotection.DeclaredField) *protectionFixture {
	t.Helper()
	ctx := context.Background()
	headers, err := schemaapi.New(f.pb).Describe(ctx, f.model.Bindings["headers"].TableID)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := schemaapi.New(f.pb).Describe(ctx, f.model.Bindings["lines"].TableID)
	if err != nil {
		t.Fatal(err)
	}
	p := &protectionFixture{transactionFixture: f, headers: headers, lines: lines, registrations: map[string]productrpc.Registration{}}
	for _, registration := range fieldProtectionRegistrations(f.pb, f.workspace) {
		p.registrations[registration.Method] = registration
	}
	if len(fields) > 0 {
		p.declare(t, fields...)
	}
	return p
}

func TestFieldProtectionWriteAndDeleteDistinguishNullFromPresentValues(t *testing.T) {
	p := protectionFixtureFor(t, newTransactionFixture(t, "protection-values"),
		fieldprotection.DeclaredField{Entity: "headers", Field: "protected_text"},
		fieldprotection.DeclaredField{Entity: "headers", Field: "protected_number"},
		fieldprotection.DeclaredField{Entity: "headers", Field: "protected_flag"},
	)
	code := p.headerField("headers", "code")
	text := p.headerField("headers", "protected_text")
	number := p.headerField("headers", "protected_number")
	flag := p.headerField("headers", "protected_flag")
	amount := p.headerField("headers", "amount")
	tableID := p.headers.Snapshot.TableID
	extension := createSchemaProductField(t, p.pb, tableID, v2.LogicalText, "用户扩展", "protection-present-extension")
	table, err := schemaapi.New(p.pb).Describe(context.Background(), tableID)
	if err != nil {
		t.Fatal(err)
	}
	p.headers = table
	note, ok := table.Field(extension.FieldID)
	if !ok {
		t.Fatalf("missing extension field %s", extension.FieldID)
	}
	noteName := note.Identity.PhysicalName
	for index, scenario := range []struct {
		key   string
		field v2FieldIdentity
		value any
	}{
		{"protected_text", text, ""},
		{"protected_number", number, 0},
		{"protected_flag", flag, false},
	} {
		t.Run(scenario.key, func(t *testing.T) {
			recordID := fmt.Sprintf("00000000000%d001", index)
			if err := p.normal(p.headers, p.insert(recordID, map[string]any{
				code.name: "SYN-" + recordID, amount.name: "0.00", scenario.field.name: scenario.value,
			})); err != nil {
				t.Fatal(err)
			}
			ref := fieldprotection.FieldRef{TableID: p.headers.Snapshot.TableID, FieldID: scenario.field.id}
			p.enable(t, fieldprotection.DeclaredField{Entity: "headers", Field: scenario.key})
			if err := p.normal(p.headers, p.update(recordID, map[string]any{scenario.field.name: scenario.value, noteName: "new"})); err != nil {
				t.Fatalf("same-value form with extension edit: %v", err)
			}
			if err := p.normal(p.headers, p.update(recordID, map[string]any{scenario.field.name: nil})); kernelError(err) == nil || kernelError(err).Code != "mutation.field_protected" {
				t.Fatalf("clearing a present value: %v", err)
			}
			if err := p.normal(p.headers, p.remove(recordID)); kernelError(err) == nil || kernelError(err).Code != "mutation.field_protected" {
				t.Fatalf("deleting a present value: %v", err)
			}
			p.release(t, ref)
			if err := p.normal(p.headers, p.remove(recordID)); err != nil {
				t.Fatalf("removing the released record: %v", err)
			}
			p.enable(t, fieldprotection.DeclaredField{Entity: "headers", Field: scenario.key})
			emptyID := fmt.Sprintf("00000000000%d002", index)
			if err := p.normal(p.headers, p.insert(emptyID, map[string]any{
				code.name: "SYN-" + emptyID, amount.name: "0.00", noteName: "without protected value",
			})); err != nil {
				t.Fatalf("insert without writing the protected field: %v", err)
			}
			if err := p.normal(p.headers, p.remove(emptyID)); err != nil {
				t.Fatalf("remove a record with null protected values: %v", err)
			}
			p.release(t, ref)
		})
	}
}

// declare installs `permissions.fieldProtection` as an in-place upgrade of the
// installed manifest (one revision bump), mirroring a plugin upgrade.
func (p *protectionFixture) declare(t *testing.T, fields ...fieldprotection.DeclaredField) {
	t.Helper()
	declaration := fieldprotection.Declaration{Contract: fieldprotection.Contract, Fields: fields}
	if err := declaration.Validate(p.installedModel(t)); err != nil {
		t.Fatal(err)
	}
	store := pluginstore.New(p.pb, p.workspace)
	raw, err := store.GetInstallation(context.Background(), p.input.ProjectKey, p.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	var manifest map[string]json.RawMessage
	_ = json.Unmarshal(object["manifest"], &manifest)
	var permissions map[string]json.RawMessage
	_ = json.Unmarshal(manifest["permissions"], &permissions)
	permissions["fieldProtection"], _ = json.Marshal(declaration)
	manifest["permissions"], _ = json.Marshal(permissions)
	object["manifest"], _ = json.Marshal(manifest)
	previous := p.input.InstallationRevision
	p.input.InstallationRevision++
	object["revision"], _ = json.Marshal(p.input.InstallationRevision)
	raw, _ = json.Marshal(object)
	if _, err := store.SaveInstallation(context.Background(), raw, &previous); err != nil {
		t.Fatal(err)
	}
	// The completed model state is bound to the installation revision: the
	// upgrade re-completes it, exactly as a plugin upgrade does.
	preview, err := modelCall(t, p.pb, p.workspace, p.input, p.domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modelCall(t, p.pb, p.workspace, p.input, p.domain, "model.commit", map[string]any{"requestId": preview.(modelPreview).RequestID}); err != nil {
		t.Fatal(err)
	}
}

// syncRevision aligns the surface input with the installed revision after a
// lifecycle transition (enable/disable) bumped it.
func (p *protectionFixture) syncRevision(t *testing.T) {
	t.Helper()
	raw, err := pluginstore.New(p.pb, p.workspace).GetInstallation(context.Background(), p.input.ProjectKey, p.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var installed struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(raw, &installed); err != nil {
		t.Fatal(err)
	}
	p.input.InstallationRevision = installed.Revision
}

func (p *protectionFixture) installedModel(t *testing.T) *pluginmodel.Declaration {
	t.Helper()
	store := pluginstore.New(p.pb, p.workspace)
	raw, err := store.GetInstallation(context.Background(), p.input.ProjectKey, p.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var installed pluginInstallation
	if err := json.Unmarshal(raw, &installed); err != nil {
		t.Fatal(err)
	}
	return installed.Manifest.Permissions.Model
}

// rpc calls a field protection registration exactly as the
// product dispatcher does: closed params validation, then the handler.
func (p *protectionFixture) rpc(t *testing.T, method string, params any) (any, error) {
	t.Helper()
	registration, ok := p.registrations[method]
	if !ok {
		t.Fatalf("method %s is not registered", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := registration.ValidateParams(raw); err != nil {
		return nil, err
	}
	return registration.Handler(context.Background(), raw)
}

func (p *protectionFixture) describe(t *testing.T) *pluginFieldProtectionDescribeResult {
	t.Helper()
	raw, err := p.rpc(t, "plugin.fieldProtection.describe", map[string]any{"projectKey": p.input.ProjectKey, "pluginId": p.input.PluginID})
	if err != nil {
		t.Fatal(err)
	}
	return raw.(*pluginFieldProtectionDescribeResult)
}

func (p *protectionFixture) enableParams(fields ...fieldprotection.DeclaredField) map[string]any {
	selected := make([]pluginFieldProtectionSelection, 0, len(fields))
	for _, field := range fields {
		binding := p.model.Bindings[field.Entity]
		tableID, fieldID := binding.TableID, binding.Fields[field.Field]
		if tableID == "" || fieldID == "" {
			tableID, fieldID = "table-not-bound", "field-not-bound"
		}
		selected = append(selected, pluginFieldProtectionSelection{
			DeclaredField: field, FieldRef: fieldprotection.FieldRef{TableID: tableID, FieldID: fieldID},
		})
	}
	return map[string]any{
		"projectKey":           p.input.ProjectKey,
		"pluginId":             p.input.PluginID,
		"installationId":       p.input.InstallationID,
		"installationRevision": p.input.InstallationRevision,
		"fields":               selected,
	}
}

func (p *protectionFixture) enable(t *testing.T, fields ...fieldprotection.DeclaredField) *pluginFieldProtectionDescribeResult {
	t.Helper()
	raw, err := p.rpc(t, "plugin.fieldProtection.enable", p.enableParams(fields...))
	if err != nil {
		t.Fatal(err)
	}
	return raw.(*pluginFieldProtectionDescribeResult)
}

// release with no refs releases every field of the plugin; nil-typed refs
// (vars ...) distinguish "explicit empty list" from "release all".
func (p *protectionFixture) release(t *testing.T, refs ...fieldprotection.FieldRef) *pluginFieldProtectionDescribeResult {
	t.Helper()
	params := map[string]any{"projectKey": p.input.ProjectKey, "pluginId": p.input.PluginID}
	if refs != nil {
		params["fields"] = refs
	}
	raw, err := p.rpc(t, "plugin.fieldProtection.release", params)
	if err != nil {
		t.Fatal(err)
	}
	return raw.(*pluginFieldProtectionDescribeResult)
}

type v2FieldIdentity struct{ id, name string }

func (p *protectionFixture) headerField(entity, key string) v2FieldIdentity {
	binding := p.model.Bindings[entity]
	table := p.headers
	if entity == "lines" {
		table = p.lines
	} else if entity != "headers" {
		described, err := schemaapi.New(p.pb).Describe(context.Background(), binding.TableID)
		if err != nil {
			panic(err)
		}
		table = described
	}
	field, found := table.Field(binding.Fields[key])
	if !found {
		panic("field " + entity + "." + key + " is not bound")
	}
	return v2FieldIdentity{id: field.Identity.FieldID, name: field.Identity.PhysicalName}
}

// normal is a host-side write by a user: no plugin owns the request.
func (p *protectionFixture) normal(table schemaexecution.Table, ops ...mutation.Operation) error {
	p.sequence++
	return p.writeAs(table, nil, ops...)
}

// writeAs runs a kernel write with an explicit protected field writer.
func (p *protectionFixture) writeAs(table schemaexecution.Table, writer *fieldprotection.Owner, ops ...mutation.Operation) error {
	p.sequence++
	_, err := p.kernel.Apply(context.Background(), mutation.Request{
		ContractVersion:      mutation.ContractVersion,
		RequestID:            fmt.Sprintf("protection-normal-%d", p.sequence),
		IdempotencyKey:       fmt.Sprintf("protection-normal-%d", p.sequence),
		TableID:              table.Snapshot.TableID,
		SchemaRevision:       table.Snapshot.SchemaRevision,
		Operations:           ops,
		Actor:                mutation.Actor{Type: "user", ID: "local-user"},
		ProtectedFieldWriter: writer,
	})
	return err
}

func (p *protectionFixture) update(id string, values map[string]any) mutation.Operation {
	return mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &id, Values: values}
}

func (p *protectionFixture) insert(id string, values map[string]any) mutation.Operation {
	return mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id, Values: values}
}

func (p *protectionFixture) remove(id string) mutation.Operation {
	return mutation.Operation{Kind: mutation.OperationDelete, RecordID: &id}
}

// normalPlan is a host-side multi-table write by a user in one transaction.
func (p *protectionFixture) normalPlan(t *testing.T, requests ...mutation.Request) error {
	t.Helper()
	p.sequence++
	for i := range requests {
		requests[i].ContractVersion = mutation.ContractVersion
		requests[i].RequestID = fmt.Sprintf("protection-plan-%d-%d", p.sequence, i)
		requests[i].IdempotencyKey = requests[i].RequestID
		requests[i].Actor = mutation.Actor{Type: "user", ID: "local-user"}
	}
	_, err := p.kernel.ApplyPlan(context.Background(), requests,
		func(context.Context, core.App, string, string) error { return nil },
		func(core.App, mutation.PlanReceipt) error { return nil },
	)
	return err
}

func (p *protectionFixture) ownerPlan(code string) transactionPlan {
	return transactionPlan{
		Contract: "vibetable.transaction-plan.v1",
		ModelID:  "synthetic",
		Operations: []transactionOperation{
			{Entity: "headers", Kind: "create", TempID: "h", Values: map[string]any{"code": code, "amount": "10.00"}},
			{Entity: "lines", Kind: "create", TempID: "l", Values: map[string]any{"amount": "10.00", "header": map[string]any{"entity": "headers", "tempId": "h"}}},
		},
	}
}

// ownerTransaction runs the owning plugin's own transaction surface.
func (p *protectionFixture) ownerTransaction(t *testing.T, plan transactionPlan) transactionResult {
	t.Helper()
	raw, err := p.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = p.call("transaction.commit", map[string]any{"requestId": raw.(transactionPreview).RequestID})
	if err != nil {
		t.Fatal(err)
	}
	return raw.(transactionResult)
}

func (p *protectionFixture) record(t *testing.T, table schemaexecution.Table, id string) *core.Record {
	t.Helper()
	record, err := p.pb.FindRecordById(table.PhysicalName, id)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func publicError(err error) *productrpc.PublicError {
	var public *productrpc.PublicError
	if errors.As(err, &public) {
		return public
	}
	return nil
}

// kernelError exposes the mutation kernel's own error envelope.
func kernelError(err error) *mutation.ProductError {
	var public *mutation.ProductError
	if errors.As(err, &public) {
		return public
	}
	return nil
}

func TestFieldProtectionDescribeDefaultOffAndEnableThenRelease(t *testing.T) {
	code, header := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}, fieldprotection.DeclaredField{Entity: "lines", Field: "header"}
	p := newProtectionFixture(t, code, header)
	before := p.describe(t)
	if before.Declared != true || len(before.Protected) != 0 || before.Events != 0 {
		t.Fatalf("protection must default to OFF: %#v", before)
	}
	if len(before.Suggested) != 2 || before.Suggested[0].State != "unprotected" || !before.Suggested[0].Eligible {
		t.Fatalf("suggested = %#v", before.Suggested)
	}
	if before.Suggested[0].TableName == "" || before.Suggested[0].FieldName == "" || before.Suggested[0].FieldType == "" || before.Suggested[0].TableID != p.headers.Snapshot.TableID || before.Suggested[0].FieldID != p.headerField("headers", "code").id {
		t.Fatalf("suggested must show the real bound table/field: %#v", before.Suggested[0])
	}
	if before.MaxProtectedFields != fieldprotection.MaxProtectedFields || before.MaxEvents != fieldprotection.MaxEvents || before.EventsRemaining != fieldprotection.MaxEvents {
		t.Fatalf("limits = %#v", before)
	}
	if len(before.BlockingReasons) != 0 {
		t.Fatalf("an enabled 3.x plugin with a declaration must be enableable: %v", before.BlockingReasons)
	}
	after := p.enable(t, code)
	if len(after.Protected) != 1 || after.Protected[0].Entity != "headers" || after.Protected[0].InstallationID != p.input.InstallationID || after.Events != 1 {
		t.Fatalf("enable = %#v", after)
	}
	if after.Suggested[0].State != "protected" || after.Suggested[0].OwnerStale || after.Suggested[1].State != "unprotected" {
		t.Fatalf("states after enable = %#v", after.Suggested)
	}
	// Audit trail survives in the plugin audit log with the real names.
	events, err := pluginstore.New(p.pb, p.workspace).ListAudit(context.Background(), p.input.ProjectKey, p.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	enabled := 0
	for _, event := range events {
		if strings.Contains(string(event), fieldprotection.EventEnabled) && strings.Contains(string(event), `"fieldName":"单号"`) {
			enabled++
		}
	}
	if enabled != 1 {
		t.Fatalf("audit events with the real field name = %d of %d", enabled, len(events))
	}
	released := p.release(t, fieldprotection.FieldRef{TableID: p.headers.Snapshot.TableID, FieldID: p.headerField("headers", "code").id})
	if len(released.Protected) != 0 || released.Events != 2 || released.EventsRemaining != fieldprotection.MaxEvents-1 {
		t.Fatalf("release = %#v", released)
	}
	if p.describe(t).Suggested[0].State != "unprotected" {
		t.Fatal("released field must be unprotected again")
	}
}

func TestFieldProtectionSurfaceReportsPersistedAndCurrentMappingsSeparately(t *testing.T) {
	code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	amount := fieldprotection.DeclaredField{Entity: "headers", Field: "amount"}
	p := newProtectionFixture(t, code)
	p.enable(t, code)
	// An updated declaration lists a different field. The original persisted
	// protection remains active and available to the workspace manager.
	p.declare(t, amount)
	input := p.input
	input.Method, input.Request = "fieldProtection.status", json.RawMessage("{}")
	status, err := executePluginSurfaceAPI(context.Background(), p.pb, nil, p.kernel, p.workspace, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Active  int `json:"activeProtectionCount"`
		Current int `json:"currentProtectedCount"`
		Fields  []struct {
			Field string `json:"field"`
			State string `json:"state"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Active != 1 || receipt.Current != 0 || len(receipt.Fields) != 1 ||
		receipt.Fields[0].Field != "amount" || receipt.Fields[0].State != "unprotected" {
		t.Fatalf("status = %s", raw)
	}
	if manager := p.describe(t); len(manager.Protected) != 1 || manager.Protected[0].Field != "code" {
		t.Fatalf("manager lost previous mapping: %#v", manager.Protected)
	}
}

func TestFieldProtectionRefusesHistoryRestorePreparedBeforeEnable(t *testing.T) {
	declared := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, declared)
	ctx := t.Context()
	tableID := p.headers.Snapshot.TableID
	recordID := "00000000000hist"
	code := p.headerField("headers", "code").name
	amount := p.headerField("headers", "amount").name
	if err := p.normal(p.headers, p.insert(recordID, map[string]any{code: "ORIGINAL", amount: "0.00"})); err != nil {
		t.Fatal(err)
	}
	history, err := audit.New(p.pb, p.kernel)
	if err != nil {
		t.Fatal(err)
	}
	page, err := history.ReadChangeSets(ctx, audit.ReadParams{TableID: tableID, ItemID: &recordID, Scope: "row", Limit: 10})
	if err != nil || len(page.ChangeSets) != 1 {
		t.Fatalf("history = %#v, %v", page, err)
	}
	if err := p.normal(p.headers, p.update(recordID, map[string]any{code: "CURRENT"})); err != nil {
		t.Fatal(err)
	}
	preview, err := history.PreviewRestore(ctx, audit.PreviewParams{
		TableID: tableID, ItemID: recordID, TargetRevision: page.ChangeSets[0].RootRevisionID, Scope: "row",
	})
	if err != nil || !preview.CanApply {
		t.Fatalf("restore preview = %#v, %v", preview, err)
	}
	p.enable(t, declared)
	_, err = history.ApplyRestore(ctx, audit.ApplyParams{TableID: tableID, ItemID: recordID, Token: preview.Token})
	var refusal *audit.Error
	if !errors.As(err, &refusal) || refusal.Details["mutationCode"] != "mutation.field_protected" {
		t.Fatalf("history apply = %v", err)
	}
	if got := p.record(t, p.headers, recordID).GetString(code); got != "CURRENT" {
		t.Fatalf("history changed the protected value to %q", got)
	}
}

func TestFieldProtectionKernelEnforcement(t *testing.T) {
	code, header := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}, fieldprotection.DeclaredField{Entity: "lines", Field: "header"}
	p := newProtectionFixture(t, code, header)
	p.enable(t, code, header)
	first := p.ownerTransaction(t, p.ownerPlan("SYN-1"))
	second := p.ownerTransaction(t, p.ownerPlan("SYN-2"))
	h1, h2, l1 := first.NewIDs["headers"]["h"], second.NewIDs["headers"]["h"], first.NewIDs["lines"]["l"]
	codeField, amountField, headerField, linesField := p.headerField("headers", "code"), p.headerField("headers", "amount"), p.headerField("lines", "header"), p.headerField("headers", "lines")
	protected := func(err error, table schemaexecution.Table, field v2FieldIdentity) {
		t.Helper()
		public := kernelError(err)
		if public == nil || public.Code != "mutation.field_protected" || public.Path == nil || *public.Path != field.name {
			t.Fatalf("err=%v want mutation.field_protected on %s", err, field.name)
		}
		if public.Details["tableId"] != table.Snapshot.TableID || public.Details["fieldId"] != field.id || public.Details["pluginId"] != p.input.PluginID || public.Details["installationId"] != p.input.InstallationID {
			t.Fatalf("details = %v", public.Details)
		}
	}
	// 1. A normal cell edit of the protected field is rejected and unchanged.
	err := p.normal(p.headers, p.update(h1, map[string]any{codeField.name: "CHANGED"}))
	protected(err, p.headers, codeField)
	if got := p.record(t, p.headers, h1).GetString(codeField.name); got != "SYN-1" {
		t.Fatalf("protected value changed to %q", got)
	}
	// Clearing a protected value is a change too.
	protected(p.normal(p.headers, p.update(h1, map[string]any{codeField.name: ""})), p.headers, codeField)
	// 2. Ordinary extension fields of the same row stay writable (the header
	// amount together with its line, as the conservation rule requires).
	lineAmount := p.headerField("lines", "amount").name
	if err := p.normalPlan(t,
		mutation.Request{TableID: p.headers.Snapshot.TableID, SchemaRevision: p.headers.Snapshot.SchemaRevision, Operations: []mutation.Operation{p.update(h1, map[string]any{amountField.name: "12.00"})}},
		mutation.Request{TableID: p.lines.Snapshot.TableID, SchemaRevision: p.lines.Snapshot.SchemaRevision, Operations: []mutation.Operation{p.update(l1, map[string]any{lineAmount: "12.00"})}},
	); err != nil {
		t.Fatalf("normal field write refused: %v", err)
	}
	if got := p.record(t, p.headers, h1).GetString(amountField.name); got != "12.00" {
		t.Fatalf("extension field not written: %q", got)
	}
	// An exact no-op on the protected field is allowed.
	if err := p.normal(p.headers, p.update(h1, map[string]any{codeField.name: "SYN-1"})); err != nil {
		t.Fatalf("no-op write refused: %v", err)
	}
	// 3. A mixed batch is rejected entirely: nothing of the batch persists.
	err = p.normal(p.headers, p.update(h2, map[string]any{amountField.name: "99.00"}), p.update(h2, map[string]any{codeField.name: "CHANGED"}))
	protected(err, p.headers, codeField)
	if got := p.record(t, p.headers, h2).GetString(amountField.name); got == "99.00" {
		t.Fatal("the mixed batch partially persisted")
	}
	// 4. Inserts: explicit protected values are refused; a row of an unprotected
	//    table (allocations) inserts and deletes freely.
	protected(p.normal(p.headers, mutation.Operation{Kind: mutation.OperationInsert, Values: map[string]any{codeField.name: "XY"}}), p.headers, codeField)
	protected(p.normal(p.headers, mutation.Operation{Kind: mutation.OperationInsert, Values: map[string]any{codeField.id: "XY"}}), p.headers, codeField)
	textInsert := p.insert("protectraw00001", map[string]any{amountField.name: "0.00"})
	textInsert.RawValues = map[string]any{codeField.id: "SYN-IMPORTED"}
	protected(p.normal(p.headers, textInsert), p.headers, codeField)
	protected(p.normalPlan(t, mutation.Request{
		TableID: p.headers.Snapshot.TableID, SchemaRevision: p.headers.Snapshot.SchemaRevision,
		Operations: []mutation.Operation{textInsert},
	}), p.headers, codeField)
	if _, err := p.pb.FindRecordById(p.headers.PhysicalName, *textInsert.RecordID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected text input created a row: %v", err)
	}
	allocations, err := schemaapi.New(p.pb).Describe(context.Background(), p.model.Bindings["allocations"].TableID)
	if err != nil {
		t.Fatal(err)
	}
	allocationAmountField, allocationLineField := p.headerField("allocations", "amount"), p.headerField("allocations", "line")
	allocationAmount, allocationLine := allocationAmountField.name, allocationLineField.name
	p.sequence++
	receipt, err := p.kernel.Apply(context.Background(), mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       fmt.Sprintf("protection-blank-%d", p.sequence),
		IdempotencyKey:  fmt.Sprintf("protection-blank-%d", p.sequence),
		TableID:         allocations.Snapshot.TableID,
		SchemaRevision:  allocations.Snapshot.SchemaRevision,
		Operations:      []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{allocationAmount: "1.00", allocationLine: l1}}},
		Actor:           mutation.Actor{Type: "user", ID: "local-user"},
	})
	if err != nil || len(receipt.AffectedRows) != 1 {
		t.Fatalf("insert without the protected field refused: %v %+v", err, receipt)
	}
	// 5. Row deletion/erase must not bypass protection: a header no line references
	//    (so no relation rule applies) is still refused while protected. The owner
	//    creates it with its own identity.
	p.sequence++
	loneReceipt, err := p.kernel.Apply(context.Background(), mutation.Request{
		ContractVersion:      mutation.ContractVersion,
		RequestID:            fmt.Sprintf("protection-lone-%d", p.sequence),
		IdempotencyKey:       fmt.Sprintf("protection-lone-%d", p.sequence),
		TableID:              p.headers.Snapshot.TableID,
		SchemaRevision:       p.headers.Snapshot.SchemaRevision,
		Operations:           []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{codeField.name: "SYN-LONE", amountField.name: "0.00"}}},
		Actor:                mutation.Actor{Type: "plugin", ID: p.input.PluginID},
		ProtectedFieldWriter: &fieldprotection.Owner{PluginID: p.input.PluginID, InstallationID: p.input.InstallationID},
	})
	if err != nil || len(loneReceipt.AffectedRows) != 1 {
		t.Fatalf("owner insert refused: %v %+v", err, loneReceipt)
	}
	lone := loneReceipt.AffectedRows[0].RecordID
	protected(p.normal(p.headers, p.remove(lone)), p.headers, codeField)
	protected(p.normal(p.headers, p.remove(h1)), p.headers, codeField)
	deleteLone := func() error {
		return p.normalPlan(t, mutation.Request{TableID: p.headers.Snapshot.TableID,
			SchemaRevision: p.headers.Snapshot.SchemaRevision, Operations: []mutation.Operation{p.remove(lone)}})
	}
	protected(deleteLone(), p.headers, codeField)
	if got := p.record(t, p.headers, lone).GetString(codeField.name); got != "SYN-LONE" {
		t.Fatalf("refused transaction deleted or changed the protected row: %q", got)
	}
	// 6. Reciprocal relations: a normal write on the other side is refused
	//    because it would rewrite the protected lines.header field.
	protected(p.normal(p.lines, p.update(l1, map[string]any{headerField.name: h2})), p.lines, headerField)
	protected(p.normal(p.headers, p.update(h2, map[string]any{linesField.name: []string{l1}})), p.lines, headerField)
	if got := p.record(t, p.lines, l1).GetString(headerField.name); got != h1 {
		t.Fatalf("reciprocal write leaked: lines.header=%q want %q", got, h1)
	}
	// 7. The owning plugin's own transaction may still change its fields (the
	//    owner creates above already did so through the transaction surface).
	owner := &fieldprotection.Owner{PluginID: p.input.PluginID, InstallationID: p.input.InstallationID}
	if err := p.writeAs(p.headers, owner, p.update(h1, map[string]any{codeField.name: "SYN-1-OWNER"})); err != nil {
		t.Fatalf("owner write refused: %v", err)
	}
	// 8. After the manager releases, normal edits work again: the user moves the
	//    line to the second header together with the amounts the strict
	//    conservation rule requires, in one transaction.
	move := func() error {
		return p.normalPlan(t,
			mutation.Request{TableID: p.lines.Snapshot.TableID, SchemaRevision: p.lines.Snapshot.SchemaRevision, Operations: []mutation.Operation{p.update(l1, map[string]any{headerField.name: h2})}},
			mutation.Request{TableID: p.headers.Snapshot.TableID, SchemaRevision: p.headers.Snapshot.SchemaRevision, Operations: []mutation.Operation{
				p.update(h1, map[string]any{amountField.name: "0.00"}),
				p.update(h2, map[string]any{amountField.name: "22.00"}),
			}},
		)
	}
	protected(move(), p.lines, headerField)
	p.release(t, fieldprotection.FieldRef{TableID: p.lines.Snapshot.TableID, FieldID: headerField.id})
	if err := move(); err != nil {
		t.Fatalf("released field still refused: %v", err)
	}
	p.release(t)
	if err := p.normal(p.headers, p.update(h1, map[string]any{codeField.id: "EDITED"})); err != nil {
		t.Fatalf("released field still refused: %v", err)
	}
	if got := p.record(t, p.headers, h1).GetString(codeField.name); got != "EDITED" {
		t.Fatalf("released field edited by canonical ID = %q", got)
	}
	// The same deletion that protection refused is an ordinary write after release.
	if err := deleteLone(); err != nil {
		t.Fatalf("released row still not deletable: %v", err)
	}
	if _, err := p.pb.FindRecordById(p.headers.PhysicalName, lone); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("released row survived the delete transaction: %v", err)
	}
}

func TestFieldProtectionPlanRequiresOneWriterAndReleasesCoordinator(t *testing.T) {
	declared := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, declared)
	p.enable(t, declared)
	first := p.ownerTransaction(t, p.ownerPlan("SYN-FIRST")).NewIDs["headers"]["h"]
	second := p.ownerTransaction(t, p.ownerPlan("SYN-SECOND")).NewIDs["headers"]["h"]
	code := p.headerField("headers", "code").name
	owner := &fieldprotection.Owner{PluginID: p.input.PluginID, InstallationID: p.input.InstallationID}
	requests := []mutation.Request{
		{TableID: p.headers.Snapshot.TableID, SchemaRevision: p.headers.Snapshot.SchemaRevision,
			Operations: []mutation.Operation{p.update(first, map[string]any{code: "SYN-UPDATED-FIRST"})}, ProtectedFieldWriter: owner},
		{TableID: p.headers.Snapshot.TableID, SchemaRevision: p.headers.Snapshot.SchemaRevision,
			Operations: []mutation.Operation{p.update(second, map[string]any{code: "SYN-UPDATED-SECOND"})}},
	}
	err := p.normalPlan(t, requests...)
	if public := kernelError(err); public == nil || public.Code != "transaction.plan_invalid" {
		t.Fatalf("mixed writer plan = %v", err)
	}
	if p.record(t, p.headers, first).GetString(code) != "SYN-FIRST" ||
		p.record(t, p.headers, second).GetString(code) != "SYN-SECOND" {
		t.Fatal("rejected plan changed one of its rows")
	}
	requests[1].ProtectedFieldWriter = owner
	if err := p.normalPlan(t, requests...); err != nil {
		t.Fatalf("the next plan with one owner was refused: %v", err)
	}
	if p.record(t, p.headers, first).GetString(code) != "SYN-UPDATED-FIRST" ||
		p.record(t, p.headers, second).GetString(code) != "SYN-UPDATED-SECOND" {
		t.Fatal("the plan with one owner did not commit both rows")
	}
}

func TestFieldProtectionUnavailableBlocksEditsAndFilePreflight(t *testing.T) {
	ctx := t.Context()
	const unavailable = "mutation.field_protection.unavailable"
	t.Run("edit", func(t *testing.T) {
		code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
		p := newProtectionFixture(t, code)
		p.enable(t, code)
		id := p.ownerTransaction(t, p.ownerPlan("SYN-BEFORE")).NewIDs["headers"]["h"]
		field := p.headerField("headers", "code")
		restore := corruptStoredProtectionEvent(t, p.pb, p.input.PluginID)
		err := p.normal(p.headers, p.update(id, map[string]any{field.name: "SYN-DURING-FAILURE"}))
		if public := kernelError(err); public == nil || public.Code != unavailable || !public.Retryable {
			t.Fatalf("write with unreadable protection = %v", err)
		}
		if got := p.record(t, p.headers, id).GetString(field.name); got != "SYN-BEFORE" {
			t.Fatalf("write with unreadable protection changed the value to %q", got)
		}
		restore()
		if err := p.normal(p.headers, p.update(id, map[string]any{field.name: "SYN-AFTER-RESTORE"})); surfaceErrorCode(t, err) != "mutation.field_protected" {
			t.Fatalf("the restored registry did not protect the field: %v", err)
		}
	})
	t.Run("file", func(t *testing.T) {
		f := newFileListFixture(t)
		f.upload(t)
		workspace := "11111111-1111-4111-8111-111111111111"
		owner, _ := surfaceIOInstallation(t, f.pb, workspace, f.tableID, []string{"id", f.fieldID})
		recordProtectionEvent(t, f.pb, workspace, "fieldProtection:enable:store-fault", owner.ProjectKey, owner.PluginID, "1.0.0", "sha256:"+owner.PackageHash,
			fieldprotection.EnabledDetails{InstallationID: owner.InstallationID, InstallationRevision: 1, ModelID: "files", ModelVersion: 1,
				Fields: []fieldprotection.Entry{{TableID: f.tableID, FieldID: f.fieldID, Entity: "report_editions", Field: "archive_file",
					TableName: "归档版本", FieldName: "归档文件", FieldType: "file", PluginID: owner.PluginID, InstallationID: owner.InstallationID}}},
			"2026-01-01T00:00:00Z")
		table, err := schemaexecution.Describe(ctx, f.pb, f.tableID)
		if err != nil {
			t.Fatal(err)
		}
		if err := mutation.RefuseProtectedField(ctx, f.pb, table, f.fieldID); surfaceErrorCode(t, err) != "mutation.field_protected" {
			t.Fatalf("unscoped host preflight did not see the stored protection: %v", err)
		}
		original, err := f.manager.RefsByID(ctx, f.pb, f.tableID, f.recordID, f.fieldID)
		if err != nil || len(original) != 1 {
			t.Fatalf("original attachment = %#v %v", original, err)
		}
		restore := corruptStoredProtectionEvent(t, f.pb, owner.PluginID)
		if err := mutation.RefuseProtectedField(ctx, f.pb, table, f.fieldID); surfaceErrorCode(t, err) != unavailable {
			t.Fatalf("host file preflight with unreadable protection = %v", err)
		}
		ports := &pluginSurfaceIO{attachments: f.manager}
		owner.Method = "attachment.stage"
		owner.Request, err = json.Marshal(map[string]any{"collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID,
			"handle": "new-archive", "fileGrantId": "grant", "runId": "run", "name": "archive.png", "base64": base64.StdEncoding.EncodeToString(f.content)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := executeSurfaceAttachment(ctx, f.pb, workspace, owner, ports); surfaceErrorCode(t, err) != unavailable {
			t.Fatalf("owner staged an attachment with unreadable protection: %v", err)
		}
		if len(ports.stages.items) != 0 {
			t.Fatal("a failed permission read staged an attachment")
		}
		after, err := f.manager.RefsByID(ctx, f.pb, f.tableID, f.recordID, f.fieldID)
		if err != nil || len(after) != 1 || after[0].StoredName != original[0].StoredName {
			t.Fatalf("attachment changed after refused stage: %#v %v", after, err)
		}
		restore()
		if _, err := executeSurfaceAttachment(ctx, f.pb, workspace, owner, ports); err != nil {
			t.Fatalf("restored protection did not admit the owner: %v", err)
		}
	})
}

// This fixture is an already persisted event that became unreadable. It is
// installed below the package API so validation at write time remains intact.
func corruptStoredProtectionEvent(t *testing.T, app core.App, pluginID string) func() {
	t.Helper()
	record, err := app.FindFirstRecordByFilter("vibetable_plugin_records",
		"kind='audit' && plugin_id={:plugin} && payload_json.eventType={:event}",
		dbx.Params{"plugin": pluginID, "event": fieldprotection.EventEnabled})
	if err != nil {
		t.Fatal(err)
	}
	original := record.GetRaw("payload_json")
	record.Set("payload_json", map[string]any{"pluginId": pluginID, "eventType": fieldprotection.EventEnabled, "details": map[string]any{}})
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		record.Set("payload_json", original)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFieldProtectionOwnerIsBoundToTheInstallation(t *testing.T) {
	code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, code)
	p.enable(t, code)
	created := p.ownerTransaction(t, p.ownerPlan("SYN-1"))
	h1 := created.NewIDs["headers"]["h"]
	codeField := p.headerField("headers", "code")
	// A reinstalled plugin has a new installation id: it is not the owner until
	// the manager enables protection again, even with the same plugin id.
	err := p.writeAs(p.headers, &fieldprotection.Owner{PluginID: p.input.PluginID, InstallationID: "evt_reinstalled"}, p.update(h1, map[string]any{codeField.name: "REINSTALLED"}))
	if public := kernelError(err); public == nil || public.Code != "mutation.field_protected" {
		t.Fatalf("a reinstalled plugin wrote a field it no longer owns: %v", err)
	}
	if got := p.record(t, p.headers, h1).GetString(codeField.name); got != "SYN-1" {
		t.Fatalf("value changed to %q", got)
	}
	// Another plugin id is refused as well, even with the same installation id.
	err = p.writeAs(p.headers, &fieldprotection.Owner{PluginID: "other.plugin", InstallationID: p.input.InstallationID}, p.update(h1, map[string]any{codeField.name: "FOREIGN"}))
	if public := kernelError(err); public == nil || public.Code != "mutation.field_protected" {
		t.Fatalf("a foreign plugin wrote a protected field: %v", err)
	}
}

func TestFieldProtectionReleaseWorksWhileDisabledAndAfterUninstall(t *testing.T) {
	code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, code)
	p.enable(t, code)
	store := pluginstore.New(p.pb, p.workspace)
	// Disabled plugin: protection persists, release stays available.
	if _, err := store.SetEnabled(context.Background(), p.input.ProjectKey, p.input.PluginID, false); err != nil {
		t.Fatal(err)
	}
	p.syncRevision(t)
	disabled := p.describe(t)
	if len(disabled.Protected) != 1 || !reflect.DeepEqual(disabled.BlockingReasons, []string{"plugin_disabled"}) {
		t.Fatalf("disabled describe = %#v", disabled)
	}
	if _, err := p.rpc(t, "plugin.fieldProtection.enable", p.enableParams(code)); publicError(err) == nil || publicError(err).Code != "field_protection.installation_unavailable" {
		t.Fatalf("enable while disabled = %v", err)
	}
	if released := p.release(t, fieldprotection.FieldRef{TableID: p.headers.Snapshot.TableID, FieldID: p.headerField("headers", "code").id}); len(released.Protected) != 0 {
		t.Fatalf("release while disabled = %#v", released)
	}
	if _, err := store.SetEnabled(context.Background(), p.input.ProjectKey, p.input.PluginID, true); err != nil {
		t.Fatal(err)
	}
	p.syncRevision(t)
	p.enable(t, code)
	if _, err := store.DeleteInstallation(context.Background(), p.input.ProjectKey, p.input.PluginID, p.input.InstallationID, p.input.InstallationRevision, false); err != nil {
		t.Fatal(err)
	}
	// Reopen the database with fresh connections after uninstall, before the
	// manager discovers and releases the persisted protection.
	if err := p.pb.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := p.pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	raw, err := p.rpc(t, "plugin.fieldProtection.list", map[string]any{"projectKey": p.input.ProjectKey})
	if err != nil || !reflect.DeepEqual(raw, []pluginFieldProtectionListItem{{PluginID: p.input.PluginID, ProtectedCount: 1}}) {
		t.Fatalf("uninstalled list = %#v, %v", raw, err)
	}
	gone := p.describe(t)
	if gone.Installation != nil || len(gone.Protected) != 1 || !reflect.DeepEqual(gone.BlockingReasons, []string{"plugin_not_installed", "no_declaration"}) {
		t.Fatalf("uninstalled describe = %#v", gone)
	}
	released := p.release(t)
	if len(released.Protected) != 0 {
		t.Fatalf("release after uninstall = %#v", released)
	}
	// The persisted event stream retains the former package attribution.
	registry, err := fieldprotection.Load(context.Background(), p.pb)
	if err != nil || registry.Count() != 0 || registry.Events() != 4 || registry.EnableEvents() != 2 {
		t.Fatalf("cold registry = %d events, err=%v", registry.Count(), err)
	}
	if identity := registry.Identity(p.input.PluginID); identity.PluginVersion != "1.0.0" || identity.PackageHash == "" {
		t.Fatalf("uninstalled plugin lost its protection identity: %#v", identity)
	}
}

func TestFieldProtectionReleaseNullReleasesAllProtectedFields(t *testing.T) {
	code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	amount := fieldprotection.DeclaredField{Entity: "headers", Field: "amount"}
	p := newProtectionFixture(t, code, amount)
	enabled := p.enable(t, code, amount)
	if len(enabled.Protected) != 2 {
		t.Fatalf("enabled protected fields = %#v", enabled.Protected)
	}
	result, err := p.rpc(t, "plugin.fieldProtection.release", map[string]any{
		"projectKey": p.input.ProjectKey, "pluginId": p.input.PluginID, "fields": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	released := result.(*pluginFieldProtectionDescribeResult)
	if len(released.Protected) != 0 || released.Events != enabled.Events+1 {
		t.Fatalf("explicit null did not persist one release-all event: %#v", released)
	}
}

func TestFieldProtectionOwnerConflictAndNotOwner(t *testing.T) {
	code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, code)
	codeField := p.headerField("headers", "code")
	// Another plugin already protects the same physical field.
	foreign, err := fieldprotection.NewEnabledEvent("fieldProtection:enable:foreign", p.input.ProjectKey, "other.plugin", "2.0.0", "sha256:"+strings.Repeat("b", 64), fieldprotection.EnabledDetails{InstallationID: "inst_other", InstallationRevision: 1, ModelID: "synthetic", ModelVersion: 1, Fields: []fieldprotection.Entry{{TableID: p.headers.Snapshot.TableID, FieldID: codeField.id, Entity: "headers", Field: "code", TableName: "单头", FieldName: "单号", FieldType: "text", PluginID: "other.plugin", InstallationID: "inst_other"}}}, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.pb.RunInTransaction(func(tx core.App) error {
		_, err := pluginstore.New(tx, p.workspace).RecordAuditInTransaction(context.Background(), foreign)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view := p.describe(t)
	if view.Suggested[0].State != "protected_by_other" || view.Suggested[0].Eligible || len(view.Foreign) != 1 || view.Foreign[0].PluginID != "other.plugin" {
		t.Fatalf("conflict view = %#v", view)
	}
	_, err = p.rpc(t, "plugin.fieldProtection.enable", p.enableParams(code))
	if public := publicError(err); public == nil || public.Code != "field_protection.owner_conflict" || public.Details["pluginId"] != "other.plugin" {
		t.Fatalf("enable over a foreign owner = %v", err)
	}
	// The manager cannot release another plugin's field through this plugin.
	_, err = p.rpc(t, "plugin.fieldProtection.release", map[string]any{"projectKey": p.input.ProjectKey, "pluginId": p.input.PluginID, "fields": []fieldprotection.FieldRef{{TableID: p.headers.Snapshot.TableID, FieldID: codeField.id}}})
	if public := publicError(err); public == nil || public.Code != "field_protection.not_owner" {
		t.Fatalf("foreign release = %v", err)
	}
	// Release-all of this plugin is harmless and never touches the foreign owner.
	if released := p.release(t); len(released.Protected) != 0 {
		t.Fatalf("release all = %#v", released)
	}
	if registry, _ := fieldprotection.Load(context.Background(), p.pb); registry.Count() != 1 {
		t.Fatal("release all must only release the plugin's own fields")
	}
}

func TestFieldProtectionEnableRefusesStaleUndeclaredAndMalformedRequests(t *testing.T) {
	code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, code)
	stale := maps.Clone(p.enableParams(code))
	stale["installationRevision"] = p.input.InstallationRevision - 1
	staleMapping := p.enableParams(code)
	staleMapping["fields"].([]pluginFieldProtectionSelection)[0].FieldID = "old-field-id"
	cases := []struct {
		name   string
		params map[string]any
		code   string
	}{
		{"stale revision", stale, "field_protection.installation_stale"},
		{"stale mapping", staleMapping, "field_protection.binding_stale"},
		{"undeclared field", p.enableParams(fieldprotection.DeclaredField{Entity: "headers", Field: "amount"}), "field_protection.field_not_declared"},
		{"unknown entity", p.enableParams(fieldprotection.DeclaredField{Entity: "missing", Field: "code"}), "field_protection.field_not_declared"},
	}
	for _, tc := range cases {
		if _, err := p.rpc(t, "plugin.fieldProtection.enable", tc.params); publicError(err) == nil || publicError(err).Code != tc.code {
			t.Errorf("%s: err=%v want %s", tc.name, err, tc.code)
		}
	}
	if registry, _ := fieldprotection.Load(context.Background(), p.pb); registry.Events() != 0 {
		t.Fatal("refused enables must not append events")
	}
	prefix := `{"projectKey":"` + p.input.ProjectKey + `","pluginId":"` + p.input.PluginID + `","installationId":"` + p.input.InstallationID + `","installationRevision":` + fmt.Sprint(p.input.InstallationRevision)
	for _, raw := range []string{
		`{"projectKey":"` + p.input.ProjectKey + `","pluginId":"` + p.input.PluginID + `"}`,
		prefix + `,"fields":null}`,
		prefix + `,"fields":[]}`,
		prefix + `,"fields":[{"entity":"headers","field":"code","tableId":"t","fieldId":"f"}],"all":true}`,
		prefix + `,"fields":[{"entity":"headers"}]}`,
		prefix + `,"fields":[{"entity":"headers","field":"code"}],"fields":[]}`,
	} {
		if err := p.registrations["plugin.fieldProtection.enable"].ValidateParams([]byte(raw)); publicError(err) == nil || publicError(err).Code != "pluginFieldProtection.params.invalid" {
			t.Errorf("closed enable decode accepted %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"projectKey":"` + p.input.ProjectKey + `","pluginId":"` + p.input.PluginID + `","fields":null,"all":true}`, `{"projectKey":"other","pluginId":"` + p.input.PluginID + `"}`, `{"projectKey":"` + p.input.ProjectKey + `","pluginId":"` + p.input.PluginID + `","fields":[]}`, `{"projectKey":"` + p.input.ProjectKey + `","pluginId":"` + p.input.PluginID + `"} {}`} {
		if err := p.registrations["plugin.fieldProtection.release"].ValidateParams([]byte(raw)); publicError(err) == nil || publicError(err).Code != "pluginFieldProtection.params.invalid" {
			t.Errorf("closed release decode accepted %s: %v", raw, err)
		}
	}
	// No declaration in the manifest: enable is refused, describe says why.
	bare := newProtectionFixture(t)
	if view := bare.describe(t); view.Declared || !reflect.DeepEqual(view.BlockingReasons, []string{"no_declaration"}) {
		t.Fatalf("undeclared describe = %#v", view)
	}
	if _, err := bare.rpc(t, "plugin.fieldProtection.enable", bare.enableParams(code)); publicError(err) == nil || publicError(err).Code != "field_protection.declaration_invalid" {
		t.Fatalf("enable without declaration = %v", err)
	}
}

func TestFieldProtectionEnableAdoptsFormerFieldsAndChecksBudget(t *testing.T) {
	code, amount := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}, fieldprotection.DeclaredField{Entity: "headers", Field: "amount"}
	p := newProtectionFixture(t, code, amount)
	p.enable(t, code)
	// Enabling a superset adopts the already-protected field under the same
	// ownership without exceeding the per-plugin budget.
	after := p.enable(t, code, amount)
	if len(after.Protected) != 2 || after.Protected[0].InstallationID != p.input.InstallationID {
		t.Fatalf("adopt = %#v", after.Protected)
	}
	// The event budget counts enables only; exhausting it blocks enabling.
	for i := 0; i < fieldprotection.MaxEvents-2; i++ {
		event, err := fieldprotection.NewEnabledEvent(fmt.Sprintf("fieldProtection:enable:bulk%d", i), p.input.ProjectKey, "bulk.plugin", "1.0.0", "sha256:"+strings.Repeat("c", 64), fieldprotection.EnabledDetails{InstallationID: "inst_bulk", InstallationRevision: 1, ModelID: "synthetic", ModelVersion: 1, Fields: []fieldprotection.Entry{{TableID: "tbl_bulk", FieldID: fmt.Sprintf("fld_%d", i), Entity: "bulk", Field: "x"}}}, "2026-01-01T00:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
		if err := p.pb.RunInTransaction(func(tx core.App) error {
			_, err := pluginstore.New(tx, p.workspace).RecordAuditInTransaction(context.Background(), event)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	view := p.describe(t)
	if view.EventsRemaining != 0 || !reflect.DeepEqual(view.BlockingReasons, []string{"events_exhausted"}) {
		t.Fatalf("exhausted view = %#v", view)
	}
	if _, err := p.rpc(t, "plugin.fieldProtection.enable", p.enableParams(code)); publicError(err) == nil || publicError(err).Code != "field_protection.budget_exceeded" {
		t.Fatalf("exhausted enable = %v", err)
	}
	// Release never consumes budget and always works.
	if released := p.release(t); len(released.Protected) != 0 || released.EventsRemaining != 0 || released.Events != view.Events+1 {
		t.Fatalf("release at budget = %#v", released)
	}
}

// pluginRecordQueries captures the SELECT statements issued against the plugin
// records table while apply runs.
func pluginRecordQueries(t *testing.T, app core.App, apply func()) map[string]string {
	t.Helper()
	db := app.NonconcurrentDB().(*dbx.DB)
	previous := db.QueryLogFunc
	queries := map[string]string{}
	var mu sync.Mutex
	db.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
		if previous != nil {
			previous(ctx, elapsed, statement, rows, err)
		}
		if err != nil || !strings.HasPrefix(statement, "SELECT") || !strings.Contains(statement, "FROM `vibetable_plugin_records`") {
			return
		}
		mu.Lock()
		queries[statement] = statement
		mu.Unlock()
	}
	defer func() { db.QueryLogFunc = previous }()
	apply()
	return queries
}

func assertIndexServed(t *testing.T, app core.App, statement string) {
	t.Helper()
	db := app.NonconcurrentDB().(*dbx.DB)
	params := dbx.Params{"kind": "audit"}
	statement = strings.ReplaceAll(statement, "'audit'", "{:kind}")
	for key, value := range map[string]string{"enabled": fieldprotection.EventEnabled, "released": fieldprotection.EventReleased} {
		literal := "'" + value + "'"
		if strings.Contains(statement, literal) {
			params[key] = value
			statement = strings.ReplaceAll(statement, literal, "{:"+key+"}")
		}
	}
	var plan []struct {
		Detail string `db:"detail"`
	}
	if err := db.NewQuery("EXPLAIN QUERY PLAN " + statement).Bind(params).All(&plan); err != nil {
		t.Fatal(err)
	}
	used := false
	for _, step := range plan {
		used = used || strings.Contains(step.Detail, "idx_vibetable_plugin_records_event_lookup") && strings.Contains(step.Detail, "<expr>=?")
	}
	if !used {
		t.Fatalf("field protection lookup scans plugin history. Plan: %v", plan)
	}
}

func TestFieldProtectionLookupIsIndexServedAndIgnoresLongHistory(t *testing.T) {
	code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, code)
	ctx := context.Background()
	// Default OFF: two empty probes, both served by the existing event index.
	load := func(tx core.App) *fieldprotection.Registry {
		registry, err := fieldprotection.Load(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return registry
	}
	queries := pluginRecordQueries(t, p.pb, func() {
		_ = p.pb.RunInTransaction(func(tx core.App) error {
			if registry := load(tx); registry.Count() != 0 {
				t.Fatalf("default registry = %d", registry.Count())
			}
			return nil
		})
	})
	if len(queries) != 2 {
		t.Fatalf("Load issued %d plugin record queries, want one per event type: %v", len(queries), queries)
	}
	for _, statement := range queries {
		assertIndexServed(t, p.pb, statement)
	}
	// A long unrelated plugin audit history is never replayed or scanned.
	p.enable(t, code)
	if _, err := p.pb.DB().NewQuery(`INSERT INTO vibetable_plugin_records(id,kind,project_key,plugin_id,item_key,payload_json,seq)WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 2000)SELECT 'history'||x,'audit',{:project},{:plugin},'history'||x,'{"eventType":"model.progress","details":{}}',1000000+x FROM n`).Bind(dbx.Params{"project": p.input.ProjectKey, "plugin": p.input.PluginID}).Execute(); err != nil {
		t.Fatal(err)
	}
	queries = pluginRecordQueries(t, p.pb, func() {
		_ = p.pb.RunInTransaction(func(tx core.App) error {
			registry := load(tx)
			if registry.Count() != 1 || registry.Events() != 1 || registry.EnableEvents() != 1 {
				t.Fatalf("registry after long history = %d events", registry.Count())
			}
			return nil
		})
	})
	if len(queries) != 2 {
		t.Fatalf("Load issued %d queries with history: %v", len(queries), queries)
	}
	for _, statement := range queries {
		assertIndexServed(t, p.pb, statement)
	}
}

func TestFieldProtectionKernelLoadsRegistryOncePerTransaction(t *testing.T) {
	code := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, code)
	p.enable(t, code)
	first, second, third := p.ownerTransaction(t, p.ownerPlan("SYN-1")), p.ownerTransaction(t, p.ownerPlan("SYN-2")), p.ownerTransaction(t, p.ownerPlan("SYN-3"))
	amount := p.headerField("headers", "amount").name
	protectionLookups := func(queries map[string]string) int {
		count := 0
		for statement := range queries {
			if strings.Contains(statement, "fieldProtection.") {
				count++
			}
		}
		return count
	}
	// A normal multi-row edit loads the registry exactly once per transaction
	// (one probe per event type), never once per row or table.
	lineAmount := p.headerField("lines", "amount").name
	queries := pluginRecordQueries(t, p.pb, func() {
		if err := p.normalPlan(t,
			mutation.Request{TableID: p.headers.Snapshot.TableID, SchemaRevision: p.headers.Snapshot.SchemaRevision, Operations: []mutation.Operation{
				p.update(first.NewIDs["headers"]["h"], map[string]any{amount: "11.00"}),
				p.update(second.NewIDs["headers"]["h"], map[string]any{amount: "12.00"}),
				p.update(third.NewIDs["headers"]["h"], map[string]any{amount: "15.00"}),
			}},
			mutation.Request{TableID: p.lines.Snapshot.TableID, SchemaRevision: p.lines.Snapshot.SchemaRevision, Operations: []mutation.Operation{
				p.update(first.NewIDs["lines"]["l"], map[string]any{lineAmount: "11.00"}),
				p.update(second.NewIDs["lines"]["l"], map[string]any{lineAmount: "12.00"}),
				p.update(third.NewIDs["lines"]["l"], map[string]any{lineAmount: "15.00"}),
			}},
		); err != nil {
			t.Fatal(err)
		}
	})
	if count := protectionLookups(queries); count != 2 {
		t.Fatalf("protection lookups per transaction = %d, want 2 (one per event type): %v", count, queries)
	}
	// With protection off the same write still costs only the two probes.
	p.release(t)
	queries = pluginRecordQueries(t, p.pb, func() {
		if err := p.normalPlan(t,
			mutation.Request{TableID: p.headers.Snapshot.TableID, SchemaRevision: p.headers.Snapshot.SchemaRevision, Operations: []mutation.Operation{p.update(second.NewIDs["headers"]["h"], map[string]any{amount: "14.00"})}},
			mutation.Request{TableID: p.lines.Snapshot.TableID, SchemaRevision: p.lines.Snapshot.SchemaRevision, Operations: []mutation.Operation{p.update(second.NewIDs["lines"]["l"], map[string]any{lineAmount: "14.00"})}},
		); err != nil {
			t.Fatal(err)
		}
	})
	if count := protectionLookups(queries); count != 2 {
		t.Fatalf("default-off lookups = %d, want 2: %v", count, queries)
	}
}

func TestFieldProtectionDeclarationRefusesReadOnlyEntities(t *testing.T) {
	p := newProtectionFixture(t)
	model := p.installedModel(t)
	model.Entities[0].Operations = []string{"read", "query"}
	if err := (fieldprotection.Declaration{Contract: fieldprotection.Contract, Fields: []fieldprotection.DeclaredField{{Entity: "headers", Field: "code"}}}).Validate(model); err == nil {
		t.Fatal("read-only entity accepted")
	}
}

// secondSurfacePlugin installs another plugin with the same data grants, so a
// non-owner reaches the protection check with an otherwise fully valid surface.
func secondSurfacePlugin(t *testing.T, app core.App, workspace, table string, fields []string, pluginID, token string) pluginSurfaceAPIRequest {
	t.Helper()
	store := pluginstore.New(app, workspace)
	hash := strings.Repeat("9", 64)
	manifest := map[string]any{
		"$schema":       "vibetable.plugin-manifest.v2",
		"pluginId":      pluginID,
		"version":       "1.0.0",
		"displayName":   map[string]any{},
		"description":   map[string]any{},
		"compatibility": map[string]any{"pluginApi": "3.x"},
		"permissions": map[string]any{
			"files": []string{"pickRead", "pickWrite"},
			"data":  []any{map[string]any{"collection": table, "operations": []string{"read", "query", "create", "update"}, "fields": fields}},
		},
		"actions": []any{map[string]any{"actionId": "save", "risk": "write", "mode": "local", "invocation": "manual"}},
		"ui":      map[string]any{"customViews": []any{map[string]any{"viewId": "save", "actionId": "save", "entry": "ui/index.html"}}},
	}
	if _, err := store.SaveInstallation(context.Background(), installationSnapshot(t, store.ProjectKey(), pluginID, hash, manifest, 1, "enabled"), nil); err != nil {
		t.Fatal(err)
	}
	return pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, ActionID: "save", Entry: "ui/index.html", PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1, InstallationID: installationRecordID(t, store, pluginID), ActiveCollection: table, SurfaceToken: token, RequestID: "files-request"}
}

// The archive a plugin issues (output.stage → mutation.commit with uploadHandles,
// e.g. report_editions.archive_file) is a protected file field like any other:
// the owner keeps every original guard, everyone else, including the host
// user, is refused, and no identity claimed in JSON is ever a pass.
func TestFieldProtectionArchivePathKeepsOwnerGuardsAndRefusesEveryoneElse(t *testing.T) {
	ctx := context.Background()
	f := newFileListFixture(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	owner, store := surfaceIOInstallation(t, f.pb, workspace, f.tableID, []string{"id", f.fieldID})
	archive := fieldprotection.Entry{TableID: f.tableID, FieldID: f.fieldID, Entity: "report_editions", Field: "archive_file", TableName: "归档版本", FieldName: "归档文件", FieldType: "file", PluginID: owner.PluginID, InstallationID: owner.InstallationID}
	recordProtectionEvent(t, f.pb, workspace, "fieldProtection:enable:archive", owner.ProjectKey, owner.PluginID, "1.0.0", "sha256:"+owner.PackageHash, fieldprotection.EnabledDetails{InstallationID: owner.InstallationID, InstallationRevision: 1, ModelID: "files", ModelVersion: 1, Fields: []fieldprotection.Entry{archive}}, "2026-01-01T00:00:00Z")
	if registry, err := fieldprotection.Load(ctx, f.pb); err != nil || registry.Count() != 1 {
		t.Fatalf("archive field not protected: %d %v", registry.Count(), err)
	}
	// 1. The owner's own archive keeps every original guard: cancel, foreign
	// surface, rollback at the provenance boundary, idempotent retry, immutable
	// reopen, and never another installation.
	checkOutputArchive(t, f, workspace, owner, store, "issued.png", "image/png")
	raw, err := store.GetAudit(ctx, owner.ProjectKey, owner.PluginID, "output-issued-one")
	if err != nil || raw == nil {
		t.Fatalf("owner archive lost its provenance: %s %v", raw, err)
	}
	var provenance map[string]any
	if err := json.Unmarshal(raw, &provenance); err != nil || provenance["eventType"] != "output.archived" {
		t.Fatalf("owner archive audit = %s %v", raw, err)
	}
	// 2. Another plugin with identical data grants is refused before any byte
	// is staged, and its preview/commit is refused as well.
	other := secondSurfacePlugin(t, f.pb, workspace, f.tableID, []string{"id", f.fieldID}, "com.example.other", "surface-b")
	ports := &pluginSurfaceIO{attachments: f.manager}
	other.Method = "output.stage.begin"
	other.Request, _ = json.Marshal(map[string]any{"handle": "other-output", "artifactId": "other-artifact", "collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID, "name": "forged.png", "mediaType": "image/png", "sizeBytes": 3, "source": surfaceOutputSource{"t", "d", "p"}})
	if _, err := executeSurfaceOutput(ctx, f.pb, workspace, other, ports); surfaceErrorCode(t, err) != "mutation.field_protected" {
		t.Fatalf("foreign plugin staged a protected archive: %v", err)
	}
	other.Method = "attachment.stage"
	other.Request, _ = json.Marshal(map[string]any{"collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID, "handle": "other-stage", "fileGrantId": "grant", "runId": "run", "offset": 0, "base64": "eA=="})
	if _, err := executeSurfaceAttachment(ctx, f.pb, workspace, other, ports); surfaceErrorCode(t, err) != "mutation.field_protected" {
		t.Fatalf("foreign plugin staged bytes into a protected field: %v", err)
	}
	schema, err := schemaexecution.Describe(ctx, f.pb, f.tableID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(f.pb, source).QueryPage(ctx, f.tableID, query.TableQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	plan := func(extra map[string]any) map[string]any {
		op := map[string]any{"kind": "setAttachments", "primaryKey": f.recordID, "expectedDigest": page.Rows[0][productrow.DigestField], "fieldId": f.fieldID, "uploadHandles": []string{"other-stage"}, "removeStoredNames": []string{}}
		for key, value := range extra {
			op[key] = value
		}
		return map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": f.tableID, "schemaRevision": schema.Snapshot.SchemaRevision, "operations": []any{op}}
	}
	other.Method = "mutation.preview"
	other.Request, _ = json.Marshal(plan(nil))
	if _, err := surfaceMutationRequest(ctx, f.pb, workspace, other, ports); surfaceErrorCode(t, err) != "mutation.field_protected" {
		t.Fatalf("foreign plugin committed into a protected field: %v", err)
	}
	// A claimed identity in the plugin's JSON is not decoded at all.
	other.Request, _ = json.Marshal(plan(map[string]any{"protectedFieldWriter": map[string]any{"pluginId": owner.PluginID, "installationId": owner.InstallationID}}))
	if _, err := surfaceMutationRequest(ctx, f.pb, workspace, other, ports); surfaceErrorCode(t, err) != "plugin_capability_invalid" {
		t.Fatalf("claimed owner key was accepted: %v", err)
	}
	// 3. Host manual writes: add/replace, delete the archive, raw value write and
	// row deletion are all refused and the issued archive stays byte-identical.
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(f.manager))
	refs, err := f.manager.RefsByID(ctx, f.pb, f.tableID, f.recordID, f.fieldID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("owner archive missing: %v %v", refs, err)
	}
	stored := refs[0].StoredName
	host := func(op mutation.Operation, writer *fieldprotection.Owner) mutation.Request {
		return mutation.Request{
			ContractVersion:      mutation.ContractVersion,
			RequestID:            "host-archive",
			IdempotencyKey:       "host-archive-" + fmt.Sprint(op.Kind) + fmt.Sprint(len(op.RemoveStoredNames)),
			TableID:              f.tableID,
			SchemaRevision:       schema.Snapshot.SchemaRevision,
			Operations:           []mutation.Operation{op},
			Actor:                mutation.Actor{Type: "user", ID: "local-user"},
			ProtectedFieldWriter: writer,
		}
	}
	if err := f.manager.Stage("host-upload", "manual.png", f.content); err != nil {
		t.Fatal(err)
	}
	add := mutation.Operation{Kind: mutation.OperationSetAttachments, RecordID: &f.recordID, FieldID: f.fieldID, UploadHandles: []string{"host-upload"}, RemoveStoredNames: []string{}}
	if _, err := kernel.Apply(ctx, host(add, nil)); surfaceErrorCode(t, err) != "mutation.field_protected" {
		t.Fatalf("host replaced a protected archive: %v", err)
	}
	remove := mutation.Operation{Kind: mutation.OperationSetAttachments, RecordID: &f.recordID, FieldID: f.fieldID, UploadHandles: []string{}, RemoveStoredNames: []string{stored}}
	if _, err := kernel.Apply(ctx, host(remove, nil)); surfaceErrorCode(t, err) != "mutation.field_protected" {
		t.Fatalf("host deleted a protected archive: %v", err)
	}
	dropRow := mutation.Operation{Kind: mutation.OperationDelete, RecordID: &f.recordID}
	if _, err := kernel.Apply(ctx, host(dropRow, nil)); surfaceErrorCode(t, err) != "mutation.field_protected" {
		t.Fatalf("host deleted the row holding a protected archive: %v", err)
	}
	// 4. A plugin identity is not a pass: the same plugin after a reinstall, or
	// another plugin presenting the owner's installation, is refused by the kernel.
	if _, err := kernel.Apply(ctx, host(add, &fieldprotection.Owner{PluginID: owner.PluginID, InstallationID: "reinstalled"})); surfaceErrorCode(t, err) != "mutation.field_protected" {
		t.Fatalf("reinstalled plugin wrote a former field: %v", err)
	}
	if _, err := kernel.Apply(ctx, host(add, &fieldprotection.Owner{PluginID: other.PluginID, InstallationID: owner.InstallationID})); surfaceErrorCode(t, err) != "mutation.field_protected" {
		t.Fatalf("foreign plugin borrowed the owner installation: %v", err)
	}
	refs, err = f.manager.RefsByID(ctx, f.pb, f.tableID, f.recordID, f.fieldID)
	if err != nil || len(refs) != 1 || refs[0].StoredName != stored {
		t.Fatalf("protected archive changed: %#v %v", refs, err)
	}
	// 5. Release hands the archive back to the host.
	recordProtectionEvent(t, f.pb, workspace, "fieldProtection:release:archive", owner.ProjectKey, owner.PluginID, "1.0.0", "sha256:"+owner.PackageHash, fieldprotection.ReleasedDetails{Fields: []fieldprotection.FieldRef{{TableID: f.tableID, FieldID: f.fieldID}}}, "2026-01-02T00:00:00Z")
	if _, err := kernel.Apply(ctx, host(remove, nil)); err != nil {
		t.Fatalf("released archive still refused: %v", err)
	}
	if refs, err := f.manager.RefsByID(ctx, f.pb, f.tableID, f.recordID, f.fieldID); err != nil || len(refs) != 0 {
		t.Fatalf("released archive not removed: %#v %v", refs, err)
	}
}

func recordProtectionEvent(t *testing.T, app core.App, workspace, eventID, projectKey, pluginID, version, hash string, details any, at string) {
	t.Helper()
	var event json.RawMessage
	var err error
	switch payload := details.(type) {
	case fieldprotection.EnabledDetails:
		event, err = fieldprotection.NewEnabledEvent(eventID, projectKey, pluginID, version, hash, payload, at)
	case fieldprotection.ReleasedDetails:
		event, err = fieldprotection.NewReleasedEvent(eventID, projectKey, pluginID, version, hash, payload, at)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RunInTransaction(func(tx core.App) error {
		_, err := pluginstore.New(tx, workspace).RecordAuditInTransaction(context.Background(), event)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Structural erasure is refused at the authoritative schema layer. Every
// protectable field is model-bound, so the plugin model binding already keeps
// it undeletable (plugin.model.core_protected, surviving disable and
// uninstall). Persistent protection adds a refusal that names the owner, so a
// manager knows what to release; display renames and user extension fields are
// unaffected while protected, and releasing exposes the model guard instead of
// the values.
func TestFieldProtectionSchemaEraseIsRefusedUntilReleased(t *testing.T) {
	declared := fieldprotection.DeclaredField{Entity: "headers", Field: "code"}
	p := newProtectionFixture(t, declared)
	ctx := context.Background()
	headers := p.model.Bindings["headers"].TableID
	code := p.headerField("headers", "code")
	p.enable(t, declared)
	table, err := schemaexecution.Describe(ctx, p.pb, headers)
	if err != nil {
		t.Fatal(err)
	}
	field, _ := table.Field(code.id)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	intent := func(action v2.ChangeAction, fieldID string, draft *v2.FieldDraft) v2.FieldChangeIntent {
		return v2.FieldChangeIntent{Action: action, TableID: headers, FieldID: fieldID, ExpectedSchemaRev: table.Snapshot.SchemaRevision, Draft: draft, Actor: actor}
	}
	refused := func(err error, code, path string) *schemaerror.ProductError {
		t.Helper()
		var productErr *schemaerror.ProductError
		if !errors.As(err, &productErr) || productErr.Code != code || productErr.Path != path {
			t.Fatalf("error = %v, want %s at %s", err, code, path)
		}
		return productErr
	}
	revision, err := v2.ParseSchemaRevision(table.Snapshot.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}

	// Purge, retire and a constraint change name the owner.
	productErr := refused(planError(p.domain, ctx, intent(v2.ActionPurge, code.id, nil)), fieldprotection.SchemaEraseCode, "fieldId")
	if productErr.Details["pluginId"] != p.input.PluginID || productErr.Details["field"] != "code" {
		t.Fatalf("details = %#v", productErr.Details)
	}
	refused(planError(p.domain, ctx, intent(v2.ActionRetire, code.id, nil)), fieldprotection.SchemaEraseCode, "fieldId")
	constraint := fieldDraft(field, "受保护单号")
	constraint.Constraints.Unique.Enabled = !field.Constraints.Unique.Enabled
	refused(planError(p.domain, ctx, intent(v2.ActionUpdate, code.id, &constraint)), fieldprotection.SchemaEraseCode, "fieldId")
	// Deleting the table is refused by the same guard; in this fixture the
	// lines relation refuses first, so the guard is exercised where DeleteTable
	// calls it, on the live registry.
	productErr = refused(fieldprotection.RefuseTableErase(ctx, p.pb, headers), fieldprotection.SchemaEraseCode, "tableId")
	if ids, _ := productErr.Details["fieldIds"].([]string); len(ids) != 1 || ids[0] != code.id {
		t.Fatalf("details = %#v", productErr.Details)
	}
	if _, err = schemaapi.New(p.pb).DeleteTable(ctx, headers, revision); err == nil {
		t.Fatal("deleting a protected table succeeded")
	}

	// A display rename of the protected field and a user extension field stay
	// editable; neither touches a protected value.
	renameDraft := fieldDraft(field, "受保护单号")
	rename, err := p.domain.Plan(ctx, intent(v2.ActionUpdate, code.id, &renameDraft))
	if err != nil || !rename.CanApply {
		t.Fatalf("rename plan = %#v, %v", rename, err)
	}
	if _, err = p.domain.Apply(ctx, v2.ApplyRequest{PlanID: rename.PlanID, PlanHash: rename.PlanHash, OperationID: "protection-rename", Actor: actor}); err != nil {
		t.Fatal(err)
	}
	ext := createSchemaProductField(t, p.pb, headers, v2.LogicalText, "用户扩展", "protection-extension")
	table, err = schemaexecution.Describe(ctx, p.pb, headers)
	if err != nil {
		t.Fatal(err)
	}
	extField, _ := table.Field(ext.FieldID)
	if err = fieldprotection.RefuseSchemaErase(ctx, p.pb, headers, ext.FieldID, &extField, nil); err != nil {
		t.Fatalf("user extension purge refused: %v", err)
	}
	retire, err := p.domain.Plan(ctx, intent(v2.ActionRetire, ext.FieldID, nil))
	if err != nil || !retire.CanApply {
		t.Fatalf("extension retire plan = %#v, %v", retire, err)
	}
	if _, err = p.domain.Apply(ctx, v2.ApplyRequest{PlanID: retire.PlanID, PlanHash: retire.PlanHash, OperationID: "protection-extension-retire", Actor: actor, Confirmations: retire.Confirmations}); err != nil {
		t.Fatal(err)
	}
	if renamed, err := schemaexecution.Describe(ctx, p.pb, headers); err != nil {
		t.Fatal(err)
	} else if current, _ := renamed.Field(code.id); current.DisplayName != "受保护单号" {
		t.Fatalf("display name = %q", current.DisplayName)
	}

	// Release exposes the model guard, which stays regardless of protection.
	p.release(t, fieldprotection.FieldRef{TableID: headers, FieldID: code.id})
	if err = fieldprotection.RefuseTableErase(ctx, p.pb, headers); err != nil {
		t.Fatalf("table guard after release = %v", err)
	}
	table, err = schemaexecution.Describe(ctx, p.pb, headers)
	if err != nil {
		t.Fatal(err)
	}
	refused(planError(p.domain, ctx, intent(v2.ActionPurge, code.id, nil)), "plugin.model.core_protected", "fieldId")
	refused(pluginmodel.ProtectTable(ctx, p.pb, headers), "plugin.model.core_protected", "tableId")
	if _, err = schemaapi.New(p.pb).DeleteTable(ctx, headers, revision); err == nil {
		t.Fatal("deleting a model-bound table succeeded after release")
	}
	if rows := p.describe(t); len(rows.Protected) != 0 {
		t.Fatalf("registry still holds %d fields after release", len(rows.Protected))
	}
}

func fieldDraft(field v2.FieldDefinition, displayName string) v2.FieldDraft {
	return v2.FieldDraft{DisplayName: displayName, Help: field.Help, LogicalType: field.LogicalType, Value: field.Value, Constraints: field.Constraints, Storage: field.Storage, Display: field.Display}
}

func planError(domain *schemaFieldChangeDomain, ctx context.Context, intent v2.FieldChangeIntent) error {
	_, err := domain.Plan(ctx, intent)
	return err
}
