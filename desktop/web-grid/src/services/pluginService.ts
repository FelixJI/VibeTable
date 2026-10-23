import type {
  PluginAction,
  PluginActionAvailability,
  PluginAuditEvent,
  PluginCommandContext,
  PluginEventEnvelope,
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
import { BridgeRequestRetiredError } from "@/bridge/hostBridge";
import { useUiStore } from "@/stores/uiStore";
import { usePluginStore } from "@/stores/pluginStore";
import {
  PluginInstallPlanLease,
  PluginInstallPlanStaleError,
  PluginProjectNotReadyError,
  type PluginProjectContext,
} from "./pluginInstallPlanLease";

const installPlanLeases = new WeakMap<object, PluginInstallPlanLease>();

function installPlanLeaseFor(owner: object): PluginInstallPlanLease {
  const existing = installPlanLeases.get(owner);
  if (existing) return existing;
  const created = new PluginInstallPlanLease();
  installPlanLeases.set(owner, created);
  return created;
}

export interface PluginService {
  init(): void;
  dispose(): void;
  openProjectContext(projectKey: string, projectRevision: string): void;
  updateProjectRevision(projectRevision: string): void;
  list(): Promise<readonly PluginSnapshot[]>;
  listAudit(pluginId: string): Promise<readonly PluginAuditEvent[]>;
  listPendingCleanup(): Promise<readonly PluginAuditEvent[]>;
  retryCleanup(event: PluginAuditEvent): Promise<PluginUninstallResult>;
  inspectInstall(sourceLocation: string): Promise<PluginInstallPlan>;
  inspectGitHubInstall(repository: string): Promise<PluginInstallPlan>;
  commitInstall(plan: PluginInstallPlan): Promise<PluginSnapshot>;
  cancelInstall(planId: string): Promise<boolean>;
  setEnabled(plugin: PluginSnapshot, enabled: boolean): Promise<PluginSnapshot>;
  upgrade(plugin: PluginSnapshot, plan: PluginInstallPlan): Promise<PluginSnapshot>;
  rollback(plugin: PluginSnapshot): Promise<PluginSnapshot>;
  uninstall(plugin: PluginSnapshot, cleanupPrivateSettings: boolean): Promise<PluginUninstallResult>;
  describeAction(
    pluginId: string,
    actionId: string,
    context: PluginCommandContext,
    openPanel?: boolean,
    isCurrentRequest?: () => boolean,
  ): Promise<PluginActionAvailability>;
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
  const installPlanLease = installPlanLeaseFor(store);
  let initialized = false;
  let lifecycleRevision = 0;
  let unsubscribe: Array<() => void> = [];
  const taskPollers = new Map<string, { cancelled: boolean }>();

  function applyEnvelope(envelope: PluginEventEnvelope): void {
    const snapshot = envelope.snapshot;
    if (envelope.projectKey !== store.projectKey) return;
    if (envelope.eventType === "plugin.task.changed" && isTaskSnapshot(snapshot)) {
      store.applyTask(snapshot, envelope.revision);
    } else if (envelope.eventType === "plugin.interaction.requested" && isInteractionSnapshot(snapshot)) {
      store.applyInteraction(snapshot, envelope.revision);
    } else if (envelope.eventType === "plugin.catalog.changed" && isPluginSnapshot(snapshot)) {
      // SSE can replay an old installation; only a current authority read may replace it.
      store.refreshCatalog(list);
    }
  }

  function init(): void {
    if (initialized) return;
    initialized = true;
    unsubscribe = [
      bridge.on("plugin.catalog.changed", applyEnvelope),
      bridge.on("realtime.recovered", () => {
        if (store.projectContextReady) void list().catch(() => { /* list records the visible failure. */ });
      }),
      bridge.on("plugin.task.changed", applyEnvelope),
      bridge.on("plugin.interaction.requested", applyEnvelope),
    ];
  }

  function dispose(): void {
    lifecycleRevision++;
    for (const stop of unsubscribe) stop();
    unsubscribe = [];
    for (const poller of taskPollers.values()) poller.cancelled = true;
    taskPollers.clear();
    initialized = false;
  }

  type RequestErrorPolicy = "foreground" | "background";

  async function call<K extends WebMessageType, T>(
    type: K,
    payload: WebPayloadMap[K],
    options: { readonly errorPolicy?: RequestErrorPolicy; readonly isCurrent?: () => boolean; readonly isErrorCurrent?: () => boolean } = {},
  ): Promise<T> {
    const context = ensureProjectReady();
    const isCurrentContext = () => store.projectKey === context.projectKey
      && store.projectContextGeneration === context.generation && (options.isCurrent?.() ?? true);
    const errorPolicy = options.errorPolicy ?? "foreground";
    // Background refreshes must not toggle buttons or release a foreground operation.
    if (errorPolicy === "foreground") store.startBusy();
    try {
      const result = await bridge.request(type, payload);
      if (errorPolicy === "foreground" && isCurrentContext()) store.finishBusy();
      return result as T;
    } catch (error) {
      // A late terminal from a retired generation must not fail or un-busy
      // the next project context; the scope/reset lifecycle settles it. The
      // local retirement of a draining workspace read is not a current user
      // failure either. Everything else keeps its original visibility.
      if (isCurrentContext() && !(error instanceof BridgeRequestRetiredError)) {
        if (options.isErrorCurrent && !options.isErrorCurrent()) {
          if (errorPolicy === "foreground") store.finishBusy();
          throw error;
        }
        if (errorPolicy === "foreground") {
          store.fail(error);
        } else {
          store.failIfNoError(error);
        }
      }
      throw error;
    }
  }

  async function list(isErrorCurrent?: () => boolean): Promise<readonly PluginSnapshot[]> {
    // dispose() retires this instance's in-flight catalog reads even while the
    // shared project context is unchanged; without the lifecycle guard a
    // retired owner's late success/failure could still write the shared
    // plugin store that a newer owner now consumes.
    const requestLifecycle = lifecycleRevision;
    const isInstanceCurrent = () => lifecycleRevision === requestLifecycle;
    const projectKey = store.projectKey;
    const generation = store.projectContextGeneration;
    const sequence = store.beginCatalogRead();
    const snapshots = await call<"plugin.catalog.list", readonly PluginSnapshot[]>(
      "plugin.catalog.list",
      { projectKey },
      {
        errorPolicy: "background",
        isCurrent: () => isInstanceCurrent() && store.isCurrentCatalogRead(sequence),
        isErrorCurrent: () => isInstanceCurrent() && (isErrorCurrent?.() ?? true),
      },
    );
    if (!isInstanceCurrent() || store.projectKey !== projectKey || store.projectContextGeneration !== generation
      || !store.isCurrentCatalogRead(sequence)) return snapshots;
    store.replaceCatalog(projectKey, snapshots);
    return snapshots;
  }

  async function mutateCatalog<T>(request: () => Promise<T>, apply: (result: T) => void): Promise<T> {
    const projectKey = store.projectKey;
    const generation = store.projectContextGeneration;
    const sequence = store.beginCatalogRead();
    const currentContext = () => store.projectKey === projectKey && store.projectContextGeneration === generation;
    try {
      const result = await request();
      if (currentContext() && store.isCurrentCatalogRead(sequence)) {
        apply(result);
      }
      return result;
    } finally {
      // A consumed notification may have settled before this write; read authority in either order.
      if (currentContext()) store.refreshCatalog(list);
    }
  }

  async function inspectInstall(sourceLocation: string): Promise<PluginInstallPlan> {
    return inspectPlan("plugin.install.inspect", { sourceLocation });
  }

  function currentInstallPlanContext(): PluginProjectContext {
    return {
      ready: store.projectContextReady,
      projectKey: store.projectKey,
      projectRevision: store.projectRevision,
      generation: store.projectContextGeneration,
    };
  }

  function ensureProjectReady(): PluginProjectContext {
    const context = currentInstallPlanContext();
    if (!context.ready || !context.projectKey || !context.projectRevision) {
      const error = new PluginProjectNotReadyError();
      store.fail(error);
      throw error;
    }
    return context;
  }

  async function inspectPlan(
    type: "plugin.install.inspect" | "plugin.install.github.inspect",
    payload: { readonly sourceLocation: string } | { readonly repository: string },
  ): Promise<PluginInstallPlan> {
    const context = ensureProjectReady();
    const { inspection, releasedPlan } = installPlanLease.begin(context);
    syncInstallPlan();
    store.startBusy();
    if (releasedPlan) await releaseInstallPlan(releasedPlan.planId);
    try {
      const plan = type === "plugin.install.inspect"
        ? await bridge.request(type, {
          projectKey: context.projectKey,
          projectRevision: context.projectRevision,
          sourceLocation: "sourceLocation" in payload ? payload.sourceLocation : "",
        })
        : await bridge.request(type, {
          projectKey: context.projectKey,
          projectRevision: context.projectRevision,
          repository: "repository" in payload ? payload.repository : "",
        });
      if (!installPlanLease.admit(inspection, currentInstallPlanContext(), plan)) {
        await releaseInstallPlan(plan.planId);
        throw new PluginInstallPlanStaleError();
      }
      syncInstallPlan();
      store.finishBusy();
      return plan;
    } catch (error) {
      if (installPlanLease.fail(inspection)) store.fail(error);
      throw error;
    }
  }

  async function inspectGitHubInstall(repository: string): Promise<PluginInstallPlan> {
    return inspectPlan("plugin.install.github.inspect", { repository });
  }

  async function cancelInstall(planId: string): Promise<boolean> {
    installPlanLease.release(planId);
    syncInstallPlan();
    try {
      const result = await call<"plugin.install.cancel", { readonly cancelled: boolean }>(
        "plugin.install.cancel",
        { planId },
      );
      return result.cancelled;
    } finally { syncInstallPlan(); }
  }

  async function commitInstall(plan: PluginInstallPlan): Promise<PluginSnapshot> {
    const consumed = consumeInstallPlan(plan);
    if (consumed.targetInstallationId != null || consumed.targetRevision != null) {
      await releaseInstallPlan(consumed.planId);
      const error = new PluginInstallPlanStaleError(); store.fail(error); throw error;
    }
    return mutateCatalog(() => call<"plugin.install.commit", PluginSnapshot>("plugin.install.commit", {
      planId: consumed.planId,
      projectRevision: store.projectRevision,
    }), snapshot => store.applyPlugin(snapshot));
  }

  async function releaseInstallPlan(planId: string): Promise<void> {
    try {
      await bridge.request("plugin.install.cancel", { planId });
    } catch { /* best effort: the invalidated plan is already unusable locally */ }
  }

  function releaseChangedContextPlan(plan: PluginInstallPlan | null): void {
    if (plan) void releaseInstallPlan(plan.planId);
  }

  function openProjectContext(projectKey: string, projectRevision: string): void {
    const releasedPlan = installPlanLease.invalidate();
    store.setProjectContext(projectKey, projectRevision);
    syncInstallPlan();
    releaseChangedContextPlan(releasedPlan);
  }

  function updateProjectRevision(projectRevision: string): void {
    if (!store.updateProjectRevision(projectRevision)) return;
    const releasedPlan = installPlanLease.invalidate();
    syncInstallPlan();
    releaseChangedContextPlan(releasedPlan);
  }

  async function applyPluginRequest<K extends
    | "plugin.lifecycle.setEnabled"
    | "plugin.lifecycle.upgrade"
    | "plugin.lifecycle.rollback"
  >(type: K, payload: WebPayloadMap[K]): Promise<PluginSnapshot> {
    return mutateCatalog(() => call<K, PluginSnapshot>(type, payload), snapshot => store.applyPlugin(snapshot));
  }

  async function upgrade(
    plugin: PluginSnapshot,
    plan: PluginInstallPlan,
  ): Promise<PluginSnapshot> {
    const consumed = consumeInstallPlan(plan);
    const current = store.plugins.find(item => item.pluginId === plugin.pluginId);
    if (consumed.manifest.pluginId !== plugin.pluginId || !consumed.targetInstallationId
      || consumed.targetInstallationId !== plugin.installationId || consumed.targetRevision !== plugin.revision
      || current?.installationId !== consumed.targetInstallationId || current.revision !== consumed.targetRevision) {
      await releaseInstallPlan(consumed.planId);
      const error = new PluginInstallPlanStaleError();
      store.fail(error);
      throw error;
    }
    const snapshot = await applyPluginRequest("plugin.lifecycle.upgrade", {
      projectKey: plugin.projectKey,
      pluginId: plugin.pluginId,
      planId: consumed.planId,
      projectRevision: store.projectRevision,
    });
    return snapshot;
  }

  function consumeInstallPlan(plan: PluginInstallPlan): PluginInstallPlan {
    if (plan.blockingReasons?.length) throw new Error("插件需要迁移，无法提交安装计划。");
    try {
      const consumed = installPlanLease.consume(plan, ensureProjectReady());
      syncInstallPlan();
      return consumed;
    } catch (error) {
      store.fail(error);
      throw error;
    }
  }

  function syncInstallPlan(): void {
    store.projectInstallPlan(installPlanLease.plan);
  }

  async function getTask(taskId: string): Promise<PluginTaskViewSnapshot> {
    const task = await call<"plugin.task.get", PluginTaskSnapshot>(
      "plugin.task.get",
      { taskId },
      { errorPolicy: "background" },
    );
    return store.applyTask(task);
  }

  function pollTaskUntilTerminal(taskId: string): void {
    const previous = taskPollers.get(taskId);
    if (previous) previous.cancelled = true;
    const poller = { cancelled: false };
    taskPollers.set(taskId, poller);
    const deadline = Date.now() + 310_000;
    void (async () => {
      try {
        while (!poller.cancelled && Date.now() < deadline) {
          await new Promise<void>((resolve) => setTimeout(resolve, 150));
          if (poller.cancelled) break;
          try {
            const snapshot = await bridge.request("plugin.task.get", { taskId });
            if (!isTaskSnapshot(snapshot)) continue;
            const current = store.applyTask(snapshot);
            if (["succeeded", "failed", "cancelled", "aborted"].includes(current.state)) break;
          } catch {
            // Notifications remain the primary fast path. A transient bridge
            // read failure must not turn the reconciliation fallback itself
            // into a user-visible error.
          }
        }
      } finally {
        if (taskPollers.get(taskId) === poller) taskPollers.delete(taskId);
      }
    })();
  }

  async function describeAction(
    pluginId: string,
    actionId: string,
    context: PluginCommandContext,
    openPanel = true,
    isCurrentRequest?: () => boolean,
  ): Promise<PluginActionAvailability> {
    const ui = useUiStore();
    const requestLifecycle = lifecycleRevision;
    const navigation = openPanel ? ui.beginNavigation() : null;
    const projectKey = store.projectKey;
    const generation = store.projectContextGeneration;
    const original = store.plugins.find(plugin => plugin.pluginId === pluginId);
    const isCurrent = () => lifecycleRevision === requestLifecycle && store.projectKey === projectKey && store.projectContextGeneration === generation
      && (navigation === null || ui.navigationRevision === navigation) && (isCurrentRequest?.() ?? true)
      && (!original || store.plugins.some(plugin => plugin.pluginId === pluginId
        && plugin.installationId === original.installationId && plugin.revision === original.revision));
    const retired = { available: false, reasons: [] };
    try {
      if (original?.manifest.compatibility.pluginApi === "3.x") await list(isCurrent);
      if (!isCurrent()) return retired;
      const availability = await call<"plugin.action.describe", PluginActionAvailability>(
        "plugin.action.describe", { projectKey: store.projectKey, pluginId, actionId, context },
        { isErrorCurrent: isCurrent },
      );
      if (!isCurrent()) return retired;
      if (!availability.available) throw new Error(availability.reasons.join("；") || "当前动作不可用");
      const plugin = store.plugins.find((item) => item.pluginId === pluginId);
      const action = plugin?.manifest.actions.find((item) => item.actionId === actionId);
      if (!plugin || !action) throw new Error("插件动作清单已变化，请刷新目录");
      if (action.invocation !== "manual") throw new Error("该动作不支持手动启动");
      const description = projectAction(plugin, action, context.locale);
      const application = description.presentation === "application";
      store.beginAction(description, context, openPanel && !application);
      if (application && openPanel) ui.openPluginApp(pluginId, actionId);
      return availability;
    } catch (error) {
      if (!isCurrent()) return retired;
      throw error;
    }
  }

  return {
    init,
    dispose,
    openProjectContext,
    updateProjectRevision,
    list,
    listAudit: (pluginId) => call(
      "plugin.audit.list",
      { projectKey: store.projectKey, pluginId },
      { errorPolicy: "background" },
    ),
    listPendingCleanup: () => call(
      "plugin.cleanup.listPending",
      { projectKey: store.projectKey },
      { errorPolicy: "background" },
    ),
    retryCleanup: async (event) => {
      const installationId = event.details.installationId;
      const expectedRevision = event.details.expectedRevision;
      if (typeof installationId !== "string" || !installationId || typeof expectedRevision !== "number"
        || !Number.isInteger(expectedRevision) || expectedRevision < 1)
        throw new Error("清理证据缺少原安装身份，不能重试卸载");
      return call("plugin.lifecycle.uninstall", { projectKey: event.projectKey, pluginId: event.pluginId,
        installationId, expectedRevision, cleanupPrivateSettings: false });
    },
    inspectInstall,
    inspectGitHubInstall,
    commitInstall,
    cancelInstall,
    setEnabled: (plugin, enabled) => applyPluginRequest("plugin.lifecycle.setEnabled", {
      projectKey: plugin.projectKey,
      pluginId: plugin.pluginId,
      enabled,
      installationId: plugin.installationId ?? "",
      expectedRevision: plugin.revision,
    }),
    upgrade,
    rollback: (plugin) => applyPluginRequest("plugin.lifecycle.rollback", {
      projectKey: plugin.projectKey,
      pluginId: plugin.pluginId,
    }),
    uninstall: async (plugin, cleanupPrivateSettings) => {
      const installationId = plugin.installationId;
      if (!installationId) throw new Error("安装身份缺失，不能卸载");
      return mutateCatalog(() => call<"plugin.lifecycle.uninstall", PluginUninstallResult>(
        "plugin.lifecycle.uninstall",
        { projectKey: plugin.projectKey, pluginId: plugin.pluginId, installationId, expectedRevision: plugin.revision, cleanupPrivateSettings },
      ), result => { if (result.uninstalled) store.removePlugin(plugin.pluginId); });
    },
    describeAction,
    startAction: async (pluginId, actionId, input, context) => {
      const action = store.plugins.find(plugin => plugin.pluginId === pluginId)
        ?.manifest.actions.find(item => item.actionId === actionId);
      if (action?.presentation === "application" || action?.presentation === "headless")
        throw new Error("应用与无界面动作通过受限页面API执行，不启动Worker");
      const task = await call<"plugin.action.start", PluginTaskSnapshot>("plugin.action.start", {
        projectKey: store.projectKey,
        pluginId,
        actionId,
        context,
        input,
      });
      const projected = store.applyTask(task);
      if (["queued", "running"].includes(projected.state)) {
        pollTaskUntilTerminal(projected.taskId);
      }
      return projected;
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
      let current = await getTask(task.taskId);
      const deadline = Date.now() + 5_000;
      while (
        (current.state === "queued" || current.state === "running")
        && Date.now() < deadline
      ) {
        await new Promise<void>((resolve) => setTimeout(resolve, 100));
        current = await getTask(task.taskId);
      }
      return current;
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
  const title = localize(action.displayName, locale, action.actionId);
  if (action.presentation === "application" || action.presentation === "headless") {
    // Application/headless surfaces are host-projected onto the action
    // itself; the legacy ui.customViews channel is not consulted.
    return {
      pluginId: plugin.pluginId,
      actionId: action.actionId,
      title,
      description: localize(action.description, locale, ""),
      risk: action.risk,
      inputSchema: { type: "object", properties: {} },
      uiSchema: undefined,
      presentation: action.presentation,
      surface: action.surfaceSrc && action.surfaceToken
        ? {
          src: action.surfaceSrc,
          surfaceToken: action.surfaceToken,
          title: action.surfaceTitle ?? title,
        }
        : null,
    };
  }
  const customView = readCustomView(plugin.manifest.ui, action.actionId);
  return {
    pluginId: plugin.pluginId,
    actionId: action.actionId,
    title,
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
