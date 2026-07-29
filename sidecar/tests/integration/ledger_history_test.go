package integration_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestWorkspaceV2LedgerHistorySurvivesBusinessAuditRollback(
	t *testing.T,
) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	definition, err := schemaapi.New(app).ApplyChange(
		ctx,
		schemaapi.Change{
			Definition: baseTable(
				"ledger_history_notes",
				"ledger_history_notes",
				[]schema.FieldDefinition{
					field(
						"title_id",
						"title",
						schema.FieldKindScalar,
						schema.DataTypeShortText,
					),
				},
			),
			ExpectedRevision: 0,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
	)
	recordID := "ledgerhistrow01"
	apply := func(key string, kind mutation.OperationKind, title string) {
		t.Helper()
		values := map[string]any{"title": title}
		if _, err := kernel.Apply(ctx, mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID:       "ledger_history_" + key,
			IdempotencyKey:  "ledger_history_" + key,
			TableID:         definition.TableID,
			SchemaRevision:  definition.SchemaRevision,
			Operations: []mutation.Operation{{
				Kind:     kind,
				RecordID: &recordID,
				Values:   values,
			}},
			Actor: mutation.Actor{
				Type: "user",
				ID:   "local-user",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	apply("create", mutation.OperationInsert, "before snapshot")
	apply("after_snapshot", mutation.OperationUpdate, "after snapshot")

	ledger, err := auditledger.Open(
		filepath.Join(t.TempDir(), "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	outbox, err := auditledger.NewPocketBaseOutbox(app)
	if err != nil {
		t.Fatal(err)
	}
	drainer, err := auditledger.NewDrainer(ledger, 256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainer.Drain(ctx, outbox); err != nil {
		t.Fatal(err)
	}
	service, err := audit.New(
		app,
		kernel,
		mutation.MetadataSchemaSource{},
		[]byte(strings.Repeat("l", 32)),
		audit.WithLedgerHistory(ledger),
	)
	if err != nil {
		t.Fatal(err)
	}
	read := func() audit.Page {
		t.Helper()
		page, err := service.ReadChangeSets(ctx, audit.ReadParams{
			TableID: definition.TableID,
			ItemID:  &recordID,
			Scope:   "row",
			Limit:   20,
		})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	beforeRollback := read()
	if len(beforeRollback.ChangeSets) != 2 {
		t.Fatalf("initial ledger history = %#v", beforeRollback)
	}
	targetRevision := beforeRollback.ChangeSets[1].
		RecordChanges[0].
		RevisionID

	// Snapshot restore replaces the business database. Deleting its derived
	// audit projection models the strongest rollback case: no target revision
	// remains in vibetable_audit_events, while the external ledger is intact.
	if _, err := app.DB().NewQuery(
		`DELETE FROM vibetable_audit_events`,
	).Execute(); err != nil {
		t.Fatal(err)
	}
	afterRollback := read()
	if len(afterRollback.ChangeSets) != 2 ||
		afterRollback.ChangeSets[0].RecordChanges[0].RevisionID !=
			beforeRollback.ChangeSets[0].RecordChanges[0].RevisionID {
		t.Fatalf("history moved backwards after rollback: %#v", afterRollback)
	}
	preview, err := service.PreviewRestore(ctx, audit.PreviewParams{
		TableID:        definition.TableID,
		ItemID:         recordID,
		TargetRevision: targetRevision,
		Scope:          "row",
	})
	if err != nil || !preview.CanApply ||
		preview.TargetRevision != targetRevision {
		t.Fatalf("ledger target preview = %#v, %v", preview, err)
	}

	restorePayload, _ := json.Marshal(map[string]any{
		"type":       "snapshot.restore",
		"snapshotId": "snapshot-before-update",
	})
	restoreEnvelope, err := auditledger.NewEnvelope(
		"snapshot-restore:1",
		"restore-epoch",
		1,
		"snapshot-restore:1",
		restorePayload,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	anchorBeforeRestore := ledger.Anchor()
	if _, err := ledger.Append(ctx, restoreEnvelope); err != nil {
		t.Fatal(err)
	}
	anchorAfterRestore := ledger.Anchor()
	if anchorAfterRestore.LedgerSequence !=
		anchorBeforeRestore.LedgerSequence+1 {
		t.Fatalf(
			"restore did not append exactly once: before=%#v after=%#v",
			anchorBeforeRestore,
			anchorAfterRestore,
		)
	}
	if len(read().ChangeSets) != 2 {
		t.Fatal("restore event displaced pre-restore business history")
	}

	// A restored business database may restart its data revision counter. The
	// external ledger sequence, not that rollbackable counter, must place this
	// post-restore mutation at the top of the history timeline.
	postRestorePayload, _ := json.Marshal(map[string]any{
		"revisionId":     "postrestore-rev",
		"changeSetId":    "postrestore-change",
		"sequence":       1,
		"tableId":        definition.TableID,
		"recordId":       recordID,
		"operation":      "update",
		"before":         map[string]any{"title": "before snapshot"},
		"after":          map[string]any{"title": "post restore"},
		"schemaRevision": 1,
		"dataRevision":   1,
		"requestId":      "postrestore-request",
		"actor": map[string]any{
			"type":        "system",
			"id":          "snapshot-restore",
			"displayName": nil,
		},
	})
	postRestoreEnvelope, err := auditledger.NewEnvelope(
		"postrestore-event",
		"business-v2",
		3,
		"postrestore-mutation",
		postRestorePayload,
		time.Now().UTC().Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Append(ctx, postRestoreEnvelope); err != nil {
		t.Fatal(err)
	}
	postRestorePage := read()
	if len(postRestorePage.ChangeSets) != 3 ||
		postRestorePage.ChangeSets[0].ChangeSetID != "postrestore-change" {
		t.Fatalf(
			"rollbackable data revision reordered history: %#v",
			postRestorePage.ChangeSets,
		)
	}
}
