<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { NAlert, NButton, NSpin } from "naive-ui";
import type { PluginTaskViewSnapshot } from "@/contracts";
import type {
  BindingVariable,
  InterfaceDefinition,
  ViewFilter,
} from "@/contracts/generated/workbench";
import { DashboardSchemaCatalog } from "@/services/dashboardBindingPorts";
import { useHostBridge } from "@/services/bridgeContext";
import { createPluginCommandContext, usePluginService } from "@/services/pluginService";
import { usePluginStore } from "@/stores/pluginStore";
import { useUiStore } from "@/stores/uiStore";
import { ActionRuntime } from "@/surfaces/surfaceCore";
import {
  HostSurfaceActionPorts,
  SurfaceRuntimeController,
  type SurfaceBindingData,
} from "@/surfaces/surfaceRuntime";
import { SurfaceCursorController } from "@/surfaces/surfaceCursor";
import InterfaceRuntimeElement from "./InterfaceRuntimeElement.vue";
import PluginActionPanel from "@/components/plugins/PluginActionPanel.vue";

const props = defineProps<{
  definition: InterfaceDefinition;
  activePageId: string;
  previewWidth: "desktop" | "tablet" | "mobile";
}>();
const emit = defineEmits<{ navigate: [pageId: string] }>();
const bridge = useHostBridge();
const schemaCatalog = new DashboardSchemaCatalog(bridge);
const pluginService = usePluginService();
const plugins = usePluginStore();
const ui = useUiStore();
const data = ref<Readonly<Record<string, SurfaceBindingData>>>({});
const selected = ref<Record<string, Readonly<Record<string, unknown>> | null>>({});
const forms = ref<Record<string, Readonly<Record<string, unknown>>>>({});
const cursors = new SurfaceCursorController(bridge);
const bindingFields = computed(() => Object.fromEntries(
  props.definition.bindings.map((binding) => [binding.bindingId, binding.query.fields]),
));
const actionKinds = computed(() => Object.fromEntries(
  props.definition.actions.map((action) => [action.actionId, action.kind]),
));
const actionState = ref<{
  state: "idle" | "running" | "failed" | "succeeded" | "rejected" | "cancelled";
  message: string;
}>({ state: "idle", message: "" });
const pluginTaskId = ref<string | null>(null);
const activePluginTask = computed(() => {
  const task = plugins.activeTask;
  return task?.taskId === pluginTaskId.value ? task : null;
});
const runtime = new SurfaceRuntimeController({
  async read(binding, signal) {
    signal.throwIfAborted();
    const filters = [
      ...binding.query.filters,
      ...binding.variables.map((variable): ViewFilter => ({
        fieldId: variable.targetFieldId,
        operator: variable.operator,
        value: variableValue(variable),
      })),
    ];
    return await cursors.read({
      bindingId: binding.bindingId,
      tableId: binding.query.tableId,
      initialCursor: binding.query.cursor,
      pageSize: binding.query.pageSize,
      query: {
        filters: filters.map((filter) => ({
          field: filter.fieldId,
          operator: queryOperator(filter.operator),
          value: filter.value,
        })),
        sorts: binding.query.sorts.map((sort) => ({
          field: sort.fieldId,
          direction: sort.direction,
        })),
      },
    }, signal);
  },
});
let activation = 0;
let actionController: AbortController | null = null;
let pluginResolution: "approved" | "rejected" | null = null;

async function activate(): Promise<void> {
  const generation = ++activation;
  const pending = runtime.activate(props.definition, props.activePageId, (binding, result) => {
    const current = selected.value[binding.bindingId];
    const currentKey = current?.rowKey;
    const next = currentKey === undefined
      ? result.rows[0] ?? null
      : result.rows.find((row) => row.rowKey === currentKey) ?? result.rows[0] ?? null;
    selected.value = { ...selected.value, [binding.bindingId]: next };
  });
  data.value = { ...runtime.data };
  await pending;
  if (generation !== activation) return;
  data.value = { ...runtime.data };
}

watch(
  () => [props.definition, props.activePageId] as const,
  () => {
    selected.value = {};
    cursors.reset();
    void activate();
  },
  { immediate: true, deep: true },
);
onBeforeUnmount(() => {
  activation += 1;
  actionController?.abort(new DOMException("Interface closed", "AbortError"));
  runtime.dispose();
});

function choose(bindingId: string, row: Readonly<Record<string, unknown>>): void {
  const dependents = dependentBindingIds(bindingId);
  selected.value = Object.fromEntries(Object.entries({ ...selected.value, [bindingId]: row })
    .filter(([candidate]) => !dependents.has(candidate)));
  cursors.reset(dependents);
  if (dependents.size > 0) void activate();
}

