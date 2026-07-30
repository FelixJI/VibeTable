package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/workspacedb"
)

var (
	ErrMutationChanged = errors.New("snapshot.mutation_changed")
	ErrVerifyFailed    = errors.New("snapshot.verify_failed")
)

type BarrierView struct {
	MutationRevision      uint64
	SnapshotSequence      uint64
	SchemaRevision        uint64
	BusinessSchemaVersion uint64
	FileRevision          uint64
	AuditRevision         uint64
	AuditAnchor           string
	AuditEpoch            uint64
	AuditSequence         uint64
	TopologyRoot          string
	FileRoot              string
	Database              []byte
	Files                 map[string][]byte
	Attachments           map[string][]byte
	WorkspaceSettings     []byte
	AuditPrefix           []byte
	CreatedByDevice       string
	MinimumAppVersion     string
	SourceWorkspaceID     string
	SourceSnapshotID      string
}

type Barrier interface {
	Freeze(context.Context) (BarrierView, func(), error)
}

type Trigger string

const (
	TriggerAutomatic  Trigger = "automatic"
	TriggerManual     Trigger = "manual"
	TriggerProtection Trigger = "protection"
	TriggerSwitch     Trigger = "workspace-switch"
	TriggerRestore    Trigger = "restore"
	TriggerImport     Trigger = "import"
)

type CaptureRequest struct {
	WorkspaceID string
	Authority   objectrepo.Authority
	Trigger     Trigger
	Pinned      bool
}

type AuditAnchor struct {
	Epoch     uint64 `json:"epoch"`
	Sequence  uint64 `json:"sequence"`
	ChainHash string `json:"chainHash"`
}

// Manifest mirrors the v2 storage contract without leaking repository-native
// identifiers or implementation details to callers.
type Manifest struct {
	FormatVersion             uint64              `json:"formatVersion"`
	SnapshotID                string              `json:"snapshotId"`
	WorkspaceID               string              `json:"workspaceId"`
	FenceEpoch                uint64              `json:"fenceEpoch"`
	ClaimID                   string              `json:"claimId"`
	MutationRevision          uint64              `json:"mutationRevision"`
	SnapshotSequence          uint64              `json:"snapshotSequence"`
	Trigger                   Trigger             `json:"trigger"`
	CreatedAt                 time.Time           `json:"createdAt"`
	CreatedByDevice           string              `json:"createdByDevice"`
	BusinessDatabaseObjectID  objectrepo.ObjectID `json:"businessDatabaseObjectId"`
	TopologyRootObjectID      objectrepo.ObjectID `json:"topologyRootObjectId"`
	FileStateRootObjectID     objectrepo.ObjectID `json:"fileStateRootObjectId"`
	WorkspaceSettingsObjectID objectrepo.ObjectID `json:"workspaceSettingsObjectId"`
	AuditAnchor               AuditAnchor         `json:"auditAnchor"`
	AuditPrefixObjectID       objectrepo.ObjectID `json:"auditPrefixObjectId"`
	SourceSnapshotID          *string             `json:"sourceSnapshotId,omitempty"`
	MinimumAppVersion         string              `json:"minimumAppVersion"`
}

type Seal struct {
	FormatVersion     uint64 `json:"formatVersion"`
	SnapshotID        string `json:"snapshotId"`
	ManifestHash      string `json:"manifestHash"`
	DatabaseHash      string `json:"databaseHash"`
	FileStateRootHash string `json:"fileStateRootHash"`
	AuditAnchorHash   string `json:"auditAnchorHash"`
	RepositoryFormat  string `json:"repositoryFormat"`
	FenceEpoch        uint64 `json:"fenceEpoch"`
	ClaimID           string `json:"claimId"`
	MutationRevision  uint64 `json:"mutationRevision"`
	SnapshotSequence  uint64 `json:"snapshotSequence"`
	Verified          bool   `json:"verified"`
}

