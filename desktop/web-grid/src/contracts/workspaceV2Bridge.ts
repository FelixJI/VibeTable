import {
  parseFileDocumentV2,
  parseFileRevisionV2,
  parseGlobalWireScope,
  parseRetentionPolicyV2,
  parseWorkspaceEventV2,
  parseWorkspaceRegistryEntryV2,
  parseWorkspaceSessionV2,
  parseWorkspaceWireScope,
  type FileDocumentV2,
  type FileRevisionV2,
  type RetentionPolicyV2,
  type WireScopeV2,
  type WorkspaceEncryptionMode,
  type WorkspaceEventV2,
  type WorkspaceRegistryEntryV2,
  type WorkspaceSessionV2,
  type WorkspaceStorageMode,
} from "@/contracts/workspaceV2";
import type {
  HistoryChangeSet,
  HistoryPage,
  HistoryRecordChange,
  RelationFieldChange,
  RestorePreview,
  RestoreResult,
} from "@/contracts";

export const WORKSPACE_V2_RPC_METHODS = [
  "workspace.list",
  "workspace.create",
  "workspace.register",
  "workspace.relink",
  "workspace.open",
  "workspace.switch",
  "workspace.close",
  "workspace.remove",
  "workspace.planDelete",
  "workspace.applyDelete",
  "workspace.storage.preview",
  "workspace.storage.apply",
  "snapshot.request",
  "snapshot.list",
  "snapshot.inspect",
  "snapshot.update",
  "snapshot.previewRestore",
  "snapshot.applyRestore",
  "snapshot.openAsNewWorkspace",
  "snapshot.previewExtract",
  "snapshot.applyExtract",
  "snapshot.export",
  "snapshot.inspectPackage",
  "snapshot.import",
  "history.query",
  "history.previewRestore",
  "history.applyRestore",
  "repository.verify",
  "repository.previewKeyRotation",
  "repository.applyKeyRotation",
  "fileHistory.import",
  "fileHistory.listDocuments",
  "fileHistory.listPendingChanges",
  "fileHistory.applyPendingChange",
  "fileHistory.unlink",
  "fileHistory.relink",
  "fileHistory.readTree",
  "fileHistory.restore",
  "fileHistory.upgrade",
  "fileHistory.activateLeaf",
  "retention.get",
  "retention.status",
  "retention.update",
  "retention.plan",
  "retention.apply",
  "replica.status",
  "replica.synchronize",
  "replica.forceTakeover",
  "conflict.list",
  "conflict.inspect",
  "conflict.preview",
  "conflict.apply",
] as const;

export type WorkspaceV2RpcMethod = (typeof WORKSPACE_V2_RPC_METHODS)[number];

export interface WorkspaceV2RpcParams {
  readonly "workspace.list": Readonly<Record<string, never>>;
  readonly "workspace.create": {
    readonly displayName: string;
    readonly encryptionMode: WorkspaceEncryptionMode;
  } & (
    | {
      readonly locationPolicy: "managedDefault";
      readonly selectedRootGrant: null;
      readonly storageMode: "direct";
      readonly userMarkedSync: false;
    }
    | {
      readonly locationPolicy: "other";
      readonly selectedRootGrant: string;
      readonly storageMode: WorkspaceStorageMode;
      readonly userMarkedSync: boolean;
    }
  );
  readonly "workspace.register": { readonly selectedRootGrant: string };
  readonly "workspace.relink": {
    readonly workspaceId: string;
    readonly selectedRootGrant: string;
  };
  readonly "workspace.open": {
    readonly workspaceId: string;
    readonly openMode: "readOnly" | "writable";
  };
  readonly "workspace.switch": {
    readonly targetWorkspaceId: string;
    readonly openMode: "readOnly" | "writable";
  };
  readonly "workspace.close": { readonly reason: "user" | "shutdown" };
  readonly "workspace.remove": { readonly workspaceId: string };
  readonly "workspace.planDelete": { readonly workspaceId: string };
  readonly "workspace.applyDelete": {
    readonly planId: string;
    readonly confirmation: string;
  };
  readonly "workspace.storage.preview": {
    readonly workspaceId: string;
    readonly action: "relocate" | "convertTopology" | "releaseActivityCache";
    readonly targetMode: WorkspaceStorageMode | null;
    readonly selectedRootGrant: string | null;
  };
  readonly "workspace.storage.apply": {
    readonly planId: string;
    readonly confirmation: string;
  };
  readonly "snapshot.request": {
    readonly trigger: "manual";
    readonly urgency: "foreground";
  };
  readonly "snapshot.list": { readonly cursor: string | null; readonly limit: number };
  readonly "snapshot.inspect": { readonly snapshotId: string };
  readonly "snapshot.update": {
    readonly snapshotId: string;
    readonly action: "pin" | "unpin";
    readonly expectedCatalogRevision: number;
  };
  readonly "snapshot.previewRestore": {
    readonly snapshotId: string;
    readonly targetMode: "currentWorkspace" | "newWorkspace";
  };
  readonly "snapshot.applyRestore": { readonly planId: string; readonly confirmed: true };
  readonly "snapshot.openAsNewWorkspace": { readonly snapshotId: string };
  readonly "snapshot.previewExtract": {
    readonly snapshotId: string;
    readonly documentId: string;
  };
  readonly "snapshot.applyExtract": {
    readonly planId: string;
    readonly pathGrant: string;
  };
  readonly "snapshot.export": {
    readonly snapshotId: string;
    readonly pathGrant: string;
    readonly encryption: "none" | "age";
    readonly recipients: readonly string[];
    readonly credential: string | null;
  };
  readonly "snapshot.inspectPackage": {
    readonly pathGrant: string;
    readonly credential: string | null;
  };
  readonly "snapshot.import": {
    readonly planId: string;
    readonly credential: string | null;
    readonly targetMode: "newWorkspace" | "currentWorkspace";
    readonly targetWorkspaceId: string | null;
  };
  readonly "history.query": {
    readonly collection: string;
    readonly scope: "table" | "row" | "cell" | "archived";
    readonly itemId: string | null;
    readonly field: string | null;
    readonly search: string;
    readonly dateFrom: string | null;
    readonly dateTo: string | null;
    readonly actorId: string | null;
    readonly actions: readonly string[];
    readonly recordId: string | null;
    readonly limit: number;
    readonly offset: number;
  };
  readonly "history.previewRestore": {
    readonly collection: string;
    readonly itemId: string;
    readonly targetRevision: string;
    readonly scope: "row" | "cell" | "archived";
    readonly field: string | null;
  };
  readonly "history.applyRestore": {
    readonly collection: string;
    readonly itemId: string;
    readonly token: string;
  };
  readonly "repository.verify": Readonly<Record<string, never>>;
  readonly "repository.previewKeyRotation": Readonly<Record<string, never>>;
  readonly "repository.applyKeyRotation": {
    readonly planId: string;
    readonly confirmed: true;
  };
  readonly "fileHistory.import": {
    readonly pathGrant: string;
    readonly relativePath: string;
    readonly mimeType: string;
  };
  readonly "fileHistory.listDocuments": { readonly includeDeleted: boolean };
  readonly "fileHistory.listPendingChanges": Readonly<Record<string, never>>;
  readonly "fileHistory.applyPendingChange": {
    readonly changeId: string;
    readonly action: "new" | "move" | "copy" | "delete" | "dismiss";
    readonly documentId: string | null;
    readonly expectedEffectiveRevisionId: string | null;
  };
  readonly "fileHistory.unlink": {
    readonly documentId: string;
    readonly expectedEffectiveRevisionId: string;
  };
  readonly "fileHistory.relink": {
    readonly documentId: string;
    readonly expectedEffectiveRevisionId: string;
    readonly pathGrant: string;
  };
  readonly "fileHistory.readTree": { readonly documentId: string };
  readonly "fileHistory.restore": {
    readonly documentId: string;
    readonly expectedEffectiveRevisionId: string;
    readonly historicalRevisionId: string;
  };
  readonly "fileHistory.upgrade": {
    readonly documentId: string;
    readonly revisionId: string;
    readonly pathGrant: string;
  };
  readonly "fileHistory.activateLeaf": {
    readonly documentId: string;
    readonly expectedEffectiveRevisionId: string;
    readonly targetLeafRevisionId: string;
  };
  readonly "retention.get": Readonly<Record<string, never>>;
  readonly "retention.status": Readonly<Record<string, never>>;
  readonly "retention.update": {
    readonly expectedRevision: number;
    readonly snapshotDays: number;
    readonly snapshotCount: number;
    readonly snapshotBuckets: readonly ("hourly" | "daily" | "weekly" | "monthly")[];
    readonly fileRevisionDays: number;
    readonly fileRevisionCount: number;
    readonly fileRevisionBuckets: readonly ("hourly" | "daily" | "weekly" | "monthly")[];
    readonly repositoryLimitBytes: number | null;
  };
  readonly "retention.plan": Readonly<Record<string, never>>;
  readonly "retention.apply": { readonly planId: string };
  readonly "replica.status": Readonly<Record<string, never>>;
  readonly "replica.synchronize": Readonly<Record<string, never>>;
  readonly "replica.forceTakeover": { readonly mode: "provisional" };
  readonly "conflict.list": { readonly cursor: string | null; readonly limit: number };
  readonly "conflict.inspect": { readonly conflictId: string };
  readonly "conflict.preview": {
    readonly conflictId: string;
    readonly choices: readonly {
      readonly itemId: string;
      readonly kind: "file" | "table" | "settings";
      readonly side: "local" | "replica" | "both";
    }[];
  };
  readonly "conflict.apply": { readonly planId: string };
}

