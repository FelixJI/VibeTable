package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

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
