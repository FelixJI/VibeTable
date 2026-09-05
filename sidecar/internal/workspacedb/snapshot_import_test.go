package workspacedb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
)

func TestPrepareImportedSnapshotRetiresOnlyProvenRuntimeState(t *testing.T) {
	const source = "3832eddd-d538-4b56-b665-2210b147e144"
	payload := json.RawMessage(`{"type":"workspace.v2.business-mutation","workspaceId":"3832eddd-d538-4b56-b665-2210b147e144","sessionEpoch":7,"fenceEpoch":3,"claimId":"source-claim","mutationRevision":1,"kind":"record.write","identity":"source-operation"}`)
	envelope, err := auditledger.NewEnvelope("workspace-business:"+source+":1", "source-epoch", 1, "source-operation", payload, time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := auditledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if _, err := ledger.Append(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	prefixRaw, _, err := ledger.ExportPrefix(ledger.Anchor())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auditledger.VerifyPrefix(prefixRaw); err != nil {
		t.Fatal(err)
	}
	var verifiedPrefix auditledger.Prefix
	if err := json.Unmarshal(prefixRaw, &verifiedPrefix); err != nil {
		t.Fatal(err)
	}

	for _, fault := range []string{"", "pending", "missing-proof", "wrong-receipt-workspace", "wrong-receipt-identity", "trigger"} {
		t.Run(fault, func(t *testing.T) {
			status, receiptWorkspace, identity := "drained", source, "source-operation"
			prefix := verifiedPrefix
			switch fault {
			case "pending":
				status = "pending"
			case "missing-proof":
				prefix.Records = nil
			case "wrong-receipt-workspace":
				receiptWorkspace = "99999999-9999-4999-8999-999999999999"
			case "wrong-receipt-identity":
				identity = "unrelated-operation"
			}
			mutation := fmt.Sprintf(`
                INSERT INTO vibetable_audit_outbox VALUES (
                    '%s', 'source-epoch', 1, 'source-operation', '%s', '%s',
                    '2026-08-07T00:00:00Z', '%s', 0);
                INSERT INTO workspace_v2_mutation_receipts VALUES (
                    1, '%s', 7, 3, 'source-claim', 'record.write', '%s', 1, '2026-08-07T00:00:00Z');
                CREATE TABLE user_notes (body TEXT NOT NULL);
                INSERT INTO user_notes VALUES ('%s');
            `, envelope.EventID, envelope.PayloadHash, string(envelope.Payload), status, receiptWorkspace, identity, source)
			if fault == "trigger" {
				mutation += `CREATE TRIGGER delete_notes AFTER DELETE ON vibetable_audit_outbox BEGIN DELETE FROM user_notes; END;`
			}
			raw := snapshotDatabaseFixtureWithMutation(t, true, mutation)
			original := append([]byte(nil), raw...)
			prepared, err := PrepareImportedSnapshot(context.Background(), raw, source, "88888888-8888-4888-8888-888888888888", prefix)
			if !bytes.Equal(raw, original) {
				t.Fatal("source snapshot bytes changed")
			}
			if fault != "" {
				if err == nil || len(prepared) != 0 {
					t.Fatalf("unproven runtime state accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateSnapshot(context.Background(), prepared, SupportedBusinessSchemaVersion); err != nil {
				t.Fatal(err)
			}
			database, err := sql.Open("sqlite", "file::memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			connection, err := database.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := connection.Raw(func(driver any) error { return driver.(sqliteDeserializer).Deserialize(prepared) }); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"vibetable_audit_outbox", "workspace_v2_mutation_receipts"} {
				var count int
				if err := connection.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("target retained source runtime state in %s", table)
				}
			}
			var completedWorkspace, completedKind, completedIdentity string
			if err := connection.QueryRowContext(context.Background(), "SELECT workspace_id, kind, identity FROM workspace_v2_imported_completions").Scan(&completedWorkspace, &completedKind, &completedIdentity); err != nil {
				t.Fatal(err)
			}
			if completedWorkspace != "88888888-8888-4888-8888-888888888888" || completedKind != "record.write" || completedIdentity != "source-operation" {
				t.Fatalf("lost imported completion: %s %s %s", completedWorkspace, completedKind, completedIdentity)
			}
			forwarded, err := PrepareImportedSnapshot(context.Background(), prepared, completedWorkspace, "77777777-7777-4777-8777-777777777777", prefix)
			if err != nil {
				t.Fatal(err)
			}
			if err := connection.Raw(func(driver any) error { return driver.(sqliteDeserializer).Deserialize(forwarded) }); err != nil {
				t.Fatal(err)
			}
			if err := connection.QueryRowContext(context.Background(), "SELECT workspace_id, kind, identity FROM workspace_v2_imported_completions").Scan(&completedWorkspace, &completedKind, &completedIdentity); err != nil || completedWorkspace != "77777777-7777-4777-8777-777777777777" || completedKind != "record.write" || completedIdentity != "source-operation" {
				t.Fatalf("completion did not survive another import: %s %s %s %v", completedWorkspace, completedKind, completedIdentity, err)
			}
			var note string
			if err := connection.QueryRowContext(context.Background(), "SELECT body FROM user_notes").Scan(&note); err != nil || note != source {
				t.Fatalf("user UUID text changed: %q %v", note, err)
			}
			if _, err := connection.ExecContext(context.Background(), `INSERT INTO workspace_v2_mutation_receipts VALUES (1, '77777777-7777-4777-8777-777777777777', 1, 1, 'target-claim', 'record.write', 'target-operation', 1, '2026-09-05T00:00:00Z')`); err != nil {
				t.Fatalf("new authority revision collides with source: %v", err)
			}
			afterPrefix, err := json.Marshal(verifiedPrefix)
			if err != nil {
				t.Fatal(err)
			}
			var beforePrefix auditledger.Prefix
			if err := json.Unmarshal(prefixRaw, &beforePrefix); err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(beforePrefix)
			if err != nil || !bytes.Equal(before, afterPrefix) {
				t.Fatal("source audit prefix changed")
			}
		})
	}
}