export type WorkspaceV2UiAction<M extends WorkspaceV2RpcMethod = WorkspaceV2RpcMethod> = {
  readonly [K in M]: {
    readonly method: K;
    readonly params: WorkspaceV2RpcParams[K];
  };
}[M];

export interface SnapshotTimelineItem {
  readonly snapshotId: string;
  readonly createdAt: string;
  readonly state:
    | "queued" | "barrier" | "captured" | "chunking" | "verifying" | "published"
    | "syncing" | "ready" | "failed" | "corrupt" | "repairing";
  readonly trigger: "automatic" | "manual" | "protection" | "import" | "restore";
  readonly integrity: "pending" | "verified" | "corrupt" | "repairing";
  readonly syncState: "localOnly" | "pending" | "syncing" | "replicated" | "failed";
  readonly pinned: boolean;
  readonly retentionReasons: readonly string[];
  readonly logicalSize: number;
  readonly physicalSize: number;
  readonly note: string | null;
  readonly catalogRevision: number;
}

export interface PendingFileChange {
  readonly changeId: string;
  readonly relativePath: string;
  readonly missing: boolean;
  readonly observedHash: string;
  readonly observedSize: number;
  readonly reason: string;
  readonly candidateDocumentIds: readonly string[];
  readonly createdAt: string;
  readonly updatedAt: string;
}

export interface WorkspaceStorageProjection {
  readonly location: string;
  readonly activityRoot: string;
  readonly mode: WorkspaceStorageMode;
  readonly provider: string;
  readonly health: "healthy" | "attention" | "offline";
  readonly logicalSize: number;
  readonly physicalSize: number;
  readonly reclaimableSize: number;
  readonly encryption: WorkspaceEncryptionMode;
  readonly keyVersion: number;
  readonly pendingSync: boolean;
  readonly remoteVerified: boolean;
}

export interface WorkspaceStorageLocation {
  readonly selectedRoot: string;
  readonly activityRoot: string | null;
  readonly mode: WorkspaceStorageMode;
}

export interface WorkspaceStoragePlan {
  readonly planId: string;
  readonly workspaceId: string;
  readonly action: "relocate" | "convertTopology" | "releaseActivityCache";
  readonly source: WorkspaceStorageLocation;
  readonly target: WorkspaceStorageLocation;
  readonly bytesToCopy: number;
  readonly requiresClosedSession: true;
  readonly warnings: readonly string[];
  readonly expiresAt: string;
  readonly verificationReceiptId: string | null;
}

export interface WorkspaceConflictSummary {
  readonly conflictId: string;
  readonly state: "pending" | "validating" | "ready" | "failed";
  readonly createdAt: string;
  readonly itemCount: number;
}

export interface WorkspaceConflictItem {
  readonly conflictId: string;
  readonly itemId: string;
  readonly path: string;
  readonly kind: "file" | "table" | "settings";
  readonly state: "pending" | "validating" | "ready" | "failed";
  readonly localSummary: string;
  readonly replicaSummary: string;
  readonly baseSummary: string;
  readonly dependencies: readonly string[];
  readonly selected: "local" | "replica" | "both" | null;
}

export interface FileRevisionTreeProjection {
  readonly documentId: string;
  readonly effectiveRevisionId: string | null;
  readonly revisions: readonly FileRevisionV2[];
}

export interface RestorePlanResult {
  readonly planId: string;
  readonly protectionRequired: boolean;
  readonly changes: readonly string[];
}

export interface SnapshotExtractPlan {
  readonly planId: string;
  readonly displayName: string;
  readonly size: number;
  readonly expiresAt: string;
}

export interface RepositoryVerificationResult {
  readonly state: "verified" | "corrupt";
  readonly snapshotCount: number;
  readonly objectCount: number;
  readonly corruptSnapshotIds: readonly string[];
}

export interface RepositoryKeyRotationPlan {
  readonly planId: string;
  readonly expiresAt: string;
  readonly protectionRequired: true;
}

export interface RepositoryKeyRotationResult {
  readonly operationId: string;
  readonly state: "hostRestartRequired";
  readonly newRecoveryKeyAvailable: false;
}

export interface CleanupPlanResult {
  readonly planId: string;
  readonly reclaimableBytes: number;
  readonly blockedReasons: readonly string[];
}

export interface ConflictPlanResult {
  readonly planId: string;
  readonly diagnostics: readonly string[];
  readonly valid: boolean;
}

export interface SnapshotPackagePlan {
  readonly planId: string;
  readonly trusted: boolean;
  readonly workspaceId: string;
  readonly sourceSnapshotId: string | null;
  readonly snapshotCount: number;
  readonly encrypted: boolean;
  readonly verified: boolean;
  readonly expiresAt: string;
}

export interface SnapshotImportResult {
  readonly operationId: string;
  readonly snapshotId: string;
  readonly sourceWorkspaceId: string;
  readonly sourceSnapshotId: string;
  readonly state: "restoreRequired";
}

export interface HistoryRestoreResultV2 extends RestoreResult {
  readonly mutationRevision: number;
}

export interface WorkspaceDeletePlan {
  readonly workspaceId: string;
  readonly planId: string;
  readonly displayName: string;
  readonly requiresTypedName: boolean;
}

