package workspacev2

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

func TestExternalFileOperationResumesBothAtomicReplaceKillWindows(
	t *testing.T,
) {
	for _, replaceBeforeCrash := range []bool{false, true} {
		t.Run(map[bool]string{
			false: "prepared-before-replace",
			true:  "replaced-before-central-receipt",
		}[replaceBeforeCrash], func(t *testing.T) {
			ctx := context.Background()
			store, err := openStateStore(
				filepath.Join(t.TempDir(), "workspace-v2.db"),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			directory := t.TempDir()
			target := filepath.Join(directory, "export.vtsnap")
			staging := filepath.Join(
				directory,
				".export.vtsnap.test.tmp",
			)
			content := []byte("durable external result")
			if err := os.WriteFile(staging, content, 0o600); err != nil {
				t.Fatal(err)
			}
			operation := externalFileOperation{
				Receipt: protocolv2.OperationReceipt{
					OperationID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
					WorkspaceID: testWorkspaceID,
					Method:      "snapshot.export",
					Scope:       protocolv2.WorkspaceScope,
					RequestHash: "sha256:request",
					Result:      []byte(`{"sha256":"sha256:result"}`),
				},
				Session: protocolv2.Session{
					WorkspaceID: testWorkspaceID,
					Epoch:       7,
					Sequence:    3,
				},
				Staging:     staging,
				Target:      target,
				ContentHash: digestBytes(content),
				ContentSize: int64(len(content)),
			}
			if err := store.prepareExternalFileOperation(
				ctx,
				operation,
			); err != nil {
				t.Fatal(err)
			}
			if replaceBeforeCrash {
				if err := replaceGrantedFile(staging, target); err != nil {
					t.Fatal(err)
				}
			}

			receipt, found, err :=
				store.loadExternalFileOperationReceipt(
					ctx,
					testWorkspaceID,
					operation.Receipt.OperationID,
				)
			if err != nil || !found {
				t.Fatalf(
					"resume receipt=%#v found=%v err=%v",
					receipt,
					found,
					err,
				)
			}
			if receipt.RequestHash != operation.Receipt.RequestHash ||
				string(receipt.Result) != string(operation.Receipt.Result) {
				t.Fatalf("receipt changed: %#v", receipt)
			}
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != string(content) {
				t.Fatalf("target=%q err=%v", raw, err)
			}
			central, found, err := store.loadOperationReceipt(
				ctx,
				testWorkspaceID,
				operation.Receipt.OperationID,
			)
			if err != nil || !found ||
				central.RequestHash != receipt.RequestHash {
				t.Fatalf(
					"central backfill=%#v found=%v err=%v",
					central,
					found,
					err,
				)
			}
			replayed, found, err :=
				store.loadExternalFileOperationReceipt(
					ctx,
					testWorkspaceID,
					operation.Receipt.OperationID,
				)
			if err != nil || !found ||
				replayed.RequestHash != receipt.RequestHash {
				t.Fatalf(
					"idempotent replay=%#v found=%v err=%v",
					replayed,
					found,
					err,
				)
			}
		})
	}
}
