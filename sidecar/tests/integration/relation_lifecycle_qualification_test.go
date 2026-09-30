package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// TestRelationApplyDeltaStaleExpectedDigestFailsAtomically pins #414 AC2:
// relation apply-delta forwards the caller's expectedDigest into the mutation
// kernel, so a stale row digest must fail closed without touching the link
// set, the table data revision, or any target record, while a fresh digest on
// the same request shape still commits.
func TestRelationApplyDeltaStaleExpectedDigestFailsAtomically(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	sources := createV2IntegrationTable(t, ctx, app, "校验来源", "digest_guard_sources")
	targets := createV2IntegrationTable(t, ctx, app, "校验目标", "digest_guard_targets")
	sourceName := createV2IntegrationField(t, ctx, app, sources.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "来源名称"), "digest_guard_source_name")
	targetName := createV2IntegrationField(t, ctx, app, targets.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "目标名称"), "digest_guard_target_name")
	link := createV2IntegrationRelation(t, ctx, app, sources.TableID, sourceName.FieldID,
		targets.TableID, targetName.FieldID, "关联目标", "来源", "many", "digest_guard_relation")
	if link.Definition == nil {
		t.Fatal("relation fixture omitted field definition")
	}
	sourcesDefinition, err := schemaexecution.Describe(ctx, app, sources.TableID)
	if err != nil {
		t.Fatal(err)
	}
	targetsDefinition, err := schemaexecution.Describe(ctx, app, targets.TableID)
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	sourceID := "dgsrc0000000001"
	targetOne := "dgtgt0000000001"
	targetTwo := "dgtgt0000000002"
	for _, target := range []struct {
		id    string
		label string
	}{{targetOne, "第一目标"}, {targetTwo, "第二目标"}} {
		if _, err := kernel.Apply(ctx, mutationRequest(
			targets.TableID, targetsDefinition.Snapshot.SchemaRevision,
			"digest-guard-target-"+target.id,
			mutation.Operation{
				Kind: mutation.OperationInsert, RecordID: &target.id,
				Values: map[string]any{
					targetName.Definition.Identity.PhysicalName: target.label,
				},
			},
		)); err != nil {
			t.Fatal(err)
		}
	}
	linkName := link.Definition.Identity.PhysicalName
	inserted, err := kernel.Apply(ctx, mutationRequest(
		sources.TableID, sourcesDefinition.Snapshot.SchemaRevision, "digest-guard-source",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &sourceID,
			Values: map[string]any{linkName: []string{targetOne}},
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted.AffectedRows) != 1 || inserted.AffectedRows[0].Digest == "" {
		t.Fatalf("source insert receipt = %#v", inserted)
	}
	staleDigest := inserted.AffectedRows[0].Digest
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(app, query.NewPort(app, querySource), kernel)
	relationID := sources.TableID + "." + link.FieldID
	delta := func(key string, digest *string, adds, removes []string) (relation.DeltaResult, error) {
		targetRefs := func(ids []string) []relation.TargetRef {
			refs := make([]relation.TargetRef, 0, len(ids))
			for _, id := range ids {
				refs = append(refs, relation.TargetRef{
					TableID: targets.TableID, RecordID: id, Label: id,
				})
			}
			return refs
		}
		return service.ApplyDelta(ctx, relation.DeltaRequest{
			RelationID: relationID, SourceRecordID: sourceID,
			SchemaRevision: sourcesDefinition.Snapshot.SchemaRevision,
			Adds:           targetRefs(adds), Removes: targetRefs(removes),
			RequestID: "req-" + key, IdempotencyKey: key,
			ExpectedDigest: digest,
			Actor:          mutation.Actor{Type: "user", ID: "local-user"},
		})
	}
	changed, err := delta("digest-guard-add", nil, []string{targetTwo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Current) != 2 {
		t.Fatalf("committed delta current = %#v", changed.Current)
	}
	if len(changed.Receipt.AffectedRows) != 1 ||
		changed.Receipt.AffectedRows[0].Digest == "" ||
		changed.Receipt.AffectedRows[0].Digest == staleDigest {
		t.Fatalf("delta receipt did not expose a fresh digest: %#v", changed.Receipt)
	}
	freshDigest := changed.Receipt.AffectedRows[0].Digest
	committedRevision := tableDataRevision(t, app, sources.TableID)
	if _, err := delta("digest-guard-stale", &staleDigest, nil, []string{targetOne}); err == nil {
		t.Fatal("stale expectedDigest was accepted")
	} else {
		var productErr *mutation.ProductError
		if !errors.As(err, &productErr) || productErr.Code != "mutation.digest_conflict" {
			t.Fatalf("stale digest error = %#v", err)
		}
	}
	if got := sourceLinks(t, app, sources.PhysicalName, sourceID, linkName); len(got) != 2 ||
		got[0] != targetOne || got[1] != targetTwo {
		t.Fatalf("rejected delta changed authority links = %#v", got)
	}
	if got := tableDataRevision(t, app, sources.TableID); got != committedRevision {
		t.Fatalf("rejected delta changed data revision %d -> %d", committedRevision, got)
	}
	fresh, err := delta("digest-guard-fresh", &freshDigest, nil, []string{targetTwo})
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Current) != 1 || fresh.Current[0].RecordID != targetOne {
		t.Fatalf("fresh guard delta current = %#v", fresh.Current)
	}
	if got := sourceLinks(t, app, sources.PhysicalName, sourceID, linkName); len(got) != 1 || got[0] != targetOne {
		t.Fatalf("fresh guard links = %#v", got)
	}
}

