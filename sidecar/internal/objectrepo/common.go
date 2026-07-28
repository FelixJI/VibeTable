package objectrepo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

var (
	ErrStaleAuthority = errors.New("repository.stale_authority")
	ErrNotFound       = errors.New("repository.not_found")
	ErrCorrupt        = errors.New("repository.corrupt")
)

func validateAuthority(authority Authority) error {
	if strings.TrimSpace(authority.WorkspaceID) == "" ||
		strings.TrimSpace(authority.ClaimID) == "" ||
		authority.FenceEpoch == 0 {
		return errors.New("repository.authority_invalid")
	}
	return nil
}

func objectID(content []byte) ObjectID {
	sum := sha256.Sum256(content)
	return ObjectID("obj_" + hex.EncodeToString(sum[:]))
}

func validateObjectID(id ObjectID) bool {
	value := string(id)
	if !strings.HasPrefix(value, "obj_") || len(value) != len("obj_")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "obj_"))
	return err == nil
}

func canonicalManifest(input ManifestInput) ([]byte, error) {
	if strings.TrimSpace(input.Name) == "" || len(input.Payload) == 0 {
		return nil, errors.New("repository.manifest_invalid")
	}
	var payload any
	if err := json.Unmarshal(input.Payload, &payload); err != nil {
		return nil, errors.New("repository.manifest_invalid")
	}
	keys := make([]string, 0, len(input.Labels))
	for key := range input.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	labels := make([][2]string, 0, len(keys))
	for _, key := range keys {
		labels = append(labels, [2]string{key, input.Labels[key]})
	}
	return json.Marshal(struct {
		Labels  [][2]string `json:"labels"`
		Payload any         `json:"payload"`
	}{Labels: labels, Payload: payload})
}

func manifestID(content []byte) ManifestID {
	sum := sha256.Sum256(content)
	return ManifestID("manifest_" + hex.EncodeToString(sum[:]))
}

// VerifyManifestRecord validates a public manifest without requiring access to
// the repository that originally stored it. Replica and export adapters use it
// to independently attest copied manifest artifacts.
func VerifyManifestRecord(record ManifestRecord) error {
	canonical, err := canonicalManifest(ManifestInput{
		Name:    record.Name,
		Labels:  record.Labels,
		Payload: record.Payload,
	})
	if err != nil || manifestID(canonical) != record.ID {
		return ErrCorrupt
	}
	return nil
}

func authorityEqual(left, right Authority) bool {
	return left.WorkspaceID == right.WorkspaceID &&
		left.FenceEpoch == right.FenceEpoch &&
		left.ClaimID == right.ClaimID
}
