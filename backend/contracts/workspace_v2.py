"""Strict protocol v2 contracts for workspaces, snapshots and file history."""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Any, Literal
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field, model_validator
from pydantic.alias_generators import to_camel


class V2Model(BaseModel):
    model_config = ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


ContractVersion = Literal["2.0"]
ObjectId = Annotated[str, Field(pattern=r"^obj_[0-9a-f]{64}$")]
Sha256 = Annotated[str, Field(pattern=r"^sha256:[0-9a-f]{64}$")]
StorageMode = Literal["direct", "mirrored"]
EncryptionMode = Literal["none", "convenient", "protected"]
StorageKind = Literal[
    "fixed",
    "network",
    "removable",
    "registeredCloud",
    "userMarkedSync",
]
CoordinationStrength = Literal["strong", "advisory"]


class GlobalWireScope(V2Model):
    scope: Literal["global"]
    operation_id: UUID
    sequence: int = Field(ge=0)


class WorkspaceWireScope(V2Model):
    scope: Literal["workspace"]
    workspace_id: UUID
    session_epoch: int = Field(ge=1)
    operation_id: UUID
    sequence: int = Field(ge=0)


def ensure_current_workspace_scope(
    scope: WorkspaceWireScope,
    *,
    workspace_id: UUID,
    session_epoch: int,
    minimum_sequence: int = 0,
) -> None:
    """Fail closed for late responses/events after a workspace switch."""

    if scope.workspace_id != workspace_id:
        raise ValueError("workspace.workspace_mismatch")
    if scope.session_epoch != session_epoch:
        raise ValueError("workspace.session_epoch_stale")
    if scope.sequence < minimum_sequence:
        raise ValueError("workspace.sequence_stale")


class WorkspaceManifest(V2Model):
    contract_version: ContractVersion
    format_version: int = Field(ge=1)
    workspace_id: UUID
    display_name: str = Field(min_length=1)
    created_at: datetime
    storage_mode: StorageMode
    encryption_mode: EncryptionMode
    repository_format: str = Field(min_length=1)
    topology_schema_version: int = Field(ge=1)
    business_schema_version: int = Field(ge=1)
    imported_from_workspace_id: UUID | None = None
    source_snapshot_id: UUID | None = None


class WorkspaceRegistryEntry(V2Model):
    contract_version: ContractVersion
    workspace_id: UUID
    display_name: str = Field(min_length=1)
    selected_root: str = Field(min_length=1)
    activity_root: str | None
    storage_kind: StorageKind
    coordination_strength: CoordinationStrength
    last_opened_at: datetime | None
    last_known_health: Literal["healthy", "offline", "degraded", "corrupt", "unknown"]
    last_snapshot_at: datetime | None
    last_sync_at: datetime | None
    pending_sync: bool


class WorkspaceSession(V2Model):
    contract_version: ContractVersion
    workspace_id: UUID | None
    session_epoch: int = Field(ge=0)
    state: Literal[
        "closed",
        "opening",
        "openedReadOnly",
        "openedWritable",
        "openedProvisional",
        "switching",
        "failed",
    ]
    open_mode: Literal["readOnly", "writable", "provisional"]
    writable: bool
    provisional: bool
    phase: Literal[
        "idle",
        "protecting",
        "draining",
        "stopping",
        "starting",
        "binding",
        "verifying",
        "rollingBack",
    ]
    error_code: str | None

    @model_validator(mode="after")
    def _coherent_state(self) -> WorkspaceSession:
        if self.state == "closed":
            if self.workspace_id is not None or self.writable or self.provisional:
                raise ValueError("closed session cannot own a workspace")
            return self
        if self.workspace_id is None:
            raise ValueError("non-closed session requires workspaceId")
        if self.state == "openedWritable" and not self.writable:
            raise ValueError("openedWritable session must be writable")
        if self.state == "openedProvisional" and not self.provisional:
            raise ValueError("openedProvisional session must be provisional")
        return self


class FileDocument(V2Model):
    contract_version: ContractVersion
    document_id: UUID
    workspace_id: UUID
    relative_path: str = Field(min_length=1)
    status: Literal["active", "deleted"]
    effective_revision_id: UUID | None
    next_revision_ordinal: int = Field(ge=1)
    next_formal_version: int = Field(ge=1)

    @model_validator(mode="after")
    def _safe_relative_path(self) -> FileDocument:
        normalized = self.relative_path.replace("\\", "/")
        if normalized.startswith("/") or ".." in normalized.split("/"):
            raise ValueError("file_history.path_invalid")
        return self


