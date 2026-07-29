const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const OBJECT_ID_PATTERN = /^obj_[0-9a-f]{64}$/;
const SHA256_PATTERN = /^sha256:[0-9a-f]{64}$/;

type JsonRecord = Record<string, unknown>;

function record(value: unknown, label: string): JsonRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value as JsonRecord;
}

function exact(value: JsonRecord, keys: readonly string[], label: string): void {
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
    throw new Error(`${label} has unknown or missing fields`);
  }
}

function string(value: unknown, label: string): string {
  if (typeof value !== "string" || value.length === 0) throw new Error(`${label} is invalid`);
  return value;
}

function uuid(value: unknown, label: string): string {
  const result = string(value, label);
  if (!UUID_PATTERN.test(result)) throw new Error(`${label} must be a UUID`);
  return result;
}

function integer(value: unknown, label: string, minimum = 0): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum) {
    throw new Error(`${label} must be an integer >= ${minimum}`);
  }
  return value as number;
}

function boolean(value: unknown, label: string): boolean {
  if (typeof value !== "boolean") throw new Error(`${label} must be boolean`);
  return value;
}

function nullableString(value: unknown, label: string): string | null {
  return value === null ? null : string(value, label);
}

function nullableUuid(value: unknown, label: string): string | null {
  return value === null ? null : uuid(value, label);
}

function oneOf<T extends string>(value: unknown, allowed: readonly T[], label: string): T {
  const result = string(value, label);
  if (!(allowed as readonly string[]).includes(result)) throw new Error(`${label} is invalid`);
  return result as T;
}

function contractVersion(value: unknown): "2.0" {
  if (value !== "2.0") throw new Error("contractVersion must be 2.0");
  return "2.0";
}

function stringArray(value: unknown, label: string): string[] {
  if (!Array.isArray(value)) throw new Error(`${label} must be an array`);
  return value.map((item, index) => string(item, `${label}[${index}]`));
}

export type WorkspaceStorageMode = "direct" | "mirrored";
export type WorkspaceEncryptionMode = "none" | "convenient" | "protected";
export type WorkspaceStorageKind =
  | "fixed"
  | "network"
  | "removable"
  | "registeredCloud"
  | "userMarkedSync";
export type CoordinationStrength = "strong" | "advisory";

export interface WorkspaceWireScope {
  readonly scope: "workspace";
  readonly workspaceId: string;
  readonly sessionEpoch: number;
  readonly operationId: string;
  readonly sequence: number;
}

export interface GlobalWireScope {
  readonly scope: "global";
  readonly operationId: string;
  readonly sequence: number;
}

export type WireScopeV2 = GlobalWireScope | WorkspaceWireScope;

export function parseGlobalWireScope(value: unknown): GlobalWireScope {
  const source = record(value, "global wire");
  exact(source, ["scope", "operationId", "sequence"], "global wire");
  if (source.scope !== "global") throw new Error("global wire scope is invalid");
  return {
    scope: "global",
    operationId: uuid(source.operationId, "operationId"),
    sequence: integer(source.sequence, "sequence"),
  };
}

export function parseWorkspaceWireScope(value: unknown): WorkspaceWireScope {
  const source = record(value, "workspace wire");
  exact(source, ["scope", "workspaceId", "sessionEpoch", "operationId", "sequence"], "workspace wire");
  if (source.scope !== "workspace") throw new Error("workspace wire scope is invalid");
  return {
    scope: "workspace",
    workspaceId: uuid(source.workspaceId, "workspaceId"),
    sessionEpoch: integer(source.sessionEpoch, "sessionEpoch", 1),
    operationId: uuid(source.operationId, "operationId"),
    sequence: integer(source.sequence, "sequence"),
  };
}

function parseWireScopeV2(value: unknown): WireScopeV2 {
  const source = record(value, "wire");
  return source.scope === "global"
    ? parseGlobalWireScope(source)
    : parseWorkspaceWireScope(source);
}

export function ensureCurrentWorkspaceScope(
  scope: WorkspaceWireScope,
  workspaceId: string,
  sessionEpoch: number,
  minimumSequence = 0,
): void {
  if (scope.workspaceId !== workspaceId) throw new Error("workspace.workspace_mismatch");
  if (scope.sessionEpoch !== sessionEpoch) throw new Error("workspace.session_epoch_stale");
  if (scope.sequence < minimumSequence) throw new Error("workspace.sequence_stale");
}

export interface WorkspaceManifestV2 {
  readonly contractVersion: "2.0";
  readonly formatVersion: number;
  readonly workspaceId: string;
  readonly displayName: string;
  readonly createdAt: string;
  readonly storageMode: WorkspaceStorageMode;
  readonly encryptionMode: WorkspaceEncryptionMode;
  readonly repositoryFormat: string;
  readonly topologySchemaVersion: number;
  readonly businessSchemaVersion: number;
  readonly importedFromWorkspaceId: string | null;
  readonly sourceSnapshotId: string | null;
}

