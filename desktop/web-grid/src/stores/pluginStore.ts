import { computed, ref } from "vue";
import { defineStore } from "pinia";
import type {
  PluginInstallPlan,
  PluginCommandContext,
  PluginInteractionSnapshot,
  PluginSnapshot,
  PluginTaskSnapshot,
  PluginTaskViewSnapshot,
  WebPluginActionDescription,
  WebPluginConfirmationPreview,
} from "@/contracts";

/** A live catalog read's ordering context; its terminal freezes a result. */
export interface CatalogReadReceipt {
  readonly generation: number;
  readonly sequence: number;
  readonly ownerCurrent: () => boolean;
  /** Started behind an active write barrier: its (possibly pre-write)
   *  projection may not consume queued demands. */
  readonly barrierWindow: boolean;
  settled: false | "projected" | "failed-current" | "locally-retired" | "retired" | "superseded";
}

/** A pending write's ordering barrier token; released only by its owner. */
export interface CatalogWriteBarrier {
  readonly generation: number;
  readonly sequence: number;
}

export const usePluginStore = defineStore("plugins", () => {
  const projectKey = ref("");
  const projectRevision = ref("");
  const projectContextReady = ref(false);
  const projectContextGeneration = ref(0);
  // Shared by every service instance; this orders UI reads, not authority data.
  let catalogReadSequence = 0;
  let catalogRefreshRunning = false;
  // A queued demand is the unsettled notification itself: it carries the
  // generation/sequence it must be served under and survives until a real
  // projection or a current failure covers it, or its owner retires.
  interface CatalogRefreshDemand {
    ownerCurrent: () => boolean;
    generation: number;
    sequence: number;
  }
  let catalogRefreshPending = new Map<() => Promise<readonly PluginSnapshot[]>, CatalogRefreshDemand>();
  // Only the newest read receipt and the active write tokens order refresh
  // activity; older terminals stay frozen and can never touch newer state.
  let catalogLatestReceipt: CatalogReadReceipt | null = null;
  const catalogWriteBarriers = new Set<CatalogWriteBarrier>();
  const currentUser = ref<Readonly<Record<string, unknown>>>({});
  const hostVersion = ref("unknown");
  const catalogRevision = ref(-1);
  const pluginById = ref<Record<string, PluginSnapshot>>({});
  const taskById = ref<Record<string, PluginTaskViewSnapshot>>({});
  let taskObservationOrder = 0;
  const admittedTaskIds = new Set<string>();
  const taskRevisionById = ref<Record<string, number>>({});
  const interactionRevisionByRun = ref<Record<string, number>>({});
  const pendingInteractionByRun = ref<Record<string, {
    readonly snapshot: PluginInteractionSnapshot;
    readonly revision: number;
  }>>({});
  const installPlan = ref<PluginInstallPlan | null>(null);
  const describedAction = ref<WebPluginActionDescription | null>(null);
  const activeContext = ref<PluginCommandContext | null>(null);
  const selectedPluginId = ref<string | null>(null);
  const activeTaskId = ref<string | null>(null);
  const actionOpen = ref(false);
  const busy = ref(false);
  const lastError = ref<string | null>(null);

  const plugins = computed(() => Object.values(pluginById.value));
  const selectedPlugin = computed(() =>
    selectedPluginId.value ? pluginById.value[selectedPluginId.value] ?? null : null,
  );
  const activeTask = computed(() =>
    activeTaskId.value ? taskById.value[activeTaskId.value] ?? null : null,
  );
  // Receipt history is a projection of Host snapshots, not authorization for
  // another frame. The existing workspace/session reset owns its lifetime.
  const nativeTaskReceipts = computed(() => Object.values(taskById.value).filter(task =>
    task.projectKey === projectKey.value && Boolean(task.surfaceToken)
    && Boolean(task.installationId) && (task.installationRevision ?? 0) > 0));
  const pendingConfirmation = computed<WebPluginConfirmationPreview | null>(() =>
    activeTask.value?.confirmation ?? null,
  );

  function currentTask(task: PluginTaskViewSnapshot): boolean {
    const plugin = pluginById.value[task.pluginId];
    if (task.projectKey !== projectKey.value) return false;
    if (admittedTaskIds.has(task.taskId) && (!plugin || task.installationId === plugin.installationId)) return true;
    return plugin?.status === "enabled"
      && Boolean(task.installationId) && task.installationId === plugin.installationId
      && task.installationRevision === plugin.revision
      && task.pluginVersion === plugin.version
      && plugin.manifest.actions.some(action => action.actionId === task.actionId);
  }
  function taskForId(taskId: string): PluginTaskViewSnapshot | null {
    const task = taskById.value[taskId];
    return task && currentTask(task) ? task : null;
  }
  function tasksForAction(pluginId: string, actionId: string, surfaceToken?: string): PluginTaskViewSnapshot[] {
    return Object.values(taskById.value).filter(task => task.pluginId === pluginId
      && task.actionId === actionId && (surfaceToken === undefined || task.surfaceToken === surfaceToken) && currentTask(task));
  }
  function taskForAction(pluginId: string, actionId: string, surfaceToken?: string): PluginTaskViewSnapshot | null {
    return tasksForAction(pluginId, actionId, surfaceToken).at(-1) ?? null;
  }

  function beginCatalogRead(): number { return ++catalogReadSequence; }
  function isCurrentCatalogRead(sequence: number): boolean { return sequence === catalogReadSequence; }
  function invalidateCatalogReads(): void { catalogReadSequence++; }

  /** A catalog read (direct or scheduler-driven) registers as the newest
   *  activity; the scheduler waits for its terminal instead of racing it. */
  function beginObservedCatalogRead(ownerCurrent: () => boolean): CatalogReadReceipt {
    const receipt: CatalogReadReceipt = {
      generation: projectContextGeneration.value,
      sequence: beginCatalogRead(),
      ownerCurrent,
      barrierWindow: catalogWriteBarriers.size > 0,
      settled: false,
    };
    catalogLatestReceipt = receipt;
    return receipt;
  }

  /**
   * Freeze the read's terminal and settle exactly the demands it covers:
   * a real projection or a current failure ends every demand of the same
   * generation queued up to this read's sequence; retirement only removes
   * the retired owner's own demand; a superseded read settles nothing. A
   * terminal from a receipt that is no longer the newest activity may only
   * wake the scheduler — it can never touch newer demands or state.
   */
  function settleObservedCatalogRead(
    receipt: CatalogReadReceipt,
    outcome: Exclude<CatalogReadReceipt["settled"], false>,
  ): void {
    if (receipt.settled !== false) return;
    if (catalogLatestReceipt !== receipt) {
      receipt.settled = "superseded";
      scheduleCatalogRefresh();
      return;
    }
    receipt.settled = outcome;
    if (outcome === "projected" || outcome === "failed-current" || outcome === "locally-retired") {
      // A read started behind an active write barrier observed pre-write
      // authority: it must not consume demands. The write terminal's release
      // re-schedules them for a post-write authority read. A locally retired
      // request (e.g. a draining workspace refused it) also ends its covered
      // demands bounded — hidden, but never an endless superseded spin.
      if (!receipt.barrierWindow) {
        for (const [read, demand] of catalogRefreshPending) {
          if (demand.generation === receipt.generation && demand.sequence <= receipt.sequence) {
            catalogRefreshPending.delete(read);
          }
        }
      }
    } else if (outcome === "retired") {
      for (const [read, demand] of catalogRefreshPending) {
        if (!demand.ownerCurrent()) catalogRefreshPending.delete(read);
      }
    }
    scheduleCatalogRefresh();
  }

  /** A write's ordering barrier: advances the sequence and holds the
   *  scheduler until the write's own terminal releases this token. */
  function beginCatalogWriteBarrier(): CatalogWriteBarrier {
    const token: CatalogWriteBarrier = {
      generation: projectContextGeneration.value,
      sequence: beginCatalogRead(),
    };
    catalogWriteBarriers.add(token);
    return token;
  }

  function endCatalogWriteBarrier(token: CatalogWriteBarrier): void {
    // Duplicate or old-generation releases are inert: they must never release
    // a newer generation's barrier or wake racing reads on their own.
    if (!catalogWriteBarriers.delete(token)) return;
    scheduleCatalogRefresh();
  }

  function scheduleCatalogRefresh(): void {
    if (catalogRefreshRunning) return;
    catalogRefreshRunning = true;
    void Promise.resolve().then(async () => {
      try {
        while (true) {
          // A retired owner's demand can never be consumed.
          for (const [read, demand] of catalogRefreshPending) {
            if (!demand.ownerCurrent()) catalogRefreshPending.delete(read);
          }
          if (catalogRefreshPending.size === 0) break;
          // A pending write orders first; so does the newest unsettled read —
          // each of their terminals re-schedules the waiting demand.
          if (catalogWriteBarriers.size > 0) break;
          if (catalogLatestReceipt !== null && catalogLatestReceipt.settled === false) break;
          const chosen = catalogRefreshPending.entries().next();
          if (chosen.done) break;
          const [read] = chosen.value;
          try {
            await read();
          } catch {
            // The reader classified and settled its own failure through the
            // receipt protocol; the loop only re-evaluates remaining demands.
          }
        }
      } finally {
        catalogRefreshRunning = false;
      }
    });
  }

  function refreshCatalog(
    read: () => Promise<readonly PluginSnapshot[]>,
    ownerCurrent: () => boolean = () => true,
  ): void {
    invalidateCatalogReads();
    // Keying by the stable reader merges repeated notifications from one
    // owner; each notification replaces the demand with a fresh object.
    catalogRefreshPending.set(read, {
      ownerCurrent,
      generation: projectContextGeneration.value,
      sequence: catalogReadSequence,
    });
    scheduleCatalogRefresh();
  }

  function projectInstallPlan(plan: PluginInstallPlan | null): void {
    installPlan.value = plan;
  }

  function setProjectContext(key: string, revision: string): void {
    invalidateCatalogReads();
    // A generation switch invalidates every queued demand, write barrier and
    // receipt; old-generation terminals can never touch the new generation.
    catalogRefreshPending = new Map();
    catalogLatestReceipt = null;
    catalogWriteBarriers.clear();
    projectContextGeneration.value += 1;
    taskObservationOrder = 0;
    if (projectKey.value !== key) {
      pluginById.value = {};
      taskById.value = {};
      admittedTaskIds.clear();
      taskRevisionById.value = {};
      interactionRevisionByRun.value = {};
      pendingInteractionByRun.value = {};
      selectedPluginId.value = null;
      activeTaskId.value = null;
      catalogRevision.value = -1;
      describedAction.value = null;
      activeContext.value = null;
      actionOpen.value = false;
      busy.value = false;
      lastError.value = null;
    } else {
      // Tasks/confirmations belong to the previous workspace session even
      // when its stable project key is unchanged.
      taskById.value = {};
      admittedTaskIds.clear();
      taskRevisionById.value = {};
      interactionRevisionByRun.value = {};
      pendingInteractionByRun.value = {};
      activeTaskId.value = null;
      // A same-key reopen rotates the generation: the previous generation's
      // busy/error state is settled synchronously here, and its late
      // terminals stay inert against the new generation in the service.
      busy.value = false;
      lastError.value = null;
    }
    projectKey.value = key;
    projectRevision.value = revision;
    projectContextReady.value = Boolean(key.trim() && revision.trim());
  }

  function updateProjectRevision(revision: string): boolean {
    if (!projectContextReady.value || projectRevision.value === revision) return false;
    projectRevision.value = revision;
    projectContextGeneration.value += 1;
    // The in-place revision advance rotates the generation like a reopen:
    // settle the previous generation's busy/error and ordering state.
    busy.value = false;
    lastError.value = null;
    catalogRefreshPending = new Map();
    catalogLatestReceipt = null;
    catalogWriteBarriers.clear();
    return true;
  }

  function setHostContext(
    user: Readonly<Record<string, unknown>> | undefined,
    version: string | undefined,
  ): void {
    currentUser.value = user ?? {};
    hostVersion.value = version?.trim() || "unknown";
  }

  function replaceCatalog(key: string, snapshots: readonly PluginSnapshot[], eventRevision?: number): void {
    if (projectKey.value !== key) setProjectContext(key, projectRevision.value);
    if (eventRevision !== undefined && eventRevision <= catalogRevision.value) return;
    pluginById.value = Object.fromEntries(snapshots.map((item) => [item.pluginId, item]));
    catalogRevision.value = eventRevision
      ?? Math.max(0, ...snapshots.map((item) => item.revision));
    if (!selectedPluginId.value || !pluginById.value[selectedPluginId.value]) {
      selectedPluginId.value = snapshots[0]?.pluginId ?? null;
    }
  }

  function applyPlugin(snapshot: PluginSnapshot, eventRevision?: number): void {
    if (snapshot.projectKey !== projectKey.value) return;
    const current = pluginById.value[snapshot.pluginId];
    if (current && current.installationId === snapshot.installationId
      && snapshot.revision <= current.revision) return;
    pluginById.value = { ...pluginById.value, [snapshot.pluginId]: snapshot };
    catalogRevision.value = Math.max(catalogRevision.value, eventRevision ?? snapshot.revision);
    selectedPluginId.value ??= snapshot.pluginId;
  }

  function removePlugin(pluginId: string): void {
    const next = { ...pluginById.value };
    delete next[pluginId];
    pluginById.value = next;
    if (selectedPluginId.value === pluginId) selectedPluginId.value = plugins.value[0]?.pluginId ?? null;
  }

  function applyTask(snapshot: PluginTaskSnapshot, eventRevision?: number): PluginTaskViewSnapshot {
    const current = taskById.value[snapshot.taskId];
    const currentTaskRevision = taskRevisionById.value[snapshot.taskId] ?? 0;
    const taskRevision = eventRevision ?? currentTaskRevision + 1;
    if (current && taskRevision <= currentTaskRevision) return current;
    if (current && ["succeeded", "failed", "cancelled", "aborted"].includes(current.state)) return current;
    taskRevisionById.value = {
      ...taskRevisionById.value,
      [snapshot.taskId]: taskRevision,
    };
    const interactionRevision = interactionRevisionByRun.value[snapshot.runId] ?? 0;
    const terminal = ["succeeded", "failed", "cancelled", "aborted"].includes(snapshot.state);
    const projected: PluginTaskViewSnapshot = {
      ...snapshot,
      installationRevision: current ? current.installationRevision : snapshot.installationRevision,
      installationId: current ? current.installationId : snapshot.installationId,
      surfaceToken: current ? current.surfaceToken : snapshot.surfaceToken,
      revision: Math.max(taskRevision, interactionRevision),
      observationOrder: ++taskObservationOrder,
      progressPercent: snapshot.progress?.total
        ? Math.min(100, Math.round((snapshot.progress.current / snapshot.progress.total) * 100))
        : current?.progressPercent ?? null,
      progressMessage: snapshot.progress?.message || current?.progressMessage || null,
      confirmation: terminal ? null : current?.confirmation ?? null,
    };
    if (currentTask(projected)) admittedTaskIds.add(projected.taskId);
    taskById.value = { ...taskById.value, [snapshot.taskId]: projected };
    activeTaskId.value = snapshot.taskId;
    const pendingInteraction = pendingInteractionByRun.value[snapshot.runId];
    if (pendingInteraction) {
      const nextPending = { ...pendingInteractionByRun.value };
      delete nextPending[snapshot.runId];
      pendingInteractionByRun.value = nextPending;
      if (!terminal) applyInteraction(pendingInteraction.snapshot, pendingInteraction.revision);
      return taskById.value[snapshot.taskId] ?? projected;
    }
    return projected;
  }

  function applyInteraction(snapshot: PluginInteractionSnapshot, eventRevision: number): void {
    const current = Object.values(taskById.value).find((task) => task.runId === snapshot.runId);
    if (!current) {
      const pending = pendingInteractionByRun.value[snapshot.runId];
      if (!pending || eventRevision > pending.revision) {
        pendingInteractionByRun.value = {
          ...pendingInteractionByRun.value,
          [snapshot.runId]: { snapshot, revision: eventRevision },
        };
      }
      return;
    }
    if (snapshot.projectKey !== projectKey.value || snapshot.pluginId !== current.pluginId
      || snapshot.actionId !== current.actionId) return;
    if (["succeeded", "failed", "cancelled", "aborted"].includes(current.state)) return;
    const currentInteractionRevision = interactionRevisionByRun.value[snapshot.runId] ?? 0;
    if (eventRevision <= currentInteractionRevision) return;
    interactionRevisionByRun.value = {
      ...interactionRevisionByRun.value,
      [snapshot.runId]: eventRevision,
    };
    const progressPercent = snapshot.progress?.total
      ? Math.min(100, Math.round((snapshot.progress.current / snapshot.progress.total) * 100))
      : current.progressPercent;
    const pending = snapshot.pendingConfirmation;
    const confirmation: WebPluginConfirmationPreview | null = pending ? {
      runId: snapshot.runId,
      interactionId: pending.interactionId,
      pluginId: snapshot.pluginId,
      actionId: snapshot.actionId,
      title: pending.title,
      summary: pending.preview.summary.length
        ? pending.preview.summary.map((item) => JSON.stringify(item)).join(" · ")
        : `将影响 ${pending.preview.affectedCount} 条记录`,
      risk: pending.risk === "read" ? "write" : pending.risk,
      targetCount: pending.preview.affectedCount,
      sample: pending.preview.sampleRows,
      expiresAt: new Date(pending.expiresAt * 1000).toISOString(),
    } : null;
    taskById.value = { ...taskById.value, [current.taskId]: {
      ...current,
      cancelRequested: snapshot.cancelRequested,
      progressPercent,
      progressMessage: snapshot.progress?.message ?? current.progressMessage,
      confirmation,
      revision: Math.max(
        taskRevisionById.value[current.taskId] ?? 0,
        eventRevision,
      ),
    } };
  }

  function clearConfirmation(runId: string, interactionId?: string): void {
    const current = Object.values(taskById.value).find((task) => task.runId === runId);
    if (!current?.confirmation) return;
    if (interactionId && current.confirmation.interactionId !== interactionId) return;
    taskById.value = {
      ...taskById.value,
      [current.taskId]: { ...current, confirmation: null },
    };
  }

  function selectPlugin(pluginId: string): void { selectedPluginId.value = pluginId; }
  function beginAction(
    description: WebPluginActionDescription,
    context: PluginCommandContext,
    openPanel = true,
  ): void {
    describedAction.value = description;
    activeContext.value = context;
    activeTaskId.value = null;
    actionOpen.value = openPanel;
  }
  function closeAction(): void {
    actionOpen.value = false;
    describedAction.value = null;
    activeContext.value = null;
  }
  function startBusy(clearError = true): void {
    busy.value = true;
    if (clearError) lastError.value = null;
  }
  function finishBusy(): void { busy.value = false; }
  function fail(error: unknown): void {
    busy.value = false;
    lastError.value = error instanceof Error ? error.message : String(error);
  }
  function failIfNoError(error: unknown): void {
    if (lastError.value === null) {
      lastError.value = error instanceof Error ? error.message : String(error);
    }
  }

  return {
    beginCatalogRead,
    isCurrentCatalogRead,
    beginObservedCatalogRead,
    settleObservedCatalogRead,
    beginCatalogWriteBarrier,
    endCatalogWriteBarrier,
    refreshCatalog,
    nativeTaskReceipts,
    taskForId,
    taskForAction,
    tasksForAction,
    projectKey,
    projectRevision,
    projectContextReady,
    projectContextGeneration,
    currentUser,
    hostVersion,
    catalogRevision,
    plugins,
    selectedPlugin,
    installPlan,
    describedAction,
    activeContext,
    activeTask,
    pendingConfirmation,
    actionOpen,
    busy,
    lastError,
    setProjectContext,
    updateProjectRevision,
    setHostContext,
    replaceCatalog,
    applyPlugin,
    removePlugin,
    applyTask,
    applyInteraction,
    clearConfirmation,
    selectPlugin,
    beginAction,
    closeAction,
    projectInstallPlan,
    startBusy,
    finishBusy,
    fail,
    failIfNoError,
  };
});
