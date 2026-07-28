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
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/workspacedb"
)

const (
	MaxBundleMaterializedBytes int64 = 512 << 20
	maxBundleEntries                 = 10_000
)

var (
	ErrBundleInvalid       = errors.New("snapshot.bundle_invalid")
	ErrBundleResourceLimit = errors.New("snapshot.bundle_resource_limit")
)

// SnapshotBundle is the complete, repository-independent closure required to
// prove that a catalog record, immutable manifest, seal, current objects and
// file-history root all describe the same capture intent.
type SnapshotBundle struct {
	Record          Record
	Manifest        objectrepo.ManifestRecord
	Seal            objectrepo.ManifestRecord
	Objects         map[string][]byte
	TopologyHead    objectrepo.ManifestRecord
	FileStateHead   objectrepo.ManifestRecord
	HistoryRoot     *objectrepo.ManifestRecord
	HistoryObjects  map[objectrepo.ObjectID][]byte
	HistoryMetadata []filehistory.RevisionObject
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
	if len(record.ObjectMap) > maxBundleEntries ||
		len(record.Objects) > maxBundleEntries {
		return SnapshotBundle{}, ErrBundleResourceLimit
	}
	budget := bundleReadBudget{remaining: MaxBundleMaterializedBytes}
	manifest, err := repository.GetManifest(ctx, record.ManifestID)
	if err != nil {
		return SnapshotBundle{}, classifyBundleLoadError(
			"load snapshot manifest", err,
		)
	}
	if err := budget.consume(manifest.Payload); err != nil {
		return SnapshotBundle{}, err
	}
	seal, err := repository.GetManifest(ctx, record.SealID)
	if err != nil {
		return SnapshotBundle{}, classifyBundleLoadError(
			"load snapshot seal", err,
		)
	}
	if err := budget.consume(seal.Payload); err != nil {
		return SnapshotBundle{}, err
	}
	objects := make(map[string][]byte, len(record.ObjectMap))
	objectCache := make(
		map[objectrepo.ObjectID][]byte,
		len(record.ObjectMap),
	)
	for name, id := range record.ObjectMap {
		raw, found := objectCache[id]
		if !found {
			raw, err = budget.readObject(ctx, repository, id)
			if err != nil {
				return SnapshotBundle{}, classifyBundleLoadError(
					fmt.Sprintf("load snapshot object %q", name),
					err,
				)
			}
			objectCache[id] = raw
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
		return SnapshotBundle{}, classifyBundleLoadError(
			"load topology head", err,
		)
	}
	if err := budget.consume(topologyHead.Payload); err != nil {
		return SnapshotBundle{}, err
	}
	fileReference, err := decodeStrictBundle[fileStateRootReference](
		objects["file-state-root"],
	)
	if err != nil || fileReference.SourceRoot == "" {
		return SnapshotBundle{}, ErrBundleInvalid
	}
	fileStateHead, err := repository.GetManifest(ctx, fileReference.SourceRoot)
	if err != nil {
		return SnapshotBundle{}, classifyBundleLoadError(
			"load file-state head", err,
		)
	}
	if err := budget.consume(fileStateHead.Payload); err != nil {
		return SnapshotBundle{}, err
	}
	fileHead, err := decodeStrictBundle[fileStateHeadPayload](
		fileStateHead.Payload,
	)
	if err != nil {
		return SnapshotBundle{}, ErrBundleInvalid
	}
	var historyRoot *objectrepo.ManifestRecord
	historyObjects := map[objectrepo.ObjectID][]byte{}
	var historyMetadata []filehistory.RevisionObject
	if fileHead.HistoryRoot != "" {
		value, err := repository.GetManifest(ctx, fileHead.HistoryRoot)
		if err != nil {
			return SnapshotBundle{}, classifyBundleLoadError(
				"load file-history root", err,
			)
		}
		if err := budget.consume(value.Payload); err != nil {
			return SnapshotBundle{}, err
		}
		historyRoot = &value
		historyMetadata, err = filehistory.ValidateRootPayload(
			value.Payload, record.WorkspaceID,
		)
		if err != nil {
			if errors.Is(err, filehistory.ErrResourceLimit) {
				return SnapshotBundle{}, ErrBundleResourceLimit
			}
			return SnapshotBundle{}, ErrBundleInvalid
		}
		if len(historyMetadata) > maxBundleEntries {
			return SnapshotBundle{}, ErrBundleResourceLimit
		}
		for _, metadata := range historyMetadata {
			if _, found := objectCache[metadata.ObjectID]; found {
				continue
			}
			raw, err := budget.readObject(
				ctx, repository, metadata.ObjectID,
			)
			if err != nil {
				return SnapshotBundle{}, classifyBundleLoadError(
					fmt.Sprintf(
						"load file-history object %q",
						metadata.ObjectID,
					),
					err,
				)
			}
			objectCache[metadata.ObjectID] = raw
			historyObjects[metadata.ObjectID] = raw
		}
	}
	bundle := SnapshotBundle{
		Record:          record,
		Manifest:        manifest,
		Seal:            seal,
		Objects:         objects,
		TopologyHead:    topologyHead,
		FileStateHead:   fileStateHead,
		HistoryRoot:     historyRoot,
		HistoryObjects:  historyObjects,
		HistoryMetadata: historyMetadata,
	}
	if err := ValidateSnapshotBundleData(ctx, bundle); err != nil {
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
func ValidateSnapshotBundleData(
	ctx context.Context,
	bundle SnapshotBundle,
) error {
	if err := validateBundleMaterializedSize(bundle); err != nil {
		return err
	}
	record := bundle.Record
	if record.SnapshotID == "" ||
		record.WorkspaceID == "" ||
		record.ManifestID == "" ||
		record.SealID == "" ||
		record.SnapshotSequence == 0 ||
		record.FenceEpoch == 0 ||
		strings.TrimSpace(record.ClaimID) == "" ||
		record.CatalogRevision == 0 ||
		(record.SourceWorkspaceID == "") !=
			(record.SourceSnapshotID == "") ||
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
		manifest.Trigger != record.Trigger ||
		!manifest.CreatedAt.Equal(record.CreatedAt) ||
		manifest.AuditAnchor.ChainHash != record.AuditAnchor ||
		manifestSourceSnapshotID(manifest) != record.SourceSnapshotID ||
		!validBundleTrigger(manifest.Trigger) ||
		manifest.CreatedAt.IsZero() ||
		strings.TrimSpace(manifest.CreatedByDevice) == "" ||
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
	return validateBundleRoots(ctx, bundle, manifest)
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

func validateBundleRoots(
	ctx context.Context,
	bundle SnapshotBundle,
	manifest Manifest,
) error {
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
	if err := workspacedb.ValidateSnapshot(
		ctx,
		bundle.Objects["database"],
		topology.BusinessSchemaVersion,
	); err != nil {
		if errors.Is(err, workspacedb.ErrSnapshotDatabaseInvalid) {
			return errors.Join(ErrBundleInvalid, err)
		}
		return err
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
			len(bundle.HistoryObjects) != 0 ||
			len(bundle.HistoryMetadata) != 0 {
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
	required, err := filehistory.ValidateRootPayload(
		bundle.HistoryRoot.Payload,
		bundle.Record.WorkspaceID,
	)
	if err != nil {
		if errors.Is(err, filehistory.ErrResourceLimit) {
			return ErrBundleResourceLimit
		}
		return ErrBundleInvalid
	}
	if len(required) > maxBundleEntries {
		return ErrBundleResourceLimit
	}
	if len(bundle.HistoryMetadata) != 0 &&
		!equalHistoryMetadata(bundle.HistoryMetadata, required) {
		return ErrBundleInvalid
	}
	objectValues := map[objectrepo.ObjectID][]byte{}
	for name, id := range bundle.Record.ObjectMap {
		objectValues[id] = bundle.Objects[name]
	}
	requiredIDs := make(map[objectrepo.ObjectID]struct{}, len(required))
	for _, metadata := range required {
		requiredIDs[metadata.ObjectID] = struct{}{}
		raw, found := objectValues[metadata.ObjectID]
		if !found {
			raw, found = bundle.HistoryObjects[metadata.ObjectID]
		}
		if !found ||
			objectIDBundle(raw) != metadata.ObjectID ||
			digestBundle(raw) != metadata.ContentHash ||
			int64(len(raw)) != metadata.Size {
			return ErrBundleInvalid
		}
	}
	for id, raw := range bundle.HistoryObjects {
		if _, required := requiredIDs[id]; !required ||
			objectIDBundle(raw) != id {
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

func readBundleObject(
	ctx context.Context,
	repository objectrepo.Repository,
	id objectrepo.ObjectID,
) ([]byte, error) {
	budget := bundleReadBudget{remaining: MaxBundleMaterializedBytes}
	return budget.readObject(ctx, repository, id)
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

type bundleReadBudget struct {
	remaining int64
}

func (budget *bundleReadBudget) consume(raw []byte) error {
	size := int64(len(raw))
	if size > budget.remaining {
		return ErrBundleResourceLimit
	}
	budget.remaining -= size
	return nil
}

func (budget *bundleReadBudget) readObject(
	ctx context.Context,
	repository objectrepo.Repository,
	id objectrepo.ObjectID,
) ([]byte, error) {
	if budget.remaining < 0 {
		return nil, ErrBundleResourceLimit
	}
	reader, err := repository.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, budget.remaining+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if err := budget.consume(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func classifyBundleLoadError(stage string, err error) error {
	if errors.Is(err, ErrBundleResourceLimit) {
		return ErrBundleResourceLimit
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", stage, err)
	}
	if errors.Is(err, objectrepo.ErrNotFound) ||
		errors.Is(err, objectrepo.ErrCorrupt) {
		return fmt.Errorf("%w: %s: %w", ErrBundleInvalid, stage, err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}

func validateBundleMaterializedSize(bundle SnapshotBundle) error {
	if len(bundle.Record.ObjectMap) > maxBundleEntries ||
		len(bundle.Record.Objects) > maxBundleEntries ||
		len(bundle.Objects) > maxBundleEntries ||
		len(bundle.HistoryObjects) > maxBundleEntries ||
		len(bundle.HistoryMetadata) > maxBundleEntries {
		return ErrBundleResourceLimit
	}
	total := int64(0)
	add := func(raw []byte) bool {
		size := int64(len(raw))
		if size > MaxBundleMaterializedBytes-total {
			return false
		}
		total += size
		return true
	}
	if !add(bundle.Manifest.Payload) ||
		!add(bundle.Seal.Payload) ||
		!add(bundle.TopologyHead.Payload) ||
		!add(bundle.FileStateHead.Payload) {
		return ErrBundleResourceLimit
	}
	if bundle.HistoryRoot != nil && !add(bundle.HistoryRoot.Payload) {
		return ErrBundleResourceLimit
	}
	for _, raw := range bundle.Objects {
		if !add(raw) {
			return ErrBundleResourceLimit
		}
	}
	for _, raw := range bundle.HistoryObjects {
		if !add(raw) {
			return ErrBundleResourceLimit
		}
	}
	return nil
}

func validBundleTrigger(trigger Trigger) bool {
	switch trigger {
	case TriggerAutomatic,
		TriggerManual,
		TriggerProtection,
		TriggerRestore,
		TriggerImport:
		return true
	default:
		return false
	}
}

func manifestSourceSnapshotID(manifest Manifest) string {
	if manifest.SourceSnapshotID == nil {
		return ""
	}
	return *manifest.SourceSnapshotID
}

func equalHistoryMetadata(
	left []filehistory.RevisionObject,
	right []filehistory.RevisionObject,
) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]filehistory.RevisionObject(nil), left...)
	rightCopy := append([]filehistory.RevisionObject(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool {
		return leftCopy[i].ObjectID < leftCopy[j].ObjectID
	})
	sort.Slice(rightCopy, func(i, j int) bool {
		return rightCopy[i].ObjectID < rightCopy[j].ObjectID
	})
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
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

func FileHistoryObjectIDs(
	payload []byte,
	workspaceID string,
) ([]objectrepo.ObjectID, error) {
	history, err := filehistory.ValidateRootPayload(payload, workspaceID)
	if err != nil {
		if errors.Is(err, filehistory.ErrResourceLimit) {
			return nil, ErrBundleResourceLimit
		}
		return nil, fmt.Errorf("%w: history root", ErrBundleInvalid)
	}
	if len(history) > maxBundleEntries {
		return nil, ErrBundleResourceLimit
	}
	result := make([]objectrepo.ObjectID, 0, len(history))
	for _, revision := range history {
		result = append(result, revision.ObjectID)
	}
	return result, nil
}
