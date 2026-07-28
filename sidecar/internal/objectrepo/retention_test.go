package objectrepo

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestKopiaRetentionInventoryAndMaintenanceSurviveReopen(t *testing.T) {
	ctx := context.Background()
	fixture := newKopiaRetentionFixture(t)
	defer fixture.repository.Close(ctx)
	inventory, err := fixture.repository.RetentionInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.PendingPublication ||
		inventory.UnknownManifest ||
		inventory.CorruptIndex ||
		inventory.Revision != fixture.revision ||
		len(inventory.Objects) != 2 {
		t.Fatalf("inventory = %#v", inventory)
	}
	result, err := fixture.repository.RetireAndMaintain(
		ctx,
		RetentionMaintenanceRequest{
			Authority:        fixture.authority,
			ExpectedRevision: inventory.Revision,
			ObjectIDs:        []ObjectID{fixture.garbage},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedObjects != 1 ||
		result.AfterRevision <= result.BeforeRevision ||
		!result.VerificationRun ||
		result.ReclaimedBytes < 0 {
		t.Fatalf("maintenance result = %#v", result)
	}
	assertObjectContent(t, fixture.repository, fixture.live, "live-content")
	if _, err := fixture.repository.Open(ctx, fixture.garbage); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("retired object Open error = %v", err)
	}
	if err := fixture.repository.Close(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.repository, err = OpenKopia(
		ctx,
		fixture.configFile,
		"test-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	assertObjectContent(t, fixture.repository, fixture.live, "live-content")
	reopened, err := fixture.repository.RetentionInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.PendingPublication ||
		reopened.UnknownManifest ||
		reopened.CorruptIndex ||
		len(reopened.Objects) != 1 ||
		reopened.Objects[0].ID != fixture.live ||
		len(reopened.CompletedRetirements) != 1 ||
		reopened.CompletedRetirements[0].ID != fixture.garbage {
		t.Fatalf("reopened inventory = %#v", reopened)
	}
	journals, _, err := fixture.repository.retentionJournals(ctx)
	if err != nil || len(journals) != 1 ||
		journals[0].Stage != "completed" {
		t.Fatalf("completed journals = %#v, %v", journals, err)
	}
	replay, err := fixture.repository.RetireAndMaintain(
		ctx,
		RetentionMaintenanceRequest{
			Authority:        fixture.authority,
			ExpectedRevision: reopened.Revision,
			ObjectIDs:        []ObjectID{fixture.garbage},
		},
	)
	if err != nil ||
		replay.BeforeRevision != reopened.Revision ||
		replay.AfterRevision <= replay.BeforeRevision ||
		!replay.VerificationRun {
		t.Fatalf("completed receipt replay = %#v, %v", replay, err)
	}
	nextCommit, err := fixture.repository.Commit(
		ctx,
		CommitRequest{
			Authority: fixture.authority,
			Objects: []ObjectInput{{
				Name:    "next-garbage",
				Content: []byte("next-garbage"),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	nextGarbage := nextCommit.Objects["next-garbage"]
	nextInventory, err := fixture.repository.RetentionInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.RetireAndMaintain(
		ctx,
		RetentionMaintenanceRequest{
			Authority:        fixture.authority,
			ExpectedRevision: nextInventory.Revision,
			ObjectIDs:        []ObjectID{nextGarbage},
		},
	); err != nil {
		t.Fatalf("completed receipt serialized a later retirement: %v", err)
	}
}

func TestKopiaRetentionFaultBoundariesReplayIdempotently(t *testing.T) {
	stages := []retentionFaultStage{
		retentionBeforeJournal,
		retentionAfterJournal,
		retentionAfterMappingRemoval,
		retentionBeforeContentDelete,
		retentionAfterContentDelete,
		retentionAfterContentFlush,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			ctx := context.Background()
			fixture := newKopiaRetentionFixture(t)
			injected := errors.New("injected " + string(stage))
			faultCtx := withRetentionFault(
				ctx,
				func(at retentionFaultStage) error {
					if at == stage {
						return injected
					}
					return nil
				},
			)
			_, err := fixture.repository.RetireAndMaintain(
				faultCtx,
				RetentionMaintenanceRequest{
					Authority:        fixture.authority,
					ExpectedRevision: fixture.revision,
					ObjectIDs:        []ObjectID{fixture.garbage},
				},
			)
			if !errors.Is(err, injected) {
				t.Fatalf("fault error = %v", err)
			}
			if err := fixture.repository.Close(ctx); err != nil {
				t.Fatal(err)
			}
			fixture.repository, err = OpenKopia(
				ctx,
				fixture.configFile,
				"test-password",
			)
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.repository.Close(ctx)
			inventory, err := fixture.repository.RetentionInventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if inventory.UnknownManifest || inventory.CorruptIndex {
				t.Fatalf("restart inventory = %#v", inventory)
			}
			result, err := fixture.repository.RetireAndMaintain(
				ctx,
				RetentionMaintenanceRequest{
					Authority:        fixture.authority,
					ExpectedRevision: inventory.Revision,
					ObjectIDs:        []ObjectID{fixture.garbage},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.DeletedObjects != 1 ||
				!result.VerificationRun ||
				result.AfterRevision <= result.BeforeRevision {
				t.Fatalf("replay result = %#v", result)
			}
			assertObjectContent(
				t,
				fixture.repository,
				fixture.live,
				"live-content",
			)
			final, err := fixture.repository.RetentionInventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if final.UnknownManifest ||
				final.CorruptIndex ||
				len(final.Objects) != 1 ||
				final.Objects[0].ID != fixture.live ||
				len(final.CompletedRetirements) != 1 ||
				final.CompletedRetirements[0].ID != fixture.garbage {
				t.Fatalf("final inventory = %#v", final)
			}
		})
	}
}

func TestKopiaRetentionNeverDeletesSharedBackingContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repository, err := CreateKopiaFilesystem(
		ctx,
		filepath.Join(root, "repository"),
		filepath.Join(root, "client", "repository.config"),
		"test-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close(ctx)
	authority := Authority{
		WorkspaceID: "workspace",
		FenceEpoch:  1,
		ClaimID:     "claim",
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	receipt, err := repository.Commit(ctx, CommitRequest{
		Authority: authority,
		Objects: []ObjectInput{
			{Name: "left", Content: []byte("same-content")},
			{Name: "right", Content: []byte("same-content")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Objects["left"] != receipt.Objects["right"] {
		t.Fatal("fixture did not deduplicate equal content")
	}
	inventory, err := repository.RetentionInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Public IDs are content-addressed, so equal bytes are one logical object.
	// Retention must reject duplicate target IDs rather than double-delete.
	_, err = repository.RetireAndMaintain(
		ctx,
		RetentionMaintenanceRequest{
			Authority: authority, ExpectedRevision: inventory.Revision,
			ObjectIDs: []ObjectID{
				receipt.Objects["left"],
				receipt.Objects["right"],
			},
		},
	)
	if err == nil || err.Error() != "retention.object_ids_invalid" {
		t.Fatalf("duplicate target error = %v", err)
	}
	assertObjectContent(
		t,
		repository,
		receipt.Objects["left"],
		"same-content",
	)
}

type kopiaRetentionFixture struct {
	repository *KopiaRepository
	configFile string
	authority  Authority
	live       ObjectID
	garbage    ObjectID
	revision   uint64
}

func newKopiaRetentionFixture(t *testing.T) kopiaRetentionFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	configFile := filepath.Join(root, "client", "repository.config")
	repository, err := CreateKopiaFilesystem(
		ctx,
		filepath.Join(root, "repository"),
		configFile,
		"test-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	authority := Authority{
		WorkspaceID: "workspace",
		FenceEpoch:  1,
		ClaimID:     "claim",
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	receipt, err := repository.Commit(ctx, CommitRequest{
		Authority: authority,
		Objects: []ObjectInput{
			{Name: "live", Content: []byte("live-content")},
			{Name: "garbage", Content: []byte("garbage-content")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return kopiaRetentionFixture{
		repository: repository,
		configFile: configFile,
		authority:  authority,
		live:       receipt.Objects["live"],
		garbage:    receipt.Objects["garbage"],
		revision:   receipt.Revision,
	}
}

func assertObjectContent(
	t *testing.T,
	repository Repository,
	id ObjectID,
	want string,
) {
	t.Helper()
	reader, err := repository.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(raw) != want {
		t.Fatalf("object = %q, read=%v close=%v", raw, readErr, closeErr)
	}
}
