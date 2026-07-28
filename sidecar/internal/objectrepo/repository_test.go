package objectrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryAndFilesystemRunRepositoryContract(t *testing.T) {
	factories := map[string]func(*testing.T) Repository{
		"memory": func(t *testing.T) Repository {
			return NewMemory()
		},
		"filesystem": func(t *testing.T) Repository {
			repository, err := OpenFilesystem(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return repository
		},
		"kopia": func(t *testing.T) Repository {
			root := t.TempDir()
			repository, err := CreateKopiaFilesystem(
				context.Background(),
				filepath.Join(root, "repository"),
				filepath.Join(root, "client", "repository.config"),
				"test-password",
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = repository.Close(context.Background()) })
			return repository
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			runRepositoryContract(t, factory(t))
		})
	}
}

func runRepositoryContract(t *testing.T, repository Repository) {
	t.Helper()
	ctx := context.Background()
	authority := Authority{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		FenceEpoch:  1,
		ClaimID:     "88888888-8888-4888-8888-888888888888",
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	receipt, err := repository.Commit(ctx, CommitRequest{
		Authority: authority,
		Objects: []ObjectInput{
			{Name: "first", Content: []byte("same")},
			{Name: "second", Content: []byte("same")},
		},
		Manifests: []ManifestInput{{
			Name: "snapshot",
			Labels: map[string]string{
				"type": "snapshot", "workspaceId": authority.WorkspaceID,
			},
			Payload: json.RawMessage(`{"snapshotId":"s-1"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Durable || receipt.Revision != 1 {
		t.Fatalf("receipt = %#v", receipt)
	}
	if receipt.Objects["first"] != receipt.Objects["second"] {
		t.Fatal("equal content was not deduplicated")
	}
	stream, err := repository.Open(ctx, receipt.Objects["first"])
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(stream)
	_ = stream.Close()
	if !bytes.Equal(raw, []byte("same")) {
		t.Fatalf("content = %q", raw)
	}
	manifest, err := repository.GetManifest(ctx, receipt.Manifests["snapshot"])
	if err != nil {
		t.Fatal(err)
	}
	if string(manifest.Payload) != `{"snapshotId":"s-1"}` {
		t.Fatalf("manifest payload = %s", manifest.Payload)
	}
	report, err := repository.Verify(ctx, []ObjectID{receipt.Objects["first"]})
	if err != nil || !report.Valid {
		t.Fatalf("verify = %#v, %v", report, err)
	}
	pin, err := repository.Pin(
		ctx, authority, []ObjectID{receipt.Objects["first"]}, "snapshot-export", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	pins, err := repository.ListPins(ctx)
	if err != nil || len(pins) != 1 || pins[0].PinID != pin.PinID {
		t.Fatalf("pins = %#v, %v", pins, err)
	}
	if err := repository.ReleasePin(ctx, authority, pin.PinID); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryCommitIsAtomicAcrossFaultPoints(t *testing.T) {
	for _, point := range []FaultPoint{
		FaultBeforeObjectWrite, FaultBeforeManifestWrite, FaultBeforeFlush,
	} {
		t.Run(string(point), func(t *testing.T) {
			triggered := false
			repository := NewMemory().WithFaultInjector(func(actual FaultPoint) error {
				if actual == point && !triggered {
					triggered = true
					return errors.New("injected")
				}
				return nil
			})
			authority := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "c"}
			if err := repository.AcceptAuthority(context.Background(), nil, authority); err != nil {
				t.Fatal(err)
			}
			_, err := repository.Commit(context.Background(), CommitRequest{
				Authority: authority,
				Objects:   []ObjectInput{{Name: "o", Content: []byte("payload")}},
				Manifests: []ManifestInput{{
					Name: "m", Labels: map[string]string{"type": "test"},
					Payload: json.RawMessage(`{"ok":true}`),
				}},
			})
			if err == nil {
				t.Fatal("fault was not returned")
			}
			report, _ := repository.Verify(
				context.Background(),
				[]ObjectID{objectID([]byte("payload"))},
			)
			if report.Valid || len(report.Missing) != 1 {
				t.Fatalf("failed commit leaked object: %#v", report)
			}
		})
	}
}

func TestFilesystemRepositoryRejectsStaleAuthorityAndSurvivesReopen(t *testing.T) {
	root := t.TempDir()
	first, err := OpenFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	old := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "old"}
	if err := first.AcceptAuthority(context.Background(), nil, old); err != nil {
		t.Fatal(err)
	}
	receipt, err := first.Commit(context.Background(), CommitRequest{
		Authority: old,
		Objects:   []ObjectInput{{Name: "o", Content: []byte("durable")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	next := Authority{WorkspaceID: "w", FenceEpoch: 2, ClaimID: "next"}
	if err := first.AcceptAuthority(context.Background(), &old, next); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Commit(context.Background(), CommitRequest{
		Authority: old,
		Objects:   []ObjectInput{{Name: "stale", Content: []byte("bad")}},
	}); !errors.Is(err, ErrStaleAuthority) {
		t.Fatalf("stale commit error = %v", err)
	}
	reopened, err := OpenFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := reopened.Open(context.Background(), receipt.Objects["o"])
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(stream)
	_ = stream.Close()
	if string(raw) != "durable" {
		t.Fatalf("reopened content = %q", raw)
	}
}

func TestFilesystemRepositorySerializesIndependentInstances(t *testing.T) {
	root := t.TempDir()
	first, err := OpenFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	left := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "left"}
	right := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "right"}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- first.AcceptAuthority(context.Background(), nil, left)
	}()
	go func() {
		<-start
		results <- second.AcceptAuthority(context.Background(), nil, right)
	}()
	close(start)
	var successes, stale int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrStaleAuthority) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("authority CAS results: success=%d stale=%d", successes, stale)
	}
}

func TestRepositoryProcessLockHonorsCancellation(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "coordination", "repository.lock")
	release, err := acquireProcessLock(context.Background(), lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := acquireProcessLock(ctx, lockPath); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock error = %v", err)
	}
}

func TestFilesystemVerifyDetectsTamperedObject(t *testing.T) {
	root := t.TempDir()
	repository, _ := OpenFilesystem(root)
	authority := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "c"}
	_ = repository.AcceptAuthority(context.Background(), nil, authority)
	receipt, err := repository.Commit(context.Background(), CommitRequest{
		Authority: authority,
		Objects:   []ObjectInput{{Name: "o", Content: []byte("original")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.Objects["o"]
	if err := os.WriteFile(
		filepath.Join(root, "objects", string(id)),
		[]byte("tampered"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	report, err := repository.Verify(context.Background(), []ObjectID{id})
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || len(report.Corrupt) != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestExpiredPinsRemainVisibleUntilRetentionPlanConsumesThem(t *testing.T) {
	repository := NewMemory().WithNow(func() time.Time {
		return time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	})
	authority := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "c"}
	_ = repository.AcceptAuthority(context.Background(), nil, authority)
	receipt, _ := repository.Commit(context.Background(), CommitRequest{
		Authority: authority,
		Objects:   []ObjectInput{{Name: "o", Content: []byte("content")}},
	})
	expiry := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	_, err := repository.Pin(
		context.Background(),
		authority,
		[]ObjectID{receipt.Objects["o"]},
		"restore-plan",
		&expiry,
	)
	if err != nil {
		t.Fatal(err)
	}
	pins, _ := repository.ListPins(context.Background())
	if len(pins) != 1 {
		t.Fatalf("expired pin disappeared implicitly: %#v", pins)
	}
}

func TestKopiaRepositorySurvivesReopenAndBundledCLIReadsManifest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	storageRoot := filepath.Join(root, "repository")
	configFile := filepath.Join(root, "client", "repository.config")
	repository, err := CreateKopiaFilesystem(ctx, storageRoot, configFile, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	authority := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "claim"}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	receipt, err := repository.Commit(ctx, CommitRequest{
		Authority: authority,
		Objects:   []ObjectInput{{Name: "data", Content: []byte("durable")}},
		Manifests: []ManifestInput{{
			Name: "snapshot", Labels: map[string]string{"type": "snapshot"},
			Payload: json.RawMessage(`{"snapshotId":"s1"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenKopia(ctx, configFile, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	stream, err := reopened.Open(ctx, receipt.Objects["data"])
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(stream)
	_ = stream.Close()
	if string(content) != "durable" {
		t.Fatalf("reopened content=%q", content)
	}

	executable := os.Getenv("VIBETABLE_KOPIA_CLI")
	if executable == "" {
		t.Skip("set VIBETABLE_KOPIA_CLI to the bundled fixed Kopia executable")
	}
	cliConfig := filepath.Join(root, "cli", "repository.config")
	cache := filepath.Join(root, "cli", "cache")
	if err := os.MkdirAll(filepath.Dir(cliConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	connect := exec.Command(
		executable,
		"repository", "connect", "filesystem",
		"--path="+storageRoot,
		"--config-file="+cliConfig,
		"--cache-directory="+cache,
		"--password=test-password",
		"--disable-file-logging",
		"--readonly",
	)
	if output, err := connect.CombinedOutput(); err != nil {
		t.Fatalf("Kopia CLI connect failed: %s: %v", output, err)
	}
	list := exec.Command(
		executable,
		"manifest", "list",
		"--config-file="+cliConfig,
		"--password=test-password",
		"--disable-file-logging",
		"--filter=type:vibetable-manifest",
		"--json",
	)
	output, err := list.Output()
	if err != nil || !bytes.Contains(output, []byte(string(receipt.Manifests["snapshot"]))) {
		t.Fatalf("Kopia CLI did not read VibeTable manifest: %s: %v", output, err)
	}
}

func TestKopiaRepositorySerializesSameActivityClientConfig(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configFile := filepath.Join(root, "client", "repository.config")
	first, err := CreateKopiaFilesystem(
		ctx, filepath.Join(root, "repository"), configFile, "test-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)
	second, err := OpenKopia(ctx, configFile, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	left := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "left"}
	right := Authority{WorkspaceID: "w", FenceEpoch: 1, ClaimID: "right"}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- first.AcceptAuthority(ctx, nil, left)
	}()
	go func() {
		<-start
		results <- second.AcceptAuthority(ctx, nil, right)
	}()
	close(start)
	var successes, stale int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrStaleAuthority) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("authority CAS results: success=%d stale=%d", successes, stale)
	}
}
