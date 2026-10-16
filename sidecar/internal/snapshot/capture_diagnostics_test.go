package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/diagnostics"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

// lockedLogBuffer serializes test reads while captures log concurrently.
type lockedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (writer *lockedLogBuffer) Write(chunk []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.Write(chunk)
}

func (writer *lockedLogBuffer) lines() []string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	raw := writer.buffer.String()
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(raw), "\n")
}

func (writer *lockedLogBuffer) events() []string {
	var events []string
	for _, line := range writer.lines() {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			continue
		}
		if event, ok := payload["event"].(string); ok {
			events = append(events, event)
		}
	}
	return events
}

func (writer *lockedLogBuffer) countEvent(event string) int {
	total := 0
	for _, candidate := range writer.events() {
		if candidate == event {
			total++
		}
	}
	return total
}

func (writer *lockedLogBuffer) waitForEvent(
	t *testing.T,
	event string,
	minimum int,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if writer.countEvent(event) >= minimum {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf(
		"timed out waiting for %d x %s, got %d; events=%v",
		minimum, event, writer.countEvent(event), writer.events(),
	)
}

func phaseTestLogger() (*slog.Logger, *lockedLogBuffer) {
	writer := &lockedLogBuffer{}
	return slog.New(diagnostics.NewJSONHandler(writer, slog.LevelInfo)), writer
}

// blockingBarrier holds captureMu for as long as the test decides. Both
// captures share one instance, so entry is signalled by send, not close.
type blockingBarrier struct {
	entered chan struct{}
	release chan struct{}
	view    BarrierView
}

func (barrier *blockingBarrier) Freeze(
	context.Context,
) (BarrierView, func(), error) {
	barrier.entered <- struct{}{}
	<-barrier.release
	return barrier.view, func() {}, nil
}

func validCaptureFixture(
	t *testing.T,
) (objectrepo.Authority, objectrepo.Repository, *MemoryCatalog, BarrierView) {
	t.Helper()
	authority := objectrepo.Authority{
		WorkspaceID: "workspace-1", FenceEpoch: 7, ClaimID: "claim-a",
	}
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(context.Background(), nil, authority); err != nil {
		t.Fatal(err)
	}
	view := BarrierView{
		MutationRevision: 1, SnapshotSequence: 1,
		BusinessSchemaVersion: 1,
		AuditAnchor:           digest([]byte("anchor")),
		Database:              snapshotDatabaseForTest(t),
		Files:                 map[string][]byte{},
	}
	return authority, repository, NewMemoryCatalog(), view
}

// TestCapturePhaseEventsDistinguishMutexQueuedFromAcquired proves a capture
// blocked on captureMu is observable as waiting-without-acquired. The old
// implementation emitted no phase evidence at all, leaving the stalled layer
// indistinguishable.
func TestCapturePhaseEventsDistinguishMutexQueuedFromAcquired(t *testing.T) {
	logger, writer := phaseTestLogger()
	authority, repository, catalog, view := validCaptureFixture(t)
	firstEntered := make(chan struct{})
	firstRelease := make(chan struct{})
	coordinator := NewCoordinator(
		repository,
		&blockingBarrier{entered: firstEntered, release: firstRelease, view: view},
		catalog,
	).WithLogger(logger)

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := coordinator.Capture(context.Background(), CaptureRequest{
			WorkspaceID: "workspace-1",
			Authority:   authority,
			Trigger:     TriggerProtection,
		})
		firstDone <- err
	}()
	<-firstEntered

	secondDone := make(chan error, 1)
	go func() {
		_, _, err := coordinator.Capture(context.Background(), CaptureRequest{
			WorkspaceID: "workspace-1",
			Authority:   authority,
			Trigger:     TriggerProtection,
		})
		secondDone <- err
	}()
	writer.waitForEvent(t, "snapshot.capture.mutex_waiting", 2)
	if acquired := writer.countEvent("snapshot.capture.mutex_acquired"); acquired != 1 {
		t.Fatalf(
			"queued capture must stay waiting: mutex_acquired=%d events=%v",
			acquired, writer.events(),
		)
	}

	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	<-firstEntered // second capture acquires the mutex and enters its freeze.
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	writer.waitForEvent(t, "snapshot.capture.completed", 2)
}

