package workspacemigrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type fakeBackend struct {
	manifest    Manifest
	staged      Manifest
	protects    int
	stages      int
	upgrades    int
	publishes   int
	discards    int
	failPublish bool
}

func (backend *fakeBackend) Inspect(_ context.Context, root string) (Manifest, error) {
	if root == "staging" {
		return backend.staged, nil
	}
	return backend.manifest, nil
}
func (backend *fakeBackend) Protect(context.Context, string) error {
	backend.protects++
	return nil
}
func (backend *fakeBackend) Stage(context.Context, string) (string, error) {
	backend.stages++
	backend.staged = backend.manifest
	return "staging", nil
}
func (backend *fakeBackend) Upgrade(_ context.Context, _ string, target int) error {
	backend.upgrades++
	backend.staged.FormatVersion = target
	backend.staged.WriterVersion = "2.0.0"
	backend.staged.Fingerprint = "upgraded"
	return nil
}
func (backend *fakeBackend) Verify(_ context.Context, root string, target int) error {
	manifest, _ := backend.Inspect(context.Background(), root)
	if manifest.FormatVersion != target {
		return errors.New("wrong format")
	}
	return nil
}
func (backend *fakeBackend) Publish(context.Context, string, string) error {
	backend.publishes++
	if backend.failPublish {
		return errors.New("power loss")
	}
	backend.manifest = backend.staged
	return nil
}
func (backend *fakeBackend) Discard(context.Context, string) error {
	backend.discards++
	return nil
}

func openFixture(t *testing.T, backend *fakeBackend) *Migrator {
	t.Helper()
	migrator, err := Open(
		filepath.Join(t.TempDir(), "migration.db"), backend, 2, "2.0.0",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrator.Close() })
	return migrator
}

func TestPreviewApplyUsesProtectionVerifiedStagingAndAtomicPublish(t *testing.T) {
	backend := &fakeBackend{manifest: Manifest{
		WorkspaceID: "workspace-1", FormatVersion: 1,
		WriterVersion: "1.0.0", MinimumAppVersion: "1.0.0", Fingerprint: "source",
	}}
	migrator := openFixture(t, backend)
	plan, err := migrator.Preview(context.Background(), "workspace", 2)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := migrator.Apply(
		context.Background(), plan.PlanID, "upgrade-workspace",
	)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "committed" ||
		backend.protects != 1 || backend.stages != 1 ||
		backend.upgrades != 1 || backend.publishes != 1 ||
		backend.manifest.FormatVersion != 2 {
		t.Fatalf("incomplete migration: %#v %#v", operation, backend)
	}
}

func TestNewerWorkspaceIsReadOnlyAndPreviewWritesNoPlan(t *testing.T) {
	backend := &fakeBackend{manifest: Manifest{
		WorkspaceID: "workspace-1", FormatVersion: 3,
		WriterVersion: "3.0.0", MinimumAppVersion: "3.0.0", Fingerprint: "future",
	}}
	migrator := openFixture(t, backend)
	plan, err := migrator.Preview(context.Background(), "workspace", 2)
	if !errors.Is(err, ErrNewerFormat) || !plan.ReadOnly {
		t.Fatalf("newer workspace was not rejected read-only: %#v %v", plan, err)
	}
	var count int
	if err := migrator.db.QueryRow("SELECT COUNT(*) FROM upgrade_plans").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 || backend.protects != 0 || backend.stages != 0 {
		t.Fatalf("newer inspection wrote state: count=%d backend=%#v", count, backend)
	}
}

func TestApplyRejectsStalePlanBeforeProtection(t *testing.T) {
	backend := &fakeBackend{manifest: Manifest{
		WorkspaceID: "workspace-1", FormatVersion: 1,
		WriterVersion: "1.0.0", MinimumAppVersion: "1.0.0", Fingerprint: "source",
	}}
	migrator := openFixture(t, backend)
	plan, err := migrator.Preview(context.Background(), "workspace", 2)
	if err != nil {
		t.Fatal(err)
	}
	backend.manifest.Fingerprint = "changed"
	if _, err := migrator.Apply(
		context.Background(), plan.PlanID, "upgrade-workspace",
	); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("stale plan error = %v", err)
	}
	if backend.protects != 0 || backend.stages != 0 {
		t.Fatalf("stale plan mutated workspace: %#v", backend)
	}
}

func TestRecoverFinishesPublicationThatCompletedBeforeJournalUpdate(t *testing.T) {
	backend := &fakeBackend{manifest: Manifest{
		WorkspaceID: "workspace-1", FormatVersion: 1,
		WriterVersion: "1.0.0", MinimumAppVersion: "1.0.0", Fingerprint: "source",
	}, failPublish: true}
	statePath := filepath.Join(t.TempDir(), "migration.db")
	migrator, err := Open(statePath, backend, 2, "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migrator.Preview(context.Background(), "workspace", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Apply(
		context.Background(), plan.PlanID, "upgrade-workspace",
	); !errors.Is(err, ErrRecovery) {
		t.Fatalf("publish interruption error = %v", err)
	}
	// Simulate that the atomic publish actually completed but the caller saw
	// an I/O error before the journal could advance.
	backend.manifest = backend.staged
	backend.failPublish = false
	if err := migrator.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(statePath, backend, 2, "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := reopened.loadPlan(context.Background(), plan.PlanID)
	if err != nil || record.Status != "complete" || record.Stage != "committed" {
		t.Fatalf("recovery did not finalize publish: %#v %v", record, err)
	}
}
