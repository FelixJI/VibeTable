import type {
  PluginAction,
  PluginActionAvailability,
  PluginAuditEvent,
  PluginCommandContext,
  PluginEventEnvelope,
  PluginExternalFlowCandidate,
  PluginFlowBindingSnapshot,
  PluginInstallPlan,
  PluginInteractionSnapshot,
  PluginSnapshot,
  PluginTaskSnapshot,
  PluginTaskViewSnapshot,
  PluginUninstallResult,
  WebMessageType,
  WebPayloadMap,
  WebPluginActionDescription,
} from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { usePluginStore } from "@/stores/pluginStore";

export interface PluginService {
  init(): void;
  dispose(): void;
  list(): Promise<readonly PluginSnapshot[]>;
  listAudit(pluginId: string): Promise<readonly PluginAuditEvent[]>;
  listPendingCleanup(): Promise<readonly PluginAuditEvent[]>;
  retryCleanup(pluginId: string): Promise<PluginUninstallResult>;
  inspectInstall(sourceLocation: string): Promise<PluginInstallPlan>;
  commitInstall(plan: PluginInstallPlan): Promise<PluginSnapshot>;
  listExternalFlows(pluginId: string, logicalFlowId: string): Promise<readonly PluginExternalFlowCandidate[]>;
  bindExternalFlow(input: {
    pluginId: string;
    logicalFlowId: string;
    directusFlowUuid: string;
    acceptsUnknownSideEffects: boolean;
  }): Promise<PluginFlowBindingSnapshot>;
  setEnabled(plugin: PluginSnapshot, enabled: boolean): Promise<PluginSnapshot>;
  upgrade(plugin: PluginSnapshot, plan: PluginInstallPlan): Promise<PluginSnapshot>;
  rollback(plugin: PluginSnapshot): Promise<PluginSnapshot>;
  resolveDrift(plugin: PluginSnapshot, logicalFlowId: string, strategy: "restore" | "detach"): Promise<PluginSnapshot>;
  uninstall(plugin: PluginSnapshot, cleanupPrivateSettings: boolean): Promise<PluginUninstallResult>;
  describeAction(pluginId: string, actionId: string, context: PluginCommandContext): Promise<PluginActionAvailability>;
  startAction(pluginId: string, actionId: string, input: Readonly<Record<string, unknown>>, context: PluginCommandContext): Promise<PluginTaskViewSnapshot>;
  resolveInteraction(task: PluginTaskViewSnapshot, decision: "approved" | "rejected"): Promise<PluginTaskViewSnapshot>;
  cancelTask(taskId: string): Promise<PluginTaskViewSnapshot>;
  getTask(taskId: string): Promise<PluginTaskViewSnapshot>;
}

export function createPluginCommandContext(input: {
  projectKey: string;
  collection?: string | null;
  selectedKeys?: readonly (string | number)[];
  querySnapshot?: Readonly<Record<string, unknown>> | null;
  locale: string;
  theme: "light" | "dark";
  density: string;
  user?: Readonly<Record<string, unknown>>;
  hostVersion?: string;
}): PluginCommandContext {
  return {
    contract: "vibetable.command-context.v1",
    projectKey: input.projectKey,
    collection: input.collection ?? null,
    selectedKeys: input.selectedKeys ?? [],
    querySnapshot: input.querySnapshot ?? null,
    locale: input.locale,
    theme: input.theme,
    density: input.density,
    user: input.user ?? {},
    hostVersion: input.hostVersion?.trim() || "unknown",
  };
}