class FileRevision(V2Model):
    contract_version: ContractVersion
    revision_id: UUID
    document_id: UUID
    parent_revision_id: UUID | None
    revision_ordinal: int = Field(ge=1)
    formal_version: int | None = Field(ge=1)
    kind: Literal["autosave", "formal", "restore"]
    object_id: ObjectId
    content_hash: Sha256
    size: int = Field(ge=0)
    mime_type: str = Field(min_length=1)
    created_at: datetime
    created_by: str = Field(min_length=1)
    device_id: UUID
    comment: str | None
    restored_from_revision_id: UUID | None

    @model_validator(mode="after")
    def _coherent_kind(self) -> FileRevision:
        if self.kind == "autosave" and self.formal_version is not None:
            raise ValueError("autosave cannot consume a formal version")
        if self.kind != "autosave" and self.formal_version is None:
            raise ValueError("formal and restore revisions require a formal version")
        if self.kind == "restore" and self.restored_from_revision_id is None:
            raise ValueError("restore revision requires restoredFromRevisionId")
        if self.kind != "restore" and self.restored_from_revision_id is not None:
            raise ValueError("only restore revisions may reference restored content")
        return self


class AuditAnchor(V2Model):
    epoch: int = Field(ge=1)
    sequence: int = Field(ge=0)
    chain_hash: Sha256


class SnapshotManifest(V2Model):
    contract_version: ContractVersion
    snapshot_id: UUID
    workspace_id: UUID
    fence_epoch: int = Field(ge=1)
    claim_id: UUID
    mutation_revision: int = Field(ge=0)
    snapshot_sequence: int = Field(ge=1)
    trigger: Literal["automatic", "manual", "protection", "import", "restore"]
    created_at: datetime
    created_by_device: UUID
    business_database_object_id: ObjectId
    topology_root_object_id: ObjectId
    file_state_root_object_id: ObjectId
    workspace_settings_object_id: ObjectId
    audit_anchor: AuditAnchor
    audit_prefix_object_id: ObjectId
    source_snapshot_id: UUID | None
    format_version: int = Field(ge=1)
    minimum_app_version: Annotated[
        str,
        Field(pattern=r"^[0-9]+\.[0-9]+\.[0-9]+$"),
    ]


class SnapshotSeal(V2Model):
    contract_version: ContractVersion
    snapshot_id: UUID
    manifest_hash: Sha256
    database_hash: Sha256
    file_state_root_hash: Sha256
    audit_anchor_hash: Sha256
    repository_format: str = Field(min_length=1)
    fence_epoch: int = Field(ge=1)
    claim_id: UUID
    mutation_revision: int = Field(ge=0)
    snapshot_sequence: int = Field(ge=1)
    verified: Literal[True]


class SnapshotCatalogEntry(V2Model):
    contract_version: ContractVersion
    snapshot_id: UUID
    state: Literal[
        "queued",
        "barrier",
        "captured",
        "chunking",
        "verifying",
        "published",
        "syncing",
        "ready",
        "failed",
        "corrupt",
        "repairing",
    ]
    pinned: bool
    retention_reasons: list[str]
    integrity: Literal["pending", "verified", "corrupt", "repairing"]
    sync_state: Literal["localOnly", "pending", "syncing", "replicated", "failed"]
    logical_size: int = Field(ge=0)
    physical_size: int = Field(ge=0)
    note: str | None
    catalog_revision: int = Field(ge=1)


class LeaseClaim(V2Model):
    contract_version: ContractVersion
    workspace_id: UUID
    fence_epoch: int = Field(ge=1)
    claim_id: UUID
    device_id: UUID
    issued_at: datetime
    heartbeat_at: datetime
    expires_at: datetime
    mode: Literal["writable", "provisional"]
    previous_claim_id: UUID | None
    coordination_strength: CoordinationStrength

    @model_validator(mode="after")
    def _ordered_times(self) -> LeaseClaim:
        if self.heartbeat_at < self.issued_at or self.expires_at <= self.heartbeat_at:
            raise ValueError("lease timestamps are not monotonic")
        return self


class RetentionPolicy(V2Model):
    contract_version: ContractVersion
    policy_revision: int = Field(ge=1)
    snapshot_days: int = Field(ge=1)
    snapshot_count: int = Field(ge=1)
    snapshot_buckets: list[Literal["hourly", "daily", "weekly", "monthly"]]
    file_revision_days: int = Field(ge=1)
    file_revision_count: int = Field(ge=1)
    file_revision_buckets: list[Literal["hourly", "daily", "weekly", "monthly"]]
    trash_months: Literal[3]
    repository_limit_bytes: int | None = Field(default=None, ge=1)


