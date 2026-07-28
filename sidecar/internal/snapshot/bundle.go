package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

var ErrBundleInvalid = errors.New("snapshot.bundle_invalid")

// SnapshotBundle is the complete, repository-independent closure required to
// prove that a catalog record, immutable manifest, seal, current objects and
// file-history root all describe the same capture intent.
type SnapshotBundle struct {
	Record         Record
	Manifest       objectrepo.ManifestRecord
	Seal           objectrepo.ManifestRecord
	Objects        map[string][]byte
	TopologyHead   objectrepo.ManifestRecord
	FileStateHead  objectrepo.ManifestRecord
	HistoryRoot    *objectrepo.ManifestRecord
	HistoryObjects map[objectrepo.ObjectID][]byte
}

type topologyRootReference struct {
	ManifestID objectrepo.ManifestID `json:"manifestId"`
}

type fileStateRootReference struct {
	FormatVersion uint64                         `json:"formatVersion"`
	SourceRoot    objectrepo.ManifestID          `json:"sourceRoot"`
	Files         map[string]objectrepo.ObjectID `json:"files"`
	Attachments   map[string]objectrepo.ObjectID `json:"attachments,omitempty"`
}

type topologyHeadPayload struct {
	FormatVersion         uint64 `json:"formatVersion"`
	WorkspaceID           string `json:"workspaceId"`
	TopologySchemaVersion uint64 `json:"topologySchemaVersion"`
	BusinessSchemaVersion uint64 `json:"businessSchemaVersion"`
	BusinessDatabaseHash  string `json:"businessDatabaseHash"`
}

type fileStateHeadPayload struct {
	FormatVersion uint64                `json:"formatVersion"`
	WorkspaceID   string                `json:"workspaceId"`
	HistoryRoot   objectrepo.ManifestID `json:"historyRoot"`
	FileRevision  uint64                `json:"fileRevision"`
}

type fileHistoryRootPayload struct {
	FormatVersion uint64            `json:"formatVersion"`
	WorkspaceID   string            `json:"workspaceId"`
	Documents     []json.RawMessage `json:"documents"`
}

type fileHistoryRevision struct {
	ObjectID objectrepo.ObjectID `json:"objectId"`
}

// LoadSnapshotBundle reads and validates the complete closure from a
// repository. Callers receive the already-validated bytes so export and
// restore do not have to perform a second, weaker interpretation.
func LoadSnapshotBundle(
	ctx context.Context,
	repository objectrepo.Repository,
	record Record,
) (SnapshotBundle, error) {
	if repository == nil {
		return SnapshotBundle{}, ErrBundleInvalid
	}
	manifest, err := repository.GetManifest(ctx, record.ManifestID)
	if err != nil {
		return SnapshotBundle{}, fmt.Errorf(
			"%w: load snapshot manifest: %w", ErrBundleInvalid, err,
		)
	}
	seal, err := repository.GetManifest(ctx, record.SealID)
	if err != nil {
		return SnapshotBundle{}, fmt.Errorf(
			"%w: load snapshot seal: %w", ErrBundleInvalid, err,
		)
	}
	objects := make(map[string][]byte, len(record.ObjectMap))
	for name, id := range record.ObjectMap {
		raw, err := readBundleObject(ctx, repository, id)
		if err != nil {
			return SnapshotBundle{}, fmt.Errorf(
				"%w: load snapshot object %q: %w",
				ErrBundleInvalid, name, err,
			)
		}
		objects[name] = raw
	}
	topologyReference, err := decodeStrictBundle[topologyRootReference](
		objects["topology-root"],
	)
	if err != nil || topologyReference.ManifestID == "" {
		return SnapshotBundle{}, ErrBundleInvalid
	}
	topologyHead, err := repository.GetManifest(
		ctx, topologyReference.ManifestID,
	)
	if err != nil {
		return SnapshotBundle{}, fmt.Errorf(
			"%w: load topology head: %w", ErrBundleInvalid, err,
		)
	}
	fileReference, err := decodeStrictBundle[fileStateRootReference](
		objects["file-state-root"],
	)
	if err != nil || fileReference.SourceRoot == "" {
		return SnapshotBundle{}, ErrBundleInvalid
	}
	fileStateHead, err := repository.GetManifest(ctx, fileReference.SourceRoot)
	if err != nil {
		return SnapshotBundle{}, fmt.Errorf(
			"%w: load file-state head: %w", ErrBundleInvalid, err,
		)
	}
	fileHead, err := decodeStrictBundle[fileStateHeadPayload](
		fileStateHead.Payload,
	)
	if err != nil {
		return SnapshotBundle{}, ErrBundleInvalid
	}
	var historyRoot *objectrepo.ManifestRecord
	historyObjects := map[objectrepo.ObjectID][]byte{}
	if fileHead.HistoryRoot != "" {
		value, err := repository.GetManifest(ctx, fileHead.HistoryRoot)
		if err != nil {
			return SnapshotBundle{}, fmt.Errorf(
				"%w: load file-history root: %w", ErrBundleInvalid, err,
			)
		}
		historyRoot = &value
		history, err := decodeStrictBundle[fileHistoryRootPayload](
			value.Payload,
		)
		if err != nil {
			return SnapshotBundle{}, ErrBundleInvalid
		}
		ids, err := historyObjectIDs(history)
		if err != nil {
			return SnapshotBundle{}, err
		}
		for _, id := range ids {
			raw, err := readBundleObject(ctx, repository, id)
			if err != nil {
				return SnapshotBundle{}, fmt.Errorf(
					"%w: load file-history object %q: %w",
					ErrBundleInvalid, id, err,
				)
			}
			historyObjects[id] = raw
		}
	}
	bundle := SnapshotBundle{
		Record:         record,
		Manifest:       manifest,
		Seal:           seal,
		Objects:        objects,
		TopologyHead:   topologyHead,
		FileStateHead:  fileStateHead,
		HistoryRoot:    historyRoot,
		HistoryObjects: historyObjects,
	}
	if err := ValidateSnapshotBundleData(bundle); err != nil {
		return SnapshotBundle{}, err
	}
	return bundle, nil
}

