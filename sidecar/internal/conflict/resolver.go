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
	Both    Side = "both"
)

type ItemKind string

const (
	FileItem  ItemKind = "file"
	TableItem ItemKind = "table"
)

type FileState struct {
	DocumentID string `json:"documentId"`
	Path       string `json:"path"`
	ContentID  string `json:"contentId"`
	Deleted    bool   `json:"deleted"`
}

// TableState is an immutable whole-table candidate. Each component is kept
// separate so a dependency scanner can prove that schema, records, views and
// in-table attachments all came from the same frozen candidate.
type TableState struct {
	TableID             string `json:"tableId"`
	DisplayName         string `json:"displayName"`
	SchemaObjectID      string `json:"schemaObjectId"`
	RecordsObjectID     string `json:"recordsObjectId"`
	ViewsObjectID       string `json:"viewsObjectId"`
	AttachmentsObjectID string `json:"attachmentsObjectId"`
	Deleted             bool   `json:"deleted"`
}

type Candidate struct {
	SnapshotID               string                `json:"snapshotId"`
	BusinessDatabaseObjectID string                `json:"businessDatabaseObjectId,omitempty"`
	Files                    map[string]FileState  `json:"files"`
	Tables                   map[string]TableState `json:"tables,omitempty"`
	Revision                 uint64                `json:"revision"`
}

type FileConflict struct {
	DocumentID string    `json:"documentId"`
	Base       FileState `json:"base"`
	Local      FileState `json:"local"`
	Replica    FileState `json:"replica"`
}

type TableConflict struct {
	TableID string     `json:"tableId"`
	Base    TableState `json:"base"`
	Local   TableState `json:"local"`
	Replica TableState `json:"replica"`
}

type Plan struct {
	PlanID          string          `json:"planId"`
	BaseSnapshot    string          `json:"baseSnapshot"`
	LocalSnapshot   string          `json:"localSnapshot"`
	ReplicaSnapshot string          `json:"replicaSnapshot"`
	LocalRevision   uint64          `json:"localRevision"`
	BaseHash        string          `json:"baseHash"`
	LocalHash       string          `json:"localHash"`
	ReplicaHash     string          `json:"replicaHash"`
	Files           []FileConflict  `json:"files"`
	Automatic       []FileState     `json:"automatic"`
	Tables          []TableConflict `json:"tables,omitempty"`
	AutomaticTables []TableState    `json:"automaticTables,omitempty"`
}

type Resolution struct {
	Choices map[string]Side `json:"choices"`
}

type ResolvedChange struct {
	Kind          ItemKind   `json:"kind"`
	ItemID        string     `json:"itemId"`
	DocumentID    string     `json:"documentId"`
	Previous      FileState  `json:"previous"`
	Chosen        FileState  `json:"chosen"`
	TablePrevious TableState `json:"tablePrevious,omitempty"`
	TableChosen   TableState `json:"tableChosen,omitempty"`
	Reason        string     `json:"reason"`
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
	tableIDs := map[string]struct{}{}
	for id := range base.Tables {
		tableIDs[id] = struct{}{}
	}
	for id := range local.Tables {
		tableIDs[id] = struct{}{}
	}
	for id := range replica.Tables {
		tableIDs[id] = struct{}{}
	}
	var tableConflicts []TableConflict
	var automaticTables []TableState
	for id := range tableIDs {
		b, l, r := base.Tables[id], local.Tables[id], replica.Tables[id]
		localChanged := l != b
		replicaChanged := r != b
		if localChanged && replicaChanged && l != r {
			tableConflicts = append(tableConflicts, TableConflict{
				TableID: id, Base: b, Local: l, Replica: r,
			})
		} else if !localChanged && replicaChanged {
			automaticTables = append(automaticTables, r)
		}
	}
	sort.Slice(tableConflicts, func(i, j int) bool {
		return tableConflicts[i].TableID < tableConflicts[j].TableID
	})
	sort.Slice(automaticTables, func(i, j int) bool {
		return automaticTables[i].TableID < automaticTables[j].TableID
	})
	plan := Plan{
		BaseSnapshot: base.SnapshotID, LocalSnapshot: local.SnapshotID,
		ReplicaSnapshot: replica.SnapshotID, LocalRevision: local.Revision,
		BaseHash: candidateHash(base), LocalHash: candidateHash(local),
		ReplicaHash: candidateHash(replica), Files: conflicts, Automatic: automatic,
		Tables: tableConflicts, AutomaticTables: automaticTables,
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
	if len(resolution.Choices) != len(plan.Files)+len(plan.Tables) {
		return nil, errors.New("conflict.choice_missing")
	}
	changes := make(
		[]ResolvedChange,
		0,
		len(plan.Automatic)+len(plan.Files)+
			len(plan.AutomaticTables)+len(plan.Tables),
	)
	for _, state := range plan.Automatic {
		changes = append(changes, ResolvedChange{
			Kind: FileItem, ItemID: state.DocumentID,
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
		} else if side != Local && side != Both {
			return nil, errors.New("conflict.choice_invalid")
		}
		reason := "user-choice"
		if side == Both {
			// The local leaf remains effective and the independently pinned
			// replica candidate remains available as a recovery Snapshot. The
			// workspace adapter must never overwrite either side.
			reason = "keep-both-recovery"
		}
		changes = append(changes, ResolvedChange{
			Kind: FileItem, ItemID: conflict.DocumentID,
			DocumentID: conflict.DocumentID,
			Previous:   conflict.Local,
			Chosen:     chosen,
			Reason:     reason,
		})
	}
	for _, state := range plan.AutomaticTables {
		changes = append(changes, ResolvedChange{
			Kind: TableItem, ItemID: state.TableID,
			TableChosen: state, Reason: "replica-only",
		})
	}
	for _, conflict := range plan.Tables {
		side, ok := resolution.Choices[conflict.TableID]
		if !ok || side == Both {
			return nil, errors.New("conflict.choice_invalid")
		}
		chosen := conflict.Local
		if side == Replica {
			chosen = conflict.Replica
		} else if side != Local {
			return nil, errors.New("conflict.choice_invalid")
		}
		changes = append(changes, ResolvedChange{
			Kind: TableItem, ItemID: conflict.TableID,
			TablePrevious: conflict.Local, TableChosen: chosen,
			Reason: "user-choice",
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
		SnapshotID string      `json:"snapshotId"`
		DatabaseID string      `json:"businessDatabaseObjectId"`
		Revision   uint64      `json:"revision"`
		Files      []item      `json:"files"`
		Tables     []tableItem `json:"tables"`
	}{
		candidate.SnapshotID,
		candidate.BusinessDatabaseObjectID,
		candidate.Revision,
		items,
		sortedTableItems(candidate.Tables),
	})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type tableItem struct {
	ID    string     `json:"id"`
	State TableState `json:"state"`
}

func sortedTableItems(tables map[string]TableState) []tableItem {
	items := make([]tableItem, 0, len(tables))
	for id, state := range tables {
		items = append(items, tableItem{ID: id, State: state})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})
	return items
}

func planHash(plan Plan) string {
	plan.PlanID = ""
	raw, _ := json.Marshal(plan)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