// TestCapturePhaseEventsCoverUnblockedLifecycle pins the closed event set and
// durationMs presence for an unblocked capture.
func TestCapturePhaseEventsCoverUnblockedLifecycle(t *testing.T) {
	logger, writer := phaseTestLogger()
	authority, repository, catalog, view := validCaptureFixture(t)
	coordinator := NewCoordinator(repository, fakeBarrier{view: view}, catalog).
		WithLogger(logger)
	if _, _, err := coordinator.Capture(context.Background(), CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerManual,
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"snapshot.capture.mutex_waiting",
		"snapshot.capture.mutex_acquired",
		"snapshot.capture.completed",
	}
	if got := writer.events(); !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, line := range writer.lines() {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatal(err)
		}
		if _, present := payload["durationMs"]; !present {
			t.Fatalf("phase event without durationMs: %v", payload)
		}
		if code, present := payload["errorCode"]; present && code != nil {
			t.Fatalf("successful phase event carries errorCode: %v", code)
		}
		if module, _ := payload["module"].(string); module != "snapshot" {
			t.Fatalf("phase event module = %#v", payload["module"])
		}
		if workspace, _ := payload["workspaceId"].(string); workspace != "workspace-1" {
			t.Fatalf("phase event workspaceId = %#v", payload["workspaceId"])
		}
	}
}

// TestCapturePhaseFailureEventKeepsStableCodeAndDiagnostics proves a failing
// capture reports exactly one stable errorCode and never leaks the underlying
// publish error text.
func TestCapturePhaseFailureEventKeepsStableCodeAndDiagnostics(t *testing.T) {
	logger, writer := phaseTestLogger()
	authority, repository, catalog, view := validCaptureFixture(t)
	coordinator := NewCoordinator(
		repository,
		fakeBarrier{view: view},
		catalog.WithPublishError(errors.New("disk full at C:\\private\\catalog.db")),
	).WithLogger(logger)
	if _, _, err := coordinator.Capture(context.Background(), CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerManual,
	}); err == nil {
		t.Fatal("expected publish failure")
	}
	want := []string{
		"snapshot.capture.mutex_waiting",
		"snapshot.capture.mutex_acquired",
		"snapshot.capture.failed",
	}
	if got := writer.events(); !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	var failure map[string]any
	for _, line := range writer.lines() {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["event"] == "snapshot.capture.failed" {
			failure = payload
		}
	}
	if failure == nil {
		t.Fatal("snapshot.capture.failed event missing")
	}
	if failure["errorCode"] != "snapshot.capture_failed" {
		t.Fatalf("errorCode = %#v, want snapshot.capture_failed", failure["errorCode"])
	}
	if _, present := failure["durationMs"]; !present {
		t.Fatalf("failure event without durationMs: %v", failure)
	}
	if raw := writer.lines(); strings.Contains(strings.Join(raw, "\n"), "private") {
		t.Fatalf("diagnostics leaked storage text: %v", raw)
	}
}

// blockingFrozenSource freezes only when the test allows it.
type blockingFrozenSource struct {
	entered chan struct{}
	release chan struct{}
}

func (source *blockingFrozenSource) Freeze(
	_ context.Context,
	_ writecoordinator.CaptureIntent,
) (BarrierView, writecoordinator.FrozenRoots, error) {
	close(source.entered)
	<-source.release
	return BarrierView{}, writecoordinator.FrozenRoots{}, nil
}