export type WorkspaceDeletePlanResult = Omit<WorkspaceDeletePlan, "workspaceId">;

export interface RetentionProtectionStatus {
  readonly repositoryUsageBytes: number;
  readonly repositoryLimitBytes: number | null;
  readonly automaticSnapshotsPaused: boolean;
  readonly warningCode: string | null;
  readonly integrityStatus: "unknown" | "verified" | "corrupt";
  readonly integrityFailure: string | null;
  readonly lastIncrementalCheckAt: string | null;
  readonly lastFullCheckAt: string | null;
  readonly maintenanceFailure: string | null;
  readonly maintenanceFailureStage: "integrity" | "sweep" | null;
  readonly lastMaintenanceFailureAt: string | null;
}

export interface WorkspaceV2RpcResultMap {
  readonly "workspace.list": { readonly workspaces: readonly WorkspaceRegistryEntryV2[] };
  readonly "workspace.create": WorkspaceOperationResult;
  readonly "workspace.register": WorkspaceOperationResult;
  readonly "workspace.relink": WorkspaceOperationResult;
  readonly "workspace.open": WorkspaceSessionResult;
  readonly "workspace.switch": WorkspaceSessionResult;
  readonly "workspace.close": WorkspaceSessionResult;
  readonly "workspace.remove": WorkspaceOperationResult;
  readonly "workspace.planDelete": WorkspaceDeletePlanResult;
  readonly "workspace.applyDelete": WorkspaceOperationResult;
  readonly "workspace.storage.preview": WorkspaceStoragePlan;
  readonly "workspace.storage.apply": {
    readonly workspaceId: string;
    readonly status: "applied";
    readonly storage: WorkspaceStorageProjection;
  };
  readonly "snapshot.request": OperationResult & {
    readonly snapshotId: string;
    readonly mutationRevision: number;
  };
  readonly "snapshot.list": {
    readonly snapshots: readonly SnapshotTimelineItem[];
    readonly nextCursor: string | null;
  };
  readonly "snapshot.inspect": SnapshotStateResult;
  readonly "snapshot.update": SnapshotStateResult;
  readonly "snapshot.previewRestore": RestorePlanResult;
  readonly "snapshot.applyRestore": OperationResult;
  readonly "snapshot.openAsNewWorkspace": WorkspaceSessionResult;
  readonly "snapshot.previewExtract": SnapshotExtractPlan;
  readonly "snapshot.applyExtract": OperationResult;
  readonly "snapshot.export": { readonly displayName: string; readonly sha256: string };
  readonly "snapshot.inspectPackage": SnapshotPackagePlan;
  readonly "snapshot.import": SnapshotImportResult;
  readonly "history.query": HistoryPage;
  readonly "history.previewRestore": RestorePreview;
  readonly "history.applyRestore": HistoryRestoreResultV2;
  readonly "repository.verify": RepositoryVerificationResult;
  readonly "repository.previewKeyRotation": RepositoryKeyRotationPlan;
  readonly "repository.applyKeyRotation": RepositoryKeyRotationResult;
  readonly "fileHistory.import": FileDocumentV2;
  readonly "fileHistory.listDocuments": {
    readonly documents: readonly FileDocumentV2[];
  };
  readonly "fileHistory.listPendingChanges": {
    readonly changes: readonly PendingFileChange[];
  };
  readonly "fileHistory.applyPendingChange": {
    readonly changeId: string;
    readonly state: "applied" | "dismissed";
    readonly document: FileDocumentV2 | null;
  };
  readonly "fileHistory.unlink": FileDocumentV2;
  readonly "fileHistory.relink": FileDocumentV2;
  readonly "fileHistory.readTree": FileRevisionTreeProjection;
  readonly "fileHistory.restore": FileRevisionResult;
  readonly "fileHistory.upgrade": FileRevisionResult;
  readonly "fileHistory.activateLeaf": {
    readonly revisionId: string;
    readonly effective: true;
  };
  readonly "retention.get": RetentionPolicyV2;
  readonly "retention.status": RetentionProtectionStatus;
  readonly "retention.update": RetentionPolicyV2;
  readonly "retention.plan": CleanupPlanResult;
  readonly "retention.apply": {
    readonly deletedObjects: number;
    readonly reclaimedBytes: number;
  };
  readonly "replica.status": {
    readonly coordinationStrength: "strong" | "advisory";
    readonly syncState: SnapshotTimelineItem["syncState"];
    readonly pendingSync: boolean;
  };
  readonly "replica.synchronize": OperationResult;
  readonly "replica.forceTakeover": {
    readonly fenceEpoch: number;
    readonly claimId: string;
    readonly mode: "provisional";
  };
  readonly "conflict.list": {
    readonly conflicts: readonly WorkspaceConflictSummary[];
    readonly nextCursor: string | null;
  };
  readonly "conflict.inspect": {
    readonly conflictId: string;
    readonly state: WorkspaceConflictItem["state"];
    readonly items: readonly WorkspaceConflictItem[];
  };
  readonly "conflict.preview": ConflictPlanResult;
  readonly "conflict.apply": {
    readonly operationId: string;
    readonly state: "applied";
    readonly recoverySnapshotIds: readonly string[];
  };
}

interface WorkspaceOperationResult {
  readonly workspaceId: string;
  readonly status: "created" | "registered" | "relinked" | "removed" | "deleted";
}

interface WorkspaceSessionResult {
  readonly workspaceId: string | null;
  readonly sessionEpoch: number;
  readonly state: WorkspaceSessionV2["state"];
}

interface OperationResult {
  readonly operationId: string;
  readonly state: string;
}

interface FileRevisionResult {
  readonly revisionId: string;
  readonly revisionOrdinal: number;
  readonly localSequence: number | null;
  readonly formalVersion: number | null;
}

interface SnapshotStateResult {
  readonly snapshotId: string;
  readonly state: SnapshotTimelineItem["state"];
  readonly integrity: SnapshotTimelineItem["integrity"];
}

export type WorkspaceV2RpcResult<M extends WorkspaceV2RpcMethod> =
  WorkspaceV2RpcResultMap[M];

export type WorkspaceV2RequestPayload<
  M extends WorkspaceV2RpcMethod = WorkspaceV2RpcMethod,
> = {
  readonly [K in M]: {
    readonly method: K;
    readonly params: WorkspaceV2RpcParams[K];
    readonly wire: WireScopeV2;
  };
}[M];

export interface WorkspaceV2Failure {
  readonly code: string;
  readonly message: string;
  readonly retryable: boolean;
}

export type WorkspaceV2SuccessReply<
  M extends WorkspaceV2RpcMethod = WorkspaceV2RpcMethod,
> = {
  readonly [K in M]: {
    readonly method: K;
    readonly wire: WireScopeV2;
    readonly ok: true;
    readonly result: WorkspaceV2RpcResultMap[K];
    readonly error: null;
  };
}[M];

export type WorkspaceV2FailureReply<
  M extends WorkspaceV2RpcMethod = WorkspaceV2RpcMethod,
> = {
  readonly [K in M]: {
    readonly method: K;
    readonly wire: WireScopeV2;
    readonly ok: false;
    readonly result: null;
    readonly error: WorkspaceV2Failure;
  };
}[M];

export type WorkspaceV2Reply<M extends WorkspaceV2RpcMethod = WorkspaceV2RpcMethod> =
  | WorkspaceV2SuccessReply<M>
  | WorkspaceV2FailureReply<M>;