function queryOperator(operator: ViewFilter["operator"]): string {
  return operator === "startsWith" ? "starts_with" : operator;
}

function variableValue(variable: BindingVariable): string | number | boolean | null {
  if (variable.source === "literal") return variable.value;
  const row = selected.value[variable.sourceBindingId ?? ""];
  const candidate = row?.[variable.sourceFieldId ?? ""];
  return typeof candidate === "string"
    || typeof candidate === "number"
    || typeof candidate === "boolean"
    || candidate === null
    ? candidate
    : variable.value;
}

function dependentBindingIds(bindingId: string): Set<string> {
  const result = new Set<string>();
  const pending = [bindingId];
  while (pending.length > 0) {
    const source = pending.shift()!;
    for (const binding of props.definition.bindings) {
      if (result.has(binding.bindingId) || !binding.variables.some(
        (variable) => variable.source === "selectedRecordField"
          && variable.sourceBindingId === source,
      )) continue;
      result.add(binding.bindingId);
      pending.push(binding.bindingId);
    }
  }
  return result;
}

function changePage(bindingId: string, direction: -1 | 1): void {
  const changed = direction < 0 ? cursors.previous(bindingId) : cursors.next(bindingId);
  if (changed) void activate();
}

function patchForm(elementId: string, field: string, value: unknown): void {
  forms.value = { ...forms.value, [elementId]: { ...forms.value[elementId], [field]: value } };
}
async function execute(actionId: string, bindingId: string | null, elementId: string): Promise<void> {
  if (actionController) return;
  const action = props.definition.actions.find((item) => item.actionId === actionId);
  if (!action) return;
  const row = bindingId
    ? selected.value[bindingId] ?? data.value[bindingId]?.rows[0] ?? null
    : null;
  const values = {
    ...(action.kind === "record.update" ? row ?? {} : {}),
    ...(forms.value[elementId] ?? {}),
    ...(action.kind === "record.update" ? { __surfaceOriginal: row } : {}),
  };
  const controller = new AbortController();
  actionController = controller;
  pluginResolution = null;
  const ports = new HostSurfaceActionPorts(
    () => props.definition,
    bridge,
    schemaCatalog,
    async () => activate(),
    async (pageId) => emit("navigate", pageId),
    {
      async run(pluginId, pluginActionId, input, signal) {
        signal.throwIfAborted();
        const context = createPluginCommandContext({
          projectKey: plugins.projectKey,
		  collection: bindingId
			? props.definition.bindings.find((item) => item.bindingId === bindingId)?.query.tableId
            : null,
          selectedKeys: recordIdentity(row) === undefined ? [] : [recordIdentity(row)!],
          locale: ui.locale,
          theme: ui.themeMode === "dark" ? "dark" : "light",
          density: ui.density,
          user: plugins.currentUser,
          hostVersion: plugins.hostVersion,
        });
        await pluginService.describeAction(pluginId, pluginActionId, context, false);
        signal.throwIfAborted();
        const task = await pluginService.startAction(pluginId, pluginActionId, input, context);
        pluginTaskId.value = task.taskId;
        const terminal = await waitForPluginTask(task.taskId, signal);
        if (terminal.state === "succeeded") return terminal.result;
        if (terminal.state === "cancelled" || terminal.state === "aborted") {
          throw new DOMException("Plugin task cancelled", "AbortError");
        }
        const failure = new Error(terminal.error?.message ?? "插件动作执行失败");
        Object.assign(failure, { code: terminal.error?.code ?? "plugin_action_failed" });
        throw failure;
      },
    },
    async () => window.confirm("确认执行此操作？"),
  );
  actionState.value = { state: "running", message: "正在执行操作…" };
  try {
    const result = await new ActionRuntime(ports).execute(action, {
      definition: props.definition,
      values,
      recordId: recordIdentity(row),
    }, controller.signal);
    if (result.state === "succeeded") {
      actionState.value = { state: "succeeded", message: "操作已完成" };
      if (action.bindingId) await activate();
    } else if (pluginResolution === "rejected" || result.state === "rejected") {
      actionState.value = { state: "rejected", message: "插件操作已拒绝" };
    } else if (result.state === "cancelled") {
      actionState.value = { state: "cancelled", message: "插件操作已取消" };
    } else {
      actionState.value = { state: "failed", message: result.error.message };
    }
  } finally {
    if (actionController === controller) actionController = null;
  }
}