export function usePluginService(): PluginService {
  const bridge = useHostBridge();
  const store = usePluginStore();
  let initialized = false;
  let unsubscribe: Array<() => void> = [];

  function applyEnvelope(envelope: PluginEventEnvelope): void {
    const snapshot = envelope.snapshot;
    if (envelope.projectKey !== store.projectKey) return;
    if (envelope.eventType === "plugin.task.changed" && isTaskSnapshot(snapshot)) {
      store.applyTask(snapshot, envelope.revision);
    } else if (envelope.eventType === "plugin.interaction.requested" && isInteractionSnapshot(snapshot)) {
      store.applyInteraction(snapshot, envelope.revision);
    } else if (envelope.eventType === "plugin.catalog.changed" && isPluginSnapshot(snapshot)) {
      store.applyPlugin(snapshot, envelope.revision);
    }
  }

  function init(): void {
    if (initialized) return;
    initialized = true;
    unsubscribe = [
      bridge.on("plugin.catalog.changed", applyEnvelope),
      bridge.on("plugin.task.changed", applyEnvelope),
      bridge.on("plugin.interaction.requested", applyEnvelope),
    ];
  }

  function dispose(): void {
    for (const stop of unsubscribe) stop();
    unsubscribe = [];
    initialized = false;
  }

  async function call<K extends WebMessageType, T>(type: K, payload: WebPayloadMap[K]): Promise<T> {
    store.startBusy();
    try {
      const result = await bridge.request(type, payload);
      store.finishBusy();
      return result as T;
    } catch (error) {
      store.fail(error);
      throw error;
    }
  }

  async function list(): Promise<readonly PluginSnapshot[]> {
    const projectKey = store.projectKey;
    const snapshots = await call<"plugin.catalog.list", readonly PluginSnapshot[]>(
      "plugin.catalog.list",
      { projectKey },
    );
    if (store.projectKey !== projectKey) return snapshots;
    store.replaceCatalog(projectKey, snapshots);
    for (const snapshot of snapshots) {
      for (const binding of snapshot.flowBindings ?? []) store.applyBinding(binding);
    }
    return snapshots;
  }

  async function inspectInstall(sourceLocation: string): Promise<PluginInstallPlan> {
    const plan = await call<"plugin.install.inspect", PluginInstallPlan>("plugin.install.inspect", {
      projectKey: store.projectKey,
      projectRevision: store.projectRevision,
      sourceLocation,
    });
    store.setInstallPlan(plan);
    return plan;
  }

  async function commitInstall(plan: PluginInstallPlan): Promise<PluginSnapshot> {
    const snapshot = await call<"plugin.install.commit", PluginSnapshot>("plugin.install.commit", {
      planId: plan.planId,
      projectRevision: store.projectRevision,
    });
    store.applyPlugin(snapshot);
    for (const binding of snapshot.flowBindings ?? []) store.applyBinding(binding);
    store.setInstallPlan(null);
    return snapshot;
  }

  async function applyPluginRequest<K extends
    | "plugin.lifecycle.setEnabled"
    | "plugin.lifecycle.upgrade"
    | "plugin.lifecycle.rollback"
    | "plugin.lifecycle.resolveDrift"
  >(type: K, payload: WebPayloadMap[K]): Promise<PluginSnapshot> {
    const snapshot = await call<K, PluginSnapshot>(type, payload);
    store.applyPlugin(snapshot);
    for (const binding of snapshot.flowBindings ?? []) store.applyBinding(binding);
    return snapshot;
  }

  async function getTask(taskId: string): Promise<PluginTaskViewSnapshot> {
    const task = await call<"plugin.task.get", PluginTaskSnapshot>("plugin.task.get", { taskId });
    return store.applyTask(task);
  }

  async function describeAction(
    pluginId: string,
    actionId: string,
    context: PluginCommandContext,
  ): Promise<PluginActionAvailability> {
    const availability = await call<"plugin.action.describe", PluginActionAvailability>(
      "plugin.action.describe",
      { projectKey: store.projectKey, pluginId, actionId, context },
    );
    if (!availability.available) throw new Error(availability.reasons.join("；") || "当前动作不可用");
    const plugin = store.plugins.find((item) => item.pluginId === pluginId);
    const action = plugin?.manifest.actions.find((item) => item.actionId === actionId);
    if (!plugin || !action) throw new Error("插件动作清单已变化，请刷新目录");
    store.beginAction(projectAction(plugin, action, context.locale), context);
    return availability;
  }

  return {
    init,
    dispose,
    list,
    listAudit: (pluginId) => call("plugin.audit.list", {
      projectKey: store.projectKey,
      pluginId,
    }),
    listPendingCleanup: () => call("plugin.cleanup.listPending", {
      projectKey: store.projectKey,
    }),
    retryCleanup: (pluginId) => call("plugin.lifecycle.uninstall", {
      projectKey: store.projectKey,
      pluginId,
      cleanupPrivateSettings: false,
    }),
    inspectInstall,
    commitInstall,
    listExternalFlows: (pluginId, logicalFlowId) => call(
      "plugin.externalFlow.listCandidates",
      { projectKey: store.projectKey, pluginId, logicalFlowId },
    ),
    bindExternalFlow: async (input) => {
      const binding = await call<"plugin.externalFlow.bind", PluginFlowBindingSnapshot>(
        "plugin.externalFlow.bind",
        { projectKey: store.projectKey, ...input },
      );
      store.applyBinding(binding);
      return binding;
    },
    setEnabled: (plugin, enabled) => applyPluginRequest("plugin.lifecycle.setEnabled", {
      projectKey: plugin.projectKey,
      pluginId: plugin.pluginId,
      enabled,
    }),
    upgrade: (plugin, plan) => applyPluginRequest("plugin.lifecycle.upgrade", {
      projectKey: plugin.projectKey,
      pluginId: plugin.pluginId,
      planId: plan.planId,
      projectRevision: store.projectRevision,
    }),
    rollback: (plugin) => applyPluginRequest("plugin.lifecycle.rollback", {
      projectKey: plugin.projectKey,
      pluginId: plugin.pluginId,
    }),
    resolveDrift: (plugin, logicalFlowId, strategy) => applyPluginRequest(
      "plugin.lifecycle.resolveDrift",
      { projectKey: plugin.projectKey, pluginId: plugin.pluginId, logicalFlowId, strategy },
    ),
    uninstall: async (plugin, cleanupPrivateSettings) => {
      const result = await call<"plugin.lifecycle.uninstall", PluginUninstallResult>(
        "plugin.lifecycle.uninstall",
        { projectKey: plugin.projectKey, pluginId: plugin.pluginId, cleanupPrivateSettings },
      );
      if (result.uninstalled) store.removePlugin(plugin.pluginId);
      return result;
    },
    describeAction,
    startAction: async (pluginId, actionId, input, context) => {
      const task = await call<"plugin.action.start", PluginTaskSnapshot>("plugin.action.start", {
        projectKey: store.projectKey,
        pluginId,
        actionId,
        context,
        input,
      });
      return store.applyTask(task);
    },
    resolveInteraction: async (task, decision) => {
      if (!task.confirmation) throw new Error("当前任务没有待确认交互");
      const interactionId = task.confirmation.interactionId;
      await call("plugin.interaction.resolve", {
        runId: task.runId,
        interactionId,
        decision,
      });
      store.clearConfirmation(task.runId, interactionId);
      return getTask(task.taskId);
    },
    cancelTask: async (taskId) => {
      const task = await call<"plugin.task.cancel", PluginTaskSnapshot>("plugin.task.cancel", { taskId });
      return store.applyTask(task);
    },
    getTask,
  };
}

