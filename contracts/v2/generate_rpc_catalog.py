"""Generate the frozen protocol/catalog v2 golden fixture.

The registry in this module is the only source of truth for new workspace-scoped
RPC and event names. It never imports or rewrites contracts/v1.
"""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Literal

ROOT = Path(__file__).parents[2]
OUTPUT = ROOT / "contracts" / "v2" / "fixtures" / "rpc-catalog.json"

WORKSPACE_ID = "11111111-1111-4111-8111-111111111111"
OPERATION_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"


@dataclass(frozen=True)
class Rpc:
    method: str
    scope: Literal["global", "workspace"]
    params_model: str
    result_model: str
    params: dict[str, Any]
    result: Any


@dataclass(frozen=True)
class ContractValue:
    """Example wire value paired with a schema not inferable from the example."""

    example: Any
    schema: dict[str, Any]


def nullable_string(example: str | None = None) -> ContractValue:
    return ContractValue(example, {"type": ["string", "null"]})


def nullable_integer(example: int | None = None) -> ContractValue:
    return ContractValue(example, {"type": ["integer", "null"]})


def enum_string(example: str, *values: str) -> ContractValue:
    return ContractValue(example, {"type": "string", "enum": list(values)})


def nullable_enum_string(example: str | None, *values: str) -> ContractValue:
    return ContractValue(
        example,
        {"type": ["string", "null"], "enum": [*values, None]},
    )


def _schema_from_example(value: Any) -> dict[str, Any]:
    if isinstance(value, ContractValue):
        return value.schema
    if isinstance(value, dict):
        return {
            "type": "object",
            "additionalProperties": False,
            "required": list(value),
            "properties": {key: _schema_from_example(item) for key, item in value.items()},
        }
    if isinstance(value, list):
        if not value:
            raise ValueError("empty arrays require typed_array()/string_array()")
        return {
            "type": "array",
            "items": _schema_from_example(value[0]),
        }
    if value is None:
        return {"type": "null"}
    if isinstance(value, bool):
        return {"type": "boolean"}
    if isinstance(value, int):
        return {"type": "integer"}
    if isinstance(value, float):
        return {"type": "number"}
    return {"type": "string"}


def typed_array(item_example: Any) -> ContractValue:
    return ContractValue(
        [item_example],
        {"type": "array", "items": _schema_from_example(item_example)},
    )


def string_array() -> ContractValue:
    return typed_array("value")