class WorkspaceEvent(V2Model):
    contract_version: ContractVersion
    topic: Literal[
        "workspace.session.changed",
        "snapshot.changed",
        "replica.changed",
        "lease.changed",
        "conflict.changed",
    ]
    wire: WorkspaceWireScope
    payload_model: str = Field(min_length=1)
    payload_schema: dict[str, Any]
    payload: dict[str, Any]

    @model_validator(mode="after")
    def _typed_payload(self) -> WorkspaceEvent:
        expected = {
            "workspace.session.changed": ("WorkspaceSessionChangedEvent", {"state", "phase"}),
            "snapshot.changed": ("SnapshotChangedEvent", {"snapshotId", "state", "integrity"}),
            "replica.changed": ("ReplicaChangedEvent", {"syncState", "pendingSync"}),
            "lease.changed": ("LeaseChangedEvent", {"mode", "coordinationStrength"}),
            "conflict.changed": ("ConflictChangedEvent", {"conflictId", "state"}),
        }[self.topic]
        if self.payload_model != expected[0] or set(self.payload) != expected[1]:
            raise ValueError("workspace.event_payload_invalid")
        if (
            self.payload_schema.get("type") != "object"
            or self.payload_schema.get("additionalProperties") is not False
            or set(self.payload_schema.get("required", [])) != expected[1]
        ):
            raise ValueError("workspace.event_schema_invalid")
        return self


class RpcRequestEnvelope(V2Model):
    jsonrpc: Literal["2.0"]
    id: str = Field(min_length=1)
    method: str = Field(min_length=3)
    wire: GlobalWireScope | WorkspaceWireScope
    params: dict[str, Any]


class RpcSuccessEnvelope(V2Model):
    jsonrpc: Literal["2.0"]
    id: str = Field(min_length=1)
    wire: GlobalWireScope | WorkspaceWireScope
    result: Any


class RpcError(V2Model):
    code: Annotated[
        str,
        Field(
            pattern=(
                r"^(workspace|snapshot|repository|lease|replica|conflict|"
                r"file_history|retention)\.[a-z0-9_]+$"
            )
        ),
    ]
    message: str = Field(min_length=1)
    details: dict[str, Any]
    retryable: bool


class RpcErrorEnvelope(V2Model):
    jsonrpc: Literal["2.0"]
    id: str = Field(min_length=1)
    wire: GlobalWireScope | WorkspaceWireScope
    error: RpcError


class RpcGoldenCase(V2Model):
    method: str = Field(min_length=3)
    scope: Literal["global", "workspace"]
    params_model: str = Field(min_length=1)
    result_model: str = Field(min_length=1)
    params_schema: dict[str, Any]
    result_schema: dict[str, Any]
    request: RpcRequestEnvelope
    success: RpcSuccessEnvelope
    error: RpcErrorEnvelope

    @model_validator(mode="after")
    def _coherent_case(self) -> RpcGoldenCase:
        if self.request.method != self.method:
            raise ValueError("rpc method fixture mismatch")
        if not (
            self.request.id == self.success.id == self.error.id
            and self.request.wire == self.success.wire == self.error.wire
            and self.request.wire.scope == self.scope
        ):
            raise ValueError("rpc wire fixture mismatch")
        for schema in (self.params_schema, self.result_schema):
            if schema.get("type") != "object" or schema.get("additionalProperties") is not False:
                raise ValueError("rpc schema must be a closed object")
        return self


class RpcContractCatalog(V2Model):
    contract_version: ContractVersion
    rpc_methods: list[str]
    event_topics: list[str]
    rpc_cases: list[RpcGoldenCase]
    event_cases: list[WorkspaceEvent]

    @model_validator(mode="after")
    def _complete_registry(self) -> RpcContractCatalog:
        methods = [case.method for case in self.rpc_cases]
        topics = [case.topic for case in self.event_cases]
        if (
            len(set(self.rpc_methods)) != len(self.rpc_methods)
            or len(set(self.event_topics)) != len(self.event_topics)
            or methods != self.rpc_methods
            or topics != self.event_topics
        ):
            raise ValueError("rpc catalog registry is missing, duplicated, or stale")
        return self


__all__ = [
    "AuditAnchor",
    "FileDocument",
    "FileRevision",
    "GlobalWireScope",
    "LeaseClaim",
    "RetentionPolicy",
    "RpcContractCatalog",
    "SnapshotCatalogEntry",
    "SnapshotManifest",
    "SnapshotSeal",
    "WorkspaceEvent",
    "WorkspaceManifest",
    "WorkspaceRegistryEntry",
    "WorkspaceSession",
    "WorkspaceWireScope",
    "ensure_current_workspace_scope",
]
