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

const DEFAULT_PROJECT_KEY = "local:default";
const DEFAULT_PROJECT_REVISION = "0";

export const usePluginStore = defineStore("plugins", () => {
  const projectKey = ref(DEFAULT_PROJECT_KEY);
  const projectRevision = ref(DEFAULT_PROJECT_REVISION);
  const currentUser = ref<Readonly<Record<string, unknown>>>({});
  const hostVersion = ref("unknown");
  const catalogRevision = ref(-1);
  const pluginById = ref<Record<string, PluginSnapshot>>({});
  const taskById = ref<Record<string, PluginTaskViewSnapshot>>({});
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
  const pendingConfirmation = computed<WebPluginConfirmationPreview | null>(() =>
    activeTask.value?.confirmation ?? null,
  );

  function setProjectContext(key: string, revision: string): void {
    if (projectKey.value !== key) {
      pluginById.value = {};
      taskById.value = {};
      taskRevisionById.value = {};
      interactionRevisionByRun.value = {};
      pendingInteractionByRun.value = {};
      selectedPluginId.value = null;
      activeTaskId.value = null;
      catalogRevision.value = -1;
      installPlan.value = null;
      describedAction.value = null;
      activeContext.value = null;
      actionOpen.value = false;
      busy.value = false;
      lastError.value = null;
    }
    projectKey.value = key;
    projectRevision.value = revision;
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
    if (current && snapshot.revision <= current.revision) return;
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
    taskRevisionById.value = {
      ...taskRevisionById.value,
      [snapshot.taskId]: taskRevision,
    };
    const interactionRevision = interactionRevisionByRun.value[snapshot.runId] ?? 0;
    const terminal = ["succeeded", "failed", "cancelled", "aborted"].includes(snapshot.state);
    const projected: PluginTaskViewSnapshot = {
      ...snapshot,
      revision: Math.max(taskRevision, interactionRevision),
      progressPercent: snapshot.progress?.total
        ? Math.min(100, Math.round((snapshot.progress.current / snapshot.progress.total) * 100))
        : current?.progressPercent ?? null,
      progressMessage: snapshot.progress?.message || current?.progressMessage || null,
      confirmation: terminal ? null : current?.confirmation ?? null,
    };
    taskById.value = { ...taskById.value, [snapshot.taskId]: projected };
    activeTaskId.value = snapshot.taskId;
    const pendingInteraction = pendingInteractionByRun.value[snapshot.runId];
    if (pendingInteraction) {
      const nextPending = { ...pendingInteractionByRun.value };
      delete nextPending[snapshot.runId];
      pendingInteractionByRun.value = nextPending;
      applyInteraction(pendingInteraction.snapshot, pendingInteraction.revision);
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
  ): void {
    describedAction.value = description;
    activeContext.value = context;
    activeTaskId.value = null;
    actionOpen.value = true;
  }
  function closeAction(): void {
    actionOpen.value = false;
    describedAction.value = null;
    activeContext.value = null;
  }
  function setInstallPlan(plan: PluginInstallPlan | null): void {
    installPlan.value = plan;
  }
  function startBusy(): void { busy.value = true; lastError.value = null; }
  function finishBusy(): void { busy.value = false; }
  function fail(error: unknown): void {
    busy.value = false;
    lastError.value = error instanceof Error ? error.message : String(error);
  }

  return {
    projectKey,
    projectRevision,
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
    setInstallPlan,
    startBusy,
    finishBusy,
    fail,
  };
});
