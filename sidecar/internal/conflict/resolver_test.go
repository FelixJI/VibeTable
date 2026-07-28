package conflict

import "testing"

type fakeAppender struct {
	revisions []FileState
	recovery  string
	committed bool
}

func (appender *fakeAppender) ApplyAtomically(
	_ Plan,
	changes []ResolvedChange,
	_ Candidate,
) (string, error) {
	for _, change := range changes {
		appender.revisions = append(appender.revisions, change.Chosen)
	}
	appender.recovery = "recovery-1"
	appender.committed = true
	return appender.recovery, nil
}

func TestConflictResolutionCreatesNewRevisionAndPreservesDiscardedCandidate(t *testing.T) {
	base := Candidate{SnapshotID: "base", Files: map[string]FileState{
		"doc": {DocumentID: "doc", Path: "a.txt", ContentID: "v1"},
	}}
	local := Candidate{SnapshotID: "local", Revision: 7, Files: map[string]FileState{
		"doc": {DocumentID: "doc", Path: "a.txt", ContentID: "local"},
	}}
	replica := Candidate{SnapshotID: "replica", Files: map[string]FileState{
		"doc": {DocumentID: "doc", Path: "renamed.txt", ContentID: "remote"},
	}}
	plan := BuildPlan(base, local, replica)
	if len(plan.Files) != 1 {
		t.Fatalf("expected conflict, got %#v", plan)
	}
	appender := &fakeAppender{}
	recovery, err := Apply(plan, local, replica, Resolution{
		Choices: map[string]Side{"doc": Replica},
	}, appender)
	if err != nil || recovery != "recovery-1" || !appender.committed {
		t.Fatalf("apply failed: %s %v %#v", recovery, err, appender)
	}
	if len(appender.revisions) != 1 || appender.revisions[0].ContentID != "remote" {
		t.Fatalf("chosen content not ingested as new revision: %#v", appender.revisions)
	}
}

func TestConflictPlanRejectsChangedCandidateAndIncludesReplicaOnlyUpdates(t *testing.T) {
	base := Candidate{SnapshotID: "base", Files: map[string]FileState{
		"automatic": {DocumentID: "automatic", Path: "one.txt", ContentID: "v1"},
	}}
	local := Candidate{SnapshotID: "local", Revision: 1, Files: map[string]FileState{
		"automatic": {DocumentID: "automatic", Path: "one.txt", ContentID: "v1"},
	}}
	replica := Candidate{SnapshotID: "replica", Files: map[string]FileState{
		"automatic": {DocumentID: "automatic", Path: "renamed.txt", ContentID: "v2"},
	}}
	plan := BuildPlan(base, local, replica)
	if len(plan.Automatic) != 1 || len(plan.Files) != 0 {
		t.Fatalf("replica-only update not planned: %#v", plan)
	}
	changed := local
	changed.Files = map[string]FileState{
		"automatic": {DocumentID: "automatic", Path: "other.txt", ContentID: "v3"},
	}
	if _, err := Apply(plan, changed, replica, Resolution{}, &fakeAppender{}); err != ErrStalePlan {
		t.Fatalf("changed candidate accepted: %v", err)
	}
	appender := &fakeAppender{}
	if _, err := Apply(plan, local, replica, Resolution{}, appender); err != nil {
		t.Fatal(err)
	}
	if len(appender.revisions) != 1 || appender.revisions[0].Path != "renamed.txt" {
		t.Fatalf("automatic update not atomically applied: %#v", appender.revisions)
	}
}

func TestResolveChangesRequiresEveryFileChoiceAndSupportsKeepBothRecovery(t *testing.T) {
	base := Candidate{SnapshotID: "base", Files: map[string]FileState{
		"doc-a": {DocumentID: "doc-a", Path: "a.txt", ContentID: "base-a"},
		"doc-b": {DocumentID: "doc-b", Path: "b.txt", ContentID: "base-b"},
	}}
	local := Candidate{SnapshotID: "local", Revision: 9, Files: map[string]FileState{
		"doc-a": {DocumentID: "doc-a", Path: "a.txt", ContentID: "local-a"},
		"doc-b": {DocumentID: "doc-b", Path: "b.txt", ContentID: "local-b"},
	}}
	replica := Candidate{SnapshotID: "replica", Files: map[string]FileState{
		"doc-a": {DocumentID: "doc-a", Path: "a.txt", ContentID: "replica-a"},
		"doc-b": {DocumentID: "doc-b", Path: "b.txt", ContentID: "replica-b"},
	}}
	plan := BuildPlan(base, local, replica)
	if _, err := ResolveChanges(plan, local, replica, Resolution{
		Choices: map[string]Side{"doc-a": Both},
	}); err == nil || err.Error() != "conflict.choice_missing" {
		t.Fatalf("partial multi-file choice accepted: %v", err)
	}

	changes, err := ResolveChanges(plan, local, replica, Resolution{
		Choices: map[string]Side{
			"doc-a": Both,
			"doc-b": Replica,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %#v", changes)
	}
	if changes[0].ItemID != "doc-a" ||
		changes[0].Chosen.ContentID != "local-a" ||
		changes[0].Reason != "keep-both-recovery" {
		t.Fatalf("keep-both change = %#v", changes[0])
	}
	if changes[1].ItemID != "doc-b" ||
		changes[1].Chosen.ContentID != "replica-b" {
		t.Fatalf("replica choice = %#v", changes[1])
	}
}

func TestBuildPlanFreezesWholeTableCandidates(t *testing.T) {
	baseTable := TableState{
		TableID: "table-a", DisplayName: "Projects",
		SchemaObjectID: "schema-base", RecordsObjectID: "records-base",
		ViewsObjectID: "views-base", AttachmentsObjectID: "attachments-base",
	}
	localTable := baseTable
	localTable.SchemaObjectID = "schema-local"
	localTable.RecordsObjectID = "records-local"
	localTable.ViewsObjectID = "views-local"
	localTable.AttachmentsObjectID = "attachments-local"
	replicaTable := baseTable
	replicaTable.SchemaObjectID = "schema-replica"
	replicaTable.RecordsObjectID = "records-replica"
	replicaTable.ViewsObjectID = "views-replica"
	replicaTable.AttachmentsObjectID = "attachments-replica"

	base := Candidate{SnapshotID: "base", Tables: map[string]TableState{"table-a": baseTable}}
	local := Candidate{
		SnapshotID: "local", Revision: 3,
		Tables: map[string]TableState{"table-a": localTable},
	}
	replica := Candidate{
		SnapshotID: "replica",
		Tables:     map[string]TableState{"table-a": replicaTable},
	}
	plan := BuildPlan(base, local, replica)
	if len(plan.Tables) != 1 {
		t.Fatalf("table conflicts = %#v", plan.Tables)
	}
	got := plan.Tables[0]
	if got.Local.SchemaObjectID != "schema-local" ||
		got.Local.RecordsObjectID != "records-local" ||
		got.Local.ViewsObjectID != "views-local" ||
		got.Local.AttachmentsObjectID != "attachments-local" {
		t.Fatalf("incomplete local table candidate = %#v", got.Local)
	}
	if _, err := ResolveChanges(plan, local, replica, Resolution{
		Choices: map[string]Side{"table-a": Both},
	}); err == nil || err.Error() != "conflict.choice_invalid" {
		t.Fatalf("table keep-both accepted: %v", err)
	}
}