// TestRelationFieldRetirementBlockedByPathLookupDependency pins #414 AC3:
// a direct relation that an active path lookup depends on cannot be retired
// while the dependency exists; an unreferenced sibling relation stays
// retirable, so the block is dependency-driven rather than blanket.
func TestRelationFieldRetirementBlockedByPathLookupDependency(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	sources := createV2IntegrationTable(t, ctx, app, "路径来源", "path_retire_sources")
	targets := createV2IntegrationTable(t, ctx, app, "路径目标", "path_retire_targets")
	sourceName := createV2IntegrationField(t, ctx, app, sources.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "名称"), "path_retire_source_name")
	targetTitle := createV2IntegrationField(t, ctx, app, targets.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "标题"), "path_retire_target_title")
	first := createV2IntegrationRelation(t, ctx, app, sources.TableID, sourceName.FieldID,
		targets.TableID, targetTitle.FieldID, "第一跳", "第一跳来源", "one", "path_retire_first")
	second := createV2IntegrationRelation(t, ctx, app, sources.TableID, sourceName.FieldID,
		targets.TableID, targetTitle.FieldID, "第二跳", "第二跳来源", "one", "path_retire_second")
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "取值")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: first.FieldID}}, TargetFieldID: targetTitle.FieldID,
	}
	lookup := createV2IntegrationField(t, ctx, app, sources.TableID, lookupDraft, "path_retire_lookup")
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(
		app, store, fieldchange.WithFormulaBackfillScheduler(&atomicFormulaScheduler{}),
	)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	retirePlan := func(fieldID string) v2.FieldChangePlan {
		t.Helper()
		revisions, err := catalog.Revisions(ctx, sources.TableID)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
			Action: v2.ActionRetire, TableID: sources.TableID, FieldID: fieldID,
			ExpectedSchemaRev: revisions.Schema, Actor: actor,
		})
		if err != nil {
			t.Fatalf("retire plan for %s: %#v", fieldID, err)
		}
		return plan
	}
	blocked := retirePlan(first.FieldID)
	if blocked.CanApply {
		t.Fatalf("retire plan ignored the path lookup dependency: %#v", blocked)
	}
	lookupDependency := false
	for _, dependency := range blocked.Impact.Dependencies {
		if dependency.Kind == "lookup" && dependency.ID == sources.TableID+"."+lookup.FieldID {
			lookupDependency = true
		}
	}
	if !lookupDependency {
		t.Fatalf("blocked plan omitted the lookup dependency: %#v", blocked.Impact.Dependencies)
	}
	blockedByRelationContract := false
	for _, diagnostic := range blocked.Errors {
		if diagnostic.Code == "relation.delete.dependency_blocked" {
			blockedByRelationContract = true
		}
	}
	if !blockedByRelationContract {
		t.Fatalf("blocked plan diagnostics = %#v", blocked.Errors)
	}
	free := retirePlan(second.FieldID)
	if !free.CanApply {
		t.Fatalf("unreferenced sibling relation retire was blocked: %#v", free.Errors)
	}
	if _, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: free.PlanID, PlanHash: free.PlanHash,
		OperationID: "path_retire_apply_free", Actor: actor,
		Confirmations: free.Confirmations,
	}); err != nil {
		t.Fatalf("unreferenced sibling relation retire failed to apply: %#v", err)
	}
	persisted, err := catalog.Fields(ctx, sources.TableID, true)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, field := range persisted {
		states[field.Identity.FieldID] = string(field.Lifecycle.State)
	}
	if states[first.FieldID] != string(v2.LifecycleActive) {
		t.Fatalf("blocked relation was retired anyway: %q", states[first.FieldID])
	}
	if states[second.FieldID] != string(v2.LifecycleRetired) {
		t.Fatalf("sibling relation retire did not persist: %q", states[second.FieldID])
	}
}

func tableDataRevision(t *testing.T, app core.App, tableID string) int64 {
	t.Helper()
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	)
	if err != nil {
		t.Fatal(err)
	}
	return int64(meta.GetFloat("data_revision"))
}

func sourceLinks(
	t *testing.T,
	app core.App,
	physicalName string,
	recordID string,
	fieldName string,
) []string {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(physicalName)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(collection, recordID)
	if err != nil {
		t.Fatal(err)
	}
	return record.GetStringSlice(fieldName)
}
