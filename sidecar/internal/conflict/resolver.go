package conflict

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
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
	FileItem     ItemKind = "file"
	TableItem    ItemKind = "table"
	SettingsItem ItemKind = "settings"
)

const WorkspaceSettingsItemID = "workspace-settings"

type FileState struct {
	DocumentID string `json:"documentId"`
	Path       string `json:"path"`
	ContentID  string `json:"contentId"`
	MimeType   string `json:"mimeType"`
	Deleted    bool   `json:"deleted"`
}

// TableState is an immutable whole-table candidate. Each component is kept
// separate so a dependency scanner can prove that schema, records, views and
// in-table attachments all came from the same frozen candidate.
type TableState struct {
	TableID             string            `json:"tableId"`
	DisplayName         string            `json:"displayName"`
	DatabaseObjectID    string            `json:"databaseObjectId"`
	SchemaObjectID      string            `json:"schemaObjectId"`
	RecordsObjectID     string            `json:"recordsObjectId"`
	ViewsObjectID       string            `json:"viewsObjectId"`
	AttachmentsObjectID string            `json:"attachmentsObjectId"`
	AttachmentObjects   map[string]string `json:"attachmentObjects,omitempty"`
	Deleted             bool              `json:"deleted"`
}

type SettingsState struct {
	ObjectID string `json:"objectId"`
}

