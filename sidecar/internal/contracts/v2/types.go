package v2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ContractVersion = "2.0"

var (
	objectIDPattern = regexp.MustCompile(`^obj_[0-9a-f]{64}$`)
	sha256Pattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	semverPattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

type Validator interface {
	Validate() error
}

func DecodeStrict[T Validator](raw []byte) (T, error) {
	var result T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode v2 contract: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return result, errors.New("decode v2 contract: trailing JSON")
	}
	if err := result.Validate(); err != nil {
		return result, fmt.Errorf("validate v2 contract: %w", err)
	}
	return result, nil
}

type WorkspaceWireScope struct {
	Scope        string `json:"scope"`
	WorkspaceID  string `json:"workspaceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	OperationID  string `json:"operationId"`
	Sequence     uint64 `json:"sequence"`
}

type GlobalWireScope struct {
	Scope       string `json:"scope"`
	OperationID string `json:"operationId"`
	Sequence    uint64 `json:"sequence"`
}

func (scope GlobalWireScope) Validate() error {
	if scope.Scope != "global" {
		return errors.New("scope must be global")
	}
	if !validUUID(scope.OperationID) {
		return errors.New("operationId must be a UUID")
	}
	return nil
}

func (scope WorkspaceWireScope) Validate() error {
	if scope.Scope != "workspace" {
		return errors.New("scope must be workspace")
	}
	if !validUUID(scope.WorkspaceID) || !validUUID(scope.OperationID) {
		return errors.New("workspaceId and operationId must be UUIDs")
	}
	if scope.SessionEpoch == 0 {
		return errors.New("sessionEpoch must be positive")
	}
	return nil
}

func (scope WorkspaceWireScope) EnsureCurrent(
	workspaceID string,
	sessionEpoch uint64,
	minimumSequence uint64,
) error {
	if scope.WorkspaceID != workspaceID {
		return errors.New("workspace.workspace_mismatch")
	}
	if scope.SessionEpoch != sessionEpoch {
		return errors.New("workspace.session_epoch_stale")
	}
	if scope.Sequence < minimumSequence {
		return errors.New("workspace.sequence_stale")
	}
	return nil
}

type WorkspaceManifest struct {
	ContractVersion         string  `json:"contractVersion"`
	FormatVersion           uint64  `json:"formatVersion"`
	WorkspaceID             string  `json:"workspaceId"`
	DisplayName             string  `json:"displayName"`
	CreatedAt               string  `json:"createdAt"`
	StorageMode             string  `json:"storageMode"`
	EncryptionMode          string  `json:"encryptionMode"`
	RepositoryFormat        string  `json:"repositoryFormat"`
	TopologySchemaVersion   uint64  `json:"topologySchemaVersion"`
	BusinessSchemaVersion   uint64  `json:"businessSchemaVersion"`
	ImportedFromWorkspaceID *string `json:"importedFromWorkspaceId"`
	SourceSnapshotID        *string `json:"sourceSnapshotId"`
}

func (value WorkspaceManifest) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if value.FormatVersion == 0 ||
		value.TopologySchemaVersion == 0 ||
		value.BusinessSchemaVersion == 0 {
		return errors.New("workspace format versions must be positive")
	}
	if !validUUID(value.WorkspaceID) || strings.TrimSpace(value.DisplayName) == "" {
		return errors.New("workspace identity is invalid")
	}
	if _, err := time.Parse(time.RFC3339, value.CreatedAt); err != nil {
		return errors.New("createdAt must be RFC3339")
	}
	if !oneOf(value.StorageMode, "direct", "mirrored") {
		return errors.New("storageMode is invalid")
	}
	if !oneOf(value.EncryptionMode, "none", "convenient", "protected") {
		return errors.New("encryptionMode is invalid")
	}
	if strings.TrimSpace(value.RepositoryFormat) == "" {
		return errors.New("repositoryFormat is required")
	}
	if !validOptionalUUID(value.ImportedFromWorkspaceID) ||
		!validOptionalUUID(value.SourceSnapshotID) {
		return errors.New("workspace provenance is invalid")
	}
	return nil
}

type WorkspaceRegistryEntry struct {
	ContractVersion      string  `json:"contractVersion"`
	WorkspaceID          string  `json:"workspaceId"`
	DisplayName          string  `json:"displayName"`
	SelectedRoot         string  `json:"selectedRoot"`
	ActivityRoot         *string `json:"activityRoot"`
	StorageKind          string  `json:"storageKind"`
	CoordinationStrength string  `json:"coordinationStrength"`
	LastOpenedAt         *string `json:"lastOpenedAt"`
	LastKnownHealth      string  `json:"lastKnownHealth"`
	LastSnapshotAt       *string `json:"lastSnapshotAt"`
	LastSyncAt           *string `json:"lastSyncAt"`
	PendingSync          bool    `json:"pendingSync"`
}

func (value WorkspaceRegistryEntry) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !validUUID(value.WorkspaceID) ||
		strings.TrimSpace(value.DisplayName) == "" ||
		strings.TrimSpace(value.SelectedRoot) == "" {
		return errors.New("registry identity is invalid")
	}
	if !oneOf(
		value.StorageKind,
		"fixed", "network", "removable", "registeredCloud", "userMarkedSync",
	) {
		return errors.New("storageKind is invalid")
	}
	if !oneOf(value.CoordinationStrength, "strong", "advisory") {
		return errors.New("coordinationStrength is invalid")
	}
	if !oneOf(
		value.LastKnownHealth,
		"healthy", "offline", "degraded", "corrupt", "unknown",
	) {
		return errors.New("lastKnownHealth is invalid")
	}
	for _, stamp := range []*string{
		value.LastOpenedAt, value.LastSnapshotAt, value.LastSyncAt,
	} {
		if stamp != nil {
			if _, err := time.Parse(time.RFC3339, *stamp); err != nil {
				return errors.New("registry timestamp must be RFC3339")
			}
		}
	}
	return nil
}

type WorkspaceSession struct {
	ContractVersion string  `json:"contractVersion"`
	WorkspaceID     *string `json:"workspaceId"`
	SessionEpoch    uint64  `json:"sessionEpoch"`
	State           string  `json:"state"`
	OpenMode        string  `json:"openMode"`
	Writable        bool    `json:"writable"`
	Provisional     bool    `json:"provisional"`
	Phase           string  `json:"phase"`
	ErrorCode       *string `json:"errorCode"`
}

func (value WorkspaceSession) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !oneOf(
		value.State,
		"closed", "opening", "openedReadOnly", "openedWritable",
		"openedProvisional", "switching", "failed",
	) || !oneOf(value.OpenMode, "readOnly", "writable", "provisional") ||
		!oneOf(
			value.Phase,
			"idle", "protecting", "draining", "stopping", "starting",
			"binding", "verifying", "rollingBack",
		) {
		return errors.New("workspace session enum is invalid")
	}
	if value.State == "closed" {
		if value.WorkspaceID != nil || value.Writable || value.Provisional {
			return errors.New("closed session cannot own a workspace")
		}
		return nil
	}
	if !validOptionalUUID(value.WorkspaceID) || value.WorkspaceID == nil {
		return errors.New("open session requires workspaceId")
	}
	if value.SessionEpoch == 0 {
		return errors.New("open session requires sessionEpoch")
	}
	if value.State == "openedWritable" && !value.Writable {
		return errors.New("openedWritable session must be writable")
	}
	if value.State == "openedProvisional" && !value.Provisional {
		return errors.New("openedProvisional session must be provisional")
	}
	return nil
}

type FileDocument struct {
	ContractVersion     string  `json:"contractVersion"`
	DocumentID          string  `json:"documentId"`
	WorkspaceID         string  `json:"workspaceId"`
	RelativePath        string  `json:"relativePath"`
	Status              string  `json:"status"`
	EffectiveRevisionID *string `json:"effectiveRevisionId"`
	NextRevisionOrdinal uint64  `json:"nextRevisionOrdinal"`
	NextFormalVersion   uint64  `json:"nextFormalVersion"`
}

func (value FileDocument) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !validUUID(value.DocumentID) || !validUUID(value.WorkspaceID) ||
		!validOptionalUUID(value.EffectiveRevisionID) {
		return errors.New("file document identity is invalid")
	}
	if !oneOf(value.Status, "active", "deleted") ||
		value.NextRevisionOrdinal == 0 ||
		value.NextFormalVersion == 0 {
		return errors.New("file document state is invalid")
	}
	path := strings.ReplaceAll(value.RelativePath, `\`, "/")
	if path == "" || strings.HasPrefix(path, "/") {
		return errors.New("file_history.path_invalid")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "" {
			return errors.New("file_history.path_invalid")
		}
	}
	return nil
}

type FileRevision struct {
	ContractVersion        string  `json:"contractVersion"`
	RevisionID             string  `json:"revisionId"`
	DocumentID             string  `json:"documentId"`
	ParentRevisionID       *string `json:"parentRevisionId"`
	RevisionOrdinal        uint64  `json:"revisionOrdinal"`
	FormalVersion          *uint64 `json:"formalVersion"`
	Kind                   string  `json:"kind"`
	ObjectID               string  `json:"objectId"`
	ContentHash            string  `json:"contentHash"`
	Size                   uint64  `json:"size"`
	MimeType               string  `json:"mimeType"`
	CreatedAt              string  `json:"createdAt"`
	CreatedBy              string  `json:"createdBy"`
	DeviceID               string  `json:"deviceId"`
	Comment                *string `json:"comment"`
	RestoredFromRevisionID *string `json:"restoredFromRevisionId"`
}

func (value FileRevision) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !validUUID(value.RevisionID) || !validUUID(value.DocumentID) ||
		!validUUID(value.DeviceID) ||
		!validOptionalUUID(value.ParentRevisionID) ||
		!validOptionalUUID(value.RestoredFromRevisionID) {
		return errors.New("file revision identity is invalid")
	}
	if value.RevisionOrdinal == 0 || !oneOf(value.Kind, "autosave", "formal", "restore") {
		return errors.New("file revision state is invalid")
	}
	if value.Kind == "autosave" && value.FormalVersion != nil {
		return errors.New("autosave cannot consume a formal version")
	}
	if value.Kind != "autosave" &&
		(value.FormalVersion == nil || *value.FormalVersion == 0) {
		return errors.New("formal revision requires a version")
	}
	if value.Kind == "restore" && value.RestoredFromRevisionID == nil {
		return errors.New("restore requires restoredFromRevisionId")
	}
	if value.Kind != "restore" && value.RestoredFromRevisionID != nil {
		return errors.New("only restore may reference restoredFromRevisionId")
	}
	if !objectIDPattern.MatchString(value.ObjectID) ||
		!sha256Pattern.MatchString(value.ContentHash) ||
		strings.TrimSpace(value.MimeType) == "" ||
		strings.TrimSpace(value.CreatedBy) == "" {
		return errors.New("file revision content is invalid")
	}
	if _, err := time.Parse(time.RFC3339, value.CreatedAt); err != nil {
		return errors.New("createdAt must be RFC3339")
	}
	return nil
}

type AuditAnchor struct {
	Epoch     uint64 `json:"epoch"`
	Sequence  uint64 `json:"sequence"`
	ChainHash string `json:"chainHash"`
}

func (value AuditAnchor) Validate() error {
	if value.Epoch == 0 || !sha256Pattern.MatchString(value.ChainHash) {
		return errors.New("audit anchor is invalid")
	}
	return nil
}

type SnapshotManifest struct {
	ContractVersion           string      `json:"contractVersion"`
	SnapshotID                string      `json:"snapshotId"`
	WorkspaceID               string      `json:"workspaceId"`
	FenceEpoch                uint64      `json:"fenceEpoch"`
	ClaimID                   string      `json:"claimId"`
	MutationRevision          uint64      `json:"mutationRevision"`
	SnapshotSequence          uint64      `json:"snapshotSequence"`
	Trigger                   string      `json:"trigger"`
	CreatedAt                 string      `json:"createdAt"`
	CreatedByDevice           string      `json:"createdByDevice"`
	BusinessDatabaseObjectID  string      `json:"businessDatabaseObjectId"`
	TopologyRootObjectID      string      `json:"topologyRootObjectId"`
	FileStateRootObjectID     string      `json:"fileStateRootObjectId"`
	WorkspaceSettingsObjectID string      `json:"workspaceSettingsObjectId"`
	AuditAnchor               AuditAnchor `json:"auditAnchor"`
	AuditPrefixObjectID       string      `json:"auditPrefixObjectId"`
	SourceSnapshotID          *string     `json:"sourceSnapshotId"`
	FormatVersion             uint64      `json:"formatVersion"`
	MinimumAppVersion         string      `json:"minimumAppVersion"`
}

func (value SnapshotManifest) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !validUUID(value.SnapshotID) || !validUUID(value.WorkspaceID) ||
		!validUUID(value.ClaimID) || !validUUID(value.CreatedByDevice) ||
		!validOptionalUUID(value.SourceSnapshotID) {
		return errors.New("snapshot identity is invalid")
	}
	if value.FenceEpoch == 0 || value.SnapshotSequence == 0 || value.FormatVersion == 0 {
		return errors.New("snapshot counters are invalid")
	}
	if !oneOf(value.Trigger, "automatic", "manual", "protection", "import", "restore") {
		return errors.New("snapshot trigger is invalid")
	}
	if _, err := time.Parse(time.RFC3339, value.CreatedAt); err != nil {
		return errors.New("createdAt must be RFC3339")
	}
	for _, id := range []string{
		value.BusinessDatabaseObjectID,
		value.TopologyRootObjectID,
		value.FileStateRootObjectID,
		value.WorkspaceSettingsObjectID,
		value.AuditPrefixObjectID,
	} {
		if !objectIDPattern.MatchString(id) {
			return errors.New("snapshot object ID is invalid")
		}
	}
	if err := value.AuditAnchor.Validate(); err != nil {
		return err
	}
	if !semverPattern.MatchString(value.MinimumAppVersion) {
		return errors.New("minimumAppVersion is invalid")
	}
	return nil
}

type SnapshotSeal struct {
	ContractVersion   string `json:"contractVersion"`
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

func (value SnapshotSeal) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !validUUID(value.SnapshotID) || !validUUID(value.ClaimID) ||
		value.FenceEpoch == 0 || value.SnapshotSequence == 0 ||
		!value.Verified || strings.TrimSpace(value.RepositoryFormat) == "" {
		return errors.New("snapshot seal is invalid")
	}
	for _, digest := range []string{
		value.ManifestHash,
		value.DatabaseHash,
		value.FileStateRootHash,
		value.AuditAnchorHash,
	} {
		if !sha256Pattern.MatchString(digest) {
			return errors.New("snapshot seal hash is invalid")
		}
	}
	return nil
}

type SnapshotCatalogEntry struct {
	ContractVersion  string   `json:"contractVersion"`
	SnapshotID       string   `json:"snapshotId"`
	State            string   `json:"state"`
	Pinned           bool     `json:"pinned"`
	RetentionReasons []string `json:"retentionReasons"`
	Integrity        string   `json:"integrity"`
	SyncState        string   `json:"syncState"`
	LogicalSize      uint64   `json:"logicalSize"`
	PhysicalSize     uint64   `json:"physicalSize"`
	Note             *string  `json:"note"`
	CatalogRevision  uint64   `json:"catalogRevision"`
}

func (value SnapshotCatalogEntry) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !validUUID(value.SnapshotID) || value.CatalogRevision == 0 ||
		!oneOf(
			value.State,
			"queued", "barrier", "captured", "chunking", "verifying",
			"published", "syncing", "ready", "failed", "corrupt", "repairing",
		) ||
		!oneOf(value.Integrity, "pending", "verified", "corrupt", "repairing") ||
		!oneOf(value.SyncState, "localOnly", "pending", "syncing", "replicated", "failed") {
		return errors.New("snapshot catalog entry is invalid")
	}
	for _, reason := range value.RetentionReasons {
		if strings.TrimSpace(reason) == "" {
			return errors.New("retention reason is invalid")
		}
	}
	return nil
}

type LeaseClaim struct {
	ContractVersion      string  `json:"contractVersion"`
	WorkspaceID          string  `json:"workspaceId"`
	FenceEpoch           uint64  `json:"fenceEpoch"`
	ClaimID              string  `json:"claimId"`
	DeviceID             string  `json:"deviceId"`
	IssuedAt             string  `json:"issuedAt"`
	HeartbeatAt          string  `json:"heartbeatAt"`
	ExpiresAt            string  `json:"expiresAt"`
	Mode                 string  `json:"mode"`
	PreviousClaimID      *string `json:"previousClaimId"`
	CoordinationStrength string  `json:"coordinationStrength"`
}

func (value LeaseClaim) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !validUUID(value.WorkspaceID) || !validUUID(value.ClaimID) ||
		!validUUID(value.DeviceID) || !validOptionalUUID(value.PreviousClaimID) ||
		value.FenceEpoch == 0 ||
		!oneOf(value.Mode, "writable", "provisional") ||
		!oneOf(value.CoordinationStrength, "strong", "advisory") {
		return errors.New("lease claim is invalid")
	}
	issued, issueErr := time.Parse(time.RFC3339, value.IssuedAt)
	heartbeat, heartbeatErr := time.Parse(time.RFC3339, value.HeartbeatAt)
	expires, expiresErr := time.Parse(time.RFC3339, value.ExpiresAt)
	if issueErr != nil || heartbeatErr != nil || expiresErr != nil ||
		heartbeat.Before(issued) || !expires.After(heartbeat) {
		return errors.New("lease timestamps are invalid")
	}
	return nil
}

type RetentionPolicy struct {
	ContractVersion      string   `json:"contractVersion"`
	PolicyRevision       uint64   `json:"policyRevision"`
	SnapshotDays         uint64   `json:"snapshotDays"`
	SnapshotCount        uint64   `json:"snapshotCount"`
	SnapshotBuckets      []string `json:"snapshotBuckets"`
	FileRevisionDays     uint64   `json:"fileRevisionDays"`
	FileRevisionCount    uint64   `json:"fileRevisionCount"`
	FileRevisionBuckets  []string `json:"fileRevisionBuckets"`
	TrashMonths          uint64   `json:"trashMonths"`
	RepositoryLimitBytes *uint64  `json:"repositoryLimitBytes"`
}

func (value RetentionPolicy) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if value.PolicyRevision == 0 || value.SnapshotDays == 0 ||
		value.SnapshotCount == 0 || value.FileRevisionDays == 0 ||
		value.FileRevisionCount == 0 || value.TrashMonths != 3 {
		return errors.New("retention policy counters are invalid")
	}
	if value.RepositoryLimitBytes != nil && *value.RepositoryLimitBytes == 0 {
		return errors.New("repository limit must be positive")
	}
	if err := validateBuckets(value.SnapshotBuckets); err != nil {
		return err
	}
	return validateBuckets(value.FileRevisionBuckets)
}

type WorkspaceEvent struct {
	ContractVersion string             `json:"contractVersion"`
	Topic           string             `json:"topic"`
	Wire            WorkspaceWireScope `json:"wire"`
	PayloadModel    string             `json:"payloadModel"`
	PayloadSchema   map[string]any     `json:"payloadSchema"`
	Payload         map[string]any     `json:"payload"`
}

type RPCRequestEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Wire    json.RawMessage `json:"wire"`
	Params  map[string]any  `json:"params"`
}

type RPCSuccessEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Wire    json.RawMessage `json:"wire"`
	Result  json.RawMessage `json:"result"`
}

type RPCErrorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	Retryable bool           `json:"retryable"`
}

type RPCErrorEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Wire    json.RawMessage `json:"wire"`
	Error   RPCErrorBody    `json:"error"`
}

type RPCGoldenCase struct {
	Method       string             `json:"method"`
	Scope        string             `json:"scope"`
	ParamsModel  string             `json:"paramsModel"`
	ResultModel  string             `json:"resultModel"`
	ParamsSchema map[string]any     `json:"paramsSchema"`
	ResultSchema map[string]any     `json:"resultSchema"`
	Request      RPCRequestEnvelope `json:"request"`
	Success      RPCSuccessEnvelope `json:"success"`
	Error        RPCErrorEnvelope   `json:"error"`
}

func (value RPCGoldenCase) Validate() error {
	if value.Method == "" || value.ParamsModel == "" || value.ResultModel == "" ||
		!oneOf(value.Scope, "global", "workspace") ||
		value.Request.JSONRPC != "2.0" || value.Success.JSONRPC != "2.0" ||
		value.Error.JSONRPC != "2.0" || value.Request.Method != value.Method ||
		value.Request.ID == "" || value.Request.ID != value.Success.ID ||
		value.Request.ID != value.Error.ID ||
		!closedSchema(value.ParamsSchema) || !closedSchema(value.ResultSchema) {
		return errors.New("RPC golden case is invalid")
	}
	if !bytes.Equal(value.Request.Wire, value.Success.Wire) ||
		!bytes.Equal(value.Request.Wire, value.Error.Wire) {
		return errors.New("RPC fixture wires differ")
	}
	if value.Scope == "global" {
		_, err := DecodeStrict[GlobalWireScope](value.Request.Wire)
		return err
	}
	_, err := DecodeStrict[WorkspaceWireScope](value.Request.Wire)
	return err
}

type RPCContractCatalog struct {
	ContractVersion string           `json:"contractVersion"`
	RPCMethods      []string         `json:"rpcMethods"`
	EventTopics     []string         `json:"eventTopics"`
	RPCCases        []RPCGoldenCase  `json:"rpcCases"`
	EventCases      []WorkspaceEvent `json:"eventCases"`
}

func (value RPCContractCatalog) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if len(value.RPCMethods) != len(value.RPCCases) ||
		len(value.EventTopics) != len(value.EventCases) {
		return errors.New("RPC catalog registry length mismatch")
	}
	methods := map[string]bool{}
	for index, item := range value.RPCCases {
		if err := item.Validate(); err != nil || value.RPCMethods[index] != item.Method ||
			methods[item.Method] {
			return errors.New("RPC catalog method registry is missing, duplicated, or stale")
		}
		methods[item.Method] = true
	}
	topics := map[string]bool{}
	for index, item := range value.EventCases {
		if err := item.Validate(); err != nil || value.EventTopics[index] != item.Topic ||
			topics[item.Topic] {
			return errors.New("RPC catalog event registry is missing, duplicated, or stale")
		}
		topics[item.Topic] = true
	}
	return nil
}

func closedSchema(value map[string]any) bool {
	return value["type"] == "object" && value["additionalProperties"] == false
}

func (value WorkspaceEvent) Validate() error {
	if err := validateVersion(value.ContractVersion); err != nil {
		return err
	}
	if !oneOf(
		value.Topic,
		"workspace.session.changed", "snapshot.changed", "replica.changed",
		"lease.changed", "conflict.changed",
	) {
		return errors.New("event topic is invalid")
	}
	if err := value.Wire.Validate(); err != nil {
		return err
	}
	models := map[string]struct {
		model string
		keys  []string
	}{
		"workspace.session.changed": {"WorkspaceSessionChangedEvent", []string{"state", "phase"}},
		"snapshot.changed":          {"SnapshotChangedEvent", []string{"snapshotId", "state", "integrity"}},
		"replica.changed":           {"ReplicaChangedEvent", []string{"syncState", "pendingSync"}},
		"lease.changed":             {"LeaseChangedEvent", []string{"mode", "coordinationStrength"}},
		"conflict.changed":          {"ConflictChangedEvent", []string{"conflictId", "state"}},
	}
	expected := models[value.Topic]
	if value.PayloadModel != expected.model || !exactMapKeys(value.Payload, expected.keys) {
		return errors.New("workspace event payload is invalid")
	}
	required, ok := value.PayloadSchema["required"].([]any)
	if !ok || value.PayloadSchema["type"] != "object" ||
		value.PayloadSchema["additionalProperties"] != false ||
		!exactAnyStrings(required, expected.keys) {
		return errors.New("workspace event payload schema is invalid")
	}
	return nil
}

func exactMapKeys(value map[string]any, expected []string) bool {
	if len(value) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}

func exactAnyStrings(value []any, expected []string) bool {
	if len(value) != len(expected) {
		return false
	}
	found := map[string]bool{}
	for _, item := range value {
		text, ok := item.(string)
		if !ok {
			return false
		}
		found[text] = true
	}
	for _, item := range expected {
		if !found[item] {
			return false
		}
	}
	return true
}

func validateVersion(value string) error {
	if value != ContractVersion {
		return errors.New("contractVersion must be 2.0")
	}
	return nil
}

func validateBuckets(values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !oneOf(value, "hourly", "daily", "weekly", "monthly") || seen[value] {
			return errors.New("retention buckets are invalid")
		}
		seen[value] = true
	}
	return nil
}

func validUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil && strings.ToLower(value) == value
}

func validOptionalUUID(value *string) bool {
	return value == nil || validUUID(*value)
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