export function parseWorkspaceManifestV2(value: unknown): WorkspaceManifestV2 {
  const source = record(value, "workspace manifest");
  exact(source, [
    "contractVersion", "formatVersion", "workspaceId", "displayName", "createdAt",
    "storageMode", "encryptionMode", "repositoryFormat", "topologySchemaVersion",
    "businessSchemaVersion", "importedFromWorkspaceId", "sourceSnapshotId",
  ], "workspace manifest");
  return {
    contractVersion: contractVersion(source.contractVersion),
    formatVersion: integer(source.formatVersion, "formatVersion", 1),
    workspaceId: uuid(source.workspaceId, "workspaceId"),
    displayName: string(source.displayName, "displayName"),
    createdAt: string(source.createdAt, "createdAt"),
    storageMode: oneOf(source.storageMode, ["direct", "mirrored"], "storageMode"),
    encryptionMode: oneOf(source.encryptionMode, ["none", "convenient", "protected"], "encryptionMode"),
    repositoryFormat: string(source.repositoryFormat, "repositoryFormat"),
    topologySchemaVersion: integer(source.topologySchemaVersion, "topologySchemaVersion", 1),
    businessSchemaVersion: integer(source.businessSchemaVersion, "businessSchemaVersion", 1),
    importedFromWorkspaceId: nullableUuid(source.importedFromWorkspaceId, "importedFromWorkspaceId"),
    sourceSnapshotId: nullableUuid(source.sourceSnapshotId, "sourceSnapshotId"),
  };
}

export interface WorkspaceRegistryEntryV2 {
  readonly contractVersion: "2.0";
  readonly workspaceId: string;
  readonly displayName: string;
  readonly selectedRoot: string;
  readonly activityRoot: string | null;
  readonly storageKind: WorkspaceStorageKind;
  readonly coordinationStrength: CoordinationStrength;
  readonly lastOpenedAt: string | null;
  readonly lastKnownHealth: "healthy" | "offline" | "degraded" | "corrupt" | "unknown";
  readonly lastSnapshotAt: string | null;
  readonly lastSyncAt: string | null;
  readonly pendingSync: boolean;
}

export function parseWorkspaceRegistryEntryV2(value: unknown): WorkspaceRegistryEntryV2 {
  const source = record(value, "workspace registry entry");
  exact(source, [
    "contractVersion", "workspaceId", "displayName", "selectedRoot", "activityRoot",
    "storageKind", "coordinationStrength", "lastOpenedAt", "lastKnownHealth",
    "lastSnapshotAt", "lastSyncAt", "pendingSync",
  ], "workspace registry entry");
  return {
    contractVersion: contractVersion(source.contractVersion),
    workspaceId: uuid(source.workspaceId, "workspaceId"),
    displayName: string(source.displayName, "displayName"),
    selectedRoot: string(source.selectedRoot, "selectedRoot"),
    activityRoot: nullableString(source.activityRoot, "activityRoot"),
    storageKind: oneOf(source.storageKind, ["fixed", "network", "removable", "registeredCloud", "userMarkedSync"], "storageKind"),
    coordinationStrength: oneOf(source.coordinationStrength, ["strong", "advisory"], "coordinationStrength"),
    lastOpenedAt: nullableString(source.lastOpenedAt, "lastOpenedAt"),
    lastKnownHealth: oneOf(source.lastKnownHealth, ["healthy", "offline", "degraded", "corrupt", "unknown"], "lastKnownHealth"),
    lastSnapshotAt: nullableString(source.lastSnapshotAt, "lastSnapshotAt"),
    lastSyncAt: nullableString(source.lastSyncAt, "lastSyncAt"),
    pendingSync: boolean(source.pendingSync, "pendingSync"),
  };
}

export interface WorkspaceSessionV2 {
  readonly contractVersion: "2.0";
  readonly workspaceId: string | null;
  readonly sessionEpoch: number;
  readonly state:
    | "closed" | "opening" | "openedReadOnly" | "openedWritable"
    | "openedProvisional" | "switching" | "failed";
  readonly openMode: "readOnly" | "writable" | "provisional";
  readonly writable: boolean;
  readonly provisional: boolean;
  readonly phase:
    | "idle" | "protecting" | "draining" | "stopping" | "starting"
    | "binding" | "verifying" | "rollingBack";
  readonly errorCode: string | null;
}

export function parseWorkspaceSessionV2(value: unknown): WorkspaceSessionV2 {
  const source = record(value, "workspace session");
  exact(source, [
    "contractVersion", "workspaceId", "sessionEpoch", "state", "openMode",
    "writable", "provisional", "phase", "errorCode",
  ], "workspace session");
  const result: WorkspaceSessionV2 = {
    contractVersion: contractVersion(source.contractVersion),
    workspaceId: nullableUuid(source.workspaceId, "workspaceId"),
    sessionEpoch: integer(source.sessionEpoch, "sessionEpoch"),
    state: oneOf(source.state, ["closed", "opening", "openedReadOnly", "openedWritable", "openedProvisional", "switching", "failed"], "state"),
    openMode: oneOf(source.openMode, ["readOnly", "writable", "provisional"], "openMode"),
    writable: boolean(source.writable, "writable"),
    provisional: boolean(source.provisional, "provisional"),
    phase: oneOf(source.phase, ["idle", "protecting", "draining", "stopping", "starting", "binding", "verifying", "rollingBack"], "phase"),
    errorCode: nullableString(source.errorCode, "errorCode"),
  };
  if (result.state === "closed") {
    if (result.workspaceId !== null || result.writable || result.provisional) {
      throw new Error("closed session cannot own a workspace");
    }
  } else if (result.workspaceId === null || result.sessionEpoch < 1) {
    throw new Error("open session requires workspace identity");
  }
  if (result.state === "openedWritable" && !result.writable) {
    throw new Error("openedWritable session must be writable");
  }
  if (result.state === "openedProvisional" && !result.provisional) {
    throw new Error("openedProvisional session must be provisional");
  }
  return result;
}

