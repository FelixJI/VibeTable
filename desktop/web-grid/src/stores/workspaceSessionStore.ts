import { computed, ref } from "vue";
import { defineStore } from "pinia";
import type {
  WorkspaceRegistryEntryV2,
  WorkspaceSessionV2,
  WorkspaceWireScope,
} from "@/contracts/workspaceV2";
import type { WorkspaceDeletePlan } from "@/contracts/workspaceV2Bridge";

export type WorkspaceV2Capability =
  | "workspace.session.v2"
  | "workspace.storage.mirrored-create.v2"
  | "workspace.storage.relocate.v2"
  | "workspace.storage.topology.v2"
  | "workspace.storage.release-cache.v2"
  | "snapshot.timeline.v2"
  | "snapshot.package.v2"
  | "snapshot.open-as-new.v2"
  | "history.restore.v2"
  | "fileHistory.tree.v2"
  | "retention.policy.v2"
  | "repository.settings.v2"
  | "repository.key-rotation.v2"
  | "conflict.center.v2";

export interface WorkspaceResetContext {
  readonly previousWorkspaceId: string | null;
  readonly previousSessionEpoch: number;
  readonly nextWorkspaceId: string | null;
  readonly nextSessionEpoch: number;
}

type WorkspaceEpochResetter = (context: WorkspaceResetContext) => void;

const epochResetters = new Map<string, WorkspaceEpochResetter>();

/**
 * Registers workspace-scoped state that must be invalidated atomically before
 * a new session becomes observable. Registration is module-level so stores do
 * not need to import one another and accidentally create lifecycle cycles.
 */
export function registerWorkspaceEpochReset(
  key: string,
  reset: WorkspaceEpochResetter,
): () => void {
  epochResetters.set(key, reset);
  return () => {
    if (epochResetters.get(key) === reset) epochResetters.delete(key);
  };
}

function runEpochResetters(context: WorkspaceResetContext): void {
  for (const reset of epochResetters.values()) reset(context);
}