// TestBarrierPhaseEventsDistinguishGateWaitFromSourceFreeze proves the two
// blocking layers under captureMu are separately observable: a capture stuck
// waiting for the shared write gate shows gate_waiting without gate_acquired,
// while a capture whose sources are frozen shows gate_acquired without
// freeze_returned. The old implementation emitted no evidence for either.
func TestBarrierPhaseEventsDistinguishGateWaitFromSourceFreeze(t *testing.T) {
	logger, writer := phaseTestLogger()
	coordinator, err := writecoordinator.New("workspace-1", 3, "claim-a", 9)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	writeEntered := make(chan struct{})
	writeRelease := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Write(context.Background(), token, func(
			context.Context,
			writecoordinator.WriteIntent,
		) error {
			close(writeEntered)
			<-writeRelease
			return nil
		})
		writeDone <- err
	}()
	<-writeEntered

	source := &blockingFrozenSource{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	barrier, err := NewCoordinatedBarrier(coordinator, token, source)
	if err != nil {
		t.Fatal(err)
	}
	barrier.WithLogger(logger)

	freezeDone := make(chan error, 1)
	go func() {
		_, _, err := barrier.Freeze(context.Background())
		freezeDone <- err
	}()
	writer.waitForEvent(t, "snapshot.capture.gate_waiting", 1)
	if acquired := writer.countEvent("snapshot.capture.gate_acquired"); acquired != 0 {
		t.Fatalf(
			"gate-blocked freeze must not report gate_acquired: %d events=%v",
			acquired, writer.events(),
		)
	}

	close(writeRelease)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	<-source.entered
	writer.waitForEvent(t, "snapshot.capture.gate_acquired", 1)
	if returned := writer.countEvent("snapshot.capture.freeze_returned"); returned != 0 {
		t.Fatalf(
			"source-blocked freeze must not report freeze_returned: %d events=%v",
			returned, writer.events(),
		)
	}

	close(source.release)
	if err := <-freezeDone; err != nil {
		t.Fatal(err)
	}
	writer.waitForEvent(t, "snapshot.capture.freeze_returned", 1)
	for _, event := range []string{
		"snapshot.capture.gate_acquired",
		"snapshot.capture.freeze_returned",
	} {
		var seen bool
		for _, line := range writer.lines() {
			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["event"] != event {
				continue
			}
			seen = true
			duration, ok := payload["durationMs"].(float64)
			if !ok || duration < 0 {
				t.Fatalf("%s durationMs = %#v", event, payload["durationMs"])
			}
		}
		if !seen {
			t.Fatalf("%s event missing", event)
		}
	}
}

// TestCapturePhaseEventsDisabledWithoutLogger proves the default (nil) logger
// keeps capture behavior and error semantics while emitting nothing.
func TestCapturePhaseEventsDisabledWithoutLogger(t *testing.T) {
	authority, repository, catalog, view := validCaptureFixture(t)
	coordinator := NewCoordinator(repository, fakeBarrier{view: view}, catalog)
	record, created, err := coordinator.Capture(context.Background(), CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerManual,
	})
	if err != nil || !created || record.SnapshotID == "" {
		t.Fatalf("nil-logger capture changed behavior: %#v %v %v", record, created, err)
	}

	failing := NewCoordinator(
		repository,
		fakeBarrier{view: view},
		catalog.WithPublishError(errors.New("disk full")),
	).WithLogger(nil)
	if _, _, err := failing.Capture(context.Background(), CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerManual,
	}); err == nil {
		t.Fatal("expected publish failure with nil logger")
	}
}

// TestCaptureErrorCodeClassificationClosedSet pins the closed code set: only
// the two named context outcomes are distinguishable, everything else
// collapses to snapshot.capture_failed. Dotted internal-looking messages
// must not be echoed (the v1 classifier leaked errors like private.customer
// straight into diagnostics).
func TestCaptureErrorCodeClassificationClosedSet(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{
			"canceled wrapped", fmt.Errorf("freeze: %w", context.Canceled),
			"context.canceled",
		},
		{
			"deadline wrapped", fmt.Errorf("gate: %w", context.DeadlineExceeded),
			"context.deadline_exceeded",
		},
		{
			"own sentinel is not echoed either",
			ErrMutationChanged, "snapshot.capture_failed",
		},
		{
			"wrapped verify failure",
			fmt.Errorf("%w: missing=%d", ErrVerifyFailed, 1),
			"snapshot.capture_failed",
		},
		{
			"foreign dotted code that must not be echoed",
			errors.New("private.customer"),
			"snapshot.capture_failed",
		},
		{
			"foreign dotted code with detail suffix",
			errors.New("private.customer: quota exceeded"),
			"snapshot.capture_failed",
		},
		{
			"internal-looking dotted code",
			errors.New("snapshot.workspace_invalid"),
			"snapshot.capture_failed",
		},
		{
			"engine text with path",
			errors.New(
				"read workspace manifest: open C:\\Users\\private\\workspace.json: Access is denied.",
			),
			"snapshot.capture_failed",
		},
		{
			"uppercase engine text",
			errors.New("SQLite error: database is locked"),
			"snapshot.capture_failed",
		},
		{
			"joined internal and storage errors",
			errors.Join(errors.New("snapshot.commit_failed"), errors.New("C:\\repo\\objects locked")),
			"snapshot.capture_failed",
		},
		{
			"canceled wrapped",
			fmt.Errorf("freeze: %w", context.Canceled), "context.canceled",
		},
		{
			"deadline wrapped",
			fmt.Errorf("gate: %w", context.DeadlineExceeded),
			"context.deadline_exceeded",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := CaptureErrorCode(test.err)
			if got != test.want {
				t.Fatalf("CaptureErrorCode() = %q, want %q", got, test.want)
			}
			if strings.ContainsAny(got, `:\/ `) {
				t.Fatalf("classified code carries content: %q", got)
			}
		})
	}
}

