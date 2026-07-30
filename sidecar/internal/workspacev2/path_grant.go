package workspacev2

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
)

const maxPathGrantHeaderBytes = 16 << 10

var (
	errPathGrantRequired = errors.New("workspace.path_grant_required")
	errPathGrantInvalid  = errors.New("workspace.path_grant_invalid")
)

type pathGrantEnvelope struct {
	GrantID     string `json:"grantId"`
	Method      string `json:"method"`
	OperationID string `json:"operationId"`
	Purpose     string `json:"purpose"`
	Path        string `json:"path"`
}

type pathGrantContextKey struct{}

type requestPathGrant struct {
	mu       sync.Mutex
	envelope pathGrantEnvelope
	err      error
	consumed bool
}

// WithPathGrantHeader attaches a native-host-issued, request-scoped path
// capability. The encoded header is never persisted or copied into RPC params.
func WithPathGrantHeader(ctx context.Context, encoded string) context.Context {
	grant := &requestPathGrant{}
	if encoded == "" {
		grant.err = errPathGrantRequired
	} else {
		grant.envelope, grant.err = decodePathGrantHeader(encoded)
	}
	return context.WithValue(ctx, pathGrantContextKey{}, grant)
}

func consumePathGrant(
	ctx context.Context,
	grantID string,
	method string,
	operationID string,
	purpose string,
) (string, error) {
	grant, ok := ctx.Value(pathGrantContextKey{}).(*requestPathGrant)
	if !ok || grant == nil {
		return "", errPathGrantRequired
	}
	grant.mu.Lock()
	defer grant.mu.Unlock()
	if grant.consumed {
		return "", errPathGrantInvalid
	}
	grant.consumed = true
	if grant.err != nil {
		return "", grant.err
	}
	envelope := grant.envelope
	if envelope.GrantID != grantID ||
		envelope.Method != method ||
		envelope.OperationID != operationID ||
		envelope.Purpose != purpose {
		return "", errPathGrantInvalid
	}
	return envelope.Path, nil
}

func decodePathGrantHeader(encoded string) (pathGrantEnvelope, error) {
	if len(encoded) > maxPathGrantHeaderBytes {
		return pathGrantEnvelope{}, errPathGrantInvalid
	}
	var (
		raw []byte
		err error
	)
	raw, err = base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(encoded)
	}
	if err != nil || len(raw) == 0 || len(raw) > maxPathGrantHeaderBytes {
		return pathGrantEnvelope{}, errPathGrantInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope pathGrantEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return pathGrantEnvelope{}, errPathGrantInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return pathGrantEnvelope{}, errPathGrantInvalid
	}
	const prefix = "host-path-grant://"
	if !strings.HasPrefix(envelope.GrantID, prefix) ||
		!validUUID(strings.TrimPrefix(envelope.GrantID, prefix)) ||
		envelope.Method == "" ||
		!validUUID(envelope.OperationID) ||
		envelope.Purpose == "" ||
		envelope.Path == "" ||
		!filepath.IsAbs(envelope.Path) ||
		filepath.Clean(envelope.Path) != envelope.Path {
		return pathGrantEnvelope{}, errPathGrantInvalid
	}
	return envelope, nil
}