export interface FileDocumentV2 {
  readonly contractVersion: "2.0";
  readonly documentId: string;
  readonly workspaceId: string;
  readonly relativePath: string;
  readonly status: "active" | "deleted";
  readonly effectiveRevisionId: string | null;
  readonly nextRevisionOrdinal: number;
  readonly nextFormalVersion: number;
}

export function parseFileDocumentV2(value: unknown): FileDocumentV2 {
  const source = record(value, "file document");
  exact(source, [
    "contractVersion", "documentId", "workspaceId", "relativePath", "status",
    "effectiveRevisionId", "nextRevisionOrdinal", "nextFormalVersion",
  ], "file document");
  const relativePath = string(source.relativePath, "relativePath");
  const parts = relativePath.replaceAll("\\", "/").split("/");
  if (relativePath.startsWith("/") || parts.some((part) => part === "" || part === "..")) {
    throw new Error("file_history.path_invalid");
  }
  return {
    contractVersion: contractVersion(source.contractVersion),
    documentId: uuid(source.documentId, "documentId"),
    workspaceId: uuid(source.workspaceId, "workspaceId"),
    relativePath,
    status: oneOf(source.status, ["active", "deleted"], "status"),
    effectiveRevisionId: nullableUuid(source.effectiveRevisionId, "effectiveRevisionId"),
    nextRevisionOrdinal: integer(source.nextRevisionOrdinal, "nextRevisionOrdinal", 1),
    nextFormalVersion: integer(source.nextFormalVersion, "nextFormalVersion", 1),
  };
}

export interface FileRevisionV2 {
  readonly contractVersion: "2.0";
  readonly revisionId: string;
  readonly documentId: string;
  readonly parentRevisionId: string | null;
  readonly revisionOrdinal: number;
  readonly localSequence: number | null;
  readonly formalVersion: number | null;
  readonly kind: "autosave" | "formal" | "restore";
  readonly objectId: string;
  readonly contentHash: string;
  readonly size: number;
  readonly mimeType: string;
  readonly createdAt: string;
  readonly createdBy: string;
  readonly deviceId: string;
  readonly comment: string | null;
  readonly restoredFromRevisionId: string | null;
}

export function parseFileRevisionV2(value: unknown): FileRevisionV2 {
  const source = record(value, "file revision");
  exact(source, [
    "contractVersion", "revisionId", "documentId", "parentRevisionId",
    "revisionOrdinal", "localSequence", "formalVersion", "kind", "objectId", "contentHash", "size",
    "mimeType", "createdAt", "createdBy", "deviceId", "comment", "restoredFromRevisionId",
  ], "file revision");
  const kind = oneOf(source.kind, ["autosave", "formal", "restore"], "kind");
  const formalVersion = source.formalVersion === null
    ? null
    : integer(source.formalVersion, "formalVersion", 1);
  const revisionOrdinal = integer(source.revisionOrdinal, "revisionOrdinal");
  const localSequence = source.localSequence === null
    ? null
    : integer(source.localSequence, "localSequence", 1);
  const provisional = revisionOrdinal === 0;
  if (provisional && localSequence === null) {
    throw new Error("provisional revision requires localSequence");
  }
  if (provisional && formalVersion !== null) {
    throw new Error("provisional revision cannot consume a formal version");
  }
  const restoredFromRevisionId = nullableUuid(source.restoredFromRevisionId, "restoredFromRevisionId");
  if (kind === "autosave" && formalVersion !== null) {
    throw new Error("autosave cannot consume a formal version");
  }
  if (!provisional && kind !== "autosave" && formalVersion === null) {
    throw new Error("formal revision requires a formal version");
  }
  if ((kind === "restore") !== (restoredFromRevisionId !== null)) {
    throw new Error("restore provenance is invalid");
  }
  const objectId = string(source.objectId, "objectId");
  const contentHash = string(source.contentHash, "contentHash");
  if (!OBJECT_ID_PATTERN.test(objectId) || !SHA256_PATTERN.test(contentHash)) {
    throw new Error("file revision content identity is invalid");
  }
  return {
    contractVersion: contractVersion(source.contractVersion),
    revisionId: uuid(source.revisionId, "revisionId"),
    documentId: uuid(source.documentId, "documentId"),
    parentRevisionId: nullableUuid(source.parentRevisionId, "parentRevisionId"),
    revisionOrdinal,
    localSequence,
    formalVersion,
    kind,
    objectId,
    contentHash,
    size: integer(source.size, "size"),
    mimeType: string(source.mimeType, "mimeType"),
    createdAt: string(source.createdAt, "createdAt"),
    createdBy: string(source.createdBy, "createdBy"),
    deviceId: uuid(source.deviceId, "deviceId"),
    comment: nullableString(source.comment, "comment"),
    restoredFromRevisionId,
  };
}

export interface AuditAnchorV2 {
  readonly epoch: number;
  readonly sequence: number;
  readonly chainHash: string;
}

function parseAuditAnchorV2(value: unknown): AuditAnchorV2 {
  const source = record(value, "audit anchor");
  exact(source, ["epoch", "sequence", "chainHash"], "audit anchor");
  const chainHash = string(source.chainHash, "chainHash");
  if (!SHA256_PATTERN.test(chainHash)) throw new Error("audit anchor hash is invalid");
  return {
    epoch: integer(source.epoch, "epoch", 1),
    sequence: integer(source.sequence, "sequence"),
    chainHash,
  };
}