type Record struct {
	SnapshotID              string                         `json:"snapshotId"`
	WorkspaceID             string                         `json:"workspaceId"`
	ManifestID              objectrepo.ManifestID          `json:"manifestId"`
	SealID                  objectrepo.ManifestID          `json:"sealId"`
	SnapshotSequence        uint64                         `json:"snapshotSequence"`
	FenceEpoch              uint64                         `json:"fenceEpoch"`
	ClaimID                 string                         `json:"claimId"`
	MutationRevision        uint64                         `json:"mutationRevision"`
	SchemaRevision          uint64                         `json:"schemaRevision"`
	FileRevision            uint64                         `json:"fileRevision"`
	AuditRevision           uint64                         `json:"auditRevision"`
	AuditAnchor             string                         `json:"auditAnchor"`
	Trigger                 Trigger                        `json:"trigger"`
	Pinned                  bool                           `json:"pinned"`
	CreatedAt               time.Time                      `json:"createdAt"`
	Objects                 []objectrepo.ObjectID          `json:"objects"`
	ObjectMap               map[string]objectrepo.ObjectID `json:"objectMap"`
	RootPinID               string                         `json:"rootPinId"`
	CatalogRevision         uint64                         `json:"catalogRevision"`
	CatalogMutationRevision uint64                         `json:"catalogMutationRevision"`
	CatalogSessionEpoch     uint64                         `json:"catalogSessionEpoch"`
	CatalogFenceEpoch       uint64                         `json:"catalogFenceEpoch"`
	CatalogClaimID          string                         `json:"catalogClaimId"`
	LogicalSize             uint64                         `json:"logicalSize"`
	PhysicalSize            uint64                         `json:"physicalSize"`
	PhysicalSizeEstimated   bool                           `json:"physicalSizeEstimated"`
	InventoryRevision       uint64                         `json:"inventoryRevision"`
	SourceWorkspaceID       string                         `json:"sourceWorkspaceId,omitempty"`
	SourceSnapshotID        string                         `json:"sourceSnapshotId,omitempty"`
}

type Catalog interface {
	Last(context.Context, string) (Record, bool, error)
	Publish(context.Context, Record) error
	List(context.Context, string) ([]Record, error)
}

type Coordinator struct {
	captureMu sync.Mutex

	repository objectrepo.Repository
	barrier    Barrier
	catalog    Catalog
	now        func() time.Time
}

