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