export interface SnapshotManifestV2 {
  readonly contractVersion: "2.0";
  readonly snapshotId: string;
  readonly workspaceId: string;
  readonly fenceEpoch: number;
  readonly claimId: string;
  readonly mutationRevision: number;
  readonly snapshotSequence: number;
  readonly trigger: "automatic" | "manual" | "protection" | "import" | "restore";
  readonly createdAt: string;
  readonly createdByDevice: string;
  readonly businessDatabaseObjectId: string;
  readonly topologyRootObjectId: string;
  readonly fileStateRootObjectId: string;
  readonly workspaceSettingsObjectId: string;
  readonly auditAnchor: AuditAnchorV2;
  readonly auditPrefixObjectId: string;
  readonly sourceSnapshotId: string | null;
  readonly formatVersion: number;
  readonly minimumAppVersion: string;
}

export function parseSnapshotManifestV2(value: unknown): SnapshotManifestV2 {
  const source = record(value, "snapshot manifest");
  exact(source, [
    "contractVersion", "snapshotId", "workspaceId", "fenceEpoch", "claimId",
    "mutationRevision", "snapshotSequence", "trigger", "createdAt", "createdByDevice",
    "businessDatabaseObjectId", "topologyRootObjectId", "fileStateRootObjectId",
    "workspaceSettingsObjectId", "auditAnchor", "auditPrefixObjectId",
    "sourceSnapshotId", "formatVersion", "minimumAppVersion",
  ], "snapshot manifest");
  const objectIds = [
    source.businessDatabaseObjectId,
    source.topologyRootObjectId,
    source.fileStateRootObjectId,
    source.workspaceSettingsObjectId,
    source.auditPrefixObjectId,
  ].map((item, index) => string(item, `objectIds[${index}]`));
  if (objectIds.some((item) => !OBJECT_ID_PATTERN.test(item))) {
    throw new Error("snapshot object ID is invalid");
  }
  return {
    contractVersion: contractVersion(source.contractVersion),
    snapshotId: uuid(source.snapshotId, "snapshotId"),
    workspaceId: uuid(source.workspaceId, "workspaceId"),
    fenceEpoch: integer(source.fenceEpoch, "fenceEpoch", 1),
    claimId: uuid(source.claimId, "claimId"),
    mutationRevision: integer(source.mutationRevision, "mutationRevision"),
    snapshotSequence: integer(source.snapshotSequence, "snapshotSequence", 1),
    trigger: oneOf(source.trigger, ["automatic", "manual", "protection", "import", "restore"], "trigger"),
    createdAt: string(source.createdAt, "createdAt"),
    createdByDevice: uuid(source.createdByDevice, "createdByDevice"),
    businessDatabaseObjectId: objectIds[0]!,
    topologyRootObjectId: objectIds[1]!,
    fileStateRootObjectId: objectIds[2]!,
    workspaceSettingsObjectId: objectIds[3]!,
    auditAnchor: parseAuditAnchorV2(source.auditAnchor),
    auditPrefixObjectId: objectIds[4]!,
    sourceSnapshotId: nullableUuid(source.sourceSnapshotId, "sourceSnapshotId"),
    formatVersion: integer(source.formatVersion, "formatVersion", 1),
    minimumAppVersion: string(source.minimumAppVersion, "minimumAppVersion"),
  };
}

export interface SnapshotSealV2 {
  readonly contractVersion: "2.0";
  readonly snapshotId: string;
  readonly manifestHash: string;
  readonly databaseHash: string;
  readonly fileStateRootHash: string;
  readonly auditAnchorHash: string;
  readonly repositoryFormat: string;
  readonly fenceEpoch: number;
  readonly claimId: string;
  readonly mutationRevision: number;
  readonly snapshotSequence: number;
  readonly verified: true;
}

export function parseSnapshotSealV2(value: unknown): SnapshotSealV2 {
  const source = record(value, "snapshot seal");
  exact(source, [
    "contractVersion", "snapshotId", "manifestHash", "databaseHash",
    "fileStateRootHash", "auditAnchorHash", "repositoryFormat", "fenceEpoch",
    "claimId", "mutationRevision", "snapshotSequence", "verified",
  ], "snapshot seal");
  const hashes = [
    source.manifestHash, source.databaseHash, source.fileStateRootHash, source.auditAnchorHash,
  ].map((item, index) => string(item, `hashes[${index}]`));
  if (hashes.some((item) => !SHA256_PATTERN.test(item)) || source.verified !== true) {
    throw new Error("snapshot seal is not verified");
  }
  return {
    contractVersion: contractVersion(source.contractVersion),
    snapshotId: uuid(source.snapshotId, "snapshotId"),
    manifestHash: hashes[0]!,
    databaseHash: hashes[1]!,
    fileStateRootHash: hashes[2]!,
    auditAnchorHash: hashes[3]!,
    repositoryFormat: string(source.repositoryFormat, "repositoryFormat"),
    fenceEpoch: integer(source.fenceEpoch, "fenceEpoch", 1),
    claimId: uuid(source.claimId, "claimId"),
    mutationRevision: integer(source.mutationRevision, "mutationRevision"),
    snapshotSequence: integer(source.snapshotSequence, "snapshotSequence", 1),
    verified: true,
  };
}