type Candidate struct {
	SnapshotID               string                `json:"snapshotId"`
	BusinessDatabaseObjectID string                `json:"businessDatabaseObjectId,omitempty"`
	Settings                 SettingsState         `json:"settings"`
	AttachmentObjects        map[string]string     `json:"attachmentObjects,omitempty"`
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

type SettingsConflict struct {
	ItemID  string        `json:"itemId"`
	Base    SettingsState `json:"base"`
	Local   SettingsState `json:"local"`
	Replica SettingsState `json:"replica"`
}

type Plan struct {
	PlanID           string            `json:"planId"`
	BaseSnapshot     string            `json:"baseSnapshot"`
	LocalSnapshot    string            `json:"localSnapshot"`
	ReplicaSnapshot  string            `json:"replicaSnapshot"`
	LocalRevision    uint64            `json:"localRevision"`
	BaseHash         string            `json:"baseHash"`
	LocalHash        string            `json:"localHash"`
	ReplicaHash      string            `json:"replicaHash"`
	Files            []FileConflict    `json:"files"`
	Automatic        []FileState       `json:"automatic"`
	Tables           []TableConflict   `json:"tables,omitempty"`
	AutomaticTables  []TableState      `json:"automaticTables,omitempty"`
	Settings         *SettingsConflict `json:"settings,omitempty"`
	AutomaticSetting *SettingsState    `json:"automaticSetting,omitempty"`
}

type Resolution struct {
	Choices map[string]Side `json:"choices"`
}

type ResolvedChange struct {
	Kind             ItemKind      `json:"kind"`
	ItemID           string        `json:"itemId"`
	DocumentID       string        `json:"documentId"`
	Previous         FileState     `json:"previous"`
	Chosen           FileState     `json:"chosen"`
	TablePrevious    TableState    `json:"tablePrevious,omitempty"`
	TableChosen      TableState    `json:"tableChosen,omitempty"`
	SettingsPrevious SettingsState `json:"settingsPrevious,omitempty"`
	SettingsChosen   SettingsState `json:"settingsChosen,omitempty"`
	Copy             *FileState    `json:"copy,omitempty"`
	Reason           string        `json:"reason"`
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
		localChanged := !reflect.DeepEqual(l, b)
		replicaChanged := !reflect.DeepEqual(r, b)
		if localChanged && replicaChanged && !reflect.DeepEqual(l, r) {
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
		b := stateForTable(base, id)
		l := stateForTable(local, id)
		r := stateForTable(replica, id)
		localChanged := !reflect.DeepEqual(l, b)
		replicaChanged := !reflect.DeepEqual(r, b)
		if localChanged && replicaChanged && !reflect.DeepEqual(l, r) {
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
	var (
		settingsConflict *SettingsConflict
		automaticSetting *SettingsState
	)
	localSettingsChanged := local.Settings != base.Settings
	replicaSettingsChanged := replica.Settings != base.Settings
	if localSettingsChanged && replicaSettingsChanged &&
		local.Settings != replica.Settings {
		settingsConflict = &SettingsConflict{
			ItemID:  WorkspaceSettingsItemID,
			Base:    base.Settings,
			Local:   local.Settings,
			Replica: replica.Settings,
		}
	} else if !localSettingsChanged && replicaSettingsChanged {
		selected := replica.Settings
		automaticSetting = &selected
	}
	plan := Plan{
		BaseSnapshot: base.SnapshotID, LocalSnapshot: local.SnapshotID,
		ReplicaSnapshot: replica.SnapshotID, LocalRevision: local.Revision,
		BaseHash: candidateHash(base), LocalHash: candidateHash(local),
		ReplicaHash: candidateHash(replica), Files: conflicts, Automatic: automatic,
		Tables: tableConflicts, AutomaticTables: automaticTables,
		Settings: settingsConflict, AutomaticSetting: automaticSetting,
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
	expectedChoices := len(plan.Files) + len(plan.Tables)
	if plan.Settings != nil {
		expectedChoices++
	}
	if len(resolution.Choices) != expectedChoices {
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
			if conflict.Local.Deleted || conflict.Replica.Deleted {
				return nil, errors.New("conflict.choice_invalid")
			}
			// The local leaf remains effective and the replica leaf is ingested
			// as a second document by the workspace adapter. Both source
			// snapshots remain independently pinned as recovery points.
			copyState := conflict.Replica
			changes = append(changes, ResolvedChange{
				Kind: FileItem, ItemID: conflict.DocumentID,
				DocumentID: conflict.DocumentID,
				Previous:   conflict.Local, Chosen: chosen,
				Copy: &copyState, Reason: "keep-both-documents",
			})
			continue
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
			TablePrevious: stateForTable(currentLocal, state.TableID),
			TableChosen:   state, Reason: "replica-only",
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
	if plan.AutomaticSetting != nil {
		changes = append(changes, ResolvedChange{
			Kind: SettingsItem, ItemID: WorkspaceSettingsItemID,
			SettingsPrevious: currentLocal.Settings,
			SettingsChosen:   *plan.AutomaticSetting,
			Reason:           "replica-only",
		})
	}
	if plan.Settings != nil {
		side, ok := resolution.Choices[plan.Settings.ItemID]
		if !ok || side == Both {
			return nil, errors.New("conflict.choice_invalid")
		}
		chosen := plan.Settings.Local
		if side == Replica {
			chosen = plan.Settings.Replica
		} else if side != Local {
			return nil, errors.New("conflict.choice_invalid")
		}
		changes = append(changes, ResolvedChange{
			Kind: SettingsItem, ItemID: plan.Settings.ItemID,
			SettingsPrevious: plan.Settings.Local,
			SettingsChosen:   chosen,
			Reason:           "user-choice",
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

func stateForTable(candidate Candidate, tableID string) TableState {
	state, exists := candidate.Tables[tableID]
	if !exists {
		return TableState{TableID: tableID, Deleted: true}
	}
	if state.TableID == "" {
		state.TableID = tableID
	}
	return state
}

func candidateHash(candidate Candidate) string {
	type item struct {
		ID    string    `json:"id"`
		State FileState `json:"state"`
	}
	type attachmentItem struct {
		Key      string `json:"key"`
		ObjectID string `json:"objectId"`
	}
	items := make([]item, 0, len(candidate.Files))
	for id, state := range candidate.Files {
		items = append(items, item{ID: id, State: state})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	attachments := make(
		[]attachmentItem, 0, len(candidate.AttachmentObjects),
	)
	for key, objectID := range candidate.AttachmentObjects {
		attachments = append(attachments, attachmentItem{
			Key: key, ObjectID: objectID,
		})
	}
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].Key < attachments[j].Key
	})
	raw, _ := json.Marshal(struct {
		SnapshotID  string           `json:"snapshotId"`
		DatabaseID  string           `json:"businessDatabaseObjectId"`
		Settings    SettingsState    `json:"settings"`
		Attachments []attachmentItem `json:"attachmentObjects"`
		Revision    uint64           `json:"revision"`
		Files       []item           `json:"files"`
		Tables      []tableItem      `json:"tables"`
	}{
		candidate.SnapshotID,
		candidate.BusinessDatabaseObjectID,
		candidate.Settings,
		attachments,
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
