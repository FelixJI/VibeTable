package workspacev2

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/diagnostics"
)

type phaseLogFixture struct {
	runtime *Runtime
	app     *pocketbase.PocketBase
	ledger  *auditledger.Ledger
}

func openRuntimeWithPhaseLogger(
	t *testing.T,
	logger *slog.Logger,
	writer *lockedPhaseLogBuffer,
) *phaseLogFixture {
	t.Helper()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	runtime, err := Open(context.Background(), Options{
		App:     app,
		DataDir: dataDir, WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		DeferBackgroundWorkers: true,
		Logger:                 logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopFileWatcher(t, runtime)
		_ = runtime.Close(context.Background())
	})
	return &phaseLogFixture{runtime: runtime, app: app, ledger: ledger}
}

// lockedPhaseLogBuffer records envelope lines. Background workers stay
// deferred in these fixtures, so every log write happens on the test
// goroutine and no cross-goroutine locking is required.
type lockedPhaseLogBuffer struct {
	lines []string
}

func (writer *lockedPhaseLogBuffer) Write(chunk []byte) (int, error) {
	writer.lines = append(writer.lines, strings.Split(
		strings.TrimRight(string(chunk), "\n"), "\n",
	)...)
	return len(chunk), nil
}

func (writer *lockedPhaseLogBuffer) events() []string {
	var events []string
	for _, line := range writer.lines {
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

// TestSnapshotRequestPhaseEventsCoverClosedLifecycle proves a dispatched
// snapshot.request records the full closed phase set through every blocking
// layer, with stable identity fields and durationMs, and that the diagnostics
// envelope stays content-free. The old implementation produced no such
// evidence, so a stalled request could not be attributed to a layer.
func TestSnapshotRequestPhaseEventsCoverClosedLifecycle(t *testing.T) {
	writer := &lockedPhaseLogBuffer{}
	fixture := openRuntimeWithPhaseLogger(
		t,
		slog.New(diagnostics.NewJSONHandler(writer, slog.LevelInfo)),
		writer,
	)

	response := dispatch(
		t,
		fixture.runtime,
		1,
		"snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if response.Error != nil {
		t.Fatalf("snapshot.request error = %#v", response.Error)
	}

	want := []string{
		"snapshot.request.entered.manual.foreground",
		"snapshot.capture.mutex_waiting",
		"snapshot.capture.mutex_acquired",
		"snapshot.capture.gate_waiting",
		"snapshot.capture.gate_acquired",
		"snapshot.capture.freeze_returned",
		"snapshot.capture.completed",
		"snapshot.request.completed.manual.foreground",
	}
	if got := writer.events(); !equalEventLists(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	expectedOperationID := "bbbbbbbb-bbbb-4bbb-8bbb-000000000001"
	for _, line := range writer.lines {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["operationId"] != expectedOperationID {
			t.Fatalf("operationId = %#v, want %s", payload["operationId"], expectedOperationID)
		}
		if payload["workspaceId"] != testWorkspaceID {
			t.Fatalf("workspaceId = %#v", payload["workspaceId"])
		}
		if payload["module"] != "snapshot" {
			t.Fatalf("module = %#v", payload["module"])
		}
		if code, present := payload["errorCode"]; present && code != nil {
			t.Fatalf("successful phase event carries errorCode: %v", code)
		}
		for key := range payload {
			switch key {
			case "timestamp", "level", "module", "event", "errorCode",
				"requestId", "operationId", "workspaceId", "sessionEpoch",
				"jobId", "durationMs":
			default:
				t.Fatalf("diagnostics envelope gained key %q: %v", key, payload)
			}
		}
		if strings.Contains(line, ".vibetable") || strings.Contains(line, "dataDir") {
			t.Fatalf("diagnostics leaked a path: %s", line)
		}
	}
	var completed map[string]any
	for _, line := range writer.lines {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["event"] == want[len(want)-1] {
			completed = payload
		}
	}
	duration, ok := completed["durationMs"].(float64)
	if !ok || duration < 0 {
		t.Fatalf("completed durationMs = %#v", completed["durationMs"])
	}
}

// TestSnapshotRequestPhaseEventsAttributeCanceledCaptureToGate proves a
// protection request whose context is canceled while waiting for the shared
// write gate is attributable: gate_waiting without gate_acquired plus a
// context.canceled failure code. The old implementation collapsed this into a
// bare dispatcher error with no layer evidence.
func TestSnapshotRequestPhaseEventsAttributeCanceledCaptureToGate(t *testing.T) {
	writer := &lockedPhaseLogBuffer{}
	fixture := openRuntimeWithPhaseLogger(
		t,
		slog.New(diagnostics.NewJSONHandler(writer, slog.LevelInfo)),
		writer,
	)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixture.runtime.requestSnapshot(
		canceled,
		json.RawMessage(`{"scope":"workspace","workspaceId":"`+testWorkspaceID+`","sessionEpoch":7,"operationId":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","sequence":1}`),
		json.RawMessage(`{"trigger":"protection","urgency":"foreground"}`),
	)
	if err == nil {
		t.Fatal("canceled snapshot request must fail")
	}

	want := []string{
		"snapshot.request.entered.protection.foreground",
		"snapshot.capture.mutex_waiting",
		"snapshot.capture.mutex_acquired",
		"snapshot.capture.gate_waiting",
		"snapshot.capture.failed",
		"snapshot.request.failed.protection.foreground",
	}
	if got := writer.events(); !equalEventLists(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, event := range []string{"snapshot.capture.failed", "snapshot.request.failed.protection.foreground"} {
		var failure map[string]any
		for _, line := range writer.lines {
			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["event"] == event {
				failure = payload
			}
		}
		if failure == nil {
			t.Fatalf("%s event missing", event)
		}
		if failure["errorCode"] != "context.canceled" {
			t.Fatalf("%s errorCode = %#v", event, failure["errorCode"])
		}
		if _, ok := failure["durationMs"].(float64); !ok {
			t.Fatalf("%s durationMs = %#v", event, failure["durationMs"])
		}
	}
}

// TestSnapshotRequestWithoutLoggerKeepsResult proves a nil Options.Logger (the
// production default) disables every phase event across all layers while
// snapshot.request behavior and its response shape stay unchanged.
func TestSnapshotRequestWithoutLoggerKeepsResult(t *testing.T) {
	writer := &lockedPhaseLogBuffer{}
	fixture := openRuntimeWithPhaseLogger(t, nil, writer)

	response := dispatch(
		t,
		fixture.runtime,
		1,
		"snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if response.Error != nil {
		t.Fatalf("snapshot.request error = %#v", response.Error)
	}
	result := response.Result.(map[string]any)
	if result["operationId"] != "bbbbbbbb-bbbb-4bbb-8bbb-000000000001" ||
		result["state"] != "ready" || result["snapshotId"] == nil {
		t.Fatalf("nil-logger snapshot result changed: %#v", result)
	}
	if events := writer.events(); len(events) != 0 {
		t.Fatalf("nil logger still emitted events: %v", events)
	}
}

func equalEventLists(left, right []string) bool {
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