function waitForPluginTask(taskId: string, signal: AbortSignal): Promise<PluginTaskViewSnapshot> {
  const current = plugins.activeTask;
  if (current?.taskId === taskId && ["succeeded", "failed", "cancelled", "aborted"].includes(current.state)) {
    return Promise.resolve(current);
  }
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise((resolve, reject) => {
    let stop: () => void = () => {};
    const abort = () => {
      stop();
      void pluginService.cancelTask(taskId).catch(() => undefined);
      reject(signal.reason ?? new DOMException("Plugin task cancelled", "AbortError"));
    };
    stop = watch(
      () => plugins.activeTask,
      (task) => {
        if (task?.taskId !== taskId || !["succeeded", "failed", "cancelled", "aborted"].includes(task.state)) return;
        signal.removeEventListener("abort", abort);
        stop();
        resolve(task);
      },
    );
    signal.addEventListener("abort", abort, { once: true });
  });
}

async function resolvePluginInteraction(decision: "approved" | "rejected"): Promise<void> {
  const task = activePluginTask.value;
  if (!task) return;
  pluginResolution = decision;
  try {
    await pluginService.resolveInteraction(task, decision);
  } catch (error) {
    actionState.value = { state: "failed", message: error instanceof Error ? error.message : String(error) };
  }
}

async function cancelPluginTask(): Promise<void> {
  const task = activePluginTask.value;
  if (!task) return;
  try {
    await pluginService.cancelTask(task.taskId);
  } catch (error) {
    actionState.value = { state: "failed", message: error instanceof Error ? error.message : String(error) };
  }
}

function recordIdentity(row: Readonly<Record<string, unknown>> | null): string | number | undefined {
  const value = row?.rowKey ?? row?.id;
  return typeof value === "string" || typeof value === "number" ? value : undefined;
}
</script>

<template>
  <div class="runtime-shell" :class="`preview-${previewWidth}`" data-testid="interface-runtime">
    <NAlert v-if="actionState.state === 'failed'" type="error" closable>{{ actionState.message }}</NAlert>
    <NAlert v-else-if="actionState.state === 'succeeded'" type="success" closable>{{ actionState.message }}</NAlert>
    <NAlert v-else-if="actionState.state === 'rejected'" type="warning" closable>{{ actionState.message }}</NAlert>
    <NAlert v-else-if="actionState.state === 'cancelled'" type="info" closable>{{ actionState.message }}</NAlert>
    <div v-if="actionState.state === 'running'" class="runtime-pending"><NSpin size="small" />{{ actionState.message }}</div>
    <PluginActionPanel
      v-if="activePluginTask && plugins.describedAction"
      :description="plugins.describedAction"
      :task="activePluginTask"
      :show-form="false"
      :closable="false"
      @resolve="resolvePluginInteraction"
      @cancel="cancelPluginTask"
    />
    <div class="runtime-page">
      <InterfaceRuntimeElement
        v-for="element in definition.pages.find((item) => item.pageId === activePageId)?.elements ?? []"
        :key="element.elementId"
        :element="element"
        :data="data"
        :selected="selected"
        :forms="forms"
        :binding-fields="bindingFields"
        :action-kinds="actionKinds"
        @select="choose"
        @action="execute"
        @form="patchForm"
      />
    </div>
    <nav
      v-for="(result, bindingId) in data"
      :key="bindingId"
      class="runtime-pagination"
      :aria-label="`${bindingId} 分页`"
    >
      <span>{{ result.filteredRows === 0 ? 0 : result.offset + 1 }}–{{ Math.min(result.offset + result.rows.length, result.filteredRows) }} / {{ result.filteredRows }}</span>
      <NButton size="tiny" :disabled="result.state !== 'ready' || !cursors.canPrevious(bindingId)" @click="changePage(bindingId, -1)">上一页</NButton>
      <NButton size="tiny" :disabled="result.state !== 'ready' || !cursors.canNext(bindingId)" @click="changePage(bindingId, 1)">下一页</NButton>
    </nav>
  </div>
</template>

<style scoped>
.runtime-shell { width:100%; min-height:100%; margin:0 auto; padding:20px; transition:max-width .2s ease; }
.runtime-shell.preview-desktop { max-width:1280px; }.runtime-shell.preview-tablet { max-width:820px; }.runtime-shell.preview-mobile { max-width:420px; }
.runtime-page { display:grid; grid-template-columns:repeat(12,minmax(0,1fr)); gap:14px; align-items:start; }
.runtime-pending { display:flex; align-items:center; gap:8px; margin-bottom:10px; color:var(--vt-fg-muted); font-size:12px; }
.runtime-pagination { display:flex; align-items:center; justify-content:flex-end; gap:6px; margin-top:10px; color:var(--vt-fg-muted); font-size:11px; }
</style>
