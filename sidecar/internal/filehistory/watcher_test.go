package filehistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/diagnostics"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestWatcherInitialAndReconnectRescanIngestsStableFilesConservatively(
	t *testing.T,
) {
	coordinator, err := writecoordinator.New(
		testWorkspaceID, 1, testDeviceID, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := coordinator.Current()
	repository := objectrepo.NewMemory()
	if err := repository.AcceptAuthority(
		context.Background(), nil, token.Authority(),
	); err != nil {
		t.Fatal(err)
	}
	service, err := New(repository, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	ingestor, err := NewIngestor(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "reports", "q3.txt")
	if err := os.WriteFile(path, []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	var events []WatchEvent
	watcher, err := NewWatcher(
		root,
		ingestor,
		func() writecoordinator.Token { return token },
		func(event WatchEvent) { events = append(events, event) },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	documents := service.List()
	if len(documents) != 1 ||
		documents[0].RelativePath != "reports/q3.txt" ||
		len(documents[0].Revisions) != 1 {
		t.Fatalf("initial rescan documents = %#v", documents)
	}
	if err := os.WriteFile(path, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	documents = service.List()
	if len(documents) != 1 || len(documents[0].Revisions) != 2 {
		t.Fatalf("stable edit documents = %#v", documents)
	}
	copyPath := filepath.Join(root, "reports", "copy.txt")
	if err := os.WriteFile(copyPath, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(service.List()) != 1 {
		t.Fatal("same-content copy was assigned identity without confirmation")
	}
	foundCopyConfirmation := false
	for _, event := range events {
		if event.Path == "reports/copy.txt" &&
			event.Confirmation != nil {
			foundCopyConfirmation = true
		}
	}
	if !foundCopyConfirmation {
		t.Fatalf("watch events = %#v", events)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.List()[0].Status != DocumentActive {
		t.Fatal("missing tracked file was automatically deleted")
	}
	foundMissingConfirmation := false
	for _, event := range events {
		if event.Path == "reports/q3.txt" &&
			event.Confirmation != nil {
			foundMissingConfirmation = true
		}
	}
	if !foundMissingConfirmation {
		t.Fatalf("missing confirmation events = %#v", events)
	}
}

func TestWatcherReadStablePreservesMissingAndRejectsUnsafePaths(t *testing.T) {
	fixture := newHistoryFixture(t)
	ingestor, err := NewIngestor(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	filesRoot := filepath.Join(root, "files")
	watcher, err := NewWatcher(filesRoot, ingestor, func() writecoordinator.Token { return fixture.token }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"missing.txt", "missing-parent/missing.txt"} {
		t.Run(relative, func(t *testing.T) {
			content, err := watcher.ReadStable(context.Background(), relative)
			if content != nil || !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing read must preserve os.ErrNotExist: %q, %v", content, err)
			}
		})
	}
	t.Run("escape", func(t *testing.T) {
		if content, err := watcher.ReadStable(context.Background(), "../outside.txt"); content != nil ||
			!errors.Is(err, ErrUnsafeFilePath) {
			t.Fatalf("escape read = %q, %v", content, err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		outside := filepath.Join(root, "outside")
		if err := os.MkdirAll(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		secret := filepath.Join(outside, "secret.txt")
		if err := os.WriteFile(secret, []byte("synthetic outside bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(filesRoot, "linked-parent")
		for _, fixturePath := range []string{link, outside} {
			relative, err := filepath.Rel(root, fixturePath)
			if err != nil || !filepath.IsAbs(fixturePath) || filepath.IsAbs(relative) ||
				relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				t.Fatalf("link fixture escapes the synthetic root: %s, %v", fixturePath, err)
			}
		}
		if runtime.GOOS == "windows" {
			if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
				t.Fatalf("create synthetic junction: %s, %v", output, err)
			}
		} else if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Remove(link); err != nil {
				t.Errorf("remove only the synthetic link: %v", err)
			}
		})
		for _, relative := range []string{"linked-parent", "linked-parent/secret.txt", "linked-parent/missing.txt", "linked-parent/missing-parent/missing.txt"} {
			content, err := watcher.ReadStable(context.Background(), relative)
			if content != nil || !errors.Is(err, ErrUnsafeFilePath) || errors.Is(err, os.ErrNotExist) {
				t.Errorf("unsafe read %s = %q, %v", relative, content, err)
			}
		}
	})
}

func TestWatcherInitialScanDiagnosticsKeepSafeCountsAndDoNotLogPeriodicScans(t *testing.T) {
	fixture := newHistoryFixture(t)
	ingestor, err := NewIngestor(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for index := range 26 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("private-report-%02d.txt", index)), []byte(fmt.Sprintf("content-%d", index)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	logger := slog.New(diagnostics.NewJSONHandler(&output, slog.LevelInfo))
	watcher, err := NewWatcher(root, ingestor, func() writecoordinator.Token { return fixture.token }, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Close(); err != nil {
		t.Fatal(err)
	}
	initial := output.String()
	if err := watcher.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if output.String() != initial {
		t.Fatal("periodic rescan emitted startup diagnostics")
	}
	var events []string
	for _, line := range strings.Split(strings.TrimSpace(initial), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if len(record) != 11 || record["errorCode"] != nil || strings.Contains(line, root) || strings.Contains(line, "private-report") || strings.Contains(line, "content-") {
			t.Fatalf("unsafe diagnostic: %s", line)
		}
		events = append(events, record["event"].(string))
	}
	expected := []string{"filehistory.initial_scan.started", "filehistory.initial_scan.enumerated.26", "filehistory.initial_scan.progress.25_of_26", "filehistory.initial_scan.completed.26"}
	if !reflect.DeepEqual(events, expected) {
		t.Fatalf("events = %#v", events)
	}
	if len(fixture.service.List()) != 26 {
		t.Fatalf("diagnostics changed ingestion: %d", len(fixture.service.List()))
	}
}

func TestWatcherFailedInitialScanHasNoSuccessfulCompletionDiagnostic(t *testing.T) {
	fixture := newHistoryFixture(t)
	ingestor, err := NewIngestor(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	watcher, err := NewWatcher(t.TempDir(), ingestor, func() writecoordinator.Token { return fixture.token }, nil, slog.New(diagnostics.NewJSONHandler(&output, slog.LevelInfo)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := watcher.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if err := watcher.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "filehistory.initial_scan.started") || strings.Contains(output.String(), "completed") {
		t.Fatalf("failure was reported as complete: %s", output.String())
	}
}
