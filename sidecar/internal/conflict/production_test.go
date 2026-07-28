package conflict

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

type stagedTestAppender struct {
	stages       map[string]ApplyStage
	receipts     map[string]ApplyReceipt
	stageFault   error
	commitFault  error
	commitCount  int
	visibleCount int
}

func newStagedTestAppender() *stagedTestAppender {
	return &stagedTestAppender{
		stages:   map[string]ApplyStage{},
		receipts: map[string]ApplyReceipt{},
	}
}

func (appender *stagedTestAppender) Stage(
	_ context.Context,
	operationID string,
	planID string,
	_ Plan,
	_ []ResolvedChange,
	_ Candidate,
) (ApplyStage, error) {
	if appender.stageFault != nil {
		return ApplyStage{}, appender.stageFault
	}
	stage := ApplyStage{
		StageID:     "stage-" + operationID,
		OperationID: operationID,
		PlanID:      planID,
	}
	appender.stages[operationID] = stage
	return stage, nil
}

func TestProductionConflictInvalidStageDiscardsPreparedPlan(t *testing.T) {
	engine, err := OpenEngine(filepath.Join(t.TempDir(), "conflicts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	set := productionSet(true)
	set.ConflictID = "conflict-invalid-stage"
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{{ItemID: "doc", Kind: FileItem, Side: Local}},
	)
	if err != nil || !first.Valid {
		t.Fatalf("first preview = %#v, %v", first, err)
	}
	appender := newStagedTestAppender()
	appender.stageFault = ErrResolutionInvalid
	if _, err := engine.Apply(
		context.Background(), first.PlanID, "operation-invalid", appender,
	); !errors.Is(err, ErrResolutionInvalid) {
		t.Fatalf("invalid apply error = %v", err)
	}
	second, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{{ItemID: "doc", Kind: FileItem, Side: Replica}},
	)
	if err != nil || !second.Valid || second.PlanID == first.PlanID {
		t.Fatalf("replacement preview = %#v, %v", second, err)
	}
}

func TestProductionConflictStaleStageRequiresReplanWithoutActivePlan(
	t *testing.T,
) {
	path := filepath.Join(t.TempDir(), "conflicts.db")
	engine, err := OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	set := productionSet(true)
	set.ConflictID = "conflict-stale-stage"
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	preview, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{{ItemID: "doc", Kind: FileItem, Side: Local}},
	)
	if err != nil {
		t.Fatal(err)
	}
	appender := newStagedTestAppender()
	appender.stageFault = ErrStalePlan
	if _, err := engine.Apply(
		context.Background(), preview.PlanID, "operation-stale", appender,
	); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("stale apply error = %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, err := reopened.Inspect(
		context.Background(), set.ConflictID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != StatePending || !current.ReplanRequired {
		t.Fatalf("stale set = %#v", current)
	}
	if err := reopened.Recover(context.Background(), appender); err != nil {
		t.Fatalf("startup recovery blocked: %v", err)
	}
}

func (appender *stagedTestAppender) Commit(
	_ context.Context,
	stage ApplyStage,
) (ApplyReceipt, error) {
	appender.commitCount++
	if appender.commitFault != nil {
		err := appender.commitFault
		appender.commitFault = nil
		return ApplyReceipt{}, err
	}
	if receipt, exists := appender.receipts[stage.OperationID]; exists {
		return receipt, nil
	}
	appender.visibleCount++
	receipt := ApplyReceipt{
		OperationID:         stage.OperationID,
		State:               "applied",
		RecoverySnapshotIDs: []string{"replica"},
		AuthorityRevision:   8,
	}
	appender.receipts[stage.OperationID] = receipt
	return receipt, nil
}

func (appender *stagedTestAppender) Probe(
	_ context.Context,
	operationID string,
) (ApplyReceipt, bool, error) {
	receipt, found := appender.receipts[operationID]
	return receipt, found, nil
}

func productionSet(complete bool) Set {
	base := Candidate{
		SnapshotID: "base",
		Revision:   1,
		Files: map[string]FileState{
			"doc": {
				DocumentID: "doc",
				Path:       "a.txt",
				ContentID:  "base",
			},
		},
	}
	local := Candidate{
		SnapshotID: "local",
		Revision:   7,
		Files: map[string]FileState{
			"doc": {
				DocumentID: "doc",
				Path:       "a.txt",
				ContentID:  "local",
			},
		},
	}
	remote := Candidate{
		SnapshotID: "replica",
		Revision:   3,
		Files: map[string]FileState{
			"doc": {
				DocumentID: "doc",
				Path:       "a.txt",
				ContentID:  "remote",
			},
		},
	}
	return Set{
		ConflictID:  "conflict-1",
		WorkspaceID: "workspace-1",
		State:       StatePending,
		Revision:    1,
		Base:        base,
		Local:       local,
		Replica:     remote,
		Dependencies: DependencyGraph{
			Complete: complete,
			Edges:    map[string][]string{"doc": {}},
		},
		RootPinIDs: []string{"pin-local", "pin-replica"},
		CreatedAt: time.Date(
			2026, 7, 28, 0, 0, 0, 0, time.UTC,
		),
	}
}

