package workspacev2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
)

func TestConflictHandlersProjectNonEmptyTypedSet(t *testing.T) {
	const (
		workspaceID = "11111111-1111-4111-8111-111111111111"
		conflictID  = "22222222-2222-4222-8222-222222222222"
		documentID  = "33333333-3333-4333-8333-333333333333"
		tableID     = "44444444-4444-4444-8444-444444444444"
	)
	engine, err := conflictresolution.OpenEngine(t.TempDir() + "/conflicts.db")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	baseTable := conflictresolution.TableState{
		TableID: tableID, DisplayName: "Projects",
		SchemaObjectID: "schema-base", RecordsObjectID: "records-base",
		ViewsObjectID: "views-base", AttachmentsObjectID: "attachments-base",
	}
	localTable := baseTable
	localTable.RecordsObjectID = "records-local"
	replicaTable := baseTable
	replicaTable.RecordsObjectID = "records-replica"
	createdAt := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	set := conflictresolution.Set{
		ConflictID: conflictID, WorkspaceID: workspaceID,
		State: conflictresolution.StatePending, Revision: 1,
		Base: conflictresolution.Candidate{
			SnapshotID: "base",
			Files: map[string]conflictresolution.FileState{
				documentID: {DocumentID: documentID, Path: "brief.docx", ContentID: "file-base"},
			},
			Tables: map[string]conflictresolution.TableState{tableID: baseTable},
		},
		Local: conflictresolution.Candidate{
			SnapshotID: "local", Revision: 7,
			Files: map[string]conflictresolution.FileState{
				documentID: {DocumentID: documentID, Path: "brief.docx", ContentID: "file-local"},
			},
			Tables: map[string]conflictresolution.TableState{tableID: localTable},
		},
		Replica: conflictresolution.Candidate{
			SnapshotID: "replica", Revision: 8,
			Files: map[string]conflictresolution.FileState{
				documentID: {DocumentID: documentID, Path: "brief.docx", ContentID: "file-replica"},
			},
			Tables: map[string]conflictresolution.TableState{tableID: replicaTable},
		},
		Dependencies: conflictresolution.DependencyGraph{
			Complete: true,
			Edges: map[string][]string{
				documentID:           {},
				tableID:              {"relation:Customers", "automation:Notify", "plugin:Calendar"},
				"relation:Customers": {},
				"automation:Notify":  {},
				"plugin:Calendar":    {},
			},
		},
		CreatedAt: createdAt,
	}
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	owner := &productionReplicaConflict{
		runtime:   &Runtime{manifest: contractsv2.WorkspaceManifest{WorkspaceID: workspaceID}},
		conflicts: engine,
	}

	listed, err := owner.listConflicts(
		context.Background(), nil,
		json.RawMessage(`{"cursor":null,"limit":50}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	listResult := listed.(map[string]any)
	summaries := listResult["conflicts"].([]conflictSummaryProjection)
	if len(summaries) != 1 || summaries[0].ItemCount != 2 ||
		summaries[0].CreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("list summaries = %#v", summaries)
	}

	inspected, err := owner.inspectConflict(
		context.Background(), nil,
		json.RawMessage(`{"conflictId":"`+conflictID+`"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	inspectResult := inspected.(map[string]any)
	items := inspectResult["items"].([]conflictItemProjection)
	if len(items) != 2 {
		t.Fatalf("detail items = %#v", items)
	}
	byKind := map[string]conflictItemProjection{}
	for _, item := range items {
		byKind[item.Kind] = item
	}
	if byKind["file"].ItemID != documentID ||
		byKind["table"].ItemID != tableID ||
		len(byKind["table"].Dependencies) != 3 {
		t.Fatalf("typed detail projection = %#v", items)
	}
}