export interface SnapshotCatalogEntryV2 {
  readonly contractVersion: "2.0";
  readonly snapshotId: string;
  readonly state:
    | "queued" | "barrier" | "captured" | "chunking" | "verifying" | "published"
    | "syncing" | "ready" | "failed" | "corrupt" | "repairing";
  readonly pinned: boolean;
  readonly retentionReasons: readonly string[];
  readonly integrity: "pending" | "verified" | "corrupt" | "repairing";
  readonly syncState: "localOnly" | "pending" | "syncing" | "replicated" | "failed";
  readonly logicalSize: number;
  readonly physicalSize: number;
  readonly note: string | null;
  readonly catalogRevision: number;
}

export function parseSnapshotCatalogEntryV2(value: unknown): SnapshotCatalogEntryV2 {
  const source = record(value, "snapshot catalog entry");
  exact(source, [
    "contractVersion", "snapshotId", "state", "pinned", "retentionReasons",
    "integrity", "syncState", "logicalSize", "physicalSize", "note", "catalogRevision",
  ], "snapshot catalog entry");
  return {
    contractVersion: contractVersion(source.contractVersion),
    snapshotId: uuid(source.snapshotId, "snapshotId"),
    state: oneOf(source.state, ["queued", "barrier", "captured", "chunking", "verifying", "published", "syncing", "ready", "failed", "corrupt", "repairing"], "state"),
    pinned: boolean(source.pinned, "pinned"),
    retentionReasons: stringArray(source.retentionReasons, "retentionReasons"),
    integrity: oneOf(source.integrity, ["pending", "verified", "corrupt", "repairing"], "integrity"),
    syncState: oneOf(source.syncState, ["localOnly", "pending", "syncing", "replicated", "failed"], "syncState"),
    logicalSize: integer(source.logicalSize, "logicalSize"),
    physicalSize: integer(source.physicalSize, "physicalSize"),
    note: nullableString(source.note, "note"),
    catalogRevision: integer(source.catalogRevision, "catalogRevision", 1),
  };
}

export interface LeaseClaimV2 {
  readonly contractVersion: "2.0";
  readonly workspaceId: string;
  readonly fenceEpoch: number;
  readonly claimId: string;
  readonly deviceId: string;
  readonly issuedAt: string;
  readonly heartbeatAt: string;
  readonly expiresAt: string;
  readonly mode: "writable" | "provisional";
  readonly previousClaimId: string | null;
  readonly coordinationStrength: CoordinationStrength;
}

export function parseLeaseClaimV2(value: unknown): LeaseClaimV2 {
  const source = record(value, "lease claim");
  exact(source, [
    "contractVersion", "workspaceId", "fenceEpoch", "claimId", "deviceId",
    "issuedAt", "heartbeatAt", "expiresAt", "mode", "previousClaimId",
    "coordinationStrength",
  ], "lease claim");
  const issuedAt = string(source.issuedAt, "issuedAt");
  const heartbeatAt = string(source.heartbeatAt, "heartbeatAt");
  const expiresAt = string(source.expiresAt, "expiresAt");
  if (!(Date.parse(issuedAt) <= Date.parse(heartbeatAt) && Date.parse(heartbeatAt) < Date.parse(expiresAt))) {
    throw new Error("lease timestamps are invalid");
  }
  return {
    contractVersion: contractVersion(source.contractVersion),
    workspaceId: uuid(source.workspaceId, "workspaceId"),
    fenceEpoch: integer(source.fenceEpoch, "fenceEpoch", 1),
    claimId: uuid(source.claimId, "claimId"),
    deviceId: uuid(source.deviceId, "deviceId"),
    issuedAt,
    heartbeatAt,
    expiresAt,
    mode: oneOf(source.mode, ["writable", "provisional"], "mode"),
    previousClaimId: nullableUuid(source.previousClaimId, "previousClaimId"),
    coordinationStrength: oneOf(source.coordinationStrength, ["strong", "advisory"], "coordinationStrength"),
  };
}

export interface RetentionPolicyV2 {
  readonly contractVersion: "2.0";
  readonly policyRevision: number;
  readonly snapshotDays: number;
  readonly snapshotCount: number;
  readonly snapshotBuckets: readonly ("hourly" | "daily" | "weekly" | "monthly")[];
  readonly fileRevisionDays: number;
  readonly fileRevisionCount: number;
  readonly fileRevisionBuckets: readonly ("hourly" | "daily" | "weekly" | "monthly")[];
  readonly trashMonths: 3;
  readonly repositoryLimitBytes: number | null;
}

export function parseRetentionPolicyV2(value: unknown): RetentionPolicyV2 {
  const source = record(value, "retention policy");
  exact(source, [
    "contractVersion", "policyRevision", "snapshotDays", "snapshotCount",
    "snapshotBuckets", "fileRevisionDays", "fileRevisionCount",
    "fileRevisionBuckets", "trashMonths", "repositoryLimitBytes",
  ], "retention policy");
  const buckets = (
    value: unknown,
    label: string,
  ): Array<"hourly" | "daily" | "weekly" | "monthly"> => {
    if (!Array.isArray(value)) throw new Error(`${label} must be an array`);
    const result = value.map((item) => oneOf(
      item,
      ["hourly", "daily", "weekly", "monthly"] as const,
      label,
    ));
    if (new Set(result).size !== result.length) throw new Error(`${label} contains duplicates`);
    return result;
  };
  if (source.trashMonths !== 3) throw new Error("trashMonths must be 3");
  return {
    contractVersion: contractVersion(source.contractVersion),
    policyRevision: integer(source.policyRevision, "policyRevision", 1),
    snapshotDays: integer(source.snapshotDays, "snapshotDays", 1),
    snapshotCount: integer(source.snapshotCount, "snapshotCount", 1),
    snapshotBuckets: buckets(source.snapshotBuckets, "snapshotBuckets"),
    fileRevisionDays: integer(source.fileRevisionDays, "fileRevisionDays", 1),
    fileRevisionCount: integer(source.fileRevisionCount, "fileRevisionCount", 1),
    fileRevisionBuckets: buckets(source.fileRevisionBuckets, "fileRevisionBuckets"),
    trashMonths: 3,
    repositoryLimitBytes: source.repositoryLimitBytes === null
      ? null
      : integer(source.repositoryLimitBytes, "repositoryLimitBytes", 1),
  };
}

