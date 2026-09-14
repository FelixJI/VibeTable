package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/metadata"
)

func TestWorkCalendarCommitPreventsABAAndReplaysOriginal(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	service := metadata.New(app)
	ctx := context.Background()
	initial, err := service.ReadWorkCalendar(ctx)
	if err != nil || initial.Revision != "" || initial.Overrides == nil || len(initial.Overrides) != 0 {
		t.Fatalf("initial=%#v, %v", initial, err)
	}
	a := []metadata.WorkCalendarOverride{{Date: "2026-09-10", Kind: "holiday", Name: "公司假日"}}
	commit := func(values []metadata.WorkCalendarOverride, revision, key string) metadata.WorkCalendarReceipt {
		t.Helper()
		result, err := service.CommitWorkCalendar(ctx, metadata.WorkCalendarCommit{Overrides: values, ExpectedRevision: revision, IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := commit(a, "", "calendar-a")
	empty := commit([]metadata.WorkCalendarOverride{}, first.Revision, "calendar-clear")
	second := commit(a, empty.Revision, "calendar-a-again")
	if first.Revision == second.Revision || empty.Revision == "" {
		t.Fatal("calendar revision allowed ABA")
	}
	if _, err := service.CommitWorkCalendar(ctx, metadata.WorkCalendarCommit{Overrides: []metadata.WorkCalendarOverride{}, ExpectedRevision: first.Revision, IdempotencyKey: "calendar-stale"}); !metadata.IsError(err, "settings.calendar.revision_conflict") {
		t.Fatalf("stale revision=%v", err)
	}
	before, err := service.List(ctx, metadata.NamespaceSharedSettings)
	if err != nil {
		t.Fatal(err)
	}
	replay := commit(a, "", "calendar-a")
	if replay.Status != metadata.StatusReplayed || replay.Revision != first.Revision || replay.ChangeSetID != first.ChangeSetID || !reflect.DeepEqual(replay.Overrides, first.Overrides) {
		t.Fatalf("replay=%#v", replay)
	}
	after, err := service.List(ctx, metadata.NamespaceSharedSettings)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("replay changed stored generation: %v", err)
	}
	if _, err := service.CommitWorkCalendar(ctx, metadata.WorkCalendarCommit{Overrides: []metadata.WorkCalendarOverride{}, IdempotencyKey: "calendar-a"}); !metadata.IsError(err, "metadata.idempotency_conflict") {
		t.Fatalf("changed-key=%v", err)
	}
}

func TestWorkCalendarRejectsCorruptionWithoutOverwritingOtherSettings(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	service := metadata.New(app)
	ctx := context.Background()
	_, err := service.Upsert(ctx, metadata.UpsertRequest{Namespace: metadata.NamespaceSharedSettings, LogicalID: "other", Payload: json.RawMessage(`{"value":9007199254740993}`), IdempotencyKey: "other-create"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CommitWorkCalendar(ctx, metadata.WorkCalendarCommit{Overrides: []metadata.WorkCalendarOverride{}, IdempotencyKey: "calendar-create"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.List(ctx, metadata.NamespaceSharedSettings)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v %v", items, err)
	}
	var revision string
	for _, item := range items {
		if item.LogicalID == "other" && string(item.Payload) != `{"value":9007199254740993}` {
			t.Fatal("unrelated changed")
		}
		if item.LogicalID == metadata.WorkCalendarID {
			revision = item.Revision
		}
	}
	_, err = service.Upsert(ctx, metadata.UpsertRequest{Namespace: metadata.NamespaceSharedSettings, LogicalID: metadata.WorkCalendarID, ExpectedRevision: revision, Payload: json.RawMessage(`{"broken":true}`), IdempotencyKey: "corruption-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ReadWorkCalendar(ctx); !metadata.IsError(err, "settings.calendar.corrupt") {
		t.Fatalf("corrupt read=%v", err)
	}
	if _, err = service.CommitWorkCalendar(ctx, metadata.WorkCalendarCommit{Overrides: []metadata.WorkCalendarOverride{}, ExpectedRevision: revision, IdempotencyKey: "corrupt-write"}); !metadata.IsError(err, "settings.calendar.corrupt") {
		t.Fatalf("corrupt write=%v", err)
	}
}

func TestWorkCalendarCancellationAndRejectedCommitLeaveAuthorityUnchanged(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	service := metadata.New(app)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ReadWorkCalendar(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancellation=%v", err)
	}
	valid := metadata.WorkCalendarCommit{Overrides: []metadata.WorkCalendarOverride{}, IdempotencyKey: "calendar-cancel"}
	if _, err := service.CommitWorkCalendar(canceled, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("write cancellation=%v", err)
	}
	for _, request := range []metadata.WorkCalendarCommit{
		{Overrides: []metadata.WorkCalendarOverride{}, ExpectedRevision: "invalid", IdempotencyKey: "bad-revision"},
		{Overrides: nil, IdempotencyKey: "bad-array"},
		{Overrides: []metadata.WorkCalendarOverride{}, IdempotencyKey: "invalid key"},
	} {
		if _, err := service.CommitWorkCalendar(context.Background(), request); err == nil {
			t.Fatal("invalid request succeeded")
		}
	}
	items, err := service.List(context.Background(), metadata.NamespaceSharedSettings)
	if err != nil || len(items) != 0 {
		t.Fatalf("rejected writes changed authority: %#v, %v", items, err)
	}
}
