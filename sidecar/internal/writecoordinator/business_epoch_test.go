package writecoordinator

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
)

func TestBusinessEpochSurvivesSessionAndRejectsMissingOrExhaustedAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "coordination.db")
	const workspace = "11111111-1111-4111-8111-111111111111"
	c, err := OpenPersistent(path, workspace, 1, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := c.Current()
	if _, err := c.RotateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if c.BusinessEpoch() != 1 {
		t.Fatal("session invalidated business admissions")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if epoch, err := ReadPersistentBusinessEpoch(ctx, path, workspace); err != nil || epoch != 1 {
		t.Fatalf("%d %v", epoch, err)
	}
	if _, err := ReadPersistentBusinessEpoch(ctx, path, "foreign"); err == nil {
		t.Fatal("foreign identity accepted")
	}
	if _, err := ReadPersistentBusinessEpoch(ctx, filepath.Join(t.TempDir(), "missing.db"), workspace); err == nil {
		t.Fatal("missing authority accepted")
	}
	if err := RotatePersistentAuditEpoch(ctx, path, workspace); err != nil {
		t.Fatal(err)
	}
	if epoch, err := ReadPersistentBusinessEpoch(ctx, path, workspace); err != nil || epoch != 2 {
		t.Fatalf("%d %v", epoch, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE coordination_state SET audit_epoch_counter=?", int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPersistentBusinessEpoch(ctx, path, workspace); err == nil {
		t.Fatal("exhausted authority accepted")
	}
	if err := RotatePersistentAuditEpoch(ctx, path, workspace); err == nil {
		t.Fatal("exhausted authority advanced")
	}
}
