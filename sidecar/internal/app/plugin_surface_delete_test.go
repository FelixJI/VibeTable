package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// The delete slice pins the closed surface contract end to end: an explicit
// manifest delete operation grant, transaction-scoped re-authorization on the
// real mutation kernel, canonical restrict/digest semantics with atomic
// zero-commit batches, and the existing relation-field write boundary against
// the canonical target-table grant.
func TestPluginSurfaceControlledDeleteAndRelationWriteBoundaries(t *testing.T) {
	ctx := context.Background()
	f := newWorkspaceMutationReplayFixture(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	store := pluginstore.New(f.pb, workspace)

	lifecycle, err := schemacore.NewTableLifecycle(f.pb)
	if err != nil {
		t.Fatal(err)
	}
	target, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "surface delete targets", OperationID: "surface-delete-targets", Actor: v2.Actor{Kind: "user", ID: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, f.pb, target.TableID, v2.LogicalText, "Label", "surface-delete-label").Definition
	relationDefaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	link := applySchemaProductField(t, f.pb, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: f.request.TableID,
		Draft: &v2.FieldDraft{DisplayName: "Target", LogicalType: v2.LogicalRelation,
			Value: relationDefaults.Value, Constraints: relationDefaults.Constraints, Storage: relationDefaults.Storage, Display: relationDefaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: target.TableID, Cardinality: "one", DeletePolicy: "restrict", DisplayField: label.Identity.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "Sources", ReciprocalCardinality: "many", SourceDisplayFieldID: f.definition.Snapshot.Fields[0].Identity.FieldID},
	}, "surface-delete-relation").Definition

	targetDescription, err := schemaexecution.Describe(ctx, f.pb, target.TableID)
	if err != nil {
		t.Fatal(err)
	}
	targetCollection, err := f.pb.FindCollectionByNameOrId(targetDescription.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	sourceCollection, err := f.pb.FindCollectionByNameOrId(f.definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	free := core.NewRecord(targetCollection)
	free.Set(label.Identity.PhysicalName, "free")
	stale := core.NewRecord(targetCollection)
	stale.Set(label.Identity.PhysicalName, "stale")
	referenced := core.NewRecord(targetCollection)
	referenced.Set(label.Identity.PhysicalName, "referenced")
	repoint := core.NewRecord(targetCollection)
	repoint.Set(label.Identity.PhysicalName, "repoint")
	for _, record := range []*core.Record{free, stale, referenced, repoint} {
		if err := f.pb.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	sourceRow := core.NewRecord(sourceCollection)
	sourceRow.Set(f.field, "source row")
	sourceRow.Set(link.Identity.PhysicalName, referenced.Id)
	if err := f.pb.Save(sourceRow); err != nil {
		t.Fatal(err)
	}

	pluginID, hash := "com.example.surface-delete", strings.Repeat("e", 64)
	sourceGrant := map[string]any{"collection": f.request.TableID, "operations": []string{"read", "query", "create", "update"}, "fields": []string{"*"}}
	targetGrant := func(operations ...string) map[string]any {
		return map[string]any{"collection": target.TableID, "operations": operations, "fields": []string{"id", label.Identity.FieldID}}
	}
	manifest := func(data ...map[string]any) map[string]any {
		return map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": pluginID, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{},
			"compatibility": map[string]any{"pluginApi": "3.x"},
			"permissions":   map[string]any{"data": data},
			"actions":       []any{map[string]any{"actionId": "save", "risk": "write", "mode": "local", "invocation": "manual"}},
			"ui":            map[string]any{"customViews": []any{map[string]any{"viewId": "save", "actionId": "save", "entry": "ui/index.html"}}}}
	}
	full := manifest(sourceGrant, targetGrant("read", "query", "update", "delete"))
	if _, err := store.SaveInstallation(ctx, installationSnapshot(t, store.ProjectKey(), pluginID, hash, full, 1, "enabled"), nil); err != nil {
		t.Fatal(err)
	}

	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{})
	input := pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, ActionID: "save", Entry: "ui/index.html", PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1, ActiveCollection: target.TableID}
	call := func(method string, params any) (any, error) {
		input.Method = method
		input.Request, _ = json.Marshal(params)
		return executePluginSurfaceAPI(ctx, f.pb, source, kernel, workspace, input, f.runtime.CoordinateBusinessWrite)
	}
	guards := func(table string) map[string]string {
		t.Helper()
		result := map[string]string{}
		params := map[string]any{"contract": "vibetable.plugin-query.v2", "collection": table, "fields": []string{"id"}, "pageSize": 200}
		for {
			output, err := call("data.query", params)
			if err != nil {
				t.Fatal(err)
			}
			page := output.(map[string]any)
			for id, digest := range page["rowGuards"].(map[string]any) {
				result[id] = digest.(string)
			}
			if next := page["nextCursor"].(*string); next != nil && *next != "" {
				input.Cursor = *next
				input.Dependencies = page["dependencies"].(map[string]pluginComputedRevision)
				continue
			}
			input.Cursor = ""
			input.Dependencies = nil
			return result
		}
	}
	targetGuards := guards(target.TableID)
	sourceGuards := guards(f.request.TableID)
	sourceSchema := describeSchemaRevision(t, f.pb, f.request.TableID)
	targetSchema := describeSchemaRevision(t, f.pb, target.TableID)
	if len(targetGuards) != 4 || len(sourceGuards) != 1 {
		t.Fatalf("unexpected guard pages: %v %v", targetGuards, sourceGuards)
	}
	plan := func(collection, schemaRevision string, operations ...map[string]any) map[string]any {
		return map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": collection, "schemaRevision": schemaRevision, "operations": operations}
	}
	deleteOp := func(id string, digest string) map[string]any {
		return map[string]any{"kind": "delete", "primaryKey": id, "expectedDigest": digest}
	}
	auditCount := func() int {
		events, err := store.ListAudit(ctx, input.ProjectKey, pluginID)
		if err != nil {
			t.Fatal(err)
		}
		return len(events)
	}
	commit := func(run string, mutationPlan map[string]any) (any, error) {
		input.RequestID = run
		if _, err := call("audit.start", map[string]any{"taskId": "task-" + run, "plan": mutationPlan}); err != nil {
			return nil, err
		}
		return call("mutation.commit", mutationPlan)
	}
	replaceInstallation := func(t *testing.T, replacement map[string]any) {
		t.Helper()
		current := input.InstallationRevision
		raw, _ := json.Marshal(map[string]any{"projectKey": store.ProjectKey(), "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash,
			"sourceType": "package", "sourceLocation": "host-managed", "manifest": replacement, "schemas": map[string]any{},
			"status": "enabled", "revision": current + 1, "sourceChanged": false, "blockingReasons": []any{}})
		expected := current
		if _, err := store.SaveInstallation(ctx, raw, &expected); err != nil {
			t.Fatal(err)
		}
		input.InstallationRevision = current + 1
	}

	t.Run("closed plan kinds keep guards and reject values on delete", func(t *testing.T) {
		if _, err := call("mutation.preview", plan(target.TableID, targetSchema, map[string]any{"kind": "delete", "primaryKey": free.Id})); surfaceErrorCode(t, err) != "plugin_guard_required" {
			t.Fatal("delete without a digest must demand a row guard")
		}
		for _, values := range []any{map[string]any{}, nil} {
			if _, err := call("mutation.preview", plan(target.TableID, targetSchema, map[string]any{"kind": "delete", "primaryKey": free.Id, "expectedDigest": targetGuards[free.Id], "values": values})); surfaceErrorCode(t, err) != "plugin_capability_invalid" {
				t.Fatal("delete plans must not carry values, including null")
			}
		}
		if _, err := call("mutation.preview", plan(target.TableID, targetSchema, map[string]any{"kind": "archive", "primaryKey": free.Id, "expectedDigest": targetGuards[free.Id]})); surfaceErrorCode(t, err) != "plugin_capability_invalid" {
			t.Fatal("archive is outside the closed surface set")
		}
		if _, err := commit("invalid-delete-guard", plan(target.TableID, targetSchema, deleteOp(free.Id, "sha256:unrelated"))); surfaceErrorCode(t, err) != "mutation.request.invalid" {
			t.Fatal("unknown digest must fail the canonical guard at commit")
		}
		if _, err := call("mutation.preview", plan(target.TableID, targetSchema, deleteOp(free.Id, targetGuards[free.Id]))); err != nil {
			t.Fatal("granted delete preview failed", err)
		}
	})

	t.Run("single table and operation count remain closed before any write", func(t *testing.T) {
		operations := make([]map[string]any, 1000)
		for i := range operations {
			operations[i] = deleteOp(stale.Id, targetGuards[stale.Id])
		}
		input.RequestID = "limit-check"
		input.Request, _ = json.Marshal(plan(target.TableID, targetSchema, operations...))
		request, err := surfaceMutationRequest(ctx, f.pb, workspace, input)
		if err != nil || len(request.Operations) != 1000 {
			t.Fatal("1000-operation boundary was not accepted", err)
		}
		invalidPlans := []map[string]any{
			plan(target.TableID, targetSchema),
			plan(target.TableID, targetSchema, append(operations, deleteOp(stale.Id, targetGuards[stale.Id]))...),
			plan(target.TableID, targetSchema, map[string]any{"kind": "delete", "primaryKey": stale.Id, "expectedDigest": targetGuards[stale.Id], "collection": f.request.TableID}),
		}
		before, audits := f.state(t), auditCount()
		for _, invalid := range invalidPlans {
			if _, err := call("audit.start", map[string]any{"taskId": "invalid-batch", "plan": invalid}); surfaceErrorCode(t, err) != "plugin_capability_invalid" {
				t.Fatal("empty, oversized, or cross-table batch was admitted", err)
			}
		}
		if after := f.state(t); after.proofs != before.proofs || auditCount() != audits {
			t.Fatal("invalid plan wrote audit or business receipts")
		}
	})

	t.Run("explicit delete grant is required and never falls back to update", func(t *testing.T) {
		replaceInstallation(t, manifest(sourceGrant, targetGrant("read", "query", "update")))
		before := f.state(t)
		audits := auditCount()
		for _, method := range []string{"mutation.preview", "audit.start", "mutation.commit"} {
			params := any(plan(target.TableID, targetSchema, deleteOp(free.Id, targetGuards[free.Id])))
			if method == "audit.start" {
				params = map[string]any{"taskId": "denied-delete", "plan": params}
			}
			input.RequestID = "no-grant-" + method
			if _, err := call(method, params); surfaceErrorCode(t, err) != "plugin_write_denied" {
				t.Fatalf("%s admitted an undeclared delete", method)
			}
		}
		if after := f.state(t); after.proofs != before.proofs || auditCount() != audits {
			t.Fatal("denied delete wrote a proof or audit")
		}
		if rows, err := f.pb.FindRecordsByFilter(targetDescription.PhysicalName, "", "", 0, 0); err != nil || len(rows) != 4 {
			t.Fatal("denied delete removed data", err)
		}
		replaceInstallation(t, full)
	})

	t.Run("delete still requires an explicit ID field grant", func(t *testing.T) {
		hiddenID := targetGrant("read", "query", "delete")
		hiddenID["fields"] = []string{label.Identity.FieldID}
		replaceInstallation(t, manifest(sourceGrant, hiddenID))
		if _, err := call("mutation.preview", plan(target.TableID, targetSchema, deleteOp(free.Id, targetGuards[free.Id]))); surfaceErrorCode(t, err) != "plugin_write_denied" {
			t.Fatal("delete admitted a hidden primary key", err)
		}
		replaceInstallation(t, full)
	})

	t.Run("granted delete commits atomically and audits once", func(t *testing.T) {
		before := f.state(t)
		audits := auditCount()
		result, err := commit("delete-free", plan(target.TableID, targetSchema, deleteOp(free.Id, targetGuards[free.Id])))
		if err != nil {
			t.Fatal(err)
		}
		receipt := result.(mutation.Receipt)
		if receipt.Status != mutation.StatusApplied || len(receipt.AffectedRows) != 1 || receipt.AffectedRows[0].Operation != mutation.OperationDelete || receipt.AffectedRows[0].RecordID != free.Id {
			t.Fatalf("delete receipt %#v", receipt)
		}
		if _, err := f.pb.FindRecordById(targetDescription.PhysicalName, free.Id); err == nil {
			t.Fatal("deleted row is still present")
		}
		if after := f.state(t); after.proofs != before.proofs+2 || after.revision != before.revision+2 || auditCount() != audits+2 {
			t.Fatalf("delete did not persist exactly start+commit receipts: proofs before=%d after=%d audits=%d", before.proofs, after.proofs, auditCount())
		}
	})

	t.Run("restrict relation keeps referenced rows and the batch commits nothing", func(t *testing.T) {
		before := f.state(t)
		audits := auditCount()
		result, err := commit("delete-referenced", plan(target.TableID, targetSchema,
			deleteOp(referenced.Id, targetGuards[referenced.Id]), deleteOp(stale.Id, targetGuards[stale.Id])))
		if surfaceErrorCode(t, err) != "mutation.relation.restricted" {
			t.Fatalf("restricted delete returned %v", err)
		}
		if outcome := surfaceCommitOutcome(ctx, f.pb, workspace, input, result, surfaceErrorCode(t, err)); outcome != "not-committed" {
			t.Fatalf("restrict failure must preserve rollback proof: %s", outcome)
		}
		for _, id := range []string{referenced.Id, stale.Id} {
			if _, err := f.pb.FindRecordById(targetDescription.PhysicalName, id); err != nil {
				t.Fatal("restricted batch deleted a row", id, err)
			}
		}
		if after := f.state(t); after.proofs != before.proofs+1 || auditCount() != audits+1 {
			t.Fatalf("failed delete batch changed proofs beyond its started audit: proofs before=%d after=%d audits=%d", before.proofs, after.proofs, auditCount())
		}
	})

	t.Run("one stale guard rejects the whole atomic batch", func(t *testing.T) {
		before := f.state(t)
		audits := auditCount()
		_, err := commit("delete-stale-mixed", plan(target.TableID, targetSchema,
			deleteOp(stale.Id, targetGuards[stale.Id]), deleteOp(referenced.Id, "sha256:"+strings.Repeat("0", 64))))
		if surfaceErrorCode(t, err) != "mutation.digest_conflict" {
			t.Fatalf("stale mixed batch returned %v", err)
		}
		if _, err := f.pb.FindRecordById(targetDescription.PhysicalName, stale.Id); err != nil {
			t.Fatal("valid item of a conflicting batch was deleted")
		}
		if after := f.state(t); after.proofs != before.proofs+1 || auditCount() != audits+1 {
			t.Fatalf("conflicting batch wrote outside its started audit: proofs before=%d after=%d audits=%d", before.proofs, after.proofs, auditCount())
		}
	})

	t.Run("revoked authorization cannot commit a captured delete plan", func(t *testing.T) {
		input.RequestID = "delete-captured"
		input.Request, _ = json.Marshal(plan(target.TableID, targetSchema, deleteOp(stale.Id, targetGuards[stale.Id])))
		guarded, err := surfaceMutationRequest(ctx, f.pb, workspace, input)
		if err != nil {
			t.Fatal(err)
		}
		expected := input.InstallationRevision
		revoked, _ := json.Marshal(map[string]any{"projectKey": store.ProjectKey(), "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash,
			"sourceType": "package", "sourceLocation": "host-managed", "manifest": full, "schemas": map[string]any{},
			"status": "disabled", "revision": expected + 1, "sourceChanged": false, "blockingReasons": []any{}})
		if _, err := store.SaveInstallation(ctx, revoked, &expected); err != nil {
			t.Fatal(err)
		}
		if _, err := kernel.Preview(ctx, guarded); err == nil {
			t.Fatal("preview admitted a revoked principal")
		}
		if _, err := kernel.Apply(ctx, guarded); err == nil {
			t.Fatal("transaction admitted a revoked principal")
		}
		if _, err := f.pb.FindRecordById(targetDescription.PhysicalName, stale.Id); err != nil {
			t.Fatal("revoked transaction deleted a row")
		}
		expected = expected + 1
		restored, _ := json.Marshal(map[string]any{"projectKey": store.ProjectKey(), "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash,
			"sourceType": "package", "sourceLocation": "host-managed", "manifest": full, "schemas": map[string]any{},
			"status": "enabled", "revision": expected + 1, "sourceChanged": false, "blockingReasons": []any{}})
		if _, err := store.SaveInstallation(ctx, restored, &expected); err != nil {
			t.Fatal(err)
		}
		input.InstallationRevision = expected + 1
	})

	t.Run("relation writes follow the canonical target-table grant", func(t *testing.T) {
		relationValues := map[string]any{link.Identity.FieldID: repoint.Id}
		mutationPlan := plan(f.request.TableID, sourceSchema, map[string]any{"kind": "update", "primaryKey": sourceRow.Id, "expectedDigest": sourceGuards[sourceRow.Id], "values": relationValues})
		result, err := commit("relation-repoint", mutationPlan)
		if err != nil {
			t.Fatal("granted relation write failed", err)
		}
		if result.(mutation.Receipt).Status != mutation.StatusApplied {
			t.Fatalf("relation write receipt %#v", result)
		}
		updated, err := f.pb.FindRecordById(f.definition.PhysicalName, sourceRow.Id)
		if err != nil || updated.GetString(link.Identity.PhysicalName) != repoint.Id {
			t.Fatal("relation write did not reach canonical storage", err)
		}
		replaceInstallation(t, manifest(sourceGrant))
		input.RequestID = "relation-without-target-grant"
		if _, err := call("audit.start", map[string]any{"taskId": "task-relation-denied", "plan": mutationPlan}); surfaceErrorCode(t, err) != "plugin_write_denied" {
			t.Fatal("relation write without a target-table grant was admitted")
		}
		if _, err := call("mutation.commit", mutationPlan); surfaceErrorCode(t, err) != "plugin_write_denied" {
			t.Fatal("relation commit without a target-table grant was admitted")
		}
		unchanged, err := f.pb.FindRecordById(f.definition.PhysicalName, sourceRow.Id)
		if err != nil || unchanged.GetString(link.Identity.PhysicalName) != repoint.Id {
			t.Fatal("denied relation write changed data", err)
		}
		replaceInstallation(t, full)
	})
}

func surfaceErrorCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var domain *query.ProductError
	if errors.As(err, &domain) {
		return domain.Code
	}
	var kernel *mutation.ProductError
	if errors.As(err, &kernel) {
		return kernel.Code
	}
	t.Fatalf("unexpected error type: %v", err)
	return ""
}

func describeSchemaRevision(t *testing.T, app core.App, tableID string) string {
	t.Helper()
	definition, err := schemaexecution.Describe(context.Background(), app, tableID)
	if err != nil {
		t.Fatal(err)
	}
	return definition.Snapshot.SchemaRevision
}

func installationSnapshot(t *testing.T, projectKey, pluginID, hash string, manifest map[string]any, revision int64, status string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"projectKey": projectKey, "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash,
		"sourceType": "package", "sourceLocation": "host-managed", "manifest": manifest, "schemas": map[string]any{},
		"status": status, "revision": revision, "sourceChanged": false, "blockingReasons": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
