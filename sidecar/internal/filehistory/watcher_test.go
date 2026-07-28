package filehistory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
