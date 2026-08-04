import type { HostBridge } from "@/bridge/hostBridge";
import {
  parseWorkspaceV2Bootstrap,
  parseWorkspaceV2Event,
  parseWorkspaceV2Reply,
  type WorkspaceV2Reply,
  type WorkspaceV2RequestPayload,
  type WorkspaceV2SuccessReply,
  type WorkspaceV2RpcMethod,
  type WorkspaceV2RpcResult,
  type WorkspaceV2UiAction,
} from "@/contracts/workspaceV2Bridge";
import type { WorkspaceSessionV2, WireScopeV2 } from "@/contracts/workspaceV2";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import type { WorkspaceV2UiPort } from "@/services/workspaceV2UiPort";
import {
  configureWorkspaceWire,
  nextWorkspaceWire,
  observeWorkspaceWire,
} from "@/services/workspaceWireAllocator";

const GLOBAL_METHODS = new Set<WorkspaceV2RpcMethod>([
  "workspace.list",
  "workspace.create",
  "workspace.register",
  "workspace.relink",
  "workspace.open",
  "workspace.remove",
  "workspace.planDelete",
  "workspace.applyDelete",
  "workspace.storage.preview",
  "workspace.storage.apply",
  "snapshot.inspectPackage",
  "snapshot.import",
]);

export const HOST_WORKSPACE_ROOT_GRANT = "host-picker://workspace-root";
export const HOST_SNAPSHOT_EXPORT_GRANT = "host-picker://snapshot-export";
export const HOST_SNAPSHOT_IMPORT_GRANT = "host-picker://snapshot-import";
export const HOST_SNAPSHOT_EXTRACT_GRANT = "host-picker://snapshot-extract";
export const HOST_FILE_UPGRADE_GRANT = "host-picker://file-upgrade";

export class WorkspaceV2RequestError extends Error {
  readonly code: string;
  readonly retryable: boolean;

  constructor(code: string, message: string, retryable: boolean) {
    super(message);
    this.name = "WorkspaceV2RequestError";
    this.code = code;
    this.retryable = retryable;
  }
}

function operationId(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  const suffix = Math.floor(Math.random() * 0xffff_ffff)
    .toString(16)
    .padStart(8, "0");
  return `00000000-0000-4000-8000-${suffix.padStart(12, "0")}`;
}

function sessionFromResult(
  result: {
    readonly workspaceId: string | null;
    readonly sessionEpoch: number;
    readonly state: WorkspaceSessionV2["state"];
  },
): WorkspaceSessionV2 {
  const writable = result.state === "openedWritable";
  const provisional = result.state === "openedProvisional";
  return {
    contractVersion: "2.0",
    workspaceId: result.workspaceId,
    sessionEpoch: result.sessionEpoch,
    state: result.state,
    openMode: provisional ? "provisional" : writable ? "writable" : "readOnly",
    writable,
    provisional,
    phase: "idle",
    errorCode: result.state === "failed" ? "workspace.session_failed" : null,
  };
}