// witnessHandler records Handle start/end times per event and burns fixed
// deterministic CPU work (no sleeping) inside the two entry events, so a
// duration that wrongly includes its own entry-log write is provably larger
// than the observable Handle-free bound.
type witnessHandler struct {
	mu       sync.Mutex
	spans    map[string][]witnessSpan
	workSink uint64
}

type witnessSpan struct {
	startedAt  time.Time
	finishedAt time.Time
	durationMs float64
	hasDur     bool
}

func (handler *witnessHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (handler *witnessHandler) Handle(_ context.Context, record slog.Record) error {
	startedAt := time.Now()
	if record.Message == "snapshot.capture.mutex_acquired" ||
		record.Message == "snapshot.capture.gate_acquired" {

		for index := uint64(1); index <= 2000000; index++ {
			handler.workSink += index / (index%17 + 1)
		}
	}
	span := witnessSpan{startedAt: startedAt, finishedAt: time.Now()}
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "durationMs" {
			if duration, ok := attr.Value.Any().(float64); ok {
				span.durationMs, span.hasDur = duration, true
			}
		}
		return true
	})
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.spans == nil {
		handler.spans = map[string][]witnessSpan{}
	}
	handler.spans[record.Message] = append(handler.spans[record.Message], span)
	return nil
}

func (handler *witnessHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }

func (handler *witnessHandler) WithGroup(string) slog.Handler { return handler }

func (handler *witnessHandler) last(event string) witnessSpan {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	spans := handler.spans[event]
	if len(spans) == 0 {
		return witnessSpan{}
	}
	return spans[len(spans)-1]
}

// fixtureViewSource freezes the valid capture fixture view so a full
// coordinator capture succeeds through the coordinated barrier.
type fixtureViewSource struct {
	view BarrierView
}

func (source fixtureViewSource) Freeze(
	_ context.Context,
	_ writecoordinator.CaptureIntent,
) (BarrierView, writecoordinator.FrozenRoots, error) {
	return source.view, writecoordinator.FrozenRoots{
		AuditAnchor: digest([]byte("audit")),
	}, nil
}

// TestCaptureExitIntervalsExcludeEntryLogCost proves each exit event's
// durationMs is measured after its entry event's log write completed: it can
// never exceed the wall time between the entry Handle finishing and the exit
// Handle starting. The v1 implementation started both clocks before the
// entry log write, so the burned entry-log cost landed inside the reported
// interval and broke this bound.
func TestCaptureExitIntervalsExcludeEntryLogCost(t *testing.T) {
	witness := &witnessHandler{}
	logger := slog.New(witness)
	gate, err := writecoordinator.New("workspace-1", 3, "claim-a", 9)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := gate.Current()
	authority, repository, catalog, view := validCaptureFixture(t)
	barrier, err := NewCoordinatedBarrier(gate, token, fixtureViewSource{view})
	if err != nil {
		t.Fatal(err)
	}
	barrier.WithLogger(logger)
	coordinator := NewCoordinator(repository, barrier, catalog).WithLogger(logger)
	if _, _, err := coordinator.Capture(context.Background(), CaptureRequest{
		WorkspaceID: "workspace-1", Authority: authority, Trigger: TriggerManual,
	}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		entry string
		exit  string
	}{
		{
			"snapshot.capture.mutex_acquired",
			"snapshot.capture.completed",
		},
		{
			"snapshot.capture.gate_acquired",
			"snapshot.capture.freeze_returned",
		},
	} {
		entrySpan := witness.last(test.entry)
		exitSpan := witness.last(test.exit)
		if !exitSpan.hasDur {
			t.Fatalf("%s missing durationMs", test.exit)
		}
		bound := exitSpan.startedAt.Sub(entrySpan.finishedAt)
		if bound < 0 {
			t.Fatalf("%s started before %s finished", test.exit, test.entry)
		}
		if exitSpan.durationMs > PhaseDurationMs(bound) {
			t.Fatalf(
				"%s durationMs %.6f exceeds log-free bound %.6f: entry log cost leaked into its own interval",
				test.exit, exitSpan.durationMs, PhaseDurationMs(bound),
			)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
