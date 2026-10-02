package filehistory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func TestWatcherReadStablePreservesMissingAndRejectsUnsafePaths(t *testing.T) {
	fixture := newHistoryFixture(t)
	ingestor, err := NewIngestor(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	filesRoot := filepath.Join(root, "files")
	watcher, err := NewWatcher(filesRoot, ingestor, func() writecoordinator.Token { return fixture.token }, nil)
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