export function createWorkspaceV2HostAdapter(bridge: HostBridge): {
  readonly port: WorkspaceV2UiPort;
  readonly dispose: () => void;
} {
  const session = useWorkspaceSessionStore();
  const protection = useWorkspaceProtectionStore();
  let globalSequence = 0;
  const actionContexts = new Map<string, WorkspaceV2UiAction>();

  function supportsMethod(method: WorkspaceV2RpcMethod): boolean {
    if (method.startsWith("workspace.")) return session.enabled;
    if (method === "snapshot.inspectPackage" || method === "snapshot.import") {
      return session.snapshotPackageEnabled;
    }
    if (method === "snapshot.openAsNewWorkspace") {
      return session.capabilities.includes("snapshot.open-as-new.v2");
    }
    if (method === "repository.verify") return session.snapshotEnabled;
    if (method === "repository.previewKeyRotation"
        || method === "repository.applyKeyRotation") {
      return session.capabilities.includes("repository.key-rotation.v2");
    }
    if (method.startsWith("snapshot.")) return session.snapshotEnabled;
    if (method.startsWith("history.")) return session.historyRestoreEnabled;
    if (method.startsWith("fileHistory.")) return session.fileHistoryEnabled;
    if (method.startsWith("retention.")) return session.policyEnabled;
    if (method.startsWith("conflict.")) return session.conflictEnabled;
    return session.capabilities.includes("repository.settings.v2");
  }

  function nextWire(method: WorkspaceV2RpcMethod): WireScopeV2 {
    const id = operationId();
    if (GLOBAL_METHODS.has(method)) {
      globalSequence += 1;
      return { scope: "global", operationId: id, sequence: globalSequence };
    }
    if (!session.activeWorkspaceId || session.sessionEpoch < 1) {
      throw new WorkspaceV2RequestError(
        "workspace.session_required",
        "Workspace operation requires an active v2 session.",
        false,
      );
    }
    configureWorkspaceWire(session.activeWorkspaceId, session.sessionEpoch);
    return nextWorkspaceWire(id);
  }

  function acceptReplyWire(wire: WireScopeV2): boolean {
    if (wire.scope === "global") return true;
    return session.acceptEnvelope(wire);
  }

  function patchSnapshot(
    snapshotId: string,
    patch: Readonly<Record<string, unknown>>,
  ): void {
    const current = protection.snapshots.find((item) => item.snapshotId === snapshotId);
    if (!current) return;
    protection.upsertSnapshot({ ...current, ...patch });
  }

  async function refreshWorkspaceList(): Promise<void> {
    await port.request({ method: "workspace.list", params: {} });
  }

  async function hydrateWorkspace(): Promise<void> {
    const actions: WorkspaceV2UiAction[] = [];
    if (session.snapshotEnabled) {
      actions.push({ method: "snapshot.list", params: { cursor: null, limit: 50 } });
    }
    if (session.fileHistoryEnabled) {
      actions.push({ method: "fileHistory.listDocuments", params: { includeDeleted: false } });
      actions.push({ method: "fileHistory.listPendingChanges", params: {} });
    }
    if (session.policyEnabled) {
      actions.push({ method: "retention.get", params: {} });
      actions.push({ method: "retention.status", params: {} });
    }
    if (session.capabilities.includes("repository.settings.v2")) {
      actions.push({ method: "replica.status", params: {} });
    }
    if (session.conflictEnabled) {
      actions.push({ method: "conflict.list", params: { cursor: null, limit: 50 } });
    }
    for (const action of actions) {
      try {
        await port.request(action);
      } catch {
        // Each projection fails closed independently. A later host event or
        // explicit refresh may recover without discarding the active session.
      }
    }
  }

  function projectSuccessfulReply(
    reply: WorkspaceV2SuccessReply,
    action?: WorkspaceV2UiAction,
  ): void {
    const { method, result } = reply;
    if (method === "workspace.list") {
      session.setWorkspaces(result.workspaces);
    } else if (
      method === "workspace.open"
      || method === "workspace.switch"
      || method === "workspace.close"
      || method === "snapshot.openAsNewWorkspace"
    ) {
      if (method === "workspace.close" || result.state === "closed") {
        session.closeSession();
        configureWorkspaceWire(null, 0);
      } else {
        session.applySession(sessionFromResult(result));
        configureWorkspaceWire(session.activeWorkspaceId, session.sessionEpoch);
        void hydrateWorkspace();
      }
    } else if (method === "workspace.planDelete" && action?.method === method) {
      session.setDeletePlan({
        workspaceId: action.params.workspaceId,
        ...result,
      });
    } else if (method === "workspace.storage.preview") {
      protection.setStoragePlan(result);
    } else if (method === "workspace.storage.apply") {
      protection.setStoragePlan(null);
      protection.setStorage(result.storage);
      void refreshWorkspaceList();
    } else if (method === "workspace.remove" || method === "workspace.applyDelete") {
      session.setDeletePlan(null);
      session.setWorkspaces(session.workspaces.filter((item) =>
        item.workspaceId !== result.workspaceId));
      if (result.workspaceId === session.activeWorkspaceId) session.closeSession();
    } else if (
      method === "workspace.create"
      || method === "workspace.register"
      || method === "workspace.relink"
    ) {
      void refreshWorkspaceList();
    } else if (method === "snapshot.request") {
      void port.request({ method: "snapshot.list", params: { cursor: null, limit: 50 } });
    } else if (method === "snapshot.list") {
      protection.setSnapshots(result.snapshots);
    } else if (method === "snapshot.inspect") {
      patchSnapshot(result.snapshotId, {
        state: result.state,
        integrity: result.integrity,
      });
    } else if (method === "snapshot.update" && action?.method === method) {
      patchSnapshot(result.snapshotId, {
        state: result.state,
        integrity: result.integrity,
        pinned: action.params.action === "pin",
        catalogRevision: action.params.expectedCatalogRevision + 1,
      });
    } else if (method === "snapshot.previewRestore") {
      protection.setRestorePlan(result);
    } else if (method === "snapshot.applyRestore") {
      protection.setRestorePlan(null);
    } else if (method === "snapshot.previewExtract") {
      protection.setExtractPlan(result);
    } else if (method === "snapshot.applyExtract") {
      protection.setExtractPlan(null);
    } else if (method === "repository.verify") {
      protection.setRepositoryVerification(result);
    } else if (method === "repository.previewKeyRotation") {
      protection.setKeyRotationPlan(result);
    } else if (method === "repository.applyKeyRotation") {
      protection.setKeyRotationPlan(null);
    } else if (method === "fileHistory.readTree") {
      protection.setFileTree(result);
    } else if (method === "fileHistory.listPendingChanges") {
      protection.setPendingFileChanges(result.changes);
    } else if (method === "fileHistory.listDocuments") {
      protection.setDocuments(result.documents);
    } else if (method === "fileHistory.applyPendingChange") {
      protection.removePendingFileChange(result.changeId);
      void port.request({ method: "fileHistory.listPendingChanges", params: {} });
    } else if (method === "snapshot.inspectPackage") {
      protection.setSnapshotPackagePlan(result);
    } else if (method === "snapshot.import") {
      protection.setSnapshotPackagePlan(null);
      void refreshWorkspaceList();
    } else if (
      method === "fileHistory.restore"
      || method === "fileHistory.activateLeaf"
      || method === "fileHistory.upgrade"
    ) {
      const documentId = action?.params && "documentId" in action.params
        ? action.params.documentId
        : null;
      if (documentId) {
        void port.request({ method: "fileHistory.readTree", params: { documentId } });
      }
    } else if (method === "retention.get" || method === "retention.update") {
      protection.setRetention(result);
    } else if (method === "retention.status") {
      protection.setRetentionStatus(result);
    } else if (method === "retention.plan") {
      protection.setRetentionPlan(result);
    } else if (method === "retention.apply") {
      protection.setRetentionPlan(null);
      void port.request({ method: "retention.get", params: {} });
      void port.request({ method: "retention.status", params: {} });
    } else if (method === "replica.status") {
      applyReplicaStorageState(result.syncState, result.pendingSync);
    } else if (method === "conflict.list") {
      protection.setConflictSets(result.conflicts);
    } else if (method === "conflict.inspect") {
      protection.setConflicts(result.items);
    } else if (method === "conflict.preview" && action?.method === method) {
      protection.setConflictPlan(action.params.conflictId, result);
    } else if (method === "conflict.apply") {
      void port.request({ method: "conflict.list", params: { cursor: null, limit: 50 } });
    }
  }

  function processReply(
    raw: unknown,
    expected?: { readonly method: WorkspaceV2RpcMethod; readonly operationId: string },
  ): WorkspaceV2Reply {
    const reply = parseWorkspaceV2Reply(raw);
    if (
      expected
      && (reply.method !== expected.method || reply.wire.operationId !== expected.operationId)
    ) {
      throw new Error("workspace.v2_reply_mismatch");
    }
    if (!acceptReplyWire(reply.wire)) throw new Error("workspace.v2_reply_stale");
    if (reply.wire.scope === "workspace") observeWorkspaceWire(reply.wire);
    const context = actionContexts.get(reply.wire.operationId);
    if (reply.ok) {
      projectSuccessfulReply(reply, context);
    }
    if (!reply.ok) {
      throw new WorkspaceV2RequestError(
        reply.error.code,
        reply.error.message,
        reply.error.retryable,
      );
    }
    return reply;
  }

  function projectBootstrap(raw: unknown): void {
    const bootstrap = parseWorkspaceV2Bootstrap(raw);
    session.configureCapabilities(bootstrap.capabilities);
    session.setWorkspaces(bootstrap.workspaces);
    session.applySession(bootstrap.session);
    configureWorkspaceWire(session.activeWorkspaceId, session.sessionEpoch);
    if (session.snapshotEnabled) protection.setSnapshots(bootstrap.snapshots);
    if (session.capabilities.includes("repository.settings.v2")) {
      protection.setStorage(bootstrap.storage);
    }
    if (session.conflictEnabled) protection.setConflicts(bootstrap.conflicts);
    if (session.fileHistoryEnabled) {
      for (const tree of bootstrap.fileTrees) protection.setFileTree(tree);
    }
  }

  function eventText(payload: Readonly<Record<string, unknown>>, key: string): string {
    const value = payload[key];
    if (typeof value !== "string" || value.length === 0) {
      throw new Error(`workspace event ${key} is invalid`);
    }
    return value;
  }

  function eventBoolean(payload: Readonly<Record<string, unknown>>, key: string): boolean {
    const value = payload[key];
    if (typeof value !== "boolean") throw new Error(`workspace event ${key} is invalid`);
    return value;
  }

  function applyReplicaStorageState(
    syncState: "localOnly" | "pending" | "syncing" | "replicated" | "failed",
    pendingSync: boolean,
  ): void {
    if (!protection.storage) return;
    const health = syncState === "replicated" || syncState === "syncing"
      ? "healthy"
      : syncState === "pending" || syncState === "failed"
        ? "attention"
        : protection.storage.health;
    protection.setStorage({
      ...protection.storage,
      health,
      pendingSync,
      replicaVerified: syncState === "replicated" && !pendingSync,
    });
  }

  function projectEvent(raw: unknown): void {
    const event = parseWorkspaceV2Event(raw);
    if (!session.acceptEnvelope(event.wire)) return;
    observeWorkspaceWire(event.wire);
    const payload = event.payload;
    if (event.topic === "workspace.session.changed") {
      const state = eventText(payload, "state") as WorkspaceSessionV2["state"];
      const phase = eventText(payload, "phase") as WorkspaceSessionV2["phase"];
      session.applySession({
        contractVersion: "2.0",
        workspaceId: event.wire.workspaceId,
        sessionEpoch: event.wire.sessionEpoch,
        state,
        openMode: state === "openedProvisional"
          ? "provisional"
          : state === "openedWritable" ? "writable" : "readOnly",
        writable: state === "openedWritable",
        provisional: state === "openedProvisional",
        phase,
        errorCode: state === "failed" ? "workspace.session_failed" : null,
      });
    } else if (event.topic === "snapshot.changed") {
      patchSnapshot(eventText(payload, "snapshotId"), {
        state: eventText(payload, "state"),
        integrity: eventText(payload, "integrity"),
      });
    } else if (event.topic === "replica.changed" && protection.storage) {
      applyReplicaStorageState(
        eventText(payload, "syncState") as
          | "localOnly" | "pending" | "syncing" | "replicated" | "failed",
        eventBoolean(payload, "pendingSync"),
      );
    } else if (event.topic === "conflict.changed") {
      const conflictId = eventText(payload, "conflictId");
      const state = eventText(payload, "state");
      protection.setConflicts(protection.conflicts.map((item) =>
        item.conflictId === conflictId
          ? { ...item, state: state as typeof item.state }
          : item));
    }
  }

  const port: WorkspaceV2UiPort = {
    async request<M extends WorkspaceV2RpcMethod>(
      action: WorkspaceV2UiAction<M>,
    ): Promise<WorkspaceV2RpcResult<M>> {
      if (!supportsMethod(action.method)) {
        throw new WorkspaceV2RequestError(
          "workspace.capability_unavailable",
          `Workspace v2 capability is unavailable for ${action.method}.`,
          false,
        );
      }
      const wire = nextWire(action.method);
      actionContexts.set(wire.operationId, action as WorkspaceV2UiAction);
      if (actionContexts.size > 100) {
        const oldest = actionContexts.keys().next().value;
        if (oldest) actionContexts.delete(oldest);
      }
      const payload = {
        method: action.method,
        params: action.params,
        wire,
      } as WorkspaceV2RequestPayload;
      const raw = await bridge.request("workspace.v2.request", payload);
      const reply = processReply(raw, {
        method: action.method,
        operationId: wire.operationId,
      });
      return reply.result as WorkspaceV2RpcResult<M>;
    },
  };

  const unsubscribers = [
    bridge.on("workspace.v2.bootstrap", projectBootstrap),
    bridge.on("workspace.v2.event", projectEvent),
    bridge.on("workspace.v2.reply", (raw) => { processReply(raw); }),
  ];

  return {
    port,
    dispose: () => {
      for (const unsubscribe of unsubscribers) unsubscribe();
      actionContexts.clear();
    },
  };
}
