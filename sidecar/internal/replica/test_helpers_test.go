package replica

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sqliteTestPath(t *testing.T, name string) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "vibetable-replica-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var removeErr error
		for attempt := 0; attempt < 20; attempt++ {
			removeErr = os.RemoveAll(directory)
			if removeErr == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("remove sqlite test directory: %v", removeErr)
	})
	return filepath.Join(directory, name)
}

func testClaim(
	workspaceID string,
	deviceID string,
	claimID string,
	nonce string,
	strength CoordinationStrength,
	now time.Time,
) Claim {
	return Claim{
		WorkspaceID: workspaceID,
		DeviceID:    deviceID,
		ClaimID:     claimID,
		FenceEpoch:  1,
		Nonce:       nonce,
		Strength:    strength,
		Mode:        Writable,
		IssuedAt:    now,
		HeartbeatAt: now,
		ExpiresAt:   now.Add(time.Minute),
	}
}
