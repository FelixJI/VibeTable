package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

func TestRuleUpgradePreflightsBothImagesWithoutBlockingDisjointTables(t *testing.T) {
	f := newTransactionFixture(t, "receipt-repair")
	ctx := t.Context()
	for entity, count := range map[string]int{"headers": 2999, "notes": 2001} {
		b := f.model.Bindings[entity]
		table, err := schemaexecution.Describe(ctx, f.pb, b.TableID)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.pb.RunInTransaction(func(tx core.App) error {
			for i := 0; i < count; i++ {
				values := dbx.Params{"id": fmt.Sprintf("%015d", i)}
				for key, fieldID := range b.Fields {
					field, _ := table.Field(fieldID)
					switch key {
					case "amount":
						values[field.Identity.PhysicalName] = "0.00"
					case "code", "name":
						values[field.Identity.PhysicalName] = fmt.Sprint(i)
					default:
						continue
					}
					if name := field.Value.Presence.PhysicalName; name != "" {
						values[name] = true
					}
				}
				if _, err := tx.DB().Insert(table.PhysicalName, values).Execute(); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	noteRule := transactionrules.Rule{ID: "notes", Kind: "unique", Entity: "notes", Fields: []string{"name"}}
	disjoint := transactionrules.Declaration{Contract: f.declaration.Contract, ModelID: f.declaration.ModelID, Version: 2, Rules: []transactionrules.Rule{noteRule}}
	if err := f.configureUpgradedRules(t, disjoint); err != nil {
		t.Fatal("disjoint registrations rejected", err)
	}
	b := f.model.Bindings["headers"]
	write := func(id, code string) error {
		_, err := f.kernel.Apply(ctx, mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: id, IdempotencyKey: id, TableID: b.TableID, SchemaRevision: f.model.Revisions[b.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{b.Fields["code"]: code, b.Fields["amount"]: "0.00"}}}})
		return err
	}
	if err := write("before", "before"); err != nil {
		t.Fatal(err)
	}
	overlap := disjoint
	overlap.Version = 3
	overlap.Rules = []transactionrules.Rule{{ID: "new_pair", Kind: "unique", Entity: "headers", Fields: []string{"code", "amount"}}}
	if err := f.configureUpgradedRules(t, overlap); err == nil || !strings.Contains(err.Error(), "transaction.read_budget_exceeded") {
		t.Fatalf("overlapping registrations exceeded the jointly triggered budget: %v", err)
	}
	all := f.declaration
	all.Version = 4
	all.Rules = append(all.Rules, noteRule)
	if err := f.configureUpgradedRules(t, all); err == nil || !strings.Contains(err.Error(), "transaction.read_budget_exceeded") {
		t.Fatalf("oversize configuration: %v", err)
	}
	registered, err := transactionrules.Load(ctx, f.pb)
	if err != nil || len(registered) != 2 {
		t.Fatalf("persisted rejected guard: %d %v", len(registered), err)
	}
	if err := write("after", "after"); err != nil {
		t.Fatal("old table no longer writable", err)
	}
	if err := write("duplicate", "after"); err == nil || !strings.Contains(err.Error(), "transaction.unique_violation") {
		t.Fatalf("lost old unique rule: %v", err)
	}
}

func (f *transactionFixture) configureUpgradedRules(t *testing.T, d transactionrules.Declaration) error {
	t.Helper()
	f.prepareUpgradedRules(t, d)
	_, err := f.call("transaction.configure", map[string]any{"declaration": d})
	return err
}

func (f *transactionFixture) prepareUpgradedRules(t *testing.T, d transactionrules.Declaration) {
	ctx := t.Context()
	t.Helper()
	store := pluginstore.New(f.pb, f.workspace)
	raw, err := store.GetInstallation(ctx, f.input.ProjectKey, f.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	obj["manifest"].(map[string]any)["permissions"].(map[string]any)["transactions"] = d
	prev := f.input.InstallationRevision
	f.input.InstallationRevision++
	obj["revision"] = f.input.InstallationRevision
	raw, err = json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveInstallation(ctx, raw, &prev); err != nil {
		t.Fatal(err)
	}
	p, err := modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.commit", map[string]string{"requestId": p.(modelPreview).RequestID}); err != nil {
		t.Fatal(err)
	}
}

func TestReciprocalRegistrationBudgetPreservesNativeInsertAndDelete(t *testing.T) {
	f := newTransactionFixture(t, "one-to-one")
	ctx := t.Context()
	for entity, count := range map[string]int{"headers": 3000, "lines": 2001} {
		binding := f.model.Bindings[entity]
		table, err := schemaexecution.Describe(ctx, f.pb, binding.TableID)
		if err != nil {
			t.Fatal(err)
		}
		field, _ := table.Field(binding.Fields["code"])
		if err := f.pb.RunInTransaction(func(tx core.App) error {
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("%015d", i)
				code := id
				if entity == "lines" {
					code = "ready"
				}
				values := dbx.Params{"id": id, field.Identity.PhysicalName: code}
				if presence := field.Value.Presence.PhysicalName; presence != "" {
					values[presence] = true
				}
				if _, err := tx.DB().Insert(table.PhysicalName, values).Execute(); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	state := transactionrules.Rule{ID: "status", Kind: "state", Entity: "lines", Field: "code", Transitions: map[string][]string{"ready": {}}}
	other := transactionrules.Declaration{Contract: f.declaration.Contract, ModelID: f.declaration.ModelID, Version: 2, Rules: []transactionrules.Rule{state}}
	if err := f.configureUpgradedRules(t, other); err != nil {
		t.Fatalf("unrelated field on the reciprocal table was counted: %v", err)
	}
	other.Version++
	state.ID = "protected_peer"
	state.Fields = []string{"peer"}
	other.Rules = append(other.Rules, state)
	if err := f.configureUpgradedRules(t, other); err == nil || !strings.Contains(err.Error(), "transaction.read_budget_exceeded") {
		t.Fatalf("reverse-field dependency was admitted: %v", err)
	}
	b := f.model.Bindings["headers"]
	req := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "native-insert", IdempotencyKey: "native-insert", TableID: b.TableID, SchemaRevision: f.model.Revisions[b.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{b.Fields["code"]: "free"}}}}
	receipt, err := f.kernel.Apply(ctx, req)
	if err != nil {
		t.Fatal("unrelated native insert blocked", err)
	}
	row := receipt.AffectedRows[0]
	req.RequestID, req.IdempotencyKey = "native-delete", "native-delete"
	req.Operations = []mutation.Operation{{Kind: mutation.OperationDelete, RecordID: &row.RecordID, ExpectedDigest: &row.Digest}}
	if _, err := f.kernel.Apply(ctx, req); err != nil {
		t.Fatal("unrelated native delete blocked", err)
	}

	// An older host could persist this declaration. Exercise both real entry points
	// against that historical state to pin the trigger set used by admission.
	installed, err := surfaceInstallation(ctx, f.pb, f.workspace, f.input)
	if err != nil {
		t.Fatal(err)
	}
	reg := transactionrules.Registration{WorkspaceID: f.workspace, PluginID: f.input.PluginID, InstallationID: f.input.InstallationID, Bindings: f.model.Bindings, Declaration: other}
	key := fmt.Sprintf("transaction:rules:%s:%s:%d", f.input.InstallationID, other.ModelID, other.Version)
	if err = f.pb.RunInTransaction(func(tx core.App) error {
		_, err := pluginstore.New(tx, f.workspace).RecordAuditInTransaction(ctx, modelAudit(f.input, installed, key, "transaction.rules", reg))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	table, err := schemaexecution.Describe(ctx, f.pb, b.TableID)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := f.pb.FindRecordById(table.PhysicalName, "000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := productrow.Digest(mutation.ProductRow(f.pb, table, existing))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []mutation.Operation{
		{Kind: mutation.OperationInsert, Values: map[string]any{b.Fields["code"]: "blocked"}},
		{Kind: mutation.OperationDelete, RecordID: &existing.Id, ExpectedDigest: &digest},
	} {
		req.RequestID = "legacy-" + string(op.Kind)
		req.IdempotencyKey = req.RequestID
		req.Operations = []mutation.Operation{op}
		if _, err = f.kernel.Apply(ctx, req); err == nil || !strings.Contains(err.Error(), "transaction.read_budget_exceeded") {
			t.Fatalf("legacy %s failed to select reciprocal guard: %v", op.Kind, err)
		}
	}
}

func TestRegistrationRejectsOversizedCombinedImageBytes(t *testing.T) {
	f := newTransactionFixture(t, "receipt-repair")
	b := f.model.Bindings["notes"]
	table, err := schemaexecution.Describe(t.Context(), f.pb, b.TableID)
	if err != nil {
		t.Fatal(err)
	}
	field, _ := table.Field(b.Fields["name"])
	data := dbx.Params{field.Identity.PhysicalName: strings.Repeat("x", transactionrules.MaxBytes/2), "id": "synthetic000001"}
	if name := field.Value.Presence.PhysicalName; name != "" {
		data[name] = true
	}
	if _, err := f.pb.DB().Insert(table.PhysicalName, data).Execute(); err != nil {
		t.Fatal(err)
	}
	d := transactionrules.Declaration{Contract: transactionrules.Contract, Version: 2, ModelID: f.declaration.ModelID, Rules: []transactionrules.Rule{{ID: "name", Kind: "unique", Entity: "notes", Fields: []string{"name"}}}}
	candidate := transactionrules.Registration{WorkspaceID: f.workspace, PluginID: f.input.PluginID, InstallationID: f.input.InstallationID, Bindings: f.model.Bindings, Declaration: d}
	if _, err := candidate.Read(t.Context(), f.pb); err != nil {
		t.Fatal("single image should fit", err)
	}
	if err := candidate.ValidateAdmission(t.Context(), f.pb, nil); err == nil || err.Error() != "transaction.read_budget_exceeded" {
		t.Fatalf("combined byte budget: %v", err)
	}
}

type transactionReadContextKey struct{}

func TestTransactionRulesReadOnlyBoundedDependencies(t *testing.T) {
	for _, scenario := range []string{"unrelated large note", "oversize dependency", "cumulative budget", "cancel query"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t)
			seed := f.submit(t, threeTablePlan())
			binding := f.model.Bindings["headers"]
			table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
			if err != nil {
				t.Fatal(err)
			}
			code, _ := table.Field(binding.Fields["code"])
			amount, _ := table.Field(binding.Fields["amount"])
			note := createSchemaProductField(t, f.pb, binding.TableID, v2.LogicalText, "大扩展备注", "read-budget-note")
			table, err = schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
			if err != nil {
				t.Fatal(err)
			}
			noteField, _ := table.Field(note.FieldID)
			noteName := noteField.Identity.PhysicalName
			if scenario == "unrelated large note" || scenario == "oversize dependency" {
				name := noteName
				if scenario == "oversize dependency" {
					name = code.Identity.PhysicalName
				}
				if _, err := f.pb.DB().Update(table.PhysicalName, dbx.Params{name: strings.Repeat("n", transactionrules.MaxBytes+1)}, dbx.HashExp{"id": seed.NewIDs["headers"]["h"]}).Execute(); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "cumulative budget" {
				for i := 0; i < 70; i++ {
					values := dbx.Params{"id": fmt.Sprintf("bounded%08d", i), code.Identity.PhysicalName: strings.Repeat("c", 16*1024), amount.Identity.PhysicalName: "10.00"}
					for _, field := range []v2.FieldDefinition{code, amount} {
						if field.Value.Presence.PhysicalName != "" {
							values[field.Value.Presence.PhysicalName] = true
						}
					}
					if _, err := f.pb.DB().Insert(table.PhysicalName, values).Execute(); err != nil {
						t.Fatal(err)
					}
				}
			}
			registrations, err := transactionrules.Load(context.Background(), f.pb)
			if err != nil || len(registrations) != 1 {
				t.Fatalf("registrations=%d %v", len(registrations), err)
			}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), transactionReadContextKey{}, "bounded-rule-read"))
			defer cancel()
			observed := false
			seen := map[*dbx.DB]bool{}
			for _, database := range []*dbx.DB{f.pb.ConcurrentDB().(*dbx.DB), f.pb.NonconcurrentDB().(*dbx.DB)} {
				if seen[database] {
					continue
				}
				seen[database] = true
				previous := database.QueryLogFunc
				database.QueryLogFunc = func(queryCtx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, queryErr error) {
					if previous != nil {
						previous(queryCtx, elapsed, statement, rows, queryErr)
					}
					if !strings.Contains(statement, "FROM `"+table.PhysicalName+"`") && !strings.Contains(statement, "FROM \""+table.PhysicalName+"\"") {
						return
					}
					observed = true
					if strings.Contains(statement, noteName) || strings.Contains(statement, ".*") || !strings.Contains(statement, "CASE WHEN") || !strings.Contains(statement, "octet_length") || !strings.Contains(statement, "LIMIT") {
						t.Errorf("unbounded or unrelated projection: %s", statement)
					}
					if queryCtx == nil || queryCtx.Value(transactionReadContextKey{}) != "bounded-rule-read" {
						t.Error("rule query lost its caller context")
					}
					if rows != nil {
						columns, err := rows.Columns()
						if err != nil {
							t.Error(err)
						}
						for _, column := range columns {
							if column == noteName {
								t.Error("large unrelated cell reached the driver projection")
							}
						}
					}
					if scenario == "cancel query" {
						cancel()
					}
				}
				t.Cleanup(func() { database.QueryLogFunc = previous })
			}
			images, err := registrations[0].Read(ctx, f.pb)
			if !observed {
				t.Fatal("no authoritative dependency SELECT observed")
			}
			switch scenario {
			case "unrelated large note":
				if err != nil || images["headers"][seed.NewIDs["headers"]["h"]]["code"] != "SYN-1" {
					t.Fatalf("unrelated note affected rule read: %v", err)
				}
			case "cancel query":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel=%v", err)
				}
			default:
				if err == nil || err.Error() != "transaction.read_budget_exceeded" {
					t.Fatalf("budget=%v", err)
				}
			}
		})
	}
}