func ValidateSnapshotBundle(
	ctx context.Context,
	repository objectrepo.Repository,
	record Record,
) error {
	_, err := LoadSnapshotBundle(ctx, repository, record)
	return err
}

// ValidateSnapshotBundleData applies the same strict validation to exported
// package material before any object or snapshot is published.
func ValidateSnapshotBundleData(bundle SnapshotBundle) error {
	record := bundle.Record
	if record.SnapshotID == "" ||
		record.WorkspaceID == "" ||
		record.ManifestID == "" ||
		record.SealID == "" ||
		record.SnapshotSequence == 0 ||
		record.CatalogRevision == 0 ||
		len(record.ObjectMap) == 0 ||
		len(bundle.Objects) != len(record.ObjectMap) {
		return ErrBundleInvalid
	}
	if err := validateManifestArtifact(
		bundle.Manifest,
		record.ManifestID,
		"snapshot",
		record.WorkspaceID,
		record.SnapshotID,
	); err != nil {
		return err
	}
	if err := validateManifestArtifact(
		bundle.Seal,
		record.SealID,
		"snapshot-seal",
		record.WorkspaceID,
		record.SnapshotID,
	); err != nil {
		return err
	}
	manifest, err := decodeStrictBundle[Manifest](bundle.Manifest.Payload)
	if err != nil {
		return ErrBundleInvalid
	}
	seal, err := decodeStrictBundle[Seal](bundle.Seal.Payload)
	if err != nil {
		return ErrBundleInvalid
	}
	if manifest.FormatVersion != 2 ||
		manifest.SnapshotID != record.SnapshotID ||
		manifest.WorkspaceID != record.WorkspaceID ||
		manifest.FenceEpoch != record.FenceEpoch ||
		manifest.ClaimID != record.ClaimID ||
		manifest.MutationRevision != record.MutationRevision ||
		manifest.SnapshotSequence != record.SnapshotSequence ||
		manifest.BusinessDatabaseObjectID != record.ObjectMap["database"] ||
		manifest.TopologyRootObjectID != record.ObjectMap["topology-root"] ||
		manifest.FileStateRootObjectID != record.ObjectMap["file-state-root"] ||
		manifest.WorkspaceSettingsObjectID !=
			record.ObjectMap["workspace-settings"] ||
		manifest.AuditPrefixObjectID != record.ObjectMap["audit-prefix"] ||
		manifest.MinimumAppVersion == "" {
		return ErrBundleInvalid
	}
	if seal.FormatVersion != 2 ||
		seal.SnapshotID != record.SnapshotID ||
		seal.ManifestHash != digestBundle(bundle.Manifest.Payload) ||
		seal.RepositoryFormat != "workspace-repository-v2" ||
		seal.FenceEpoch != record.FenceEpoch ||
		seal.ClaimID != record.ClaimID ||
		seal.MutationRevision != record.MutationRevision ||
		seal.SnapshotSequence != record.SnapshotSequence ||
		!seal.Verified {
		return ErrBundleInvalid
	}
	if err := validateBundleObjects(record, bundle.Objects); err != nil {
		return err
	}
	if seal.DatabaseHash != digestBundle(bundle.Objects["database"]) ||
		seal.FileStateRootHash !=
			digestBundle(bundle.Objects["file-state-root"]) {
		return ErrBundleInvalid
	}
	anchorRaw, err := json.Marshal(manifest.AuditAnchor)
	if err != nil ||
		seal.AuditAnchorHash != digestBundle(anchorRaw) ||
		manifest.AuditAnchor.Epoch == 0 ||
		manifest.AuditAnchor.Sequence == 0 ||
		manifest.AuditAnchor.ChainHash == "" {
		return ErrBundleInvalid
	}
	auditAnchor, err := auditledger.VerifyPrefix(
		bundle.Objects["audit-prefix"],
	)
	if err != nil ||
		auditAnchor.SourceSequence != manifest.AuditAnchor.Sequence ||
		auditAnchor.Hash != manifest.AuditAnchor.ChainHash ||
		auditAnchor.LedgerSequence != record.AuditRevision {
		return ErrBundleInvalid
	}
	if err := validateJSONMap(bundle.Objects["workspace-settings"]); err != nil {
		return err
	}
	return validateBundleRoots(bundle, manifest)
}