export interface WorkspaceV2Bootstrap {
  readonly contractVersion: "2.0";
  readonly capabilities: readonly string[];
  readonly workspaces: readonly WorkspaceRegistryEntryV2[];
  readonly session: WorkspaceSessionV2;
  readonly snapshots: readonly SnapshotTimelineItem[];
  readonly storage: WorkspaceStorageProjection | null;
  readonly retention: RetentionPolicyV2;
  readonly conflicts: readonly WorkspaceConflictItem[];
  readonly fileTrees: readonly FileRevisionTreeProjection[];
}

export const WORKSPACE_V2_HOST_MESSAGE_TYPES = [
  "workspace.v2.bootstrap",
  "workspace.v2.event",
  "workspace.v2.reply",
  "workspace.v2.response",
] as const;

export const WORKSPACE_V2_WEB_MESSAGE_TYPES = ["workspace.v2.request"] as const;

export type WorkspaceV2HostMessageType =
  (typeof WORKSPACE_V2_HOST_MESSAGE_TYPES)[number];
export type WorkspaceV2WebMessageType =
  (typeof WORKSPACE_V2_WEB_MESSAGE_TYPES)[number];

export interface WorkspaceV2HostPayloadMap {
  readonly "workspace.v2.bootstrap": unknown;
  readonly "workspace.v2.event": unknown;
  readonly "workspace.v2.reply": unknown;
  readonly "workspace.v2.response": unknown;
}

export interface WorkspaceV2WebPayloadMap {
  readonly "workspace.v2.request": WorkspaceV2RequestPayload;
}

type JsonObject = Record<string, unknown>;

function object(value: unknown, label: string): JsonObject {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value as JsonObject;
}

function exact(source: JsonObject, keys: readonly string[], label: string): void {
  const actual = Object.keys(source);
  if (
    actual.length !== keys.length
    || actual.some((key) => !keys.includes(key))
  ) {
    throw new Error(`${label} has unknown or missing fields`);
  }
}

function text(value: unknown, label: string): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`${label} must be a non-empty string`);
  }
  return value;
}

function integer(value: unknown, label: string, minimum = 0): number {
  if (!Number.isInteger(value) || (value as number) < minimum) {
    throw new Error(`${label} must be an integer >= ${minimum}`);
  }
  return value as number;
}

function bool(value: unknown, label: string): boolean {
  if (typeof value !== "boolean") throw new Error(`${label} must be a boolean`);
  return value;
}

function nullableText(value: unknown, label: string): string | null {
  return value === null ? null : text(value, label);
}

function stringList(value: unknown, label: string): readonly string[] {
  if (!Array.isArray(value)) throw new Error(`${label} must be an array`);
  return value.map((item, index) => text(item, `${label}[${index}]`));
}

function oneOf<T extends string>(
  value: unknown,
  values: readonly T[],
  label: string,
): T {
  if (typeof value !== "string" || !values.includes(value as T)) {
    throw new Error(`${label} is invalid`);
  }
  return value as T;
}

function parseWire(value: unknown): WireScopeV2 {
  const source = object(value, "workspace v2 wire");
  return source.scope === "global"
    ? parseGlobalWireScope(source)
    : parseWorkspaceWireScope(source);
}

function parseSnapshot(value: unknown): SnapshotTimelineItem {
  const source = object(value, "snapshot projection");
  exact(source, [
    "snapshotId", "createdAt", "state", "trigger", "integrity", "syncState",
    "pinned", "retentionReasons", "logicalSize", "physicalSize", "note",
    "catalogRevision",
  ], "snapshot projection");
  return {
    snapshotId: text(source.snapshotId, "snapshotId"),
    createdAt: text(source.createdAt, "createdAt"),
    state: oneOf(source.state, [
      "queued", "barrier", "captured", "chunking", "verifying", "published",
      "syncing", "ready", "failed", "corrupt", "repairing",
    ], "snapshot state"),
    trigger: oneOf(source.trigger, ["automatic", "manual", "protection", "import", "restore"], "snapshot trigger"),
    integrity: oneOf(source.integrity, ["pending", "verified", "corrupt", "repairing"], "snapshot integrity"),
    syncState: oneOf(source.syncState, ["localOnly", "pending", "syncing", "replicated", "failed"], "snapshot sync state"),
    pinned: bool(source.pinned, "snapshot pinned"),
    retentionReasons: stringList(source.retentionReasons, "retention reasons"),
    logicalSize: integer(source.logicalSize, "logicalSize"),
    physicalSize: integer(source.physicalSize, "physicalSize"),
    note: nullableText(source.note, "snapshot note"),
    catalogRevision: integer(source.catalogRevision, "catalogRevision", 1),
  };
}

function parseStorage(value: unknown): WorkspaceStorageProjection {
  const source = object(value, "storage projection");
  exact(source, [
    "location", "activityRoot", "mode", "provider", "health", "logicalSize",
    "physicalSize", "reclaimableSize", "encryption", "keyVersion",
    "pendingSync", "remoteVerified",
  ], "storage projection");
  return {
    location: text(source.location, "storage location"),
    activityRoot: text(source.activityRoot, "activity root"),
    mode: oneOf(source.mode, ["direct", "mirrored"], "storage mode"),
    provider: text(source.provider, "storage provider"),
    health: oneOf(source.health, ["healthy", "attention", "offline"], "storage health"),
    logicalSize: integer(source.logicalSize, "logicalSize"),
    physicalSize: integer(source.physicalSize, "physicalSize"),
    reclaimableSize: integer(source.reclaimableSize, "reclaimableSize"),
    encryption: oneOf(source.encryption, ["none", "convenient", "protected"], "encryption"),
    keyVersion: integer(source.keyVersion, "keyVersion"),
    pendingSync: bool(source.pendingSync, "pendingSync"),
    remoteVerified: bool(source.remoteVerified, "remoteVerified"),
  };
}

function parseConflict(value: unknown): WorkspaceConflictItem {
  const source = object(value, "conflict projection");
  exact(source, [
    "conflictId", "itemId", "path", "kind", "state", "localSummary", "replicaSummary",
    "baseSummary", "dependencies", "selected",
  ], "conflict projection");
  const selected: "local" | "replica" | "both" | null = source.selected === null
    ? null
    : oneOf(source.selected, ["local", "replica", "both"] as const, "conflict selection");
  return {
    conflictId: text(source.conflictId, "conflictId"),
    itemId: text(source.itemId, "itemId"),
    path: text(source.path, "conflict path"),
    kind: oneOf(source.kind, ["file", "table", "settings"], "conflict kind"),
    state: oneOf(source.state, ["pending", "validating", "ready", "failed"], "conflict state"),
    localSummary: text(source.localSummary, "local summary"),
    replicaSummary: text(source.replicaSummary, "replica summary"),
    baseSummary: text(source.baseSummary, "base summary"),
    dependencies: stringList(source.dependencies, "conflict dependencies"),
    selected,
  };
}

function parseConflictSummary(value: unknown): WorkspaceConflictSummary {
  const source = object(value, "conflict summary");
  exact(source, [
    "conflictId", "state", "createdAt", "itemCount",
  ], "conflict summary");
  return {
    conflictId: text(source.conflictId, "conflictId"),
    state: oneOf(source.state, ["pending", "validating", "ready", "failed"], "conflict state"),
    createdAt: text(source.createdAt, "createdAt"),
    itemCount: integer(source.itemCount, "itemCount", 0),
  };
}