func TestTransactionOneDeniedEntityLeavesAllThreeTablesEmpty(t *testing.T) {
	f := newTransactionFixture(t, "deny-create")
	_, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
	var denied *query.ProductError
	if !errors.As(err, &denied) || denied.Code != "plugin_write_denied" {
		t.Fatalf("third entity permission failure=%v", err)
	}
	for _, binding := range f.model.Bindings {
		table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.pb.FindAllRecords(table.PhysicalName)
		if err != nil || len(rows) != 0 {
			t.Fatalf("permission rejection left rows: %d %v", len(rows), err)
		}
	}
}

func TestTransactionNativeStateFreezeStillAllowsExtensionNotes(t *testing.T) {
	f := newTransactionFixture(t, "state")
	plan := threeTablePlan()
	plan.Operations[0].Values["status"] = "posted"
	seed := f.submit(t, plan)
	binding := f.model.Bindings["headers"]
	note := createSchemaProductField(t, f.pb, binding.TableID, v2.LogicalText, "普通扩展备注", "frozen-note")
	table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
	if err != nil {
		t.Fatal(err)
	}
	noteField, _ := table.Field(note.FieldID)
	id := seed.NewIDs["headers"]["h"]
	record, err := f.pb.FindRecordById(table.PhysicalName, id)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := productrow.Digest(mutation.ProductRow(f.pb, table, record))
	if err != nil {
		t.Fatal(err)
	}
	request := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "native-frozen", IdempotencyKey: "native-frozen", TableID: binding.TableID, SchemaRevision: table.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &id, ExpectedDigest: &digest, Values: map[string]any{binding.Fields["code"]: "SYN-2"}}}}
	_, err = f.kernel.Apply(context.Background(), request)
	var frozen *mutation.ProductError
	if !errors.As(err, &frozen) || frozen.Code != "transaction.state_frozen" {
		t.Fatalf("native frozen edit=%v", err)
	}
	request.RequestID, request.IdempotencyKey = "native-note", "native-note"
	request.Operations[0].Values = map[string]any{note.FieldID: "允许填写备注"}
	if _, err := f.kernel.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	record, err = f.pb.FindRecordById(table.PhysicalName, id)
	if err != nil || record.GetString(noteField.Identity.PhysicalName) != "允许填写备注" {
		t.Fatalf("extension note was frozen: %v", err)
	}
}