func validateBundleObjects(
	record Record,
	objects map[string][]byte,
) error {
	expectedRoots := make(map[objectrepo.ObjectID]struct{}, len(record.ObjectMap))
	for name, id := range record.ObjectMap {
		raw, found := objects[name]
		if !found || id == "" || objectIDBundle(raw) != id {
			return ErrBundleInvalid
		}
		expectedRoots[id] = struct{}{}
	}
	actualRoots := make(map[objectrepo.ObjectID]struct{}, len(record.Objects))
	for _, id := range record.Objects {
		if id == "" {
			return ErrBundleInvalid
		}
		actualRoots[id] = struct{}{}
	}
	if len(actualRoots) != len(expectedRoots) {
		return ErrBundleInvalid
	}
	for id := range expectedRoots {
		if _, found := actualRoots[id]; !found {
			return ErrBundleInvalid
		}
	}
	return nil
}

func validateBundleRoots(bundle SnapshotBundle, manifest Manifest) error {
	topologyReference, err := decodeStrictBundle[topologyRootReference](
		bundle.Objects["topology-root"],
	)
	if err != nil || topologyReference.ManifestID != bundle.TopologyHead.ID {
		return ErrBundleInvalid
	}
	if err := validateManifestArtifact(
		bundle.TopologyHead,
		topologyReference.ManifestID,
		"topology-head",
		bundle.Record.WorkspaceID,
		"",
	); err != nil {
		return err
	}
	topology, err := decodeStrictBundle[topologyHeadPayload](
		bundle.TopologyHead.Payload,
	)
	if err != nil ||
		topology.FormatVersion != 1 ||
		topology.WorkspaceID != bundle.Record.WorkspaceID ||
		topology.TopologySchemaVersion != bundle.Record.SchemaRevision ||
		topology.BusinessDatabaseHash !=
			digestBundle(bundle.Objects["database"]) {
		return ErrBundleInvalid
	}
	fileReference, err := decodeStrictBundle[fileStateRootReference](
		bundle.Objects["file-state-root"],
	)
	if err != nil ||
		fileReference.FormatVersion != 1 ||
		fileReference.SourceRoot != bundle.FileStateHead.ID ||
		fileReference.Files == nil {
		return ErrBundleInvalid
	}
	if err := validateManifestArtifact(
		bundle.FileStateHead,
		fileReference.SourceRoot,
		"file-state-head",
		bundle.Record.WorkspaceID,
		"",
	); err != nil {
		return err
	}
	fileHead, err := decodeStrictBundle[fileStateHeadPayload](
		bundle.FileStateHead.Payload,
	)
	if err != nil ||
		fileHead.FormatVersion != 1 ||
		fileHead.WorkspaceID != bundle.Record.WorkspaceID ||
		fileHead.FileRevision != bundle.Record.FileRevision {
		return ErrBundleInvalid
	}
	if err := validateCurrentFileClosure(
		bundle.Record.ObjectMap, fileReference,
	); err != nil {
		return err
	}
	return validateHistoryClosure(bundle, fileHead)
}

func validateCurrentFileClosure(
	objectMap map[string]objectrepo.ObjectID,
	fileReference fileStateRootReference,
) error {
	expected := map[string]objectrepo.ObjectID{}
	for path, id := range fileReference.Files {
		if strings.TrimSpace(path) == "" || id == "" {
			return ErrBundleInvalid
		}
		expected["file:"+path] = id
	}
	for path, id := range fileReference.Attachments {
		if strings.TrimSpace(path) == "" || id == "" {
			return ErrBundleInvalid
		}
		expected["attachment:"+path] = id
	}
	for name, id := range expected {
		if objectMap[name] != id {
			return ErrBundleInvalid
		}
	}
	for name := range objectMap {
		if strings.HasPrefix(name, "file:") ||
			strings.HasPrefix(name, "attachment:") {
			if _, found := expected[name]; !found {
				return ErrBundleInvalid
			}
		}
	}
	return nil
}

