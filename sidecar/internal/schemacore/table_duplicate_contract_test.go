package schemacore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func assertCopyRolledBack(t *testing.T, app core.App, before map[string]int64, operation string) {
	t.Helper()
	for name, want := range before {
		count, err := app.CountRecords(name)
		if err != nil || count != want {
			t.Fatalf("copy leaked %s records: count=%d want=%d error=%v", name, count, want, err)
		}
	}
	_, physical := tableIdentities(operation)
	if _, err := app.FindCollectionByNameOrId(physical); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed copy collection lookup=%v want no rows", err)
	}
}

func TestTableDuplicateReplayRequiresMatchingDurableIntent(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, _ := NewTableLifecycle(app)
	ctx := context.Background()
	intent := v2.TableCreateIntent{DisplayName: "重放绑定", OperationID: "copy-replay-binding", Actor: v2.Actor{ID: "test", Kind: "user"}}
	if _, err := lifecycle.Create(ctx, intent); err != nil {
		t.Fatal(err)
	}
	before := copyRecordCounts(t, app)
	copyIntent := intent
	copyIntent.SourceTableID = "tbl_another_source"
	if _, found, err := lifecycle.FindReplay(copyIntent); err == nil || found {
		t.Fatalf("plain create replayed as copy: found=%t error=%v", found, err)
	}
	for name, want := range before {
		if count, err := app.CountRecords(name); err != nil || count != want {
			t.Fatalf("conflicting replay changed %s: count=%d want=%d error=%v", name, count, want, err)
		}
	}
	audit, err := app.FindFirstRecordByFilter("vibetable_schema_audit", "operation_id={:operation}", dbx.Params{"operation": intent.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(audit); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Create(ctx, intent); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing durable intent was accepted: %v", err)
	}
}

func TestTableDuplicateRollsBackRejectedStorageWrites(t *testing.T) {
	for _, scenario := range []struct {
		name, collection string
		update           bool
	}{
		{"field-metadata", "vibetable_fields", false},
		{"table-metadata", "vibetable_tables", true},
		{"formula-metadata", "vibetable_formulas", false},
		{"input-revision", "vibetable_computation_dependencies", true},
		{"row-audit", "vibetable_audit_events", false},
		{"audit-outbox", "vibetable_audit_outbox", false},
		{"copied-view", "vibetable_presets", false},
		{"automatic-number-state", "vibetable_fields", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			app := commitProjectionApp(t)
			lifecycle, _ := NewTableLifecycle(app)
			ctx := context.Background()
			actor := v2.Actor{ID: "test", Kind: "user"}
			source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "事务来源", OperationID: "storage-source", Actor: actor})
			if err != nil {
				t.Fatal(err)
			}
			metadata, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
			collection, _ := app.FindCollectionByNameOrId(metadata.GetString("collection_id"))
			amount, relation, total := copiedField(t, v2.LogicalNumber), copiedField(t, v2.LogicalRelation), copiedField(t, v2.LogicalFormula)
			relation.Relation = &v2.RelationSpec{TargetTableID: source.TableID, Cardinality: "many", DeletePolicy: "restrict", DisplayField: amount.Identity.FieldID}
			total.Formula = &v2.FormulaSpec{Language: "cel-v1", Source: fmt.Sprintf("relationSum(%s, %q)", relation.Identity.PhysicalName, amount.Identity.PhysicalName), ResultType: v2.LogicalNumber}
			for _, field := range []v2.FieldDefinition{amount, relation, total} {
				if err := fieldchange.InstallCopiedField(app, collection, source.TableID, field); err != nil {
					t.Fatal(err)
				}
			}
			if err := schemaapi.New(app).SyncComputedMetadataForTable(ctx, source.TableID); err != nil {
				t.Fatal(err)
			}
			row := core.NewRecord(collection)
			row.Set(amount.Identity.PhysicalName, 7)
			row.Set(amount.Value.Presence.PhysicalName, true)
			if err := app.Save(row); err != nil {
				t.Fatal(err)
			}
			row.Set(relation.Identity.PhysicalName, []string{row.Id})
			row.Set(relation.Value.Presence.PhysicalName, true)
			if err := app.Save(row); err != nil {
				t.Fatal(err)
			}
			if err := materializeCopiedFormulas(ctx, app, source.TableID, collection); err != nil {
				t.Fatal(err)
			}
			presets, _ := app.FindCollectionByNameOrId("vibetable_presets")
			preset := core.NewRecord(presets)
			preset.Set("logical_id", "source-preset")
			preset.Set("payload_json", types.JSONRaw([]byte(`{"scope":"`+source.TableID+`","name":"source view"}`)))
			if err := app.Save(preset); err != nil {
				t.Fatal(err)
			}
			intent := v2.TableCreateIntent{DisplayName: "事务副本", OperationID: "storage-copy", SourceTableID: source.TableID, Actor: actor}
			if scenario.name == "automatic-number-state" {
				number := copiedField(t, v2.LogicalAutoNumber)
				number.AutoNumber = &v2.AutoNumberSpec{Prefix: "ID-", Start: 1, Width: 3}
				if err := fieldchange.InstallCopiedField(app, collection, source.TableID, number); err != nil {
					t.Fatal(err)
				}
				no := false
				intent.CopyRecords = &no
			}
			before := copyRecordCounts(t, app)
			injected := errors.New("extension rejected copied storage write")
			observed := false
			veto := func(*core.RecordEvent) error { observed = true; return injected }
			if scenario.update {
				app.OnRecordUpdate(scenario.collection).BindFunc(veto)
			} else {
				app.OnRecordCreate(scenario.collection).BindFunc(veto)
			}
			_, err = lifecycle.Create(ctx, intent)
			if err == nil || !observed {
				t.Fatalf("write veto observed=%t error=%v", observed, err)
			}
			assertCopyRolledBack(t, app, before, intent.OperationID)
		})
	}
}

