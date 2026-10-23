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

export const usePluginStore = defineStore("plugins", () => {
  const projectKey = ref("");
  const projectRevision = ref("");
  const projectContextReady = ref(false);
  const projectContextGeneration = ref(0);
  // Shared by every service instance; this orders UI reads, not authority data.
  let catalogReadSequence = 0;
  let catalogRefreshRunning = false;
  let catalogRefreshAgain = false;
  // Queued refresh candidates: each legal consumer registers its stable
  // reader with an owner-current predicate captured at queue time. A single
  // reader slot would let one retired owner swallow the whole notification.
  let catalogRefreshPending = new Map<() => Promise<readonly PluginSnapshot[]>, () => boolean>();
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
  function refreshCatalog(
    read: () => Promise<readonly PluginSnapshot[]>,
    ownerCurrent: () => boolean = () => true,
  ): void {
    invalidateCatalogReads();
    // Keying by the stable reader merges repeated notifications from one
    // owner instead of stacking candidates.
    catalogRefreshPending.set(read, ownerCurrent);
    catalogRefreshAgain = true;
    if (catalogRefreshRunning) return;
    catalogRefreshRunning = true;
    void Promise.resolve().then(async () => {
      try {
        // One active read and one dirty bit coalesce a recovered event backlog.
        while (catalogRefreshAgain && catalogRefreshPending.size > 0) {
          const batch = [...catalogRefreshPending];
          catalogRefreshPending = new Map();
          catalogRefreshAgain = false;
          while (batch.length > 0) {
            // Owners retired before this batch reads drop out; any one live
            // candidate serves the whole notification.
            const chosen = batch.find(([, current]) => current());
            if (!chosen) break;
            const [read, ownerCurrent] = chosen;
            batch.splice(batch.indexOf(chosen), 1);
            const generation = projectContextGeneration.value;
            const reading = read();
            const sequence = catalogReadSequence;
            const batchIsCurrent = () => generation === projectContextGeneration.value
              && isCurrentCatalogRead(sequence);
            try {
              await reading;
              // A reader retired mid-flight may have suppressed its projection;
              // one other live candidate takes over the read exactly once.
              if (!ownerCurrent() && batchIsCurrent()
                && batch.some(([, current]) => current())) continue;
              break;
            } catch (error) {
              // A live reader's failure stays a current user-visible error
              // and ends the batch without automatic retry; a retired reader
              // only hands over to a still-live candidate.
              if (ownerCurrent()) {
                if (batchIsCurrent()) failIfNoError(error);
                break;
              }
              if (batchIsCurrent() && batch.some(([, current]) => current())) continue;
              break;
            }
          }
        }
      } finally {
        catalogRefreshRunning = false;
      }
    });
  }

  function projectInstallPlan(plan: PluginInstallPlan | null): void {
    installPlan.value = plan;
  }

  function setProjectContext(key: string, revision: string): void {
    invalidateCatalogReads();
    catalogRefreshAgain = false;
    catalogRefreshPending = new Map();
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
    // The in-place revision advance rotates the generation like a same-key
    // reopen: settle the previous generation's busy/error synchronously.
    busy.value = false;
    lastError.value = null;
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