func NewCoordinator(repository objectrepo.Repository, barrier Barrier, catalog Catalog) *Coordinator {
	return &Coordinator{
		repository: repository,
		barrier:    barrier,
		catalog:    catalog,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

func (coordinator *Coordinator) Capture(
	ctx context.Context,
	request CaptureRequest,
) (Record, bool, error) {
	coordinator.captureMu.Lock()
	defer coordinator.captureMu.Unlock()
	if coordinator.repository == nil || coordinator.barrier == nil || coordinator.catalog == nil {
		return Record{}, false, errors.New("snapshot.dependencies_required")
	}
	if request.WorkspaceID == "" || request.Authority.WorkspaceID != request.WorkspaceID {
		return Record{}, false, errors.New("snapshot.workspace_invalid")
	}
	if request.Trigger == "" {
		request.Trigger = TriggerAutomatic
	}
	view, release, err := coordinator.barrier.Freeze(ctx)
	if err != nil {
		return Record{}, false, err
	}
	defer release()
	if view.SnapshotSequence == 0 {
		return Record{}, false, errors.New("snapshot.sequence_invalid")
	}

	last, ok, err := coordinator.catalog.Last(ctx, request.WorkspaceID)
	if err != nil {
		return Record{}, false, err
	}
	if ok &&
		request.Trigger == TriggerAutomatic &&
		last.MutationRevision == view.MutationRevision {
		return last, false, nil
	}

	createdAt := coordinator.now()
	snapshotID := uuid.NewString()
	normalized, err := normalizeBarrierView(view)
	if err != nil {
		return Record{}, false, err
	}
	view = normalized
	if err := workspacedb.ValidateSnapshot(
		ctx,
		view.Database,
		view.BusinessSchemaVersion,
	); err != nil {
		return Record{}, false, err
	}
	inputs, manifest, seal, err := buildSnapshot(
		request, view, snapshotID, createdAt,
	)
	if err != nil {
		return Record{}, false, err
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		return Record{}, false, err
	}
	receipt, err := coordinator.repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: request.Authority,
		Objects:   inputs,
		Manifests: []objectrepo.ManifestInput{{
			Name: "snapshot",
			Labels: map[string]string{
				"type":        "snapshot",
				"workspaceId": request.WorkspaceID,
				"snapshotId":  snapshotID,
			},
			Payload: manifestPayload,
		}},
	})
	if err != nil {
		return Record{}, false, err
	}
	if err := validateReceipt(request, inputs, receipt); err != nil {
		return Record{}, false, err
	}
	roots := objectRoots(receipt.Objects)
	historyRoots, err := FileHistoryObjectIDsForHead(
		ctx,
		coordinator.repository,
		request.WorkspaceID,
		objectrepo.ManifestID(view.FileRoot),
	)
	if err != nil {
		return Record{}, false, err
	}
	reachabilityRoots := mergeSnapshotObjectIDs(roots, historyRoots)
	pinned := request.Pinned || isProtectionTrigger(request.Trigger)
	expiry := createdAt.Add(24 * time.Hour)
	var pinExpiry *time.Time
	if !pinned {
		pinExpiry = &expiry
	}
	pin, err := coordinator.repository.Pin(
		ctx,
		request.Authority,
		reachabilityRoots,
		"snapshot:"+snapshotID,
		pinExpiry,
	)
	if err != nil {
		return Record{}, false, err
	}
	releasePin := func() {
		_ = coordinator.repository.ReleasePin(
			context.WithoutCancel(ctx), request.Authority, pin.PinID,
		)
	}
	report, err := coordinator.repository.Verify(ctx, reachabilityRoots)
	if err != nil {
		releasePin()
		return Record{}, false, err
	}
	if !report.Valid {
		releasePin()
		return Record{}, false, fmt.Errorf(
			"%w: missing=%d corrupt=%d",
			ErrVerifyFailed, len(report.Missing), len(report.Corrupt),
		)
	}
	seal.ManifestHash = digest(manifestPayload)
	sealPayload, err := json.Marshal(seal)
	if err != nil {
		releasePin()
		return Record{}, false, err
	}
	sealReceipt, err := coordinator.repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: request.Authority,
		Manifests: []objectrepo.ManifestInput{{
			Name: "snapshot-seal",
			Labels: map[string]string{
				"type":        "snapshot-seal",
				"workspaceId": request.WorkspaceID,
				"snapshotId":  snapshotID,
			},
			Payload: sealPayload,
		}},
	})
	if err != nil {
		releasePin()
		return Record{}, false, err
	}
	if !sealReceipt.Durable ||
		sealReceipt.WorkspaceID != request.WorkspaceID ||
		sealReceipt.FenceEpoch != request.Authority.FenceEpoch ||
		sealReceipt.ClaimID != request.Authority.ClaimID ||
		sealReceipt.Manifests["snapshot-seal"] == "" {
		releasePin()
		return Record{}, false, errors.New("snapshot.seal_receipt_invalid")
	}
	inventorySource, ok := coordinator.repository.(objectrepo.StorageInventorySource)
	if !ok {
		releasePin()
		return Record{}, false, errors.New(
			"snapshot.storage_inventory_unavailable",
		)
	}
	inventory, err := inventorySource.StorageInventory(ctx, reachabilityRoots)
	if err != nil {
		releasePin()
		return Record{}, false, err
	}
	record := Record{
		SnapshotID: snapshotID, WorkspaceID: request.WorkspaceID,
		ManifestID:            receipt.Manifests["snapshot"],
		SealID:                sealReceipt.Manifests["snapshot-seal"],
		SnapshotSequence:      view.SnapshotSequence,
		FenceEpoch:            request.Authority.FenceEpoch,
		ClaimID:               request.Authority.ClaimID,
		MutationRevision:      view.MutationRevision,
		SchemaRevision:        view.SchemaRevision,
		FileRevision:          view.FileRevision,
		AuditRevision:         view.AuditRevision,
		AuditAnchor:           view.AuditAnchor,
		Trigger:               manifest.Trigger,
		Pinned:                pinned,
		CreatedAt:             createdAt,
		Objects:               roots,
		ObjectMap:             cloneObjectMap(receipt.Objects),
		RootPinID:             pin.PinID,
		CatalogRevision:       view.SnapshotSequence,
		LogicalSize:           inventory.LogicalBytes,
		PhysicalSize:          inventory.PhysicalBytes,
		PhysicalSizeEstimated: inventory.PhysicalBytesEstimated,
		InventoryRevision:     inventory.RepositoryRevision,
		SourceWorkspaceID:     view.SourceWorkspaceID,
		SourceSnapshotID:      view.SourceSnapshotID,
	}
	var publishErr error
	if builder, requested := operationReceiptBuilder(ctx); requested {
		catalog, ok := coordinator.catalog.(OperationReceiptCatalog)
		if !ok {
			releasePin()
			return Record{}, false,
				errors.New("snapshot.operation_receipt_store_required")
		}
		operationReceipt, buildErr := builder(record)
		if buildErr != nil {
			releasePin()
			return Record{}, false, buildErr
		}
		publishErr = catalog.PublishWithOperationReceipt(
			ctx,
			record,
			operationReceipt,
		)
	} else {
		publishErr = coordinator.catalog.Publish(ctx, record)
	}
	if publishErr != nil {
		releasePin()
		return Record{}, false, publishErr
	}
	return record, true, nil
}