func copyRecordCounts(t *testing.T, app core.App) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for _, name := range []string{"vibetable_tables", "vibetable_fields", "vibetable_formulas", "vibetable_computation_dependencies", "vibetable_presets", "vibetable_content_profiles", "vibetable_record_document_links", "vibetable_audit_events", "vibetable_audit_outbox", "vibetable_schema_audit"} {
		count, err := app.CountRecords(name)
		if err != nil {
			t.Fatal(err)
		}
		counts[name] = count
	}
	return counts
}

func TestTableDuplicateRejectsUnsafeSourceWithoutPartialState(t *testing.T) {
	for _, scenario := range []string{"missing", "non-base", "legacy", "invalid-definition", "reciprocal-external", "self-default", "collection-formula", "pending-formula", "unmanaged-provider", "dangling-self-row", "invalid-archive", "linked-document", "invalid-view"} {
		t.Run(scenario, func(t *testing.T) {
			app := commitProjectionApp(t)
			lifecycle, _ := NewTableLifecycle(app)
			ctx := context.Background()
			actor := v2.Actor{ID: "test", Kind: "user"}
			source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "拒绝来源", OperationID: "rejected-source", Actor: actor})
			if err != nil {
				t.Fatal(err)
			}
			metadata, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
			collection, _ := app.FindCollectionByNameOrId(metadata.GetString("collection_id"))
			flag := copiedField(t, v2.LogicalBool)
			if err := fieldchange.InstallCopiedField(app, collection, source.TableID, flag); err != nil {
				t.Fatal(err)
			}
			row := core.NewRecord(collection)
			row.Set(flag.Identity.PhysicalName, true)
			row.Set(flag.Value.Presence.PhysicalName, true)
			if err := app.Save(row); err != nil {
				t.Fatal(err)
			}
			intent := v2.TableCreateIntent{DisplayName: "应拒绝副本", OperationID: "rejected-copy", SourceTableID: source.TableID, Actor: actor}
			wantCode, wantMessage := "schema.table.duplicate_unsupported", ""
			switch scenario {
			case "missing":
				intent.SourceTableID = "tbl_missing"
				wantCode = "schema.table.not_found"
			case "non-base":
				metadata.Set("kind", "view")
				if err := app.Save(metadata); err != nil {
					t.Fatal(err)
				}
				wantMessage = "Only base tables"
			case "legacy", "invalid-definition":
				field, _ := app.FindFirstRecordByFilter("vibetable_fields", "field_id={:field}", dbx.Params{"field": flag.Identity.FieldID})
				field.Set("definition_v2_json", types.JSONRaw([]byte(`{"identity":42}`)))
				if scenario == "invalid-definition" {
					wantCode = ""
				} else {
					wantCode = ""
					wantMessage = "field.contract.invalid"
				}
				if err := app.Save(field); err != nil {
					t.Fatal(err)
				}
				if scenario == "legacy" {
					if _, err := app.DB().NewQuery("UPDATE vibetable_fields SET definition_v2_json=NULL WHERE id={:id}").Bind(dbx.Params{"id": field.Id}).Execute(); err != nil {
						t.Fatal(err)
					}
				}
			case "reciprocal-external", "self-default", "dangling-self-row":
				field := copiedField(t, v2.LogicalRelation)
				field.Relation = &v2.RelationSpec{TargetTableID: source.TableID, Cardinality: "one", DeletePolicy: "restrict", DisplayField: flag.Identity.FieldID}
				if scenario == "reciprocal-external" {
					external, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "外部目标", OperationID: "external-source", Actor: actor})
					if err != nil {
						t.Fatal(err)
					}
					field.Relation.TargetTableID = external.TableID
					field.Relation.PairID = "relp_01234567890123456789"
					field.Relation.ReciprocalFieldID = flag.Identity.FieldID
					wantMessage = "reciprocal relations"
				} else if scenario == "self-default" {
					wantMessage = "default records"
					wantCode = ""
				} else {
					wantMessage = "missing source row"
				}
				if err := fieldchange.InstallCopiedField(app, collection, source.TableID, field); err != nil {
					t.Fatal(err)
				}
				if scenario == "self-default" {
					field.Value.Default.Enabled, field.Value.Default.Value = true, row.Id
					raw, _ := json.Marshal(field)
					if _, err := app.DB().NewQuery("UPDATE vibetable_fields SET definition_v2_json={:definition} WHERE field_id={:field}").Bind(dbx.Params{"definition": string(raw), "field": field.Identity.FieldID}).Execute(); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "dangling-self-row" {
					if _, err := app.DB().Update(collection.Name, dbx.Params{field.Identity.PhysicalName: "missingrecord01"}, dbx.HashExp{"id": row.Id}).Execute(); err != nil {
						t.Fatal(err)
					}
				}
			case "collection-formula", "pending-formula":
				field := copiedField(t, v2.LogicalFormula)
				field.Formula = &v2.FormulaSpec{Language: "cel-v1", Source: "1.0", ResultType: v2.LogicalNumber}
				if scenario == "collection-formula" {
					field.Formula.Source = `SUM(TABLE("tbl_other").amount)`
					wantMessage = "Collection formulas"
				} else {
					wantMessage = "Wait for formula computation"
				}
				if err := fieldchange.InstallCopiedField(app, collection, source.TableID, field); err != nil {
					t.Fatal(err)
				}
			case "unmanaged-provider":
				collection.Fields.Add(&core.TextField{Name: "unmanaged"})
				if err := app.Save(collection); err != nil {
					t.Fatal(err)
				}
				wantMessage = "Unmanaged provider fields"
			case "invalid-archive":
				metadata.Set("archive_policy", "[]")
				if err := app.Save(metadata); err != nil {
					t.Fatal(err)
				}
				wantCode = ""
			case "linked-document", "invalid-view":
				name, payload := "vibetable_record_document_links", `{"tableId":"`+source.TableID+`"}`
				wantMessage = "linked file documents"
				if scenario == "invalid-view" {
					name, payload, wantCode = "vibetable_presets", `[]`, ""
				}
				definition, _ := app.FindCollectionByNameOrId(name)
				entry := core.NewRecord(definition)
				entry.Set("logical_id", "source-entry")
				entry.Set("payload_json", types.JSONRaw([]byte(payload)))
				if err := app.Save(entry); err != nil {
					t.Fatal(err)
				}
			}
			before := copyRecordCounts(t, app)
			_, err = lifecycle.Create(ctx, intent)
			if err == nil {
				t.Fatal("unsafe copy unexpectedly succeeded")
			}
			if wantCode != "" {
				var productErr *schemaerror.ProductError
				if !errors.As(err, &productErr) || productErr.Code != wantCode || !strings.Contains(productErr.Message, wantMessage) {
					t.Fatalf("copy error=%v want code=%s message containing=%q", err, wantCode, wantMessage)
				}
			} else if scenario == "legacy" && !strings.Contains(err.Error(), wantMessage) {
				t.Fatalf("legacy metadata error=%v want=%s", err, wantMessage)
			}
			assertCopyRolledBack(t, app, before, intent.OperationID)
			stored, err := app.FindRecordById(collection, row.Id)
			if err != nil || !stored.GetBool(flag.Identity.PhysicalName) {
				t.Fatalf("failed copy changed source row: %v", err)
			}
		})
	}
}