function parseFileTree(value: unknown): FileRevisionTreeProjection {
  const source = object(value, "file revision tree");
  exact(source, ["documentId", "effectiveRevisionId", "revisions"], "file revision tree");
  if (!Array.isArray(source.revisions)) throw new Error("file revisions must be an array");
  return {
    documentId: text(source.documentId, "documentId"),
    effectiveRevisionId: nullableText(source.effectiveRevisionId, "effectiveRevisionId"),
    revisions: source.revisions.map(parseFileRevisionV2),
  };
}

function parseHistoryRelationChange(value: unknown): RelationFieldChange {
  const source = object(value, "history relation change");
  exact(source, [
    "field", "kind", "relatedCollection", "relatedItemId", "displayValue",
    "beforeItemId", "afterItemId", "beforeDisplayValue",
    "afterDisplayValue", "targetAvailable",
  ], "history relation change");
  return {
    field: text(source.field, "history relation field"),
    kind: oneOf(source.kind, ["m2o", "o2m", "m2m", "m2a", "file"], "history relation kind"),
    relatedCollection: nullableText(source.relatedCollection, "relatedCollection"),
    relatedItemId: nullableText(source.relatedItemId, "relatedItemId"),
    displayValue: nullableText(source.displayValue, "displayValue"),
    beforeItemId: nullableText(source.beforeItemId, "beforeItemId"),
    afterItemId: nullableText(source.afterItemId, "afterItemId"),
    beforeDisplayValue: nullableText(source.beforeDisplayValue, "beforeDisplayValue"),
    afterDisplayValue: nullableText(source.afterDisplayValue, "afterDisplayValue"),
    targetAvailable: bool(source.targetAvailable, "targetAvailable"),
  };
}

function parseHistoryScalarChange(value: unknown): {
  readonly field: string;
  readonly before: unknown;
  readonly after: unknown;
} {
  const source = object(value, "history scalar change");
  exact(source, ["field", "before", "after"], "history scalar change");
  return {
    field: text(source.field, "history scalar field"),
    before: source.before,
    after: source.after,
  };
}

function parseHistoryRecordChange(value: unknown): HistoryRecordChange {
  const source = object(value, "history record change");
  exact(source, [
    "revisionId", "itemId", "recordLabel", "action",
    "scalarChanges", "relationChanges",
  ], "history record change");
  if (!Array.isArray(source.scalarChanges)
      || !Array.isArray(source.relationChanges)) {
    throw new Error("history record change collections are invalid");
  }
  return {
    revisionId: text(source.revisionId, "history revisionId"),
    itemId: text(source.itemId, "history record itemId"),
    recordLabel: nullableText(source.recordLabel, "history record label"),
    action: text(source.action, "history record action"),
    scalarChanges: source.scalarChanges.map(parseHistoryScalarChange),
    relationChanges: source.relationChanges.map(parseHistoryRelationChange),
  };
}

function parseHistoryChangeSet(value: unknown): HistoryChangeSet {
  const source = object(value, "history change set");
  exact(source, [
    "rootRevisionId", "changeSetId", "activityId", "action", "timestamp",
    "actor", "scalarChanges", "relationChanges", "itemId", "recordLabel",
    "revisionIds", "affectedRecords", "recordChanges",
  ], "history change set");
  if (!Array.isArray(source.scalarChanges)
      || !Array.isArray(source.relationChanges)
      || !Array.isArray(source.recordChanges)) {
    throw new Error("history change set collections are invalid");
  }
  const actor = object(source.actor, "history actor");
  exact(actor, ["userId", "displayName"], "history actor");
  return {
    rootRevisionId: text(source.rootRevisionId, "history rootRevisionId"),
    changeSetId: text(source.changeSetId, "history changeSetId"),
    activityId: nullableText(source.activityId, "history activityId"),
    action: text(source.action, "history action"),
    timestamp: text(source.timestamp, "history timestamp"),
    actor: {
      userId: nullableText(actor.userId, "history actor userId"),
      displayName: nullableText(actor.displayName, "history actor displayName"),
    },
    scalarChanges: source.scalarChanges.map(parseHistoryScalarChange),
    relationChanges: source.relationChanges.map(parseHistoryRelationChange),
    itemId: nullableText(source.itemId, "history itemId"),
    recordLabel: nullableText(source.recordLabel, "history record label"),
    revisionIds: stringList(source.revisionIds, "history revisionIds"),
    affectedRecords: integer(
      source.affectedRecords,
      "history affectedRecords",
    ),
    recordChanges: source.recordChanges.map(parseHistoryRecordChange),
  };
}

function parseHistoryPage(value: unknown): HistoryPage {
  const source = object(value, "history page");
  exact(source, [
    "collection", "itemId", "changeSets", "total", "capabilityHash",
    "schemaRevision", "scope", "field", "hasMore",
    "archivedDefaultRevisionIds",
  ], "history page");
  if (!Array.isArray(source.changeSets)) {
    throw new Error("history changeSets must be an array");
  }
  const archived = object(
    source.archivedDefaultRevisionIds,
    "archived default revision ids",
  );
  return {
    collection: text(source.collection, "history collection"),
    itemId: nullableText(source.itemId, "history page itemId"),
    changeSets: source.changeSets.map(parseHistoryChangeSet),
    total: integer(source.total, "history total"),
    capabilityHash: text(source.capabilityHash, "history capabilityHash"),
    schemaRevision: text(source.schemaRevision, "history schemaRevision"),
    scope: oneOf(
      source.scope,
      ["table", "row", "cell", "archived"] as const,
      "history scope",
    ),
    field: nullableText(source.field, "history page field"),
    hasMore: bool(source.hasMore, "history hasMore"),
    archivedDefaultRevisionIds: Object.fromEntries(
      Object.entries(archived).map(([itemId, revisionId]) => [
        text(itemId, "archived itemId"),
        text(revisionId, "archived revisionId"),
      ]),
    ),
  };
}

function parseHistoryRestorePreview(value: unknown): RestorePreview {
  const source = object(value, "history restore preview");
  exact(source, [
    "collection", "itemId", "targetRevision", "currentHash",
    "schemaRevision", "scalarChanges", "relationChanges", "diagnostics",
    "token", "expiresAt", "scope", "field", "canApply",
    "restorableFields",
  ], "history restore preview");
  if (!Array.isArray(source.scalarChanges)
      || !Array.isArray(source.relationChanges)
      || !Array.isArray(source.diagnostics)) {
    throw new Error("history restore preview collections are invalid");
  }
  return {
    collection: text(source.collection, "history collection"),
    itemId: text(source.itemId, "history itemId"),
    targetRevision: text(source.targetRevision, "targetRevision"),
    currentHash: text(source.currentHash, "currentHash"),
    schemaRevision: text(source.schemaRevision, "schemaRevision"),
    scalarChanges: source.scalarChanges.map(parseHistoryScalarChange),
    relationChanges: source.relationChanges.map(parseHistoryRelationChange),
    diagnostics: source.diagnostics.map((value) => {
      const diagnostic = object(value, "history restore diagnostic");
      exact(diagnostic, [
        "field", "classification", "severity", "code", "message",
      ], "history restore diagnostic");
      return {
        field: text(diagnostic.field, "diagnostic field"),
        classification: oneOf(diagnostic.classification, [
          "recoverable", "readonly_system", "derived", "sensitive",
          "schema_retired", "permission_denied", "incompatible",
          "relation_unsafe",
        ], "diagnostic classification"),
        severity: oneOf(diagnostic.severity, ["warning", "error"], "diagnostic severity"),
        code: text(diagnostic.code, "diagnostic code"),
        message: text(diagnostic.message, "diagnostic message"),
      };
    }),
    token: text(source.token, "history restore token"),
    expiresAt: text(source.expiresAt, "history restore expiry"),
    scope: oneOf(
      source.scope,
      ["row", "cell", "archived"] as const,
      "history restore scope",
    ),
    field: nullableText(source.field, "history restore field"),
    canApply: bool(source.canApply, "history restore canApply"),
    restorableFields: stringList(source.restorableFields, "restorableFields"),
  };
}