function projectAction(
  plugin: PluginSnapshot,
  action: PluginAction,
  locale: string,
): WebPluginActionDescription {
  const schema = action.inputSchema ? plugin.schemas[action.inputSchema] : undefined;
  const uiSchema = action.formSchema ? plugin.schemas[action.formSchema] : undefined;
  const customView = readCustomView(plugin.manifest.ui, action.actionId);
  return {
    pluginId: plugin.pluginId,
    actionId: action.actionId,
    title: localize(action.displayName, locale, action.actionId),
    description: localize(action.description, locale, ""),
    risk: action.risk,
    inputSchema: schema ?? { type: "object", properties: {} },
    uiSchema,
    presentation: customView ? "custom" : "standard",
    surface: customView,
  };
}

function localize(values: Readonly<Record<string, string>>, locale: string, fallback: string): string {
  return values[locale] ?? values["zh-CN"] ?? values["en-US"] ?? Object.values(values)[0] ?? fallback;
}

function readCustomView(
  ui: Readonly<Record<string, unknown>>,
  actionId: string,
): WebPluginActionDescription["surface"] {
  const views = ui.customViews;
  if (!Array.isArray(views)) return null;
  const value = views.find((item) => typeof item === "object" && item !== null
    && (item as Record<string, unknown>).actionId === actionId) as Record<string, unknown> | undefined;
  if (!value || typeof value.src !== "string" || typeof value.surfaceToken !== "string") return null;
  return {
    src: value.src,
    surfaceToken: value.surfaceToken,
    title: typeof value.title === "string" ? value.title : actionId,
  };
}

function isPluginSnapshot(value: unknown): value is PluginSnapshot {
  if (!isRecord(value)) return false;
  return typeof value.projectKey === "string"
    && typeof value.pluginId === "string"
    && typeof value.version === "string"
    && typeof value.packageHash === "string"
    && isRecord(value.manifest)
    && Array.isArray(value.flowRequirements)
    && typeof value.status === "string"
    && typeof value.revision === "number";
}

function isTaskSnapshot(value: unknown): value is PluginTaskSnapshot {
  if (!isRecord(value)) return false;
  return typeof value.taskId === "string"
    && typeof value.runId === "string"
    && typeof value.pluginId === "string"
    && typeof value.pluginVersion === "string"
    && typeof value.actionId === "string"
    && typeof value.projectKey === "string"
    && typeof value.state === "string";
}

function isInteractionSnapshot(value: unknown): value is PluginInteractionSnapshot {
  if (!isRecord(value)) return false;
  return typeof value.runId === "string"
    && typeof value.projectKey === "string"
    && typeof value.pluginId === "string"
    && typeof value.actionId === "string"
    && typeof value.caller === "string";
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