func TestTableDuplicateRemapsPairedRelationsArchiveAndContentProfile(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, _ := NewTableLifecycle(app)
	ctx := context.Background()
	actor := v2.Actor{ID: "test", Kind: "user"}
	source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "成对来源", OperationID: "paired-source", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
	collection, _ := app.FindCollectionByNameOrId(metadata.GetString("collection_id"))
	flag, left, right := copiedField(t, v2.LogicalBool), copiedField(t, v2.LogicalRelation), copiedField(t, v2.LogicalRelation)
	pair, err := v2.NewIdentityAllocator(nil).AllocateRelationPair(ctx)
	if err != nil {
		t.Fatal(err)
	}
	left.Relation = &v2.RelationSpec{TargetTableID: source.TableID, Cardinality: "one", DeletePolicy: "restrict", DisplayField: flag.Identity.FieldID, PairID: pair, ReciprocalFieldID: right.Identity.FieldID}
	right.Relation = &v2.RelationSpec{TargetTableID: source.TableID, Cardinality: "many", DeletePolicy: "restrict", DisplayField: flag.Identity.FieldID, PairID: pair, ReciprocalFieldID: left.Identity.FieldID}
	for _, field := range []v2.FieldDefinition{flag, left, right} {
		if err := fieldchange.InstallCopiedField(app, collection, source.TableID, field); err != nil {
			t.Fatal(err)
		}
	}
	metadata.Set("primary_display_field_id", flag.Identity.FieldID)
	metadata.Set("archive_policy", `{"mode":"field","fieldId":"`+flag.Identity.FieldID+`","archivedValue":true}`)
	if err := app.Save(metadata); err != nil {
		t.Fatal(err)
	}
	profiles, _ := app.FindCollectionByNameOrId("vibetable_content_profiles")
	profile := core.NewRecord(profiles)
	profile.Set("logical_id", source.TableID)
	profile.Set("payload_json", types.JSONRaw([]byte(`{"tableId":"`+source.TableID+`","titleFieldId":"`+flag.Identity.FieldID+`","searchableFieldIds":["`+flag.Identity.FieldID+`"],"view":{"sorts":[{"field":"`+flag.Identity.FieldID+`","direction":"asc"}]}}`)))
	if err := app.Save(profile); err != nil {
		t.Fatal(err)
	}
	copy, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "成对副本", OperationID: "paired-copy", SourceTableID: source.TableID, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := fieldchange.NewCatalog(app).Fields(ctx, copy.TableID, false)
	if err != nil {
		t.Fatal(err)
	}
	byPhysical := map[string]v2.FieldDefinition{}
	for _, field := range fields {
		byPhysical[field.Identity.PhysicalName] = field
	}
	copyFlag, copyLeft, copyRight := byPhysical[flag.Identity.PhysicalName], byPhysical[left.Identity.PhysicalName], byPhysical[right.Identity.PhysicalName]
	if copyLeft.Relation.PairID == pair || copyLeft.Relation.PairID != copyRight.Relation.PairID || copyLeft.Relation.ReciprocalFieldID != copyRight.Identity.FieldID || copyRight.Relation.ReciprocalFieldID != copyLeft.Identity.FieldID {
		t.Fatalf("copied reciprocal identities=%#v %#v", copyLeft.Relation, copyRight.Relation)
	}
	metadata, _ = app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": copy.TableID})
	var policy v2.ArchivePolicy
	if err := json.Unmarshal([]byte(metadata.GetString("archive_policy")), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.FieldID == nil || *policy.FieldID != copyFlag.Identity.FieldID || metadata.GetString("primary_display_field_id") != copyFlag.Identity.FieldID {
		t.Fatalf("copied archive/display metadata=%s %s", metadata.GetString("archive_policy"), metadata.GetString("primary_display_field_id"))
	}
	copiedProfile, err := app.FindFirstRecordByFilter(profiles.Name, "logical_id={:table}", dbx.Params{"table": copy.TableID})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		TableID string   `json:"tableId"`
		Title   string   `json:"titleFieldId"`
		Search  []string `json:"searchableFieldIds"`
		View    struct {
			Sorts []struct {
				Field string `json:"field"`
			} `json:"sorts"`
		} `json:"view"`
	}
	if err := json.Unmarshal([]byte(copiedProfile.GetString("payload_json")), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TableID != copy.TableID || payload.Title != copyFlag.Identity.FieldID || len(payload.Search) != 1 || payload.Search[0] != copyFlag.Identity.FieldID || len(payload.View.Sorts) != 1 || payload.View.Sorts[0].Field != copyFlag.Identity.FieldID {
		t.Fatalf("copied profile=%s", copiedProfile.GetString("payload_json"))
	}
	if original, err := app.FindRecordById(profiles, profile.Id); err != nil || original.GetString("payload_json") != profile.GetString("payload_json") {
		t.Fatalf("source profile changed: %v", err)
	}
}