export function parseWorkspaceV2Bootstrap(value: unknown): WorkspaceV2Bootstrap {
  const source = object(value, "workspace v2 bootstrap");
  exact(source, [
    "contractVersion", "capabilities", "workspaces", "session", "snapshots",
    "storage", "retention", "conflicts", "fileTrees",
  ], "workspace v2 bootstrap");
  if (source.contractVersion !== "2.0") throw new Error("workspace v2 bootstrap version is invalid");
  if (!Array.isArray(source.capabilities)
      || !Array.isArray(source.workspaces)
      || !Array.isArray(source.snapshots)
      || !Array.isArray(source.conflicts)
      || !Array.isArray(source.fileTrees)) {
    throw new Error("workspace v2 bootstrap collections are invalid");
  }
  return {
    contractVersion: "2.0",
    capabilities: source.capabilities.map((item, index) =>
      text(item, `capabilities[${index}]`)),
    workspaces: source.workspaces.map(parseWorkspaceRegistryEntryV2),
    session: parseWorkspaceSessionV2(source.session),
    snapshots: source.snapshots.map(parseSnapshot),
    storage: source.storage === null ? null : parseStorage(source.storage),
    retention: parseRetentionPolicyV2(source.retention),
    conflicts: source.conflicts.map(parseConflict),
    fileTrees: source.fileTrees.map(parseFileTree),
  };
}

export function parseWorkspaceV2Event(value: unknown): WorkspaceEventV2 {
  return parseWorkspaceEventV2(value);
}