func normalizeBarrierView(view BarrierView) (BarrierView, error) {
	var err error
	if view.AuditEpoch == 0 {
		view.AuditEpoch = 1
	}
	if view.AuditSequence == 0 {
		view.AuditSequence = view.AuditRevision
	}
	if len(view.WorkspaceSettings) == 0 {
		view.WorkspaceSettings = []byte("{}")
	}
	if len(view.AuditPrefix) == 0 {
		view.AuditPrefix, err = json.Marshal(AuditAnchor{
			Epoch: view.AuditEpoch, Sequence: view.AuditSequence,
			ChainHash: view.AuditAnchor,
		})
		if err != nil {
			return BarrierView{}, err
		}
	}
	if view.MinimumAppVersion == "" {
		view.MinimumAppVersion = "2.0.0"
	}
	if view.BusinessSchemaVersion == 0 {
		return BarrierView{}, errors.New(
			"snapshot.business_schema_version_invalid",
		)
	}
	if view.CreatedByDevice == "" {
		view.CreatedByDevice = "00000000-0000-4000-8000-000000000000"
	}
	if !validDigest(view.AuditAnchor) {
		return BarrierView{}, errors.New("snapshot.audit_anchor_invalid")
	}
	return view, nil
}

func buildSnapshot(
	request CaptureRequest,
	view BarrierView,
	snapshotID string,
	createdAt time.Time,
) ([]objectrepo.ObjectInput, Manifest, Seal, error) {
	topologyRoot, err := json.Marshal(map[string]string{
		"manifestId": view.TopologyRoot,
	})
	if err != nil {
		return nil, Manifest{}, Seal{}, err
	}
	fileNames := make([]string, 0, len(view.Files))
	for name := range view.Files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	fileObjects := make(map[string]objectrepo.ObjectID, len(fileNames))
	attachmentNames := make([]string, 0, len(view.Attachments))
	for name := range view.Attachments {
		attachmentNames = append(attachmentNames, name)
	}
	sort.Strings(attachmentNames)
	attachmentObjects := make(
		map[string]objectrepo.ObjectID,
		len(attachmentNames),
	)
	inputs := []objectrepo.ObjectInput{
		{Name: "database", Content: view.Database},
		{Name: "topology-root", Content: topologyRoot},
		{Name: "workspace-settings", Content: view.WorkspaceSettings},
		{Name: "audit-prefix", Content: view.AuditPrefix},
	}
	for _, name := range fileNames {
		content := view.Files[name]
		fileObjects[name] = contentObjectID(content)
		inputs = append(inputs, objectrepo.ObjectInput{
			Name: "file:" + name, Content: content,
		})
	}
	for _, name := range attachmentNames {
		content := view.Attachments[name]
		attachmentObjects[name] = contentObjectID(content)
		inputs = append(inputs, objectrepo.ObjectInput{
			Name: "attachment:" + name, Content: content,
		})
	}
	fileStateRoot, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"sourceRoot":    view.FileRoot,
		"files":         fileObjects,
		"attachments":   attachmentObjects,
	})
	if err != nil {
		return nil, Manifest{}, Seal{}, err
	}
	inputs = append(inputs, objectrepo.ObjectInput{
		Name: "file-state-root", Content: fileStateRoot,
	})
	auditAnchor := AuditAnchor{
		Epoch: view.AuditEpoch, Sequence: view.AuditSequence,
		ChainHash: view.AuditAnchor,
	}
	manifest := Manifest{
		FormatVersion: 2, SnapshotID: snapshotID, WorkspaceID: request.WorkspaceID,
		FenceEpoch: request.Authority.FenceEpoch, ClaimID: request.Authority.ClaimID,
		MutationRevision: view.MutationRevision, SnapshotSequence: view.SnapshotSequence,
		Trigger: manifestTrigger(request.Trigger), CreatedAt: createdAt,
		CreatedByDevice:          view.CreatedByDevice,
		BusinessDatabaseObjectID: contentObjectID(view.Database),
		TopologyRootObjectID:     contentObjectID(topologyRoot),
		FileStateRootObjectID:    contentObjectID(fileStateRoot),
		WorkspaceSettingsObjectID: contentObjectID(
			view.WorkspaceSettings,
		),
		AuditAnchor:         auditAnchor,
		AuditPrefixObjectID: contentObjectID(view.AuditPrefix),
		MinimumAppVersion:   view.MinimumAppVersion,
	}
	if view.SourceSnapshotID != "" {
		source := view.SourceSnapshotID
		manifest.SourceSnapshotID = &source
	}
	auditAnchorRaw, err := json.Marshal(auditAnchor)
	if err != nil {
		return nil, Manifest{}, Seal{}, err
	}
	seal := Seal{
		FormatVersion: 2, SnapshotID: snapshotID,
		DatabaseHash:      digest(view.Database),
		FileStateRootHash: digest(fileStateRoot),
		AuditAnchorHash:   digest(auditAnchorRaw),
		RepositoryFormat:  "workspace-repository-v2",
		FenceEpoch:        request.Authority.FenceEpoch,
		ClaimID:           request.Authority.ClaimID,
		MutationRevision:  view.MutationRevision,
		SnapshotSequence:  view.SnapshotSequence,
		Verified:          true,
	}
	return inputs, manifest, seal, nil
}

