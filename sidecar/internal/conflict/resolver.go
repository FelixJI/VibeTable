package conflict

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

var ErrStalePlan = errors.New("conflict.stale_plan")

type Side string

const (
	Local   Side = "local"
	Replica Side = "replica"
)

type FileState struct {
	DocumentID string `json:"documentId"`
	Path       string `json:"path"`
	ContentID  string `json:"contentId"`
	Deleted    bool   `json:"deleted"`
}

type Candidate struct {
	SnapshotID string               `json:"snapshotId"`
	Files      map[string]FileState `json:"files"`
	Revision   uint64               `json:"revision"`
}

type FileConflict struct {
	DocumentID string    `json:"documentId"`
	Base       FileState `json:"base"`
	Local      FileState `json:"local"`
	Replica    FileState `json:"replica"`
}

type Plan struct {
	PlanID          string         `json:"planId"`
	BaseSnapshot    string         `json:"baseSnapshot"`
	LocalSnapshot   string         `json:"localSnapshot"`
	ReplicaSnapshot string         `json:"replicaSnapshot"`
	LocalRevision   uint64         `json:"localRevision"`
	BaseHash        string         `json:"baseHash"`
	LocalHash       string         `json:"localHash"`
	ReplicaHash     string         `json:"replicaHash"`
	Files           []FileConflict `json:"files"`
	Automatic       []FileState    `json:"automatic"`
}

type Resolution struct {
	Choices map[string]Side `json:"choices"`
}

type ResolvedChange struct {
	DocumentID string    `json:"documentId"`
	Previous   FileState `json:"previous"`
	Chosen     FileState `json:"chosen"`
	Reason     string    `json:"reason"`
}

type AtomicAppender interface {
	// ApplyAtomically must stage every revision and the discarded recovery
	// snapshot, validate relationships, then publish them through one gated
	// CAS. On error it must leave no effective revision changed.
	ApplyAtomically(plan Plan, changes []ResolvedChange, discarded Candidate) (string, error)
}

func BuildPlan(base, local, replica Candidate) Plan {
	ids := map[string]struct{}{}
	for id := range base.Files {
		ids[id] = struct{}{}
	}
	for id := range local.Files {
		ids[id] = struct{}{}
	}
	for id := range replica.Files {
		ids[id] = struct{}{}
	}
	var conflicts []FileConflict
	var automatic []FileState
	for id := range ids {
		b, l, r := base.Files[id], local.Files[id], replica.Files[id]
		localChanged := l != b
		replicaChanged := r != b
		if localChanged && replicaChanged && l != r {
			conflicts = append(conflicts, FileConflict{DocumentID: id, Base: b, Local: l, Replica: r})
		} else if !localChanged && replicaChanged {
			automatic = append(automatic, r)
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].DocumentID < conflicts[j].DocumentID })
	sort.Slice(automatic, func(i, j int) bool { return automatic[i].DocumentID < automatic[j].DocumentID })
	plan := Plan{
		BaseSnapshot: base.SnapshotID, LocalSnapshot: local.SnapshotID,
		ReplicaSnapshot: replica.SnapshotID, LocalRevision: local.Revision,
		BaseHash: candidateHash(base), LocalHash: candidateHash(local),
		ReplicaHash: candidateHash(replica), Files: conflicts, Automatic: automatic,
	}
	plan.PlanID = planHash(plan)
	return plan
}

func Apply(
	plan Plan,
	currentLocal Candidate,
	replica Candidate,
	resolution Resolution,
	appender AtomicAppender,
) (string, error) {
	changes, err := ResolveChanges(
		plan,
		currentLocal,
		replica,
		resolution,
	)
	if err != nil {
		return "", err
	}
	return appender.ApplyAtomically(plan, changes, replica)
}

func ResolveChanges(
	plan Plan,
	currentLocal Candidate,
	replica Candidate,
	resolution Resolution,
) ([]ResolvedChange, error) {
	if currentLocal.SnapshotID != plan.LocalSnapshot || currentLocal.Revision != plan.LocalRevision ||
		replica.SnapshotID != plan.ReplicaSnapshot ||
		candidateHash(currentLocal) != plan.LocalHash ||
		candidateHash(replica) != plan.ReplicaHash ||
		planHash(plan) != plan.PlanID {
		return nil, ErrStalePlan
	}
	if len(resolution.Choices) != len(plan.Files) {
		return nil, errors.New("conflict.choice_missing")
	}
	changes := make([]ResolvedChange, 0, len(plan.Automatic)+len(plan.Files))
	for _, state := range plan.Automatic {
		changes = append(changes, ResolvedChange{
			DocumentID: state.DocumentID,
			Previous: stateForDocument(
				currentLocal, state.DocumentID,
			),
			Chosen: state,
			Reason: "replica-only",
		})
	}
	for _, conflict := range plan.Files {
		side, ok := resolution.Choices[conflict.DocumentID]
		if !ok {
			return nil, errors.New("conflict.choice_missing")
		}
		chosen := conflict.Local
		if side == Replica {
			chosen = conflict.Replica
		} else if side != Local {
			return nil, errors.New("conflict.choice_invalid")
		}
		changes = append(changes, ResolvedChange{
			DocumentID: conflict.DocumentID,
			Previous:   conflict.Local,
			Chosen:     chosen,
			Reason:     "user-choice",
		})
	}
	return changes, nil
}

func stateForDocument(
	candidate Candidate,
	documentID string,
) FileState {
	state := candidate.Files[documentID]
	if state.DocumentID == "" {
		state.DocumentID = documentID
	}
	return state
}

func candidateHash(candidate Candidate) string {
	type item struct {
		ID    string    `json:"id"`
		State FileState `json:"state"`
	}
	items := make([]item, 0, len(candidate.Files))
	for id, state := range candidate.Files {
		items = append(items, item{ID: id, State: state})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	raw, _ := json.Marshal(struct {
		SnapshotID string `json:"snapshotId"`
		Revision   uint64 `json:"revision"`
		Files      []item `json:"files"`
	}{candidate.SnapshotID, candidate.Revision, items})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func planHash(plan Plan) string {
	plan.PlanID = ""
	raw, _ := json.Marshal(plan)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
