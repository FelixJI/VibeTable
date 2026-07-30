package workspacev2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestPathGrantIsStrictlyBoundAndSingleUse(t *testing.T) {
	grantID := "host-path-grant://cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	path := filepath.Join(t.TempDir(), "snapshot.vtsnapshot")
	raw, err := json.Marshal(pathGrantEnvelope{
		GrantID:     grantID,
		Method:      "snapshot.export",
		OperationID: testOperationID,
		Purpose:     "snapshot-export",
		Path:        path,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPathGrantHeader(
		context.Background(),
		base64.RawURLEncoding.EncodeToString(raw),
	)
	resolved, err := consumePathGrant(
		ctx,
		grantID,
		"snapshot.export",
		testOperationID,
		"snapshot-export",
	)
	if err != nil || resolved != path {
		t.Fatalf("resolved path = %q, %v", resolved, err)
	}
	if _, err := consumePathGrant(
		ctx,
		grantID,
		"snapshot.export",
		testOperationID,
		"snapshot-export",
	); !errors.Is(err, errPathGrantInvalid) {
		t.Fatalf("reused grant error = %v", err)
	}
}

func TestPathGrantRejectsCrossOperationAndRawPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.bin")
	raw, err := json.Marshal(pathGrantEnvelope{
		GrantID:     "host-path-grant://cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Method:      "fileHistory.upgrade",
		OperationID: testOperationID,
		Purpose:     "file-upgrade",
		Path:        path,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPathGrantHeader(
		context.Background(),
		base64.RawURLEncoding.EncodeToString(raw),
	)
	if _, err := consumePathGrant(
		ctx,
		"host-path-grant://cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"snapshot.export",
		testOperationID,
		"snapshot-export",
	); !errors.Is(err, errPathGrantInvalid) {
		t.Fatalf("cross-purpose grant error = %v", err)
	}
	relative, err := json.Marshal(pathGrantEnvelope{
		GrantID:     "host-path-grant://cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Method:      "snapshot.export",
		OperationID: testOperationID,
		Purpose:     "snapshot-export",
		Path:        "renderer-controlled.vtsnapshot",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithPathGrantHeader(
		context.Background(),
		base64.RawURLEncoding.EncodeToString(relative),
	)
	if _, err := consumePathGrant(
		ctx,
		"host-path-grant://cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"snapshot.export",
		testOperationID,
		"snapshot-export",
	); !errors.Is(err, errPathGrantInvalid) {
		t.Fatalf("relative path grant error = %v", err)
	}
}