export interface WorkspaceEventV2 {
  readonly contractVersion: "2.0";
  readonly topic:
    | "workspace.session.changed"
    | "snapshot.changed"
    | "replica.changed"
    | "lease.changed"
    | "conflict.changed";
  readonly wire: WorkspaceWireScope;
  readonly payloadModel: string;
  readonly payloadSchema: Readonly<Record<string, unknown>>;
  readonly payload: Readonly<Record<string, unknown>>;
}

export function parseWorkspaceEventV2(value: unknown): WorkspaceEventV2 {
  const source = record(value, "workspace event");
  exact(source, ["contractVersion", "topic", "wire", "payloadModel", "payloadSchema", "payload"], "workspace event");
  const topic = oneOf(source.topic, ["workspace.session.changed", "snapshot.changed", "replica.changed", "lease.changed", "conflict.changed"], "topic");
  const expected = {
    "workspace.session.changed": ["WorkspaceSessionChangedEvent", ["state", "phase"]],
    "snapshot.changed": ["SnapshotChangedEvent", ["snapshotId", "state", "integrity"]],
    "replica.changed": ["ReplicaChangedEvent", ["syncState", "pendingSync"]],
    "lease.changed": ["LeaseChangedEvent", ["mode", "coordinationStrength"]],
    "conflict.changed": ["ConflictChangedEvent", ["conflictId", "state"]],
  } as const;
  const [model, keys] = expected[topic];
  const payload = record(source.payload, "payload");
  const payloadSchema = parseClosedSchema(source.payloadSchema, "event payload schema");
  const required = payloadSchema.required;
  if (
    payloadSchema.type !== "object" ||
    payloadSchema.additionalProperties !== false ||
    !Array.isArray(required) ||
    new Set(required).size !== keys.length ||
    keys.some((key) => !required.includes(key))
  ) throw new Error("workspace.event_schema_invalid");
  if (source.payloadModel !== model) throw new Error("workspace.event_payload_invalid");
  validateClosedPayload(payload, payloadSchema, "event payload");
  return {
    contractVersion: contractVersion(source.contractVersion),
    topic,
    wire: parseWorkspaceWireScope(source.wire),
    payloadModel: model,
    payloadSchema,
    payload,
  };
}

export interface RpcGoldenCaseV2 {
  readonly method: string;
  readonly scope: "global" | "workspace";
  readonly paramsModel: string;
  readonly resultModel: string;
  readonly paramsSchema: Readonly<JsonRecord>;
  readonly resultSchema: Readonly<JsonRecord>;
  readonly request: Readonly<JsonRecord>;
  readonly success: Readonly<JsonRecord>;
  readonly error: Readonly<JsonRecord>;
}

export interface RpcContractCatalogV2 {
  readonly contractVersion: "2.0";
  readonly rpcMethods: readonly string[];
  readonly eventTopics: readonly WorkspaceEventV2["topic"][];
  readonly rpcCases: readonly RpcGoldenCaseV2[];
  readonly eventCases: readonly WorkspaceEventV2[];
}

function parseClosedSchema(value: unknown, label: string): JsonRecord {
  const schema = parsePropertySchema(value, label);
  if (
    schema.type !== "object"
    || schema.additionalProperties !== false
    || !Array.isArray(schema.required)
  ) {
    throw new Error(`${label} must be a closed object schema`);
  }
  const properties = record(schema.properties, `${label}.properties`);
  const required = stringArray(schema.required, `${label}.required`);
  if (
    new Set(required).size !== required.length
    || required.length !== Object.keys(properties).length
    || required.some((key) => !(key in properties))
  ) {
    throw new Error(`${label} required fields are invalid`);
  }
  for (const [key, rawProperty] of Object.entries(properties)) {
    parsePropertySchema(rawProperty, `${label}.properties.${key}`);
  }
  return schema;
}