export const useWorkspaceSessionStore = defineStore("workspace-session-v2", () => {
  const capabilities = ref<readonly WorkspaceV2Capability[]>([]);
  const workspaces = ref<readonly WorkspaceRegistryEntryV2[]>([]);
  const activeWorkspaceId = ref<string | null>(null);
  const sessionEpoch = ref(0);
  const sessionState = ref<WorkspaceSessionV2["state"]>("closed");
  const sessionPhase = ref<WorkspaceSessionV2["phase"]>("idle");
  const writable = ref(false);
  const provisional = ref(false);
  const errorCode = ref<string | null>(null);
  const targetWorkspaceId = ref<string | null>(null);
  const lastSequence = ref(0);
  const deletePlan = ref<WorkspaceDeletePlan | null>(null);

  const enabled = computed(() => capabilities.value.includes("workspace.session.v2"));
  const snapshotEnabled = computed(() => capabilities.value.includes("snapshot.timeline.v2"));
  const snapshotPackageEnabled = computed(() => capabilities.value.includes("snapshot.package.v2"));
  const historyRestoreEnabled = computed(() => capabilities.value.includes("history.restore.v2"));
  const fileHistoryEnabled = computed(() => capabilities.value.includes("fileHistory.tree.v2"));
  const policyEnabled = computed(() =>
    capabilities.value.includes("retention.policy.v2")
    && capabilities.value.includes("repository.settings.v2"));
  const conflictEnabled = computed(() => capabilities.value.includes("conflict.center.v2"));
  const activeWorkspace = computed(() =>
    workspaces.value.find((workspace) => workspace.workspaceId === activeWorkspaceId.value) ?? null);
  const hasOpenWorkspace = computed(() =>
    activeWorkspace.value !== null
    && !["closed", "failed"].includes(sessionState.value));
  const isTransitioning = computed(() =>
    sessionState.value === "switching"
    || sessionState.value === "opening"
    || sessionPhase.value !== "idle");

  function configureCapabilities(next: readonly string[]): void {
    const accepted = next.filter((capability): capability is WorkspaceV2Capability =>
      [
        "workspace.session.v2",
        "workspace.storage.mirrored-create.v2",
        "workspace.storage.relocate.v2",
        "workspace.storage.topology.v2",
        "workspace.storage.release-cache.v2",
        "snapshot.timeline.v2",
        "snapshot.package.v2",
        "snapshot.open-as-new.v2",
        "history.restore.v2",
        "fileHistory.tree.v2",
        "retention.policy.v2",
        "repository.settings.v2",
        "repository.key-rotation.v2",
        "conflict.center.v2",
      ].includes(capability));
    capabilities.value = [...new Set(accepted)];
    if (!capabilities.value.includes("workspace.session.v2")) closeSession();
  }

  function setWorkspaces(next: readonly WorkspaceRegistryEntryV2[]): void {
    workspaces.value = [...next];
  }

  function beginSwitch(workspaceId: string): boolean {
    if (!enabled.value || isTransitioning.value || workspaceId === activeWorkspaceId.value) {
      return false;
    }
    targetWorkspaceId.value = workspaceId;
    sessionState.value = "switching";
    sessionPhase.value = "protecting";
    errorCode.value = null;
    return true;
  }

  function reportTransitionPhase(phase: WorkspaceSessionV2["phase"]): void {
    if (!enabled.value || !isTransitioning.value) return;
    sessionPhase.value = phase;
  }

  function applySession(next: WorkspaceSessionV2): boolean {
    if (!enabled.value) return false;
    if (
      next.workspaceId === activeWorkspaceId.value
      && next.sessionEpoch < sessionEpoch.value
    ) {
      return false;
    }

    const rotatesEpoch =
      next.workspaceId !== activeWorkspaceId.value
      || next.sessionEpoch !== sessionEpoch.value;
    if (rotatesEpoch) {
      runEpochResetters({
        previousWorkspaceId: activeWorkspaceId.value,
        previousSessionEpoch: sessionEpoch.value,
        nextWorkspaceId: next.workspaceId,
        nextSessionEpoch: next.sessionEpoch,
      });
      lastSequence.value = 0;
    }

    activeWorkspaceId.value = next.workspaceId;
    sessionEpoch.value = next.sessionEpoch;
    sessionState.value = next.state;
    sessionPhase.value = next.phase;
    writable.value = next.writable;
    provisional.value = next.provisional;
    errorCode.value = next.errorCode;
    targetWorkspaceId.value = null;
    return true;
  }

  function failSwitch(code: string): void {
    errorCode.value = code;
    targetWorkspaceId.value = null;
    sessionState.value = activeWorkspaceId.value
      ? (provisional.value ? "openedProvisional" : writable.value ? "openedWritable" : "openedReadOnly")
      : "failed";
    sessionPhase.value = "idle";
  }

  function acceptEnvelope(scope: WorkspaceWireScope): boolean {
    if (
      !enabled.value
      || scope.workspaceId !== activeWorkspaceId.value
      || scope.sessionEpoch !== sessionEpoch.value
      || scope.sequence <= lastSequence.value
    ) {
      return false;
    }
    lastSequence.value = scope.sequence;
    return true;
  }

  function closeSession(): void {
    if (activeWorkspaceId.value !== null || sessionEpoch.value !== 0) {
      runEpochResetters({
        previousWorkspaceId: activeWorkspaceId.value,
        previousSessionEpoch: sessionEpoch.value,
        nextWorkspaceId: null,
        nextSessionEpoch: 0,
      });
    }
    activeWorkspaceId.value = null;
    sessionEpoch.value = 0;
    sessionState.value = "closed";
    sessionPhase.value = "idle";
    writable.value = false;
    provisional.value = false;
    errorCode.value = null;
    targetWorkspaceId.value = null;
    lastSequence.value = 0;
    deletePlan.value = null;
  }

  function setDeletePlan(next: WorkspaceDeletePlan | null): void {
    deletePlan.value = next;
  }

  return {
    capabilities,
    workspaces,
    activeWorkspaceId,
    sessionEpoch,
    sessionState,
    sessionPhase,
    writable,
    provisional,
    errorCode,
    targetWorkspaceId,
    lastSequence,
    deletePlan,
    enabled,
    snapshotEnabled,
    snapshotPackageEnabled,
    historyRestoreEnabled,
    fileHistoryEnabled,
    policyEnabled,
    conflictEnabled,
    activeWorkspace,
    hasOpenWorkspace,
    isTransitioning,
    configureCapabilities,
    setWorkspaces,
    beginSwitch,
    reportTransitionPhase,
    applySession,
    failSwitch,
    acceptEnvelope,
    closeSession,
    setDeletePlan,
  };
});
