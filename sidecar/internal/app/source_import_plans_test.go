package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

func TestSourceImportOwnerKeepsPlanClaimsSeparateAndFrozen(t *testing.T) {
	source := sourceExecutionSnapshot()
	plan := sourceExecutionPreview(t, source, nil)
	owner := newImportPlanOwner("workspace-one")
	reply, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan.Tables[0].Records[0].Values["title"] = "modified"
	if owner.sourcePlans[reply.Token].plan.Tables[0].Records[0].Values["title"] == "modified" {
		t.Fatal("owner retained preview response buffer")
	}
	if _, err := owner.stage(importPlanStageRequest{Contract: importPlanContract, Token: reply.Token}); err == nil {
		t.Fatal("source plan entered legacy atomic import authority")
	}
	claim := sourceClaimRequest{Contract: sourceimport.Contract, Token: reply.Token, JobID: "source-job", SessionEpoch: 7, Observation: sourceimport.Observation{Version: "v1", TableVersions: map[string]string{"a": "a1", "b": "b1", "c": "c1"}}}
	if _, err := owner.claimSource(claim, 7); err == nil {
		t.Fatal("unconfirmed migration claimed")
	}
	claim.Confirmed = true
	claim.SessionEpoch = 8
	if _, err := owner.claimSource(claim, 7); err == nil {
		t.Fatal("old epoch admitted")
	}
	claim.SessionEpoch = 7
	claim.Observation.Version = "v2"
	if _, err := owner.claimSource(claim, 7); err == nil {
		t.Fatal("changed source admitted")
	}
	claim.Observation.Version = "v1"
	if _, err := owner.claimSource(claim, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.claimSource(claim, 7); err == nil {
		t.Fatal("consumed migration claimed twice")
	}
	owner.retireSource(reply.Token)
	if _, err := owner.claimSource(claim, 7); err == nil {
		t.Fatal("retired source token restored")
	}
}

func TestSourceImportOwnerDoesNotCoercePreciseSnapshotNumbers(t *testing.T) {
	plan := sourceExecutionPreview(t, sourceExecutionSnapshot(), nil)
	plan.Tables[0].Records[0].Values["exact"] = json.Number("9007199254740993")
	owner := newImportPlanOwner("workspace-one")
	reply, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	number, ok := owner.sourcePlans[reply.Token].plan.Tables[0].Records[0].Values["exact"].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatal("owner coerced frozen source numeric precision")
	}
}

func TestSourceImportAttachmentBytesPersistOfflineAndCleanupIsJobScoped(t *testing.T) {
	ctx := context.Background()
	fixture := newSourceImportAuthorityFixture(t)
	manager, err := attachments.New()
	if err != nil {
		t.Fatal(err)
	}
	fixture.authority.kernel = mutation.New(fixture.app, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(manager))
	content := []byte("offline migration bytes\n")
	source := sourceimport.Snapshot{Contract: sourceimport.Contract, Provider: "synthetic", ContainerID: "files", DisplayName: "合成附件", Version: "v1",
		ReadWindow: sourceimport.ReadWindow{StartedAt: "2026-10-04T08:00:00Z", FinishedAt: "2026-10-04T08:00:01Z", Consistency: "window"},
		Tables: []sourceimport.Table{{ID: "t", Name: "附件迁移", Version: "t1", PrimaryFieldID: "title", Fields: []sourceimport.Field{
			{ID: "title", Name: "标题", Kind: "text", ValueKind: v2.LogicalText},
			{ID: "file", Name: "文件", Kind: "file", ValueKind: v2.LogicalFile},
		}, Records: []sourceimport.Record{{ID: "r", Values: map[string]any{"title": "offline", "file": []string{"a"}}}}}},
		Attachments: []sourceimport.Attachment{{ID: "a", TableID: "t", RecordID: "r", FieldID: "file", Name: "offline.txt", MIME: "text/plain", Size: int64(len(content))}}}
	plan, err := sourceimport.Preview(ctx, source, sourceimport.Options{SelectedTableIDs: []string{"t"}}, nil)
	if err != nil || !plan.CanApply {
		t.Fatalf("file preview: %v %#v", err, plan.Diagnostics)
	}
	owner := newImportPlanOwner("workspace-one")
	reply, err := owner.mintSource(plan, 7, manager)
	if err != nil {
		t.Fatal(err)
	}
	claim := sourceClaimRequest{Contract: sourceimport.Contract, Token: reply.Token, JobID: "files-job", SessionEpoch: 7, Confirmed: true, Observation: sourceimport.Observation{Version: "v1", TableVersions: map[string]string{"t": "t1"}}}
	if _, err := owner.claimSource(claim, 7); err == nil {
		t.Fatal("metadata-only files accepted as migrated bytes")
	}
	key := sourceimport.Key{Provider: "synthetic", ContainerID: "files", TableID: "t", FieldID: "file", RecordID: "r", ObjectID: "a"}
	before := sourceExecutionFacts(t, fixture.app)
	if err := owner.uploadSource(reply.Token, 7, key, []byte("short")); err == nil {
		t.Fatal("wrong content length accepted")
	}
	if err := owner.uploadSource(reply.Token, 7, key, append([]byte{}, content...)); err != nil {
		t.Fatal(err)
	}
	if after := sourceExecutionFacts(t, fixture.app); !reflect.DeepEqual(before, after) {
		t.Fatal("attachment staging wrote business schema or rows before confirmation")
	}
	if err := manager.Stage("other_job", "other.txt", []byte("unrelated staged bytes")); err != nil {
		t.Fatal(err)
	}
	stored, err := owner.claimSource(claim, 7)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sourceimport.NewExecutor(fixture.authority, sourceimport.NewJournal(fixture.app), stored).Execute(ctx, stored.plan, claim.JobID, 7)
	if err != nil || result.State != "succeeded" {
		t.Fatalf("file migration: %v %#v", err, result)
	}
	owner.retireSource(reply.Token)
	// An unrelated handle must remain reserved after this job's cleanup.
	if err := manager.Stage("other_job", "different.txt", []byte("changed")); err == nil {
		t.Fatal("cleanup deleted another job's staged upload")
	}
	manager.Drop("other_job")
	var tableID, recordID, fieldID string
	for _, batch := range result.Batches {
		for _, mapping := range batch.Mappings {
			switch mapping.Kind {
			case "table":
				tableID = mapping.LocalID
			case "record":
				recordID = mapping.LocalID
			case "field":
				if mapping.Source.FieldID == "file" {
					fieldID = mapping.LocalID
				}
			}
		}
	}
	if err := fixture.app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// A fresh manager has no staged buffers or network source. Open uses only
	// the persisted local attachment metadata and PocketBase file capability.
	reopened, err := attachments.New()
	if err != nil {
		t.Fatal(err)
	}
	refs, err := reopened.RefsByID(ctx, fixture.app, tableID, recordID, fieldID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("offline refs: %#v %v", refs, err)
	}
	download, err := reopened.Open(ctx, fixture.app, refs[0].DownloadCapability)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Reader.Close()
	got, err := io.ReadAll(download.Reader)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("reopened offline bytes do not match staged source")
	}
}
