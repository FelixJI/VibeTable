package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const e2eFileRestoreBarrierEnvironment = "VIBETABLE_E2E_FILE_RESTORE_BARRIER_DIR"

type e2eFileRestoreArm struct {
	WorkspaceID                 string `json:"workspaceId"`
	OperationID                 string `json:"operationId"`
	DocumentID                  string `json:"documentId"`
	HistoricalRevisionID        string `json:"historicalRevisionId"`
	ExpectedEffectiveRevisionID string `json:"expectedEffectiveRevisionId"`
	RelativePath                string `json:"relativePath"`
	ObjectID                    string `json:"objectId"`
}

type e2eFileRestoreReady struct {
	e2eFileRestoreArm
	ProcessID        int                                    `json:"pid"`
	Point            writecoordinator.PersistenceFaultPoint `json:"point"`
	MutationRevision uint64                                 `json:"mutationRevision"`
	SessionEpoch     uint64                                 `json:"sessionEpoch"`
	FenceEpoch       uint64                                 `json:"fenceEpoch"`
	ClaimID          string                                 `json:"claimId"`
	Result           json.RawMessage                        `json:"result"`
}

// This permanent test-only injector never consults the coordinator's mutex:
// finishMutation calls it while Write holds that mutex. The arm is created by
// the external E2E driver after pausing one genuine Restore outbound message.
// Startup and watcher commits cannot claim it before that exact RPC receipt
// has been published with the applied materialization and pending head.
func newE2EFileRestoreBarrierFromEnvironment(
	dataDir, workspaceID string,
) writecoordinator.PersistenceFaultInjector {
	directory := os.Getenv(e2eFileRestoreBarrierEnvironment)
	if !filepath.IsAbs(directory) {
		return nil
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil
	}
	var once sync.Once
	var barrierErr error
	return func(point writecoordinator.PersistenceFaultPoint) error {
		if point != writecoordinator.FaultBeforeFinishCommittedMutation {
			return nil
		}
		armPath := filepath.Join(directory, "file-restore-barrier.arm.json")
		raw, err := os.ReadFile(armPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var arm e2eFileRestoreArm
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&arm); err != nil {
			return err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("invalid Restore barrier arm trailing data")
		}
		for _, id := range []string{arm.WorkspaceID, arm.OperationID, arm.DocumentID,
			arm.HistoricalRevisionID, arm.ExpectedEffectiveRevisionID} {
			parsed, err := uuid.Parse(id)
			if err != nil || parsed.String() != id {
				return errors.New("invalid Restore barrier identity")
			}
		}
		if arm.WorkspaceID != workspaceID || arm.RelativePath != "restore-crash-44.txt" || arm.ObjectID == "" {
			return errors.New("invalid Restore barrier binding")
		}
		ready, matched, err := inspectE2EFileRestorePublication(dataDir, arm)
		if err != nil || !matched {
			return err
		}
		once.Do(func() {
			if err := os.Rename(armPath, filepath.Join(directory, "file-restore-barrier.claimed.json")); err != nil {
				barrierErr = err
				return
			}
			ready.ProcessID, ready.Point = os.Getpid(), point
			raw, barrierErr = json.Marshal(ready)
			if barrierErr != nil {
				return
			}
			readyPath := filepath.Join(directory, "file-restore-barrier.ready.json")
			if barrierErr = os.WriteFile(readyPath+".tmp", append(raw, '\n'), 0o600); barrierErr != nil {
				return
			}
			if barrierErr = os.Rename(readyPath+".tmp", readyPath); barrierErr != nil {
				return
			}
			barrierErr = waitForE2EMutationBarrierRelease(filepath.Join(directory, "file-restore-barrier.release"))
		})
		return barrierErr
	}
}

func openE2EFileRestoreReadOnly(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Match the proven file: URI form in pluginstore's legacy reader. A bare
	// filename with ?mode=ro is not a SQLite URI and can create a new database.
	uriPath := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(filepath.ToSlash(absolute))
	db, err := sql.Open("sqlite", "file:"+uriPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func inspectE2EFileRestorePublication(dataDir string, arm e2eFileRestoreArm) (e2eFileRestoreReady, bool, error) {
	ready := e2eFileRestoreReady{e2eFileRestoreArm: arm}
	metadata := filepath.Dir(dataDir)
	head, err := openE2EFileRestoreReadOnly(filepath.Join(metadata, "topology", "filehistory-head.db"))
	if err != nil {
		return ready, false, err
	}
	defer head.Close()
	var method string
	var result []byte
	err = head.QueryRow(`SELECT h.mutation_revision, h.session_epoch, h.fence_epoch,
		h.claim_id, r.method, r.result_json FROM filehistory_heads h
		JOIN filehistory_operation_receipts r ON r.workspace_id = h.workspace_id
		WHERE h.workspace_id = ? AND r.operation_id = ?`, arm.WorkspaceID, arm.OperationID).Scan(
		&ready.MutationRevision, &ready.SessionEpoch, &ready.FenceEpoch, &ready.ClaimID, &method, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return ready, false, nil
	}
	if err != nil {
		return ready, false, err
	}
	if method != "fileHistory.restore" {
		return ready, false, nil
	}
	ready.Result = json.RawMessage(result)
	coordination, err := openE2EFileRestoreReadOnly(filepath.Join(metadata, "coordination", "write-coordinator.db"))
	if err != nil {
		return ready, false, err
	}
	defer coordination.Close()
	var prepared int
	err = coordination.QueryRow(`SELECT COUNT(*) FROM mutation_intents
		WHERE state = 'prepared' AND mutation_revision = ? AND workspace_id = ?
		AND session_epoch = ? AND fence_epoch = ? AND claim_id = ?`,
		ready.MutationRevision, arm.WorkspaceID, ready.SessionEpoch, ready.FenceEpoch, ready.ClaimID).Scan(&prepared)
	if err != nil || prepared != 1 {
		return ready, false, err
	}
	raw, err := os.ReadFile(filepath.Join(metadata, "coordination", "file-materializer", "journal.json"))
	if err != nil {
		return ready, false, err
	}
	var journal struct {
		WorkspaceID      string `json:"workspaceId"`
		MutationRevision uint64 `json:"mutationRevision"`
		SessionEpoch     uint64 `json:"sessionEpoch"`
		FenceEpoch       uint64 `json:"fenceEpoch"`
		ClaimID          string `json:"claimId"`
		State            string `json:"state"`
		Operations       []struct {
			Path     string `json:"path"`
			Desired  bool   `json:"desired"`
			ObjectID string `json:"objectId"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &journal); err != nil {
		return ready, false, err
	}
	matched := journal.WorkspaceID == arm.WorkspaceID && journal.MutationRevision == ready.MutationRevision &&
		journal.SessionEpoch == ready.SessionEpoch && journal.FenceEpoch == ready.FenceEpoch &&
		journal.ClaimID == ready.ClaimID && journal.State == "applied"
	operationMatched := false
	for _, operation := range journal.Operations {
		if operation.Path == arm.RelativePath && operation.Desired && operation.ObjectID == arm.ObjectID {
			operationMatched = true
		}
	}
	return ready, matched && operationMatched, nil
}