function parseResult<M extends WorkspaceV2RpcMethod>(
  method: M,
  value: unknown,
): WorkspaceV2RpcResultMap[M] {
  const source = object(value, `${method} result`);
  let parsed: unknown;
  if (method === "workspace.list") {
    exact(source, ["workspaces"], `${method} result`);
    if (!Array.isArray(source.workspaces)) throw new Error("workspaces must be an array");
    parsed = { workspaces: source.workspaces.map(parseWorkspaceRegistryEntryV2) };
  } else if ([
    "workspace.open",
    "workspace.switch",
    "workspace.close",
    "snapshot.openAsNewWorkspace",
  ].includes(method)) {
    exact(source, ["workspaceId", "sessionEpoch", "state"], `${method} result`);
    parsed = {
      workspaceId: source.workspaceId === null ? null : text(source.workspaceId, "workspaceId"),
      sessionEpoch: integer(source.sessionEpoch, "sessionEpoch"),
      state: oneOf(source.state, [
        "closed", "opening", "openedReadOnly", "openedWritable",
        "openedProvisional", "switching", "failed",
      ], "session state"),
    };
  } else if ([
    "workspace.create",
    "workspace.register",
    "workspace.relink",
    "workspace.remove",
    "workspace.applyDelete",
  ].includes(method)) {
    exact(source, ["workspaceId", "status"], `${method} result`);
    parsed = {
      workspaceId: text(source.workspaceId, "workspaceId"),
      status: oneOf(
        source.status,
        ["created", "registered", "relinked", "removed", "deleted"],
        "workspace status",
      ),
    };
  } else if (method === "workspace.planDelete") {
    exact(source, ["planId", "displayName", "requiresTypedName"], `${method} result`);
    parsed = {
      planId: text(source.planId, "planId"),
      displayName: text(source.displayName, "displayName"),
      requiresTypedName: bool(source.requiresTypedName, "requiresTypedName"),
    };
  } else if (method === "workspace.storage.preview") {
    exact(source, [
      "planId", "workspaceId", "action", "source", "target", "bytesToCopy",
      "requiresClosedSession", "warnings", "expiresAt",
      "verificationReceiptId",
    ], `${method} result`);
    const parseLocation = (
      value: unknown,
      label: string,
    ): WorkspaceStorageLocation => {
      const location = object(value, label);
      exact(location, [
        "selectedRoot", "activityRoot", "mode",
      ], label);
      return {
        selectedRoot: text(location.selectedRoot, `${label} selectedRoot`),
        activityRoot: nullableText(
          location.activityRoot,
          `${label} activityRoot`,
        ),
        mode: oneOf(location.mode, ["direct", "mirrored"], `${label} mode`),
      };
    };
    if (source.requiresClosedSession !== true) {
      throw new Error("storage plan must require a closed session");
    }
    parsed = {
      planId: text(source.planId, "planId"),
      workspaceId: text(source.workspaceId, "workspaceId"),
      action: oneOf(
        source.action,
        ["relocate", "convertTopology", "releaseActivityCache"],
        "storage action",
      ),
      source: parseLocation(source.source, "storage source"),
      target: parseLocation(source.target, "storage target"),
      bytesToCopy: integer(source.bytesToCopy, "bytesToCopy"),
      requiresClosedSession: true,
      warnings: stringList(source.warnings, "storage warnings"),
      expiresAt: text(source.expiresAt, "expiresAt"),
      verificationReceiptId: nullableText(
        source.verificationReceiptId,
        "verificationReceiptId",
      ),
    };
  } else if (method === "workspace.storage.apply") {
    exact(source, ["workspaceId", "status", "storage"], `${method} result`);
    if (source.status !== "applied") {
      throw new Error("storage apply status is invalid");
    }
    parsed = {
      workspaceId: text(source.workspaceId, "workspaceId"),
      status: "applied",
      storage: parseStorage(source.storage),
    };
  } else if (method === "snapshot.list") {
    exact(source, ["snapshots", "nextCursor"], `${method} result`);
    if (!Array.isArray(source.snapshots)) throw new Error("snapshots must be an array");
    parsed = {
      snapshots: source.snapshots.map(parseSnapshot),
      nextCursor: nullableText(source.nextCursor, "nextCursor"),
    };
  } else if (method === "snapshot.inspect" || method === "snapshot.update") {
    exact(source, ["snapshotId", "state", "integrity"], `${method} result`);
    parsed = {
      snapshotId: text(source.snapshotId, "snapshotId"),
      state: oneOf(source.state, [
        "queued", "barrier", "captured", "chunking", "verifying", "published",
        "syncing", "ready", "failed", "corrupt", "repairing",
      ], "snapshot state"),
      integrity: oneOf(source.integrity, ["pending", "verified", "corrupt", "repairing"], "snapshot integrity"),
    };
  } else if (method === "snapshot.previewRestore") {
    exact(source, ["planId", "protectionRequired", "changes"], `${method} result`);
    parsed = {
      planId: text(source.planId, "planId"),
      protectionRequired: bool(source.protectionRequired, "protectionRequired"),
      changes: stringList(source.changes, "restore changes"),
    };
  } else if (method === "snapshot.previewExtract") {
    exact(source, ["planId", "displayName", "size", "expiresAt"], `${method} result`);
    parsed = {
      planId: text(source.planId, "planId"),
      displayName: text(source.displayName, "displayName"),
      size: integer(source.size, "size"),
      expiresAt: text(source.expiresAt, "expiresAt"),
    };
  } else if (method === "repository.verify") {
    exact(source, [
      "state", "snapshotCount", "objectCount", "corruptSnapshotIds",
    ], `${method} result`);
    parsed = {
      state: oneOf(source.state, ["verified", "corrupt"], "repository state"),
      snapshotCount: integer(source.snapshotCount, "snapshotCount"),
      objectCount: integer(source.objectCount, "objectCount"),
      corruptSnapshotIds: stringList(
        source.corruptSnapshotIds,
        "corruptSnapshotIds",
      ),
    };
  } else if (method === "repository.previewKeyRotation") {
    exact(source, [
      "planId", "expiresAt", "protectionRequired",
    ], `${method} result`);
    if (source.protectionRequired !== true) {
      throw new Error("repository key rotation must require protection");
    }
    parsed = {
      planId: text(source.planId, "planId"),
      expiresAt: text(source.expiresAt, "expiresAt"),
      protectionRequired: true,
    };
  } else if (method === "repository.applyKeyRotation") {
    exact(source, [
      "operationId", "state", "newRecoveryKeyAvailable",
    ], `${method} result`);
    if (source.state !== "hostRestartRequired"
        || source.newRecoveryKeyAvailable !== false) {
      throw new Error("repository key rotation result is invalid");
    }
    parsed = {
      operationId: text(source.operationId, "operationId"),
      state: "hostRestartRequired",
      newRecoveryKeyAvailable: false,
    };
  } else if (method === "snapshot.export") {
    exact(source, ["displayName", "sha256"], `${method} result`);
    parsed = { displayName: text(source.displayName, "displayName"), sha256: text(source.sha256, "sha256") };
  } else if (method === "snapshot.inspectPackage") {
    exact(source, [
      "planId", "trusted", "workspaceId", "sourceSnapshotId",
      "snapshotCount", "encrypted", "verified", "expiresAt",
    ], `${method} result`);
    const verified = bool(source.verified, "verified");
    const sourceSnapshotId = nullableText(
      source.sourceSnapshotId,
      "sourceSnapshotId",
    );
    if (verified !== (sourceSnapshotId !== null)) {
      throw new Error("snapshot package source identity is invalid");
    }
    parsed = {
      planId: text(source.planId, "planId"),
      trusted: bool(source.trusted, "trusted"),
      workspaceId: text(source.workspaceId, "workspaceId"),
      sourceSnapshotId,
      snapshotCount: integer(source.snapshotCount, "snapshotCount"),
      encrypted: bool(source.encrypted, "encrypted"),
      verified,
      expiresAt: text(source.expiresAt, "expiresAt"),
    };
  } else if (method === "snapshot.import") {
    exact(source, [
      "operationId", "snapshotId", "sourceWorkspaceId",
      "sourceSnapshotId", "state",
    ], `${method} result`);
    if (source.state !== "restoreRequired") {
      throw new Error("snapshot import state is invalid");
    }
    parsed = {
      operationId: text(source.operationId, "operationId"),
      snapshotId: text(source.snapshotId, "snapshotId"),
      sourceWorkspaceId: text(
        source.sourceWorkspaceId,
        "sourceWorkspaceId",
      ),
      sourceSnapshotId: text(
        source.sourceSnapshotId,
        "sourceSnapshotId",
      ),
      state: "restoreRequired",
    };
  } else if (method === "history.query") {
    parsed = parseHistoryPage(source);
  } else if (method === "history.previewRestore") {
    parsed = parseHistoryRestorePreview(source);
  } else if (method === "history.applyRestore") {
    exact(source, [
      "collection", "itemId", "restoredToRevision", "newRevisionId",
      "item", "mutationRevision",
    ], `${method} result`);
    parsed = {
      collection: text(source.collection, "history collection"),
      itemId: text(source.itemId, "history itemId"),
      restoredToRevision: text(
        source.restoredToRevision,
        "restoredToRevision",
      ),
      newRevisionId: nullableText(source.newRevisionId, "newRevisionId"),
      item: { ...object(source.item, "restored item") },
      mutationRevision: integer(
        source.mutationRevision,
        "mutationRevision",
        1,
      ),
    };
  } else if (
    method === "fileHistory.import"
    || method === "fileHistory.unlink"
    || method === "fileHistory.relink"
  ) {
    parsed = parseFileDocumentV2(source);
  } else if (method === "fileHistory.listDocuments") {
    exact(source, ["documents"], `${method} result`);
    if (!Array.isArray(source.documents)) {
      throw new Error("file documents must be an array");
    }
    parsed = { documents: source.documents.map(parseFileDocumentV2) };
  } else if (method === "fileHistory.listPendingChanges") {
    exact(source, ["changes"], `${method} result`);
    if (!Array.isArray(source.changes)) {
      throw new Error("pending file changes must be an array");
    }
    parsed = {
      changes: source.changes.map((item) => {
        const change = object(item, "pending file change");
        exact(change, [
          "changeId", "relativePath", "missing", "observedHash",
          "observedSize", "reason", "candidateDocumentIds",
          "createdAt", "updatedAt",
        ], "pending file change");
        return {
          changeId: text(change.changeId, "changeId"),
          relativePath: text(change.relativePath, "relativePath"),
          missing: bool(change.missing, "missing"),
          observedHash: text(change.observedHash, "observedHash"),
          observedSize: integer(change.observedSize, "observedSize"),
          reason: text(change.reason, "reason"),
          candidateDocumentIds: stringList(
            change.candidateDocumentIds,
            "candidateDocumentIds",
          ),
          createdAt: text(change.createdAt, "createdAt"),
          updatedAt: text(change.updatedAt, "updatedAt"),
        };
      }),
    };
  } else if (method === "fileHistory.applyPendingChange") {
    exact(source, ["changeId", "state", "document"], `${method} result`);
    parsed = {
      changeId: text(source.changeId, "changeId"),
      state: oneOf(source.state, ["applied", "dismissed"], "pending file change state"),
      document: source.document === null ? null : parseFileDocumentV2(source.document),
    };
  } else if (method === "fileHistory.readTree") {
    parsed = parseFileTree(source);
  } else if (["fileHistory.restore", "fileHistory.upgrade"].includes(method)) {
    exact(
      source,
      ["revisionId", "revisionOrdinal", "localSequence", "formalVersion"],
      `${method} result`,
    );
    const revisionOrdinal = integer(source.revisionOrdinal, "revisionOrdinal");
    const localSequence = source.localSequence === null
      ? null
      : integer(source.localSequence, "localSequence", 1);
    const formalVersion = source.formalVersion === null
      ? null
      : integer(source.formalVersion, "formalVersion", 1);
    if (revisionOrdinal === 0 && localSequence === null) {
      throw new Error(`${method} provisional result requires localSequence`);
    }
    if (revisionOrdinal === 0 && formalVersion !== null) {
      throw new Error(`${method} provisional result cannot allocate formalVersion`);
    }
    if (revisionOrdinal > 0 && formalVersion === null) {
      throw new Error(`${method} canonical result requires formalVersion`);
    }
    parsed = {
      revisionId: text(source.revisionId, "revisionId"),
      revisionOrdinal,
      localSequence,
      formalVersion,
    };
  } else if (method === "fileHistory.activateLeaf") {
    exact(source, ["revisionId", "effective"], `${method} result`);
    if (source.effective !== true) {
      throw new Error("activated file revision must be effective");
    }
    parsed = {
      revisionId: text(source.revisionId, "revisionId"),
      effective: true,
    };
  } else if (method === "retention.get" || method === "retention.update") {
    parsed = parseRetentionPolicyV2(source);
  } else if (method === "retention.status") {
    exact(source, [
      "repositoryUsageBytes",
      "repositoryLimitBytes",
      "automaticSnapshotsPaused",
      "warningCode",
      "integrityStatus",
      "integrityFailure",
      "lastIncrementalCheckAt",
      "lastFullCheckAt",
      "maintenanceFailure",
      "maintenanceFailureStage",
      "lastMaintenanceFailureAt",
    ], `${method} result`);
    const paused = bool(
      source.automaticSnapshotsPaused,
      "automaticSnapshotsPaused",
    );
    const warningCode = nullableText(source.warningCode, "warningCode");
    const integrityStatus = oneOf(
      source.integrityStatus,
      ["unknown", "verified", "corrupt"],
      "integrityStatus",
    );
    const integrityFailure = nullableText(
      source.integrityFailure,
      "integrityFailure",
    );
    const maintenanceFailure = nullableText(
      source.maintenanceFailure,
      "maintenanceFailure",
    );
    const maintenanceFailureStage = source.maintenanceFailureStage === null
      ? null
      : oneOf(
        source.maintenanceFailureStage,
        ["integrity", "sweep"] as const,
        "maintenanceFailureStage",
      );
    const lastMaintenanceFailureAt = nullableText(
      source.lastMaintenanceFailureAt,
      "lastMaintenanceFailureAt",
    );
    if (paused !== (warningCode !== null)
      || (integrityStatus === "corrupt") !== (integrityFailure !== null)
      || (maintenanceFailure !== null) !== (maintenanceFailureStage !== null)
      || (maintenanceFailure !== null) !== (lastMaintenanceFailureAt !== null)) {
      throw new Error("retention status is inconsistent");
    }
    parsed = {
      repositoryUsageBytes: integer(
        source.repositoryUsageBytes,
        "repositoryUsageBytes",
      ),
      repositoryLimitBytes: source.repositoryLimitBytes === null
        ? null
        : integer(source.repositoryLimitBytes, "repositoryLimitBytes", 1),
      automaticSnapshotsPaused: paused,
      warningCode,
      integrityStatus,
      integrityFailure,
      lastIncrementalCheckAt: nullableText(
        source.lastIncrementalCheckAt,
        "lastIncrementalCheckAt",
      ),
      lastFullCheckAt: nullableText(
        source.lastFullCheckAt,
        "lastFullCheckAt",
      ),
      maintenanceFailure,
      maintenanceFailureStage,
      lastMaintenanceFailureAt,
    };
  } else if (method === "retention.plan") {
    exact(source, ["planId", "reclaimableBytes", "blockedReasons"], `${method} result`);
    parsed = {
      planId: text(source.planId, "planId"),
      reclaimableBytes: integer(source.reclaimableBytes, "reclaimableBytes"),
      blockedReasons: stringList(source.blockedReasons, "blockedReasons"),
    };
  } else if (method === "retention.apply") {
    exact(source, ["deletedObjects", "reclaimedBytes"], `${method} result`);
    parsed = {
      deletedObjects: integer(source.deletedObjects, "deletedObjects"),
      reclaimedBytes: integer(source.reclaimedBytes, "reclaimedBytes"),
    };
  } else if (method === "replica.status") {
    exact(source, ["coordinationStrength", "syncState", "pendingSync"], `${method} result`);
    parsed = {
      coordinationStrength: oneOf(source.coordinationStrength, ["strong", "advisory"], "coordinationStrength"),
      syncState: oneOf(source.syncState, ["localOnly", "pending", "syncing", "replicated", "failed"], "syncState"),
      pendingSync: bool(source.pendingSync, "pendingSync"),
    };
  } else if (method === "replica.forceTakeover") {
    exact(source, ["fenceEpoch", "claimId", "mode"], `${method} result`);
    parsed = {
      fenceEpoch: integer(source.fenceEpoch, "fenceEpoch", 1),
      claimId: text(source.claimId, "claimId"),
      mode: oneOf(source.mode, ["provisional"], "lease mode"),
    };
  } else if (method === "conflict.list") {
    exact(source, ["conflicts", "nextCursor"], `${method} result`);
    if (!Array.isArray(source.conflicts)) throw new Error("conflicts must be an array");
    parsed = {
      conflicts: source.conflicts.map(parseConflictSummary),
      nextCursor: nullableText(source.nextCursor, "nextCursor"),
    };
  } else if (method === "conflict.inspect") {
    exact(source, ["conflictId", "state", "items"], `${method} result`);
    if (!Array.isArray(source.items)) throw new Error("conflict items must be an array");
    parsed = {
      conflictId: text(source.conflictId, "conflictId"),
      state: oneOf(source.state, ["pending", "validating", "ready", "failed"], "conflict state"),
      items: source.items.map(parseConflict),
    };
  } else if (method === "conflict.preview") {
    exact(source, ["planId", "diagnostics", "valid"], `${method} result`);
    parsed = {
      planId: text(source.planId, "planId"),
      diagnostics: stringList(source.diagnostics, "diagnostics"),
      valid: bool(source.valid, "valid"),
    };
  } else if (method === "conflict.apply") {
    exact(source, ["operationId", "state", "recoverySnapshotIds"], `${method} result`);
    if (source.state !== "applied") throw new Error("conflict resolution state is invalid");
    parsed = {
      operationId: text(source.operationId, "operationId"),
      state: "applied",
      recoverySnapshotIds: stringList(source.recoverySnapshotIds, "recoverySnapshotIds"),
    };
  } else if (method === "snapshot.request") {
    exact(
      source,
      ["operationId", "state", "snapshotId", "mutationRevision"],
      `${method} result`,
    );
    parsed = {
      operationId: text(source.operationId, "operationId"),
      state: text(source.state, "operation state"),
      snapshotId: text(source.snapshotId, "snapshotId"),
      mutationRevision: integer(source.mutationRevision, "mutationRevision", 1),
    };
  } else {
    exact(source, ["operationId", "state"], `${method} result`);
    parsed = {
      operationId: text(source.operationId, "operationId"),
      state: text(source.state, "operation state"),
    };
  }
  return parsed as WorkspaceV2RpcResultMap[M];
}

export function parseWorkspaceV2Reply(value: unknown): WorkspaceV2Reply {
  const source = object(value, "workspace v2 reply");
  exact(source, ["method", "wire", "ok", "result", "error"], "workspace v2 reply");
  const method = oneOf(source.method, WORKSPACE_V2_RPC_METHODS, "workspace v2 method");
  const wire = parseWire(source.wire);
  const ok = bool(source.ok, "workspace v2 reply ok");
  if (ok) {
    if (source.error !== null) throw new Error("successful workspace v2 reply cannot contain an error");
    return {
      method,
      wire,
      ok: true,
      result: parseResult(method, source.result),
      error: null,
    } as WorkspaceV2Reply;
  }
  if (source.result !== null) throw new Error("failed workspace v2 reply cannot contain a result");
  const error = object(source.error, "workspace v2 failure");
  exact(error, ["code", "message", "retryable"], "workspace v2 failure");
  return {
    method,
    wire,
    ok: false,
    result: null,
    error: {
      code: text(error.code, "error code"),
      message: text(error.message, "error message"),
      retryable: bool(error.retryable, "error retryable"),
    },
  } as WorkspaceV2Reply;
}