func validateReceipt(
	request CaptureRequest,
	inputs []objectrepo.ObjectInput,
	receipt objectrepo.DurableCommitReceipt,
) error {
	if !receipt.Durable ||
		receipt.WorkspaceID != request.WorkspaceID ||
		receipt.FenceEpoch != request.Authority.FenceEpoch ||
		receipt.ClaimID != request.Authority.ClaimID ||
		receipt.Manifests["snapshot"] == "" {
		return errors.New("snapshot.commit_receipt_invalid")
	}
	for _, input := range inputs {
		if receipt.Objects[input.Name] != contentObjectID(input.Content) {
			return errors.New("snapshot.object_receipt_invalid")
		}
	}
	return nil
}

func manifestTrigger(trigger Trigger) Trigger {
	switch trigger {
	case TriggerSwitch:
		return TriggerProtection
	default:
		return trigger
	}
}

func isProtectionTrigger(trigger Trigger) bool {
	return trigger == TriggerManual ||
		trigger == TriggerProtection ||
		trigger == TriggerSwitch ||
		trigger == TriggerRestore
}

func validDigest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 ||
		value[:len(prefix)] != prefix {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func objectRoots(objects map[string]objectrepo.ObjectID) []objectrepo.ObjectID {
	unique := map[objectrepo.ObjectID]struct{}{}
	for _, id := range objects {
		unique[id] = struct{}{}
	}
	roots := make([]objectrepo.ObjectID, 0, len(unique))
	for id := range unique {
		roots = append(roots, id)
	}
	sort.Slice(roots, func(left, right int) bool {
		return roots[left] < roots[right]
	})
	return roots
}

func contentObjectID(content []byte) objectrepo.ObjectID {
	sum := sha256.Sum256(content)
	return objectrepo.ObjectID("obj_" + hex.EncodeToString(sum[:]))
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func cloneObjectMap(
	source map[string]objectrepo.ObjectID,
) map[string]objectrepo.ObjectID {
	result := make(map[string]objectrepo.ObjectID, len(source))
	for name, id := range source {
		result[name] = id
	}
	return result
}

type MemoryCatalog struct {
	mu      sync.RWMutex
	records map[string][]Record
	fault   error
}

func NewMemoryCatalog() *MemoryCatalog {
	return &MemoryCatalog{records: map[string][]Record{}}
}

func (catalog *MemoryCatalog) WithPublishError(err error) *MemoryCatalog {
	catalog.fault = err
	return catalog
}

func (catalog *MemoryCatalog) Last(
	_ context.Context,
	workspaceID string,
) (Record, bool, error) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	records := catalog.records[workspaceID]
	if len(records) == 0 {
		return Record{}, false, nil
	}
	return cloneRecord(records[len(records)-1]), true, nil
}

func (catalog *MemoryCatalog) Publish(_ context.Context, record Record) error {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if catalog.fault != nil {
		return catalog.fault
	}
	if record.CatalogRevision == 0 {
		record.CatalogRevision = record.SnapshotSequence
	}
	catalog.records[record.WorkspaceID] = append(
		catalog.records[record.WorkspaceID], cloneRecord(record),
	)
	return nil
}

func (catalog *MemoryCatalog) List(
	_ context.Context,
	workspaceID string,
) ([]Record, error) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	records := catalog.records[workspaceID]
	result := make([]Record, len(records))
	for index, record := range records {
		result[index] = cloneRecord(record)
	}
	return result, nil
}

func cloneRecord(record Record) Record {
	record.Objects = append([]objectrepo.ObjectID(nil), record.Objects...)
	record.ObjectMap = cloneObjectMap(record.ObjectMap)
	return record
}