RPC_REGISTRY: tuple[Rpc, ...] = (
    Rpc(
        "workspace.list",
        "global",
        "ListWorkspacesParams",
        "WorkspaceListResult",
        {},
        {
            "workspaces": typed_array(
                {
                    "contractVersion": "2.0",
                    "workspaceId": WORKSPACE_ID,
                    "displayName": "季度规划",
                    "selectedRoot": "E:\\Workspaces\\Quarter",
                    "activityRoot": nullable_string(),
                    "storageKind": enum_string(
                        "fixed",
                        "fixed",
                        "network",
                        "removable",
                        "registeredCloud",
                        "userMarkedSync",
                    ),
                    "coordinationStrength": enum_string("strong", "strong", "advisory"),
                    "lastOpenedAt": nullable_string(),
                    "lastKnownHealth": enum_string(
                        "healthy",
                        "healthy",
                        "offline",
                        "degraded",
                        "corrupt",
                        "unknown",
                    ),
                    "lastSnapshotAt": nullable_string(),
                    "lastSyncAt": nullable_string(),
                    "pendingSync": False,
                }
            )
        },
    ),
    Rpc(
        "workspace.create",
        "global",
        "CreateWorkspaceParams",
        "WorkspaceOperationResult",
        {
            "displayName": "季度规划",
            "locationPolicy": enum_string(
                "managedDefault",
                "managedDefault",
                "other",
            ),
            "selectedRootGrant": nullable_string(),
            "storageMode": enum_string("direct", "direct", "mirrored"),
            "encryptionMode": "convenient",
        },
        {"workspaceId": WORKSPACE_ID, "status": "created"},
    ),
    Rpc(
        "workspace.register",
        "global",
        "RegisterWorkspaceParams",
        "WorkspaceOperationResult",
        {"selectedRootGrant": "grant_register_1"},
        {"workspaceId": WORKSPACE_ID, "status": "registered"},
    ),
    Rpc(
        "workspace.relink",
        "global",
        "RelinkWorkspaceParams",
        "WorkspaceOperationResult",
        {
            "workspaceId": WORKSPACE_ID,
            "selectedRootGrant": "grant_relink_workspace_1",
        },
        {"workspaceId": WORKSPACE_ID, "status": "relinked"},
    ),
    Rpc(
        "workspace.open",
        "global",
        "OpenWorkspaceParams",
        "WorkspaceSessionResult",
        {"workspaceId": WORKSPACE_ID, "openMode": "writable"},
        {"workspaceId": WORKSPACE_ID, "sessionEpoch": 7, "state": "openedWritable"},
    ),
    Rpc(
        "workspace.switch",
        "workspace",
        "SwitchWorkspaceParams",
        "WorkspaceSessionResult",
        {"targetWorkspaceId": "99999999-9999-4999-8999-999999999999", "openMode": "writable"},
        {
            "workspaceId": "99999999-9999-4999-8999-999999999999",
            "sessionEpoch": 8,
            "state": "openedWritable",
        },
    ),
    Rpc(
        "workspace.close",
        "workspace",
        "CloseWorkspaceParams",
        "WorkspaceSessionResult",
        {"reason": "user"},
        {"workspaceId": nullable_string(), "sessionEpoch": 8, "state": "closed"},
    ),
    Rpc(
        "workspace.remove",
        "global",
        "RemoveWorkspaceParams",
        "WorkspaceOperationResult",
        {"workspaceId": WORKSPACE_ID},
        {"workspaceId": WORKSPACE_ID, "status": "removed"},
    ),
    Rpc(
        "workspace.planDelete",
        "global",
        "PlanWorkspaceDeleteParams",
        "WorkspaceDeletePlan",
        {"workspaceId": WORKSPACE_ID},
        {"planId": OPERATION_ID, "displayName": "季度规划", "requiresTypedName": True},
    ),
    Rpc(
        "workspace.applyDelete",
        "global",
        "ApplyWorkspaceDeleteParams",
        "WorkspaceOperationResult",
        {"planId": OPERATION_ID, "confirmation": "季度规划"},
        {"workspaceId": WORKSPACE_ID, "status": "deleted"},
    ),
    Rpc(
        "workspace.storage.preview",
        "global",
        "PreviewWorkspaceStorageParams",
        "WorkspaceStoragePlan",
        {
            "workspaceId": WORKSPACE_ID,
            "action": enum_string(
                "relocate",
                "relocate",
                "convertTopology",
                "releaseActivityCache",
            ),
            "targetMode": nullable_enum_string(
                "direct",
                "direct",
                "mirrored",
            ),
            "selectedRootGrant": nullable_string("grant_storage_target_1"),
        },
        {
            "planId": OPERATION_ID,
            "workspaceId": WORKSPACE_ID,
            "action": enum_string(
                "relocate",
                "relocate",
                "convertTopology",
                "releaseActivityCache",
            ),
            "source": {
                "selectedRoot": "D:\\Workspaces\\Quarter",
                "activityRoot": nullable_string(),
                "mode": "direct",
            },
            "target": {
                "selectedRoot": "E:\\Workspaces\\Quarter",
                "activityRoot": nullable_string(),
                "mode": "direct",
            },
            "bytesToCopy": 4096,
            "requiresClosedSession": True,
            "warnings": ["The verified source copy is retained after relocation."],
            "expiresAt": "2026-07-28T10:10:00Z",
            "verificationReceiptId": nullable_string(),
        },
    ),
    Rpc(
        "workspace.storage.apply",
        "global",
        "ApplyWorkspaceStorageParams",
        "WorkspaceStorageResult",
        {"planId": OPERATION_ID, "confirmation": "季度规划"},
        {
            "workspaceId": WORKSPACE_ID,
            "status": "applied",
            "storage": {
                "location": "E:\\Workspaces\\Quarter",
                "activityRoot": "E:\\Workspaces\\Quarter",
                "mode": "direct",
                "provider": "fixed",
                "health": "healthy",
                "logicalSize": 4096,
                "physicalSize": 2048,
                "reclaimableSize": 0,
                "encryption": "convenient",
                "keyVersion": 1,
                "pendingSync": False,
                "remoteVerified": True,
            },
        },
    ),
    Rpc(
        "snapshot.request",
        "workspace",
        "RequestSnapshotParams",
        "SnapshotOperationResult",
        {"trigger": "manual", "urgency": "foreground"},
        {
            "operationId": OPERATION_ID,
            "state": "ready",
            "snapshotId": "77777777-7777-4777-8777-777777777777",
            "mutationRevision": 42,
        },
    ),
    Rpc(
        "snapshot.list",
        "workspace",
        "ListSnapshotsParams",
        "SnapshotListResult",
        {"cursor": nullable_string(), "limit": 50},
        {
            "snapshots": typed_array(
                {
                    "snapshotId": "77777777-7777-4777-8777-777777777777",
                    "createdAt": "2026-07-28T10:00:00Z",
                    "state": enum_string(
                        "ready",
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
                    ),
                    "trigger": enum_string(
                        "manual",
                        "automatic",
                        "manual",
                        "protection",
                        "import",
                        "restore",
                    ),
                    "integrity": enum_string(
                        "verified",
                        "pending",
                        "verified",
                        "corrupt",
                        "repairing",
                    ),
                    "syncState": enum_string(
                        "replicated",
                        "localOnly",
                        "pending",
                        "syncing",
                        "replicated",
                        "failed",
                    ),
                    "pinned": False,
                    "retentionReasons": string_array(),
                    "logicalSize": 4096,
                    "physicalSize": 2048,
                    "note": nullable_string(),
                    "catalogRevision": 1,
                }
            ),
            "nextCursor": nullable_string(),
        },
    ),
    Rpc(
        "snapshot.inspect",
        "workspace",
        "InspectSnapshotParams",
        "SnapshotDetails",
        {"snapshotId": "77777777-7777-4777-8777-777777777777"},
        {
            "snapshotId": "77777777-7777-4777-8777-777777777777",
            "state": "ready",
            "integrity": "verified",
        },
    ),
    Rpc(
        "snapshot.update",
        "workspace",
        "UpdateSnapshotParams",
        "SnapshotDetails",
        {
            "snapshotId": "77777777-7777-4777-8777-777777777777",
            "action": "pin",
            "expectedCatalogRevision": 3,
        },
        {
            "snapshotId": "77777777-7777-4777-8777-777777777777",
            "state": "ready",
            "integrity": "verified",
        },
    ),
    Rpc(
        "snapshot.previewRestore",
        "workspace",
        "PreviewSnapshotRestoreParams",
        "RestorePlanResult",
        {"snapshotId": "77777777-7777-4777-8777-777777777777", "targetMode": "currentWorkspace"},
        {
            "planId": OPERATION_ID,
            "protectionRequired": True,
            "changes": string_array(),
        },
    ),
    Rpc(
        "snapshot.applyRestore",
        "workspace",
        "ApplySnapshotRestoreParams",
        "RestoreOperationResult",
        {"planId": OPERATION_ID, "confirmed": True},
        {"operationId": OPERATION_ID, "state": "prepared"},
    ),
    Rpc(
        "snapshot.openAsNewWorkspace",
        "workspace",
        "OpenSnapshotAsNewWorkspaceParams",
        "WorkspaceSessionResult",
        {"snapshotId": "77777777-7777-4777-8777-777777777777"},
        {
            "workspaceId": "99999999-9999-4999-8999-999999999999",
            "sessionEpoch": 8,
            "state": "openedWritable",
        },
    ),
    Rpc(
        "snapshot.previewExtract",
        "workspace",
        "PreviewSnapshotExtractParams",
        "SnapshotExtractPlan",
        {
            "snapshotId": "77777777-7777-4777-8777-777777777777",
            "documentId": "22222222-2222-4222-8222-222222222222",
        },
        {
            "planId": OPERATION_ID,
            "displayName": "季度规划.docx",
            "size": 1024,
            "expiresAt": "2026-07-28T10:10:00Z",
        },
    ),
    Rpc(
        "snapshot.applyExtract",
        "workspace",
        "ApplySnapshotExtractParams",
        "SnapshotOperationResult",
        {"planId": OPERATION_ID, "pathGrant": "grant_extract_1"},
        {"operationId": OPERATION_ID, "state": "completed"},
    ),
    Rpc(
        "snapshot.export",
        "workspace",
        "ExportSnapshotParams",
        "SnapshotPackageReceipt",
        {
            "snapshotId": "77777777-7777-4777-8777-777777777777",
            "pathGrant": "grant_export_1",
            "encryption": "age",
            "recipients": ["age1examplepublicrecipient"],
            "credential": nullable_string(),
        },
        {
            "displayName": "季度规划.vtsnapshot.age",
            "sha256": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
        },
    ),
    Rpc(
        "snapshot.inspectPackage",
        "global",
        "InspectSnapshotPackageParams",
        "SnapshotPackageInfo",
        {"pathGrant": "grant_import_1", "credential": nullable_string()},
        {
            "planId": OPERATION_ID,
            "trusted": False,
            "workspaceId": WORKSPACE_ID,
            "sourceSnapshotId": "77777777-7777-4777-8777-777777777777",
            "snapshotCount": 1,
            "encrypted": False,
            "verified": True,
            "expiresAt": "2026-07-28T10:10:00Z",
        },
    ),
    Rpc(
        "snapshot.import",
        "global",
        "ImportSnapshotPackageParams",
        "SnapshotImportResult",
        {
            "planId": OPERATION_ID,
            "credential": nullable_string(),
            "targetMode": "newWorkspace",
            "targetWorkspaceId": nullable_string(),
        },
        {
            "operationId": OPERATION_ID,
            "snapshotId": "77777777-7777-4777-8777-777777777777",
            "sourceWorkspaceId": WORKSPACE_ID,
            "sourceSnapshotId": "88888888-8888-4888-8888-888888888888",
            "state": "restoreRequired",
        },
    ),
    Rpc(
        "repository.verify",
        "workspace",
        "VerifyRepositoryParams",
        "RepositoryVerificationResult",
        {},
        {
            "state": "verified",
            "snapshotCount": 1,
            "objectCount": 7,
            "corruptSnapshotIds": string_array(),
        },
    ),
    Rpc(
        "repository.previewKeyRotation",
        "workspace",
        "PreviewRepositoryKeyRotationParams",
        "RepositoryKeyRotationPlan",
        {},
        {
            "planId": OPERATION_ID,
            "expiresAt": "2026-07-28T10:10:00Z",
            "protectionRequired": True,
        },
    ),
    Rpc(
        "repository.applyKeyRotation",
        "workspace",
        "ApplyRepositoryKeyRotationParams",
        "RepositoryKeyRotationResult",
        {"planId": OPERATION_ID, "confirmed": True},
        {
            "operationId": OPERATION_ID,
            "state": "hostRestartRequired",
            "newRecoveryKeyAvailable": False,
        },
    ),
    Rpc(
        "fileHistory.import",
        "workspace",
        "ImportFileDocumentParams",
        "FileDocumentResult",
        {
            "pathGrant": "grant_document_import_1",
            "relativePath": "季度规划.docx",
            "mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        },
        {
            "contractVersion": "2.0",
            "documentId": "22222222-2222-4222-8222-222222222222",
            "workspaceId": WORKSPACE_ID,
            "relativePath": "季度规划.docx",
            "status": "active",
            "effectiveRevisionId": nullable_string("33333333-3333-4333-8333-333333333333"),
            "nextRevisionOrdinal": 2,
            "nextFormalVersion": 2,
        },
    ),
    Rpc(
        "fileHistory.listDocuments",
        "workspace",
        "ListFileDocumentsParams",
        "FileDocumentListResult",
        {"includeDeleted": False},
        {
            "documents": [
                {
                    "contractVersion": "2.0",
                    "documentId": "22222222-2222-4222-8222-222222222222",
                    "workspaceId": WORKSPACE_ID,
                    "relativePath": "季度规划.docx",
                    "status": "active",
                    "effectiveRevisionId": nullable_string("33333333-3333-4333-8333-333333333333"),
                    "nextRevisionOrdinal": 4,
                    "nextFormalVersion": 4,
                }
            ]
        },
    ),
    Rpc(
        "fileHistory.listPendingChanges",
        "workspace",
        "ListPendingFileChangesParams",
        "PendingFileChangeListResult",
        {},
        {
            "changes": [
                {
                    "changeId": OPERATION_ID,
                    "relativePath": "季度规划.docx",
                    "missing": False,
                    "observedHash": "sha256:" + "ab" * 32,
                    "observedSize": 1024,
                    "reason": "same content may be a copy, rename, or move",
                    "candidateDocumentIds": ["22222222-2222-4222-8222-222222222222"],
                    "createdAt": "2026-07-28T09:00:00Z",
                    "updatedAt": "2026-07-28T09:00:00Z",
                }
            ]
        },
    ),
    Rpc(
        "fileHistory.applyPendingChange",
        "workspace",
        "ApplyPendingFileChangeParams",
        "PendingFileChangeResult",
        {
            "changeId": OPERATION_ID,
            "action": "move",
            "documentId": nullable_string("22222222-2222-4222-8222-222222222222"),
            "expectedEffectiveRevisionId": nullable_string("33333333-3333-4333-8333-333333333333"),
        },
        {
            "changeId": OPERATION_ID,
            "state": "applied",
            "document": {
                "contractVersion": "2.0",
                "documentId": "22222222-2222-4222-8222-222222222222",
                "workspaceId": WORKSPACE_ID,
                "relativePath": "季度规划.docx",
                "status": "active",
                "effectiveRevisionId": nullable_string("33333333-3333-4333-8333-333333333333"),
                "nextRevisionOrdinal": 4,
                "nextFormalVersion": 4,
            },
        },
    ),
    Rpc(
        "fileHistory.unlink",
        "workspace",
        "UnlinkFileDocumentParams",
        "FileDocumentResult",
        {
            "documentId": "22222222-2222-4222-8222-222222222222",
            "expectedEffectiveRevisionId": "33333333-3333-4333-8333-333333333333",
        },
        {
            "contractVersion": "2.0",
            "documentId": "22222222-2222-4222-8222-222222222222",
            "workspaceId": WORKSPACE_ID,
            "relativePath": "季度规划.docx",
            "status": "deleted",
            "effectiveRevisionId": nullable_string("33333333-3333-4333-8333-333333333333"),
            "nextRevisionOrdinal": 4,
            "nextFormalVersion": 4,
        },
    ),
    Rpc(
        "fileHistory.relink",
        "workspace",
        "RelinkFileDocumentParams",
        "FileDocumentResult",
        {
            "documentId": "22222222-2222-4222-8222-222222222222",
            "expectedEffectiveRevisionId": "33333333-3333-4333-8333-333333333333",
            "pathGrant": "grant_relink_1",
        },
        {
            "contractVersion": "2.0",
            "documentId": "22222222-2222-4222-8222-222222222222",
            "workspaceId": WORKSPACE_ID,
            "relativePath": "季度规划.docx",
            "status": "active",
            "effectiveRevisionId": nullable_string("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
            "nextRevisionOrdinal": 5,
            "nextFormalVersion": 5,
        },
    ),
    Rpc(
        "fileHistory.readTree",
        "workspace",
        "ReadFileTreeParams",
        "FileRevisionTreeResult",
        {"documentId": "22222222-2222-4222-8222-222222222222"},
        {
            "documentId": "22222222-2222-4222-8222-222222222222",
            "effectiveRevisionId": nullable_string("33333333-3333-4333-8333-333333333333"),
            "revisions": typed_array(
                {
                    "contractVersion": "2.0",
                    "revisionId": "88888888-8888-4888-8888-888888888888",
                    "documentId": "99999999-9999-4999-8999-999999999999",
                    "parentRevisionId": nullable_string(),
                    "revisionOrdinal": 1,
                    "formalVersion": nullable_integer(),
                    "kind": enum_string("autosave", "autosave", "formal", "restore"),
                    "objectId": "obj_" + "a" * 64,
                    "contentHash": "sha256:" + "b" * 64,
                    "size": 128,
                    "mimeType": "text/csv",
                    "createdAt": "2026-07-28T10:00:00Z",
                    "createdBy": "user",
                    "deviceId": "66666666-6666-4666-8666-666666666666",
                    "comment": nullable_string(),
                    "restoredFromRevisionId": nullable_string(),
                }
            ),
        },
    ),
    Rpc(
        "fileHistory.restore",
        "workspace",
        "RestoreFileRevisionParams",
        "FileRevisionResult",
        {
            "documentId": "22222222-2222-4222-8222-222222222222",
            "expectedEffectiveRevisionId": "33333333-3333-4333-8333-333333333333",
            "historicalRevisionId": "66666666-6666-4666-8666-666666666666",
        },
        {"revisionId": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "formalVersion": 4},
    ),
    Rpc(
        "fileHistory.upgrade",
        "workspace",
        "UpgradeFileRevisionParams",
        "FileRevisionResult",
        {
            "documentId": "22222222-2222-4222-8222-222222222222",
            "revisionId": "66666666-6666-4666-8666-666666666666",
            "pathGrant": "grant_upgrade_1",
        },
        {"revisionId": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "formalVersion": 4},
    ),
    Rpc(
        "fileHistory.activateLeaf",
        "workspace",
        "ActivateFileLeafParams",
        "FileActivationResult",
        {
            "documentId": "22222222-2222-4222-8222-222222222222",
            "expectedEffectiveRevisionId": "33333333-3333-4333-8333-333333333333",
            "targetLeafRevisionId": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        },
        {"revisionId": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "effective": True},
    ),
    Rpc(
        "retention.get",
        "workspace",
        "GetRetentionParams",
        "RetentionPolicyResult",
        {},
        {
            "contractVersion": "2.0",
            "policyRevision": 1,
            "snapshotDays": 30,
            "snapshotCount": 50,
            "snapshotBuckets": ["hourly", "daily", "weekly", "monthly"],
            "fileRevisionDays": 30,
            "fileRevisionCount": 100,
            "fileRevisionBuckets": ["daily", "weekly", "monthly"],
            "trashMonths": 3,
            "repositoryLimitBytes": nullable_integer(),
        },
    ),
    Rpc(
        "retention.status",
        "workspace",
        "GetRetentionStatusParams",
        "RetentionStatusResult",
        {},
        {
            "repositoryUsageBytes": 1048576,
            "repositoryLimitBytes": nullable_integer(1048576),
            "automaticSnapshotsPaused": True,
            "warningCode": nullable_string("snapshot.repository_limit_reached"),
            "integrityStatus": enum_string(
                "verified",
                "unknown",
                "verified",
                "corrupt",
            ),
            "integrityFailure": nullable_string(),
            "lastIncrementalCheckAt": nullable_string("2026-07-28T00:00:00Z"),
            "lastFullCheckAt": nullable_string("2026-07-01T00:00:00Z"),
            "maintenanceFailure": nullable_string(),
            "maintenanceFailureStage": nullable_enum_string(
                None,
                "integrity",
                "sweep",
            ),
            "lastMaintenanceFailureAt": nullable_string(),
        },
    ),
    Rpc(
        "retention.update",
        "workspace",
        "UpdateRetentionParams",
        "RetentionPolicyResult",
        {
            "expectedRevision": 1,
            "snapshotDays": 30,
            "snapshotCount": 50,
            "snapshotBuckets": ["hourly", "daily", "weekly", "monthly"],
            "fileRevisionDays": 30,
            "fileRevisionCount": 100,
            "fileRevisionBuckets": ["daily", "weekly", "monthly"],
            "repositoryLimitBytes": nullable_integer(),
        },
        {
            "contractVersion": "2.0",
            "policyRevision": 2,
            "snapshotDays": 30,
            "snapshotCount": 50,
            "snapshotBuckets": ["hourly", "daily", "weekly", "monthly"],
            "fileRevisionDays": 30,
            "fileRevisionCount": 100,
            "fileRevisionBuckets": ["daily", "weekly", "monthly"],
            "trashMonths": 3,
            "repositoryLimitBytes": nullable_integer(),
        },
    ),
    Rpc(
        "retention.plan",
        "workspace",
        "PlanRetentionParams",
        "CleanupPlanResult",
        {},
        {
            "planId": OPERATION_ID,
            "reclaimableBytes": 0,
            "blockedReasons": string_array(),
        },
    ),
    Rpc(
        "retention.apply",
        "workspace",
        "ApplyRetentionParams",
        "CleanupResult",
        {"planId": OPERATION_ID},
        {"deletedObjects": 0, "reclaimedBytes": 0},
    ),
    Rpc(
        "replica.status",
        "workspace",
        "ReplicaStatusParams",
        "ReplicaStatusResult",
        {},
        {"coordinationStrength": "strong", "syncState": "localOnly", "pendingSync": False},
    ),
    Rpc(
        "replica.synchronize",
        "workspace",
        "SynchronizeReplicaParams",
        "ReplicaOperationResult",
        {},
        {"operationId": OPERATION_ID, "state": "queued"},
    ),
    Rpc(
        "replica.forceTakeover",
        "workspace",
        "ForceTakeoverParams",
        "LeaseResult",
        {"mode": "provisional"},
        {"fenceEpoch": 2, "claimId": "88888888-8888-4888-8888-888888888888", "mode": "provisional"},
    ),
    Rpc(
        "conflict.list",
        "workspace",
        "ListConflictsParams",
        "ConflictListResult",
        {"cursor": nullable_string(), "limit": 50},
        {
            "conflicts": typed_array(
                {
                    "conflictId": OPERATION_ID,
                    "state": enum_string(
                        "pending",
                        "pending",
                        "validating",
                        "ready",
                        "failed",
                    ),
                    "createdAt": "2026-07-28T10:00:00Z",
                    "itemCount": 2,
                }
            ),
            "nextCursor": nullable_string(),
        },
    ),
    Rpc(
        "conflict.inspect",
        "workspace",
        "InspectConflictParams",
        "ConflictSetResult",
        {"conflictId": OPERATION_ID},
        {
            "conflictId": OPERATION_ID,
            "state": enum_string("pending", "pending", "validating", "ready", "failed"),
            "items": typed_array(
                {
                    "conflictId": OPERATION_ID,
                    "itemId": "document:99999999-9999-4999-8999-999999999999",
                    "path": "files/report.csv",
                    "kind": enum_string("file", "file", "table", "settings"),
                    "state": enum_string(
                        "pending",
                        "pending",
                        "validating",
                        "ready",
                        "failed",
                    ),
                    "localSummary": "local",
                    "replicaSummary": "replica",
                    "baseSummary": "base",
                    "dependencies": string_array(),
                    "selected": nullable_enum_string(None, "local", "replica", "both"),
                }
            ),
        },
    ),
    Rpc(
        "conflict.preview",
        "workspace",
        "PreviewConflictParams",
        "ConflictPlanResult",
        {
            "conflictId": OPERATION_ID,
            "choices": typed_array(
                {
                    "itemId": "document:99999999-9999-4999-8999-999999999999",
                    "kind": enum_string("file", "file", "table", "settings"),
                    "side": enum_string("local", "local", "replica", "both"),
                }
            ),
        },
        {
            "planId": OPERATION_ID,
            "diagnostics": string_array(),
            "valid": True,
        },
    ),
    Rpc(
        "conflict.apply",
        "workspace",
        "ApplyConflictParams",
        "ConflictResolutionResult",
        {"planId": OPERATION_ID},
        {
            "operationId": OPERATION_ID,
            "state": "applied",
            "recoverySnapshotIds": string_array(),
        },
    ),
)

EVENT_REGISTRY = (
    (
        "workspace.session.changed",
        "WorkspaceSessionChangedEvent",
        {"state": "openedWritable", "phase": "idle"},
    ),
    (
        "snapshot.changed",
        "SnapshotChangedEvent",
        {
            "snapshotId": "77777777-7777-4777-8777-777777777777",
            "state": "ready",
            "integrity": "verified",
        },
    ),
    ("replica.changed", "ReplicaChangedEvent", {"syncState": "replicated", "pendingSync": False}),
    ("lease.changed", "LeaseChangedEvent", {"mode": "writable", "coordinationStrength": "strong"}),
    ("conflict.changed", "ConflictChangedEvent", {"conflictId": OPERATION_ID, "state": "pending"}),
)
EVENT_TOPICS = tuple(item[0] for item in EVENT_REGISTRY)


def _wire(scope: str, sequence: int) -> dict[str, Any]:
    wire: dict[str, Any] = {
        "scope": scope,
        "operationId": OPERATION_ID,
        "sequence": sequence,
    }
    if scope == "workspace":
        wire["workspaceId"] = WORKSPACE_ID
        wire["sessionEpoch"] = 7
    return wire


def _error_code(method: str) -> str:
    prefix = method.split(".", 1)[0]
    if prefix == "fileHistory":
        prefix = "file_history"
    return f"{prefix}.request_invalid"


def _closed_schema(value: Any) -> dict[str, Any]:
    return _schema_from_example(value)


def _example(value: Any) -> Any:
    if isinstance(value, ContractValue):
        return _example(value.example)
    if isinstance(value, dict):
        return {key: _example(item) for key, item in value.items()}
    if isinstance(value, list):
        return [_example(item) for item in value]
    return value


def build_catalog() -> dict[str, Any]:
    rpc_cases: list[dict[str, Any]] = []
    for sequence, rpc in enumerate(RPC_REGISTRY, start=1):
        wire = _wire(rpc.scope, sequence)
        request_id = f"v2-{sequence:03d}"
        params = _example(rpc.params)
        result = _example(rpc.result)
        rpc_cases.append(
            {
                "method": rpc.method,
                "scope": rpc.scope,
                "paramsModel": rpc.params_model,
                "resultModel": rpc.result_model,
                "paramsSchema": _closed_schema(rpc.params),
                "resultSchema": _closed_schema(rpc.result),
                "request": {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "method": rpc.method,
                    "wire": wire,
                    "params": params,
                },
                "success": {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "wire": wire,
                    "result": result,
                },
                "error": {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "wire": wire,
                    "error": {
                        "code": _error_code(rpc.method),
                        "message": "request is invalid",
                        "details": {"path": "params"},
                        "retryable": False,
                    },
                },
            }
        )

    event_cases = [
        {
            "contractVersion": "2.0",
            "topic": topic,
            "wire": _wire("workspace", 100 + index),
            "payloadModel": payload_model,
            "payloadSchema": _closed_schema(payload),
            "payload": payload,
        }
        for index, (topic, payload_model, payload) in enumerate(EVENT_REGISTRY)
    ]
    return {
        "contractVersion": "2.0",
        "rpcMethods": [rpc.method for rpc in RPC_REGISTRY],
        "eventTopics": list(EVENT_TOPICS),
        "rpcCases": rpc_cases,
        "eventCases": event_cases,
    }


def _encoded() -> str:
    return json.dumps(build_catalog(), ensure_ascii=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    expected = _encoded()
    if args.check:
        actual = OUTPUT.read_text(encoding="utf-8") if OUTPUT.exists() else ""
        if actual != expected:
            raise SystemExit("contracts/v2/fixtures/rpc-catalog.json is stale")
        return 0
    OUTPUT.write_text(expected, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