func validateHistoryClosure(
	bundle SnapshotBundle,
	fileHead fileStateHeadPayload,
) error {
	if fileHead.HistoryRoot == "" {
		if bundle.Record.FileRevision != 0 ||
			bundle.HistoryRoot != nil ||
			len(bundle.HistoryObjects) != 0 {
			return ErrBundleInvalid
		}
		return nil
	}
	if bundle.Record.FileRevision == 0 ||
		bundle.HistoryRoot == nil ||
		bundle.HistoryRoot.ID != fileHead.HistoryRoot {
		return ErrBundleInvalid
	}
	if err := validateManifestArtifact(
		*bundle.HistoryRoot,
		fileHead.HistoryRoot,
		"filehistory-root",
		bundle.Record.WorkspaceID,
		"",
	); err != nil {
		return err
	}
	history, err := decodeStrictBundle[fileHistoryRootPayload](
		bundle.HistoryRoot.Payload,
	)
	if err != nil ||
		history.FormatVersion != 2 ||
		history.WorkspaceID != bundle.Record.WorkspaceID {
		return ErrBundleInvalid
	}
	required, err := historyObjectIDs(history)
	if err != nil {
		return err
	}
	objectValues := map[objectrepo.ObjectID][]byte{}
	for name, id := range bundle.Record.ObjectMap {
		objectValues[id] = bundle.Objects[name]
	}
	for _, id := range required {
		raw, found := objectValues[id]
		if !found {
			raw, found = bundle.HistoryObjects[id]
		}
		if !found || objectIDBundle(raw) != id {
			return ErrBundleInvalid
		}
	}
	for id, raw := range bundle.HistoryObjects {
		if objectIDBundle(raw) != id {
			return ErrBundleInvalid
		}
	}
	return nil
}

func validateManifestArtifact(
	record objectrepo.ManifestRecord,
	expectedID objectrepo.ManifestID,
	expectedName string,
	workspaceID string,
	snapshotID string,
) error {
	if record.ID != expectedID ||
		record.Name != expectedName ||
		record.Labels["type"] != expectedName ||
		record.Labels["workspaceId"] != workspaceID ||
		snapshotID != "" &&
			record.Labels["snapshotId"] != snapshotID ||
		objectrepo.VerifyManifestRecord(record) != nil {
		return ErrBundleInvalid
	}
	return nil
}

func historyObjectIDs(
	history fileHistoryRootPayload,
) ([]objectrepo.ObjectID, error) {
	seen := map[objectrepo.ObjectID]struct{}{}
	for _, raw := range history.Documents {
		var document struct {
			Revisions []fileHistoryRevision `json:"revisions"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			return nil, ErrBundleInvalid
		}
		for _, revision := range document.Revisions {
			if revision.ObjectID == "" {
				return nil, ErrBundleInvalid
			}
			seen[revision.ObjectID] = struct{}{}
		}
	}
	result := make([]objectrepo.ObjectID, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func readBundleObject(
	ctx context.Context,
	repository objectrepo.Repository,
	id objectrepo.ObjectID,
) ([]byte, error) {
	reader, err := repository.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(reader)
	return raw, errors.Join(readErr, reader.Close())
}

func decodeStrictBundle[T any](raw []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return value, ErrBundleInvalid
	}
	return value, nil
}

func validateJSONMap(raw []byte) error {
	value, err := decodeStrictBundle[map[string]any](raw)
	if err != nil || value == nil {
		return ErrBundleInvalid
	}
	return nil
}

func objectIDBundle(raw []byte) objectrepo.ObjectID {
	sum := sha256.Sum256(raw)
	return objectrepo.ObjectID("obj_" + hex.EncodeToString(sum[:]))
}

func digestBundle(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func NewPackageManifestRecord(
	id objectrepo.ManifestID,
	name string,
	workspaceID string,
	snapshotID string,
	payload []byte,
) objectrepo.ManifestRecord {
	labels := map[string]string{
		"type":        name,
		"workspaceId": workspaceID,
	}
	if snapshotID != "" {
		labels["snapshotId"] = snapshotID
	}
	return objectrepo.ManifestRecord{
		ID:      id,
		Name:    name,
		Labels:  labels,
		Payload: append(json.RawMessage(nil), payload...),
	}
}

func FileHistoryObjectIDs(payload []byte) ([]objectrepo.ObjectID, error) {
	history, err := decodeStrictBundle[fileHistoryRootPayload](payload)
	if err != nil {
		return nil, fmt.Errorf("%w: history root", ErrBundleInvalid)
	}
	return historyObjectIDs(history)
}