function parsePropertySchema(
  value: unknown,
  label: string,
  conditional = false,
): JsonRecord {
  const property = record(value, label);
  if (Object.keys(property).length === 0) return property;
  const allowed = [
    "type", "enum", "const", "minimum", "minLength", "pattern",
    "additionalProperties", "required", "properties", "items", "allOf",
    "oneOf",
  ];
  if (Object.keys(property).some((key) => !allowed.includes(key))) {
    throw new Error(`${label} has unknown or missing fields`);
  }
  const types = "type" in property
    ? schemaPropertyTypes(property.type, `${label}.type`)
    : [];
  if (
    types.length === 0
    && !["const", "properties", "allOf", "oneOf"].some((key) => key in property)
  ) throw new Error(`${label}.type is invalid`);
  if ("enum" in property) {
    if (
      !Array.isArray(property.enum)
      || property.enum.length === 0
      || new Set(property.enum).size !== property.enum.length
    ) {
      throw new Error(`${label}.enum is invalid`);
    }
    for (const candidate of property.enum) {
      const candidateType = candidate === null ? "null" : typeof candidate;
      if (!types.includes(candidateType)) {
        throw new Error(`${label}.enum is invalid`);
      }
    }
  }
  if ("minimum" in property && (
    typeof property.minimum !== "number"
    || !Number.isFinite(property.minimum)
  )) throw new Error(`${label}.minimum is invalid`);
  if ("minLength" in property && (
    !Number.isInteger(property.minLength)
    || (property.minLength as number) < 0
  )) throw new Error(`${label}.minLength is invalid`);
  if ("pattern" in property) {
    if (typeof property.pattern !== "string") {
      throw new Error(`${label}.pattern is invalid`);
    }
    try {
      new RegExp(property.pattern);
    } catch {
      throw new Error(`${label}.pattern is invalid`);
    }
  }
  if (
    "additionalProperties" in property
    && typeof property.additionalProperties !== "boolean"
  ) throw new Error(`${label}.additionalProperties is invalid`);
  if ("properties" in property) {
    const properties = record(property.properties, `${label}.properties`);
    for (const [key, child] of Object.entries(properties)) {
      parsePropertySchema(child, `${label}.properties.${key}`, conditional);
    }
  }
  if ("required" in property) {
    const required = stringArray(property.required, `${label}.required`);
    const properties = record(property.properties, `${label}.properties`);
    if (
      new Set(required).size !== required.length
      || required.some((key) => !(key in properties))
    ) throw new Error(`${label}.required fields are invalid`);
  }
  for (const keyword of ["allOf", "oneOf"] as const) {
    if (!(keyword in property)) continue;
    const branches = property[keyword];
    if (!Array.isArray(branches) || branches.length === 0) {
      throw new Error(`${label}.${keyword} is invalid`);
    }
    branches.forEach((branch, index) =>
      parsePropertySchema(branch, `${label}.${keyword}[${index}]`, true));
  }
  if (types.includes("object") && !conditional) {
    const properties = record(property.properties, `${label}.properties`);
    const required = stringArray(property.required, `${label}.required`);
    if (
      property.additionalProperties !== false
      || required.length !== Object.keys(properties).length
      || !required.every((key) => key in properties)
    ) throw new Error(`${label} must be a closed object schema`);
  }
  if (types.includes("array")) {
    if (!("items" in property)) throw new Error(`${label}.items is invalid`);
    parsePropertySchema(property.items, `${label}.items`);
  } else if ("items" in property) {
    throw new Error(`${label}.items is invalid`);
  }
  return property;
}

function schemaPropertyTypes(value: unknown, label: string): readonly string[] {
  const allowed = [
    "string", "integer", "number", "boolean", "null", "array", "object",
  ];
  if (typeof value === "string" && allowed.includes(value)) return [value];
  if (
    Array.isArray(value)
    && value.length === 2
    && new Set(value).size === 2
    && value.includes("null")
    && value.every((item) =>
      typeof item === "string"
      && ["string", "integer", "number", "boolean", "null"].includes(item))
  ) {
    return value as string[];
  }
  throw new Error(`${label} is invalid`);
}

function validateSchemaValue(
  value: unknown,
  schema: JsonRecord,
  label: string,
): void {
  if (Object.keys(schema).length === 0) return;
  if ("const" in schema && !Object.is(value, schema.const)) {
    throw new Error(`${label} does not match its const schema`);
  }
  if (Array.isArray(schema.enum) && !schema.enum.includes(value)) {
    throw new Error(`${label} does not match its enum schema`);
  }
  if ("type" in schema) {
    const types = schemaPropertyTypes(schema.type, `${label}.schema.type`);
    const valid = types.some((type) =>
      type === "string"
        ? typeof value === "string"
        : type === "integer"
          ? Number.isInteger(value)
          : type === "number"
            ? typeof value === "number" && Number.isFinite(value)
            : type === "boolean"
              ? typeof value === "boolean"
              : type === "null"
                ? value === null
                : type === "array"
                  ? Array.isArray(value)
                  : type === "object"
                    ? value !== null && typeof value === "object" && !Array.isArray(value)
                    : false);
    if (!valid) throw new Error(`${label} has invalid type`);
  }
  if (
    typeof schema.minimum === "number"
    && typeof value === "number"
    && value < schema.minimum
  ) throw new Error(`${label} is below its minimum`);
  if (
    typeof schema.minLength === "number"
    && typeof value === "string"
    && value.length < schema.minLength
  ) throw new Error(`${label} is shorter than its minimum length`);
  if (
    typeof schema.pattern === "string"
    && typeof value === "string"
    && !new RegExp(schema.pattern).test(value)
  ) throw new Error(`${label} does not match its pattern`);
  if (
    (schema.type === "object" || "properties" in schema)
    && value !== null
    && typeof value === "object"
    && !Array.isArray(value)
  ) validateObjectPayload(value, schema, label);
  if (schema.type === "array" && Array.isArray(value)) {
    const itemSchema = record(schema.items, `${label}.schema.items`);
    value.forEach((item, index) =>
      validateSchemaValue(item, itemSchema, `${label}[${index}]`));
  }
  if (Array.isArray(schema.allOf)) {
    schema.allOf.forEach((branch, index) =>
      validateSchemaValue(
        value,
        record(branch, `${label}.schema.allOf[${index}]`),
        label,
      ));
  }
  if (Array.isArray(schema.oneOf)) {
    const matches = schema.oneOf.filter((branch, index) => {
      try {
        validateSchemaValue(
          value,
          record(branch, `${label}.schema.oneOf[${index}]`),
          label,
        );
        return true;
      } catch {
        return false;
      }
    }).length;
    if (matches !== 1) {
      throw new Error(`${label} does not match exactly one schema`);
    }
  }
}