func TestProductionConflictRejectsIncompleteDependencyClosure(t *testing.T) {
	engine, err := OpenEngine(filepath.Join(t.TempDir(), "conflicts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	set := productionSet(false)
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	preview, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{{DocumentID: "doc", Side: Local}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid ||
		len(preview.Diagnostics) != 1 ||
		preview.Diagnostics[0] != ErrDependencyIncomplete.Error() {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestProductionConflictRequiresCompleteTypedChoicesAndAcceptsFileBoth(t *testing.T) {
	engine, err := OpenEngine(filepath.Join(t.TempDir(), "conflicts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	set := productionSet(true)
	set.ConflictID = "conflict-typed-both"
	set.Base.Files["doc-2"] = FileState{
		DocumentID: "doc-2", Path: "b.txt", ContentID: "base-2",
	}
	set.Local.Files["doc-2"] = FileState{
		DocumentID: "doc-2", Path: "b.txt", ContentID: "local-2",
	}
	set.Replica.Files["doc-2"] = FileState{
		DocumentID: "doc-2", Path: "b.txt", ContentID: "replica-2",
	}
	set.Dependencies.Edges["doc-2"] = []string{}
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}

	partial, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{{ItemID: "doc", Kind: FileItem, Side: Both}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Valid ||
		len(partial.Diagnostics) != 1 ||
		partial.Diagnostics[0] != "conflict.choice_missing" {
		t.Fatalf("partial preview = %#v", partial)
	}

	preview, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{
			{ItemID: "doc", Kind: FileItem, Side: Both},
			{ItemID: "doc-2", Kind: FileItem, Side: Replica},
		},
	)
	if err != nil || !preview.Valid || len(preview.Diagnostics) != 0 {
		t.Fatalf("typed keep-both preview = %#v, %v", preview, err)
	}
}

func TestProductionConflictAcceptsIsolatedFileAndSettingsChoices(t *testing.T) {
	engine, err := OpenEngine(filepath.Join(t.TempDir(), "conflicts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	set := productionSet(true)
	set.ConflictID = "conflict-file-settings"
	set.Base.Settings = SettingsState{ObjectID: "settings-base"}
	set.Local.Settings = SettingsState{ObjectID: "settings-local"}
	set.Replica.Settings = SettingsState{ObjectID: "settings-replica"}
	set.Dependencies.Edges[WorkspaceSettingsItemID] = []string{}
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}

	preview, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{
			{ItemID: "doc", Kind: FileItem, Side: Local},
			{
				ItemID: WorkspaceSettingsItemID,
				Kind:   SettingsItem,
				Side:   Replica,
			},
		},
	)
	if err != nil || !preview.Valid || len(preview.Diagnostics) != 0 {
		t.Fatalf("file+settings preview = %#v, %v", preview, err)
	}

	invalid, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{
			{ItemID: "doc", Kind: FileItem, Side: Local},
			{
				ItemID: WorkspaceSettingsItemID,
				Kind:   SettingsItem,
				Side:   Both,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if invalid.Valid ||
		len(invalid.Diagnostics) != 1 ||
		invalid.Diagnostics[0] != "conflict.choice_invalid" {
		t.Fatalf("settings both preview = %#v", invalid)
	}
}

func TestInvalidPreviewReceiptSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflicts.db")
	engine, err := OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	set := productionSet(false)
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	preview, err := engine.PreviewWithReceipt(
		context.Background(),
		set.ConflictID,
		[]Choice{{DocumentID: "doc", Side: Local}},
		func(result Preview) (protocolv2.OperationReceipt, error) {
			raw, err := json.Marshal(result)
			return protocolv2.OperationReceipt{
				OperationID: "operation-invalid-preview",
				WorkspaceID: set.WorkspaceID,
				Method:      "conflict.preview",
				Scope:       protocolv2.WorkspaceScope,
				RequestHash: "request-hash",
				Result:      raw,
			}, err
		},
	)
	if err != nil || preview.Valid {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	receipt, found, err := reopened.LoadOperationReceipt(
		context.Background(),
		set.WorkspaceID,
		"operation-invalid-preview",
	)
	if err != nil || !found {
		t.Fatalf("receipt=%#v found=%v err=%v", receipt, found, err)
	}
	var replay Preview
	if err := json.Unmarshal(receipt.Result, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.PlanID != preview.PlanID ||
		replay.Valid ||
		len(replay.Diagnostics) != 1 {
		t.Fatalf("replay=%#v preview=%#v", replay, preview)
	}
}

func TestProductionConflictPlanCASAndRestartRecoveryAreAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflicts.db")
	engine, err := OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	set := productionSet(true)
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	preview, err := engine.Preview(
		context.Background(),
		set.ConflictID,
		[]Choice{{DocumentID: "doc", Side: Replica}},
	)
	if err != nil || !preview.Valid {
		t.Fatalf("preview = %#v, %v", preview, err)
	}
	appender := newStagedTestAppender()
	appender.commitFault = errors.New("injected commit failure")
	if _, err := engine.Apply(
		context.Background(),
		preview.PlanID,
		"operation-1",
		appender,
	); err == nil {
		t.Fatal("expected injected failure")
	}
	if appender.visibleCount != 0 {
		t.Fatal("failed commit exposed a partial head")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(
		context.Background(), appender,
	); err != nil {
		t.Fatal(err)
	}
	if appender.visibleCount != 1 {
		t.Fatalf("visible commits = %d", appender.visibleCount)
	}
	receipt, err := reopened.Apply(
		context.Background(),
		preview.PlanID,
		"operation-1",
		appender,
	)
	if err != nil || receipt.State != "applied" {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if appender.visibleCount != 1 {
		t.Fatal("idempotent retry published twice")
	}
	if _, err := reopened.Apply(
		context.Background(),
		preview.PlanID,
		"other-operation",
		appender,
	); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("stale operation accepted: %v", err)
	}
}

func TestProductionConflictCandidatesSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflicts.db")
	engine, err := OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	set := productionSet(true)
	if err := engine.Add(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Inspect(context.Background(), set.ConflictID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Base.SnapshotID != "base" ||
		got.Local.Files["doc"].ContentID != "local" ||
		got.Replica.Files["doc"].ContentID != "remote" {
		t.Fatalf("candidate loss after restart: %#v", got)
	}
}
