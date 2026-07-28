package workspacev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const (
	keyRotationIntentName      = "key-rotation-intent.json"
	keyRotationIntentRequested = "requested"
	keyRotationIntentCompleted = "completed"
)

type keyRotationHostIntent struct {
	FormatVersion        int             `json:"formatVersion"`
	OperationID          string          `json:"operationId"`
	PlanID               string          `json:"planId"`
	WorkspaceID          string          `json:"workspaceId"`
	SessionEpoch         uint64          `json:"sessionEpoch"`
	FenceEpoch           uint64          `json:"fenceEpoch"`
	ClaimID              string          `json:"claimId"`
	ProtectionSnapshotID string          `json:"protectionSnapshotId"`
	State                string          `json:"state"`
	Sequence             uint64          `json:"sequence"`
	Method               string          `json:"method"`
	Scope                string          `json:"scope"`
	RequestHash          string          `json:"requestHash"`
	Result               json.RawMessage `json:"result"`
}

func keyRotationIntentPath(paths workspacePaths) string {
	return filepath.Join(paths.coordination, keyRotationIntentName)
}

func writeKeyRotationIntent(
	paths workspacePaths,
	intent keyRotationHostIntent,
) error {
	if err := validateKeyRotationIntent(intent); err != nil {
		return err
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(
		paths.coordination,
		".key-rotation-intent-*.tmp",
	)
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceGrantedFile(name, keyRotationIntentPath(paths))
}

func readKeyRotationIntent(
	paths workspacePaths,
) (keyRotationHostIntent, bool, error) {
	raw, err := readFileBounded(keyRotationIntentPath(paths), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return keyRotationHostIntent{}, false, nil
	}
	if err != nil {
		return keyRotationHostIntent{}, false, err
	}
	var intent keyRotationHostIntent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return keyRotationHostIntent{}, false,
			errors.New("repository.key_rotation_intent_corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return keyRotationHostIntent{}, false,
			errors.New("repository.key_rotation_intent_corrupt")
	}
	if err := validateKeyRotationIntent(intent); err != nil {
		return keyRotationHostIntent{}, false, err
	}
	return intent, true, nil
}

func validateKeyRotationIntent(intent keyRotationHostIntent) error {
	if intent.FormatVersion != 1 ||
		!validUUID(intent.OperationID) ||
		!validUUID(intent.PlanID) ||
		!validUUID(intent.WorkspaceID) ||
		!validUUID(intent.ClaimID) ||
		!validUUID(intent.ProtectionSnapshotID) ||
		intent.SessionEpoch == 0 ||
		intent.FenceEpoch == 0 ||
		intent.Sequence == 0 ||
		intent.Method != "repository.applyKeyRotation" ||
		intent.Scope != string(protocolv2.WorkspaceScope) ||
		intent.RequestHash == "" ||
		!json.Valid(intent.Result) {
		return errors.New("repository.key_rotation_intent_corrupt")
	}
	if intent.State != keyRotationIntentRequested &&
		intent.State != keyRotationIntentCompleted {
		return errors.New("repository.key_rotation_intent_corrupt")
	}
	return nil
}

func ensureKeyRotationStartupReady(
	paths workspacePaths,
	workspaceID string,
) error {
	intent, found, err := readKeyRotationIntent(paths)
	if err != nil || !found {
		return err
	}
	if intent.WorkspaceID != workspaceID {
		return errors.New("repository.key_rotation_intent_corrupt")
	}
	if intent.State != keyRotationIntentCompleted {
		return errors.New("repository.key_rotation_pending")
	}
	return nil
}

func recoverCompletedKeyRotationReceipt(
	ctx context.Context,
	paths workspacePaths,
	store *stateStore,
) error {
	intent, found, err := readKeyRotationIntent(paths)
	if err != nil || !found {
		return err
	}
	if intent.State != keyRotationIntentCompleted {
		return errors.New("repository.key_rotation_pending")
	}
	if err := store.commitOperationReceipt(
		ctx,
		protocolv2.Session{
			WorkspaceID: intent.WorkspaceID,
			Epoch:       intent.SessionEpoch,
			Sequence:    intent.Sequence,
		},
		protocolv2.OperationReceipt{
			OperationID: intent.OperationID,
			WorkspaceID: intent.WorkspaceID,
			Method:      intent.Method,
			Scope:       protocolv2.ScopeKind(intent.Scope),
			RequestHash: intent.RequestHash,
			Result:      append(json.RawMessage(nil), intent.Result...),
		},
	); err != nil {
		return err
	}
	if journal, found, journalErr := readKeyRotationJournal(paths); journalErr != nil {
		return journalErr
	} else if found {
		if journal.WorkspaceID != intent.WorkspaceID ||
			journal.Phase != keyRotationVault {
			return errors.New("repository.rotation_journal_corrupt")
		}
		if err := cleanupKeyRotation(paths, journal.RotationID); err != nil {
			return err
		}
	}
	err = os.Remove(keyRotationIntentPath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func tokenMatchesRotationPlan(
	token writecoordinator.Token,
	plan repositoryKeyRotationPlan,
) bool {
	return token.SessionEpoch == plan.SessionEpoch &&
		token.FenceEpoch == plan.FenceEpoch &&
		token.ClaimID == plan.ClaimID
}