function validateObjectPayload(
  value: unknown,
  schema: JsonRecord,
  label: string,
): JsonRecord {
  const payload = record(value, label);
  const properties = "properties" in schema
    ? record(schema.properties, `${label}.schema.properties`)
    : {};
  const required = "required" in schema
    ? stringArray(schema.required, `${label}.schema.required`)
    : [];
  const allowed = Object.keys(properties);
  if (
    (schema.additionalProperties === false
      && Object.keys(payload).some((key) => !allowed.includes(key)))
    || required.some((key) => !(key in payload))
  ) {
    throw new Error(`${label} does not match its closed schema`);
  }
  for (const [key, raw] of Object.entries(payload)) {
    if (!(key in properties)) continue;
    const property = record(properties[key], `${label}.schema.properties.${key}`);
    validateSchemaValue(raw, property, `${label}.${key}`);
  }
  return payload;
}

function validateClosedPayload(
  value: unknown,
  schema: JsonRecord,
  label: string,
): JsonRecord {
  return validateObjectPayload(value, schema, label);
}

function parseRpcCaseV2(value: unknown): RpcGoldenCaseV2 {
  const source = record(value, "rpc case");
  exact(source, [
    "method", "scope", "paramsModel", "resultModel", "paramsSchema",
    "resultSchema", "request", "success", "error",
  ], "rpc case");
  const method = string(source.method, "rpc case method");
  const scope = oneOf(source.scope, ["global", "workspace"], "rpc case scope");
  const paramsSchema = parseClosedSchema(source.paramsSchema, "params schema");
  const resultSchema = parseClosedSchema(source.resultSchema, "result schema");

  const request = record(source.request, "rpc request");
  exact(request, ["jsonrpc", "id", "method", "wire", "params"], "rpc request");
  const requestWire = parseWireScopeV2(request.wire);
  if (
    request.jsonrpc !== "2.0"
    || string(request.id, "rpc request id").length === 0
    || request.method !== method
    || requestWire.scope !== scope
  ) {
    throw new Error("rpc request identity is invalid");
  }
  validateClosedPayload(request.params, paramsSchema, "rpc params");

  const success = record(source.success, "rpc success");
  exact(success, ["jsonrpc", "id", "wire", "result"], "rpc success");
  const successWire = parseWireScopeV2(success.wire);
  if (
    success.jsonrpc !== "2.0"
    || success.id !== request.id
    || successWire.scope !== scope
    || successWire.operationId !== requestWire.operationId
    || successWire.sequence !== requestWire.sequence
  ) {
    throw new Error("rpc success identity is invalid");
  }
  validateClosedPayload(success.result, resultSchema, "rpc result");

  const failure = record(source.error, "rpc error response");
  exact(failure, ["jsonrpc", "id", "wire", "error"], "rpc error response");
  const failureWire = parseWireScopeV2(failure.wire);
  const error = record(failure.error, "rpc error");
  exact(error, ["code", "message", "details", "retryable"], "rpc error");
  record(error.details, "rpc error details");
  if (
    failure.jsonrpc !== "2.0"
    || failure.id !== request.id
    || failureWire.scope !== scope
    || failureWire.operationId !== requestWire.operationId
    || failureWire.sequence !== requestWire.sequence
    || typeof error.retryable !== "boolean"
  ) {
    throw new Error("rpc error identity is invalid");
  }
  string(error.code, "rpc error code");
  string(error.message, "rpc error message");

  return {
    method,
    scope,
    paramsModel: string(source.paramsModel, "paramsModel"),
    resultModel: string(source.resultModel, "resultModel"),
    paramsSchema,
    resultSchema,
    request,
    success,
    error: failure,
  };
}

export function parseRpcContractCatalogV2(value: unknown): RpcContractCatalogV2 {
  const source = record(value, "rpc catalog");
  exact(
    source,
    ["contractVersion", "rpcMethods", "eventTopics", "rpcCases", "eventCases"],
    "rpc catalog",
  );
  if (!Array.isArray(source.rpcCases) || !Array.isArray(source.eventCases)) {
    throw new Error("rpc catalog cases must be arrays");
  }
  const rpcMethods = stringArray(source.rpcMethods, "rpcMethods");
  const eventTopics = stringArray(source.eventTopics, "eventTopics").map((topic) =>
    oneOf(
      topic,
      [
        "workspace.session.changed",
        "snapshot.changed",
        "replica.changed",
        "lease.changed",
        "conflict.changed",
      ] as const,
      "event topic",
    )
  );
  const rpcCases = source.rpcCases.map(parseRpcCaseV2);
  const eventCases = source.eventCases.map(parseWorkspaceEventV2);
  if (
    new Set(rpcMethods).size !== rpcMethods.length
    || new Set(eventTopics).size !== eventTopics.length
    || rpcCases.length !== rpcMethods.length
    || rpcCases.some((item, index) => item.method !== rpcMethods[index])
    || eventCases.length !== eventTopics.length
    || eventCases.some((item, index) => item.topic !== eventTopics[index])
  ) {
    throw new Error("rpc catalog index and cases do not match");
  }
  return {
    contractVersion: contractVersion(source.contractVersion),
    rpcMethods,
    eventTopics,
    rpcCases,
    eventCases,
  };
}
