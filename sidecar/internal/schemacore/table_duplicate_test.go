package schemacore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/autonumber"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func copiedField(t *testing.T, kind v2.LogicalType) v2.FieldDefinition {
	t.Helper()
	defaults, err := v2.RecommendedDefaults(kind)
	if err != nil {
		t.Fatal(err)
	}
	allocator := v2.NewIdentityAllocator(nil)
	identity, err := allocator.AllocateField(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	value := defaults.Value
	if value.Presence.Mode == v2.PresenceCompanion {
		value.Presence, err = allocator.AllocatePresence(context.Background(), identity.PhysicalName)
		if err != nil {
			t.Fatal(err)
		}
	}
	return v2.FieldDefinition{Contract: v2.Contract, Identity: identity, DisplayName: "字段", LogicalType: kind,
		Lifecycle: v2.Lifecycle{State: v2.LifecycleActive}, Value: value,
		Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display}
}

func TestTableDuplicatePreservesAutomaticDatesAndRematerializesFormulaVersions(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, _ := NewTableLifecycle(app)
	ctx := context.Background()
	actor := v2.Actor{ID: "test", Kind: "user"}
	source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "计算来源", OperationID: "formula-copy-source", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
	collection, _ := app.FindCollectionByNameOrId(metadata.GetString("collection_id"))
	date := copiedField(t, v2.LogicalAutoDate)
	date.AutoDate = &v2.AutoDateSpec{Role: "createdAt"}
	if err := fieldchange.InstallCopiedField(app, collection, source.TableID, date); err != nil {
		t.Fatal(err)
	}
	formula := copiedField(t, v2.LogicalFormula)
	formula.Formula = &v2.FormulaSpec{Language: "cel-v1", Source: "1.0 + 2.0", ResultType: v2.LogicalNumber}
	if err := fieldchange.InstallCopiedField(app, collection, source.TableID, formula); err != nil {
		t.Fatal(err)
	}
	if err := schemaapi.New(app).SyncComputedMetadataForTable(ctx, source.TableID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery("UPDATE vibetable_formulas SET status='ready', version=7 WHERE table_id={:table}").Bind(dbx.Params{"table": source.TableID}).Execute(); err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	row, err = app.FindRecordById(collection, row.Id)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "计算副本", OperationID: "formula-copy-target", SourceTableID: source.TableID, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := fieldchange.NewCatalog(app).Fields(ctx, copy.TableID, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": copy.TableID})
	rows, err := app.FindAllRecords(meta.GetString("collection_id"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%#v %v", rows, err)
	}
	if rows[0].GetDateTime(date.Identity.PhysicalName) != row.GetDateTime(date.Identity.PhysicalName) {
		t.Fatal("automatic creation date changed")
	}
	var computed v2.FieldDefinition
	for _, field := range fields {
		if field.Formula != nil {
			computed = field
		}
	}
	envelope, ok := relatedcomputation.Decode(rows[0].GetRaw(computed.Identity.PhysicalName))
	if !ok {
		t.Fatal("computed envelope absent")
	}
	expectation, err := relatedcomputation.ExpectationFor(ctx, app, copy.TableID, fields, computed.Identity.FieldID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !envelope.Fresh(expectation) || envelope.Value != float64(3) || envelope.Version.DefinitionVersion != 1 {
		t.Fatalf("envelope=%#v expectation=%#v", envelope, expectation)
	}
}

func TestTableDuplicateCopiesRowsViewsAndSelfRelationsWithIndependentIdentities(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, _ := NewTableLifecycle(app)
	ctx := context.Background()
	actor := v2.Actor{ID: "test", Kind: "user"}
	source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "来源", OperationID: "copy-source", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(metadata.GetString("collection_id"))
	if err != nil {
		t.Fatal(err)
	}
	flag := copiedField(t, v2.LogicalBool)
	if err := fieldchange.InstallCopiedField(app, collection, source.TableID, flag); err != nil {
		t.Fatal(err)
	}
	relation := copiedField(t, v2.LogicalRelation)
	relation.Relation = &v2.RelationSpec{TargetTableID: source.TableID, Cardinality: "one", DeletePolicy: "restrict", DisplayField: flag.Identity.FieldID}
	if err := fieldchange.InstallCopiedField(app, collection, source.TableID, relation); err != nil {
		t.Fatal(err)
	}
	first := core.NewRecord(collection)
	first.Set(flag.Identity.PhysicalName, false)
	first.Set(flag.Value.Presence.PhysicalName, true)
	if err := app.Save(first); err != nil {
		t.Fatal(err)
	}
	second := core.NewRecord(collection)
	second.Set(relation.Identity.PhysicalName, first.Id)
	if err := app.Save(second); err != nil {
		t.Fatal(err)
	}
	presets, _ := app.FindCollectionByNameOrId("vibetable_presets")
	preset := core.NewRecord(presets)
	preset.Set("logical_id", "source-view")
	raw, _ := json.Marshal(map[string]any{"scope": source.TableID, "name": "视图", "view": map[string]any{"visibleFields": []string{flag.Identity.FieldID}, "filters": []any{map[string]any{"field": flag.Identity.FieldID, "operator": "eq", "value": flag.Identity.FieldID}}}})
	preset.Set("payload_json", types.JSONRaw(raw))
	if err := app.Save(preset); err != nil {
		t.Fatal(err)
	}
	intent := v2.TableCreateIntent{DisplayName: "副本", OperationID: "copy-target", SourceTableID: source.TableID, Actor: actor}
	copyTime := time.Now().UTC().Truncate(time.Millisecond)
	lifecycle.clock = func() time.Time { return copyTime }
	copy, err := lifecycle.Create(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if copy.TableID == source.TableID {
		t.Fatal("table identity was reused")
	}
	fields, err := fieldchange.NewCatalog(app).Fields(ctx, copy.TableID, true)
	if err != nil || len(fields) != 2 {
		t.Fatalf("fields=%#v err=%v", fields, err)
	}
	var copyFlag, copyRelation v2.FieldDefinition
	for _, field := range fields {
		if field.Relation != nil {
			copyRelation = field
		} else {
			copyFlag = field
		}
	}
	if copyFlag.Identity.FieldID == flag.Identity.FieldID || copyFlag.Identity.ProviderFieldID == flag.Identity.ProviderFieldID {
		t.Fatal("field identity was reused")
	}
	if copyRelation.Relation.TargetTableID != copy.TableID || copyRelation.Relation.DisplayField != copyFlag.Identity.FieldID {
		t.Fatalf("self relation=%#v", copyRelation.Relation)
	}
	physical, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": copy.TableID})
	rows, err := app.FindAllRecords(physical.GetString("collection_id"))
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	var copiedFirst, copiedSecond *core.Record
	for _, row := range rows {
		if row.Id == first.Id || row.Id == second.Id {
			t.Fatal("row identity was reused")
		}
		if row.GetBool(copyFlag.Value.Presence.PhysicalName) {
			copiedFirst = row
		} else {
			copiedSecond = row
		}
	}
	if copiedFirst == nil || copiedSecond == nil || copiedFirst.GetBool(copyFlag.Identity.PhysicalName) || copiedSecond.GetString(copyRelation.Identity.PhysicalName) != copiedFirst.Id {
		t.Fatalf("copied values=%#v", rows)
	}
	creations, err := autonumber.CreationTimes(ctx, app, copy.TableID)
	if err != nil || len(creations) != 2 {
		t.Fatalf("copy creation audit=%#v %v", creations, err)
	}
	for _, created := range creations {
		if !created.Equal(copyTime) {
			t.Fatalf("copy audit time=%s want=%s", created, copyTime)
		}
	}
	allPresets, _ := app.FindAllRecords("vibetable_presets")
	if len(allPresets) != 2 {
		t.Fatalf("presets=%d", len(allPresets))
	}
	for _, view := range allPresets {
		if view.Id == preset.Id {
			continue
		}
		if view.GetString("logical_id") == "source-view" {
			t.Fatal("view identity reused")
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(view.GetString("payload_json")), &payload); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(payload)
		var actual struct {
			Scope string `json:"scope"`
			View  struct {
				Visible []string `json:"visibleFields"`
				Filters []struct {
					Field string `json:"field"`
					Value string `json:"value"`
				} `json:"filters"`
			} `json:"view"`
		}
		if err := json.Unmarshal(encoded, &actual); err != nil {
			t.Fatal(err)
		}
		if actual.Scope != copy.TableID || actual.View.Visible[0] != copyFlag.Identity.FieldID || actual.View.Filters[0].Field != copyFlag.Identity.FieldID || actual.View.Filters[0].Value != flag.Identity.FieldID {
			t.Fatalf("view=%s", encoded)
		}
	}
	if replay, err := lifecycle.Create(ctx, intent); err != nil || replay != copy {
		t.Fatalf("replay=%#v %v", replay, err)
	}
	intent.SourceTableID = "another-source"
	_, err = lifecycle.Create(ctx, intent)
	var conflict *schemaerror.ProductError
	if !errors.As(err, &conflict) || conflict.Code != "schema.table.operation_conflict" {
		t.Fatalf("binding=%v", err)
	}
	intent.SourceTableID = source.TableID
	intent.OperationID = "copy-duplicate-name"
	_, err = lifecycle.Create(ctx, intent)
	if !errors.As(err, &conflict) || conflict.Code != "schema.table.display_name_duplicate" {
		t.Fatalf("duplicate name=%v", err)
	}
	intent.DisplayName = "应回滚"
	intent.OperationID = "copy-rollback"
	beforeAudit, _ := app.CountRecords("vibetable_audit_events")
	beforeOutbox, _ := app.CountRecords("vibetable_audit_outbox")
	boom := errors.New("projection failed")
	_, err = lifecycle.CreateWithCommit(ctx, intent, func(core.App, v2.TableCreateReceipt) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("rollback=%v", err)
	}
	count, _ := app.CountRecords("vibetable_tables")
	if count != 2 {
		t.Fatalf("partial table remained: %d", count)
	}
	afterAudit, _ := app.CountRecords("vibetable_audit_events")
	afterOutbox, _ := app.CountRecords("vibetable_audit_outbox")
	if afterAudit != beforeAudit || afterOutbox != beforeOutbox {
		t.Fatal("copy rollback leaked row history or audit outbox")
	}
	if _, err := lifecycle.Create(ctx, intent); err != nil {
		t.Fatalf("retry after rollback=%v", err)
	}
}

func TestTableDuplicateRejectsUnsupportedFieldsWithoutPartialTable(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, _ := NewTableLifecycle(app)
	ctx := context.Background()
	actor := v2.Actor{ID: "test", Kind: "user"}
	source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "附件", OperationID: "attachment-source", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
	collection, _ := app.FindCollectionByNameOrId(metadata.GetString("collection_id"))
	field := copiedField(t, v2.LogicalFile)
	field.File = &v2.FileSpec{MaxFiles: 1, MaxBytesPerFile: 1024, AllowedMIMETypes: []string{}, Thumbs: []string{}}
	if err := fieldchange.InstallCopiedField(app, collection, source.TableID, field); err != nil {
		t.Fatal(err)
	}
	_, err = lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "不完整副本", OperationID: "attachment-copy", SourceTableID: source.TableID, Actor: actor})
	var failure *schemaerror.ProductError
	if !errors.As(err, &failure) || failure.Code != "schema.table.duplicate_unsupported" {
		t.Fatalf("unsupported=%v", err)
	}
	count, _ := app.CountRecords("vibetable_tables")
	if count != 1 {
		t.Fatalf("partial tables=%d", count)
	}
	_, physical := tableIdentities("attachment-copy")
	if _, err := app.FindCollectionByNameOrId(physical); err == nil {
		t.Fatal("partial collection remained")
	}
	no := false
	if _, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "附件结构", OperationID: "attachment-structure", SourceTableID: source.TableID, Actor: actor, CopyRecords: &no, CopyViews: &no}); err != nil {
		t.Fatalf("structure-only attachment: %v", err)
	}
}

func TestTableDuplicateCopyScopesAndReplayBinding(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, _ := NewTableLifecycle(app)
	ctx := context.Background()
	actor := v2.Actor{ID: "test", Kind: "user"}
	source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "范围来源", OperationID: "scope-source", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
	collection, _ := app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	field := copiedField(t, v2.LogicalBool)
	if err := fieldchange.InstallCopiedField(app, collection, source.TableID, field); err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Set(field.Identity.PhysicalName, true)
	row.Set(field.Value.Presence.PhysicalName, true)
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	presets, _ := app.FindCollectionByNameOrId("vibetable_presets")
	preset := core.NewRecord(presets)
	preset.Set("logical_id", "scope-view")
	raw, _ := json.Marshal(map[string]any{"scope": source.TableID, "name": "视图", "view": map[string]any{}})
	preset.Set("payload_json", types.JSONRaw(raw))
	if err := app.Save(preset); err != nil {
		t.Fatal(err)
	}
	for _, records := range []bool{false, true} {
		for _, views := range []bool{false, true} {
			name := fmt.Sprintf("范围-%t-%t", records, views)
			intent := v2.TableCreateIntent{DisplayName: name, OperationID: name, Actor: actor, SourceTableID: source.TableID, CopyRecords: &records, CopyViews: &views}
			copy, err := lifecycle.Create(ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			fields, err := fieldchange.NewCatalog(app).Fields(ctx, copy.TableID, true)
			if err != nil || len(fields) != 1 {
				t.Fatalf("structure=%#v %v", fields, err)
			}
			meta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": copy.TableID})
			count, _ := app.CountRecords(meta.GetString("collection_id"))
			wantRows := int64(0)
			if records {
				wantRows = 1
			}
			if count != wantRows {
				t.Fatalf("%s rows=%d", name, count)
			}
			audit, _ := app.CountRecords("vibetable_audit_events", dbx.HashExp{"table_id": copy.TableID})
			if audit != wantRows {
				t.Fatalf("copy row audit=%d", audit)
			}
			viewCount := 0
			all, _ := app.FindAllRecords("vibetable_presets")
			for _, entry := range all {
				var payload map[string]any
				if err := json.Unmarshal([]byte(entry.GetString("payload_json")), &payload); err != nil {
					t.Fatal(err)
				}
				if payload["scope"] == copy.TableID {
					viewCount++
				}
			}
			if (viewCount == 1) != views {
				t.Fatalf("%s views=%d", name, viewCount)
			}
			if replay, err := lifecycle.Create(ctx, intent); err != nil || replay != copy {
				t.Fatalf("scope replay=%#v %v", replay, err)
			}
			changed := !records
			intent.CopyRecords = &changed
			_, err = lifecycle.Create(ctx, intent)
			var conflict *schemaerror.ProductError
			if !errors.As(err, &conflict) || conflict.Code != "schema.table.operation_conflict" {
				t.Fatalf("scope conflict=%v", err)
			}
			intent.CopyRecords = &records
			changed = !views
			intent.CopyViews = &changed
			_, err = lifecycle.Create(ctx, intent)
			if !errors.As(err, &conflict) {
				t.Fatalf("view scope conflict=%v", err)
			}
		}
	}
}

func TestTableDuplicateStructureInitializesAutomaticNumberIndependently(t *testing.T) {
	app := commitProjectionApp(t)
	lifecycle, _ := NewTableLifecycle(app)
	ctx := context.Background()
	actor := v2.Actor{ID: "test", Kind: "user"}
	source, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "编号来源", OperationID: "number-source", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": source.TableID})
	collection, _ := app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	field := copiedField(t, v2.LogicalAutoNumber)
	field.AutoNumber = &v2.AutoNumberSpec{Prefix: "ID-", Start: 7, Width: 3}
	if err := fieldchange.InstallCopiedField(app, collection, source.TableID, field); err != nil {
		t.Fatal(err)
	}
	no := false
	copy, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "编号结构", OperationID: "number-copy", Actor: actor, SourceTableID: source.TableID, CopyRecords: &no})
	if err != nil {
		t.Fatal(err)
	}
	numbers, err := autonumber.Load(ctx, app, copy.TableID)
	if err != nil || len(numbers) != 1 {
		t.Fatalf("number state=%#v %v", numbers, err)
	}
	meta, _ = app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": copy.TableID})
	collection, _ = app.FindCollectionByNameOrId(meta.GetString("collection_id"))
	row := core.NewRecord(collection)
	if err := numbers[0].Assign(row, time.Now()); err != nil {
		t.Fatal(err)
	}
	if row.GetString(field.Identity.PhysicalName) != "ID-007" {
		t.Fatalf("fresh sequence=%s", row.GetString(field.Identity.PhysicalName))
	}
}
