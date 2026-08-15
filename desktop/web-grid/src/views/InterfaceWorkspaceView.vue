<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NEmpty,
  NIcon,
  NInput,
  NSelect,
  NSpin,
} from "naive-ui";
import {
  Box,
  ChevronRight,
  PlusCircle,
  Monitor,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  Smartphone,
  Tablet,
  Trash2,
  Wrench,
} from "lucide-vue-next";
import type {
  DataBinding,
  InterfaceAction,
  InterfaceDefinition,
  InterfaceElement,
} from "@/contracts/generated/workbench";
import type { BindingCollectionSchema } from "@/dashboard/bindingRuntime";
import { DashboardSchemaCatalog } from "@/services/dashboardBindingPorts";
import { useHostBridge } from "@/services/bridgeContext";
import { useProvidedSurfaceService } from "@/services/surfaceService";
import { usePluginStore } from "@/stores/pluginStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { collectionLabel } from "@/components/layout/collectionLabel";
import InterfaceBuilderElement from "@/components/surfaces/InterfaceBuilderElement.vue";
import InterfaceBindingEditor from "@/components/surfaces/InterfaceBindingEditor.vue";
import InterfaceRuntime from "@/components/surfaces/InterfaceRuntime.vue";

type ElementKind = InterfaceElement["kind"];
type ActionKind = InterfaceAction["kind"];
const service = useProvidedSurfaceService();
const workspace = useWorkspaceStore();
const plugins = usePluginStore();
const ui = useUiStore();
const schemaCatalog = new DashboardSchemaCatalog(useHostBridge());
const mode = ref<"edit" | "run">("edit");
const previewWidth = ref<"desktop" | "tablet" | "mobile">("desktop");
const activePageId = ref("");
const selectedElementId = ref<string | null>(null);
const childTargetId = ref<string | null>(null);
const bindingSource = ref<string | null>(null);
const bindingFields = ref<string[]>([]);
const bindingSchema = ref<BindingCollectionSchema | null>(null);
const bindingLoading = ref(false);
const actionKind = ref<ActionKind>("record.create");
const actionBindingId = ref<string | null>(null);
const actionTargetPageId = ref<string | null>(null);
const actionPluginId = ref<string | null>(null);
const actionPluginActionId = ref<string | null>(null);
const definition = computed(() => service.state.value.draft);
const activePage = computed(() => definition.value?.pages.find((page) => page.pageId === activePageId.value) ?? definition.value?.pages[0] ?? null);
const selectedElement = computed(() => definition.value ? findElement(definition.value, selectedElementId.value) : null);
const collectionOptions = computed(() => workspace.collections.map((item) => ({
  value: item.collection,
  label: collectionLabel(item, workspace.displayNames),
})));
const fieldOptions = computed(() => bindingSchema.value?.fields.map((field) => ({ value: field.ref, label: field.label })) ?? []);
const bindingOptions = computed(() => definition.value?.bindings.map((binding) => ({ value: binding.bindingId, label: binding.bindingId })) ?? []);
const pageOptions = computed(() => definition.value?.pages.map((page) => ({ value: page.pageId, label: page.title })) ?? []);
const actionOptions = computed(() => definition.value?.actions.map((action) => ({ value: action.actionId, label: `${action.kind} · ${action.actionId}` })) ?? []);
const pluginOptions = computed(() => plugins.plugins.map((plugin) => ({
  value: plugin.pluginId,
  label: localizedName(plugin.manifest.displayName, plugin.pluginId),
})));
const pluginActionOptions = computed(() => plugins.plugins
  .find((item) => item.pluginId === actionPluginId.value)?.manifest.actions.map((action) => ({
    value: action.actionId,
    label: localizedName(action.displayName, action.actionId),
  })) ?? []);
const elementKinds: readonly ElementKind[] = ["section", "columns", "tabs", "text", "metric", "chart", "record-list", "record-detail", "form", "button", "navigation"];
const actionKinds: readonly ActionKind[] = ["record.create", "record.update", "binding.refresh", "navigate", "plugin"];
const elementKindOptions = elementKinds.map((value) => ({ value, label: labelKind(value) }));
const actionKindOptions = actionKinds.map((value) => ({ value, label: value }));
const widthOptions = [
  { value: "full", label: "整行" }, { value: "half", label: "半宽" }, { value: "third", label: "三分之一" },
];

function localizedName(names: Readonly<Record<string, string>>, fallback: string): string {
  return names[ui.locale] ?? names["zh-CN"] ?? names["en-US"] ?? fallback;
}

onMounted(() => service.refreshList());
onBeforeUnmount(() => schemaCatalog.invalidate());
watch(definition, (next) => {
  if (!next) { activePageId.value = ""; selectedElementId.value = null; return; }
  if (!next.pages.some((page) => page.pageId === activePageId.value)) activePageId.value = next.pages[0]?.pageId ?? "";
}, { immediate: true });

async function openInterface(id: string): Promise<void> {
  if (service.state.value.dirty && !window.confirm("放弃当前未保存更改？")) return;
  mode.value = "edit";
  selectedElementId.value = null;
  await service.open(id);
}
function createInterface(): void {
  const name = window.prompt("界面名称", "业务工作台")?.trim();
  if (!name) return;
  service.create(name);
  mode.value = "edit";
}
function replace(next: InterfaceDefinition): void { service.replace(next); }
function rename(value: string): void {
  if (definition.value) replace({ ...definition.value, name: value });
}
function addPage(): void {
  if (!definition.value) return;
  const title = window.prompt("页面名称", "新页面")?.trim();
  if (!title) return;
  const pageId = `page-${crypto.randomUUID()}`;
  replace({ ...definition.value, pages: [...definition.value.pages, { pageId, title, elements: [] }] });
  activePageId.value = pageId;
}
function removePage(): void {
  if (!definition.value || definition.value.pages.length <= 1 || !activePage.value) return;
  const pageId = activePage.value.pageId;
  const pages = definition.value.pages.filter((page) => page.pageId !== pageId);
  const actions = definition.value.actions.filter((action) => action.targetPageId !== pageId);
  replace({ ...definition.value, pages, actions });
  activePageId.value = pages[0]!.pageId;
}
function addElement(kind: ElementKind, parentId = childTargetId.value): void {
  if (!definition.value || !activePage.value) return;
  const structural = ["section", "columns", "tabs"].includes(kind);
  const bound = ["metric", "chart", "record-list", "record-detail", "form"].includes(kind);
  const actionable = ["form", "button", "navigation"].includes(kind);
  const element: InterfaceElement = {
    elementId: `element-${crypto.randomUUID()}`,
    kind,
    bindingId: bound ? definition.value.bindings[0]?.bindingId ?? null : null,
    actionId: actionable ? definition.value.actions[0]?.actionId ?? null : null,
    text: structural || ["text", "metric", "chart", "form", "button", "navigation"].includes(kind) ? labelKind(kind) : null,
    width: "full",
    children: [],
  };
  const pages = definition.value.pages.map((page) => page.pageId !== activePage.value!.pageId
    ? page
    : { ...page, elements: parentId ? addChild(page.elements, parentId, element) : [...page.elements, element] });
  replace({ ...definition.value, pages });
  selectedElementId.value = element.elementId;
  childTargetId.value = null;
}
function removeElement(elementId: string): void {
  if (!definition.value || !activePage.value) return;
  replace({ ...definition.value, pages: definition.value.pages.map((page) => page.pageId === activePage.value!.pageId ? { ...page, elements: removeFrom(page.elements, elementId) } : page) });
  if (selectedElementId.value === elementId) selectedElementId.value = null;
}
function updateElement(patch: Partial<InterfaceElement>): void {
  if (!definition.value || !activePage.value || !selectedElement.value) return;
  const id = selectedElement.value.elementId;
  replace({ ...definition.value, pages: definition.value.pages.map((page) => page.pageId === activePage.value!.pageId ? { ...page, elements: mapElements(page.elements, (element) => element.elementId === id ? { ...element, ...patch } : element) } : page) });
}
async function loadBindingSchema(source: string | null): Promise<void> {
  bindingSource.value = source;
  bindingFields.value = [];
  bindingSchema.value = null;
  if (!source) return;
  bindingLoading.value = true;
  try { bindingSchema.value = await schemaCatalog.describe(source, new AbortController().signal); }
  finally { bindingLoading.value = false; }
}
function addBinding(): void {
  if (!definition.value || !bindingSource.value || bindingFields.value.length === 0) return;
  const binding: DataBinding = {
    bindingId: `binding-${crypto.randomUUID()}`,
    query: {
      contractVersion: "1.0",
      tableId: bindingSource.value,
      fields: [...bindingFields.value],
      filters: [],
      sorts: [],
      cursor: null,
      pageSize: 100,
    },
    variables: [],
  };
  replace({ ...definition.value, bindings: [...definition.value.bindings, binding] });
  bindingFields.value = [];
}
function updateBinding(next: DataBinding): void {
  if (!definition.value) return;
  replace({
    ...definition.value,
    bindings: definition.value.bindings.map((binding) => (
      binding.bindingId === next.bindingId ? next : binding
    )),
  });
}
function removeBinding(bindingId: string): void {
  if (!definition.value) return;
  const pages = definition.value.pages.map((page) => ({ ...page, elements: mapElements(page.elements, (element) => element.bindingId === bindingId ? { ...element, bindingId: null } : element) }));
  const actions = definition.value.actions.filter((action) => action.bindingId !== bindingId);
  replace({ ...definition.value, bindings: definition.value.bindings.filter((item) => item.bindingId !== bindingId), actions, pages });
}
function addAction(): void {
  if (!definition.value) return;
  const action: InterfaceAction = {
    actionId: `action-${crypto.randomUUID()}`,
    kind: actionKind.value,
    bindingId: ["record.create", "record.update", "binding.refresh"].includes(actionKind.value) ? actionBindingId.value : null,
    targetPageId: actionKind.value === "navigate" ? actionTargetPageId.value : null,
    pluginId: actionKind.value === "plugin" ? actionPluginId.value : null,
    pluginActionId: actionKind.value === "plugin" ? actionPluginActionId.value : null,
    requiresConfirmation: actionKind.value === "record.update" || actionKind.value === "plugin",
  };
  replace({ ...definition.value, actions: [...definition.value.actions, action] });
}
function removeAction(actionId: string): void {
  if (!definition.value) return;
  const pages = definition.value.pages.map((page) => ({ ...page, elements: mapElements(page.elements, (element) => element.actionId === actionId ? { ...element, actionId: null } : element) }));
  replace({ ...definition.value, actions: definition.value.actions.filter((item) => item.actionId !== actionId), pages });
}
async function save(): Promise<void> { await service.save(); }
async function removeCurrent(): Promise<void> {
  if (!definition.value || !window.confirm(`删除“${definition.value.name}”？`)) return;
  await service.remove();
}
function labelKind(kind: ElementKind): string {
  return ({ section: "分区", columns: "分栏", tabs: "标签组", text: "文本", metric: "指标", chart: "图表", "record-list": "记录列表", "record-detail": "记录详情", form: "表单", button: "按钮", navigation: "导航" } as const)[kind];
}
function findElement(current: InterfaceDefinition, id: string | null): InterfaceElement | null {
  if (!id) return null;
  for (const page of current.pages) { const found = walkFind(page.elements, id); if (found) return found; }
  return null;
}
function walkFind(elements: readonly InterfaceElement[], id: string): InterfaceElement | null {
  for (const element of elements) { if (element.elementId === id) return element; const found = walkFind(element.children, id); if (found) return found; }
  return null;
}
function mapElements(elements: readonly InterfaceElement[], mapper: (element: InterfaceElement) => InterfaceElement): InterfaceElement[] {
  return elements.map((element) => { const mapped = mapper(element); return { ...mapped, children: mapElements(mapped.children, mapper) }; });
}
function removeFrom(elements: readonly InterfaceElement[], id: string): InterfaceElement[] {
  return elements.filter((element) => element.elementId !== id).map((element) => ({ ...element, children: removeFrom(element.children, id) }));
}
function addChild(elements: readonly InterfaceElement[], parentId: string, child: InterfaceElement): InterfaceElement[] {
  return elements.map((element) => element.elementId === parentId ? { ...element, children: [...element.children, child] } : { ...element, children: addChild(element.children, parentId, child) });
}
</script>

<template>
  <div class="interface-workspace" data-testid="interface-workspace">
    <aside class="interface-list">
      <header><div><small>PRODUCT SURFACES</small><strong>界面</strong></div><NButton quaternary size="small" aria-label="新建界面" data-testid="interface-create" @click="createInterface"><NIcon><Plus /></NIcon></NButton></header>
      <div v-if="service.loadingList.value" class="list-state"><NSpin size="small" />载入中</div>
      <NAlert v-else-if="service.listError.value" type="error" size="small">{{ service.listError.value }}</NAlert>
      <button v-for="item in service.list.value" :key="item.interfaceId" type="button" class="interface-list-item" :class="{ active: definition?.interfaceId === item.interfaceId }" :data-testid="`interface-select-${item.interfaceId}`" @click="openInterface(item.interfaceId)">
        <span><Box :size="15" /></span><div><strong>{{ item.name }}</strong><small>{{ item.revision.slice(0, 18) }}</small></div><ChevronRight :size="14" />
      </button>
      <NEmpty v-if="!service.loadingList.value && service.list.value.length === 0" description="还没有界面" size="small" />
    </aside>

    <main class="interface-main">
      <template v-if="definition">
        <header class="interface-toolbar">
          <NInput :value="definition.name" size="small" class="surface-name" maxlength="120" @update:value="rename" />
          <span v-if="service.state.value.dirty" class="dirty-dot">未保存</span>
          <div class="toolbar-segment">
            <NButton size="tiny" :type="mode === 'edit' ? 'primary' : 'default'" @click="mode = 'edit'"><NIcon><Wrench /></NIcon>构建</NButton>
            <NButton size="tiny" :type="mode === 'run' ? 'primary' : 'default'" data-testid="interface-run" @click="mode = 'run'"><NIcon><Play /></NIcon>运行</NButton>
          </div>
          <div class="toolbar-segment preview-switcher" aria-label="预览宽度">
            <NButton quaternary size="tiny" :type="previewWidth === 'desktop' ? 'primary' : 'default'" aria-label="桌面预览" @click="previewWidth = 'desktop'"><NIcon><Monitor /></NIcon></NButton>
            <NButton quaternary size="tiny" :type="previewWidth === 'tablet' ? 'primary' : 'default'" aria-label="平板预览" @click="previewWidth = 'tablet'"><NIcon><Tablet /></NIcon></NButton>
            <NButton quaternary size="tiny" :type="previewWidth === 'mobile' ? 'primary' : 'default'" aria-label="手机预览" @click="previewWidth = 'mobile'"><NIcon><Smartphone /></NIcon></NButton>
          </div>
          <NButton quaternary size="small" aria-label="放弃更改" :disabled="!service.state.value.dirty" @click="service.discard"><NIcon><RotateCcw /></NIcon></NButton>
          <NButton quaternary size="small" type="error" aria-label="删除界面" :disabled="!service.state.value.revision" @click="removeCurrent"><NIcon><Trash2 /></NIcon></NButton>
          <NButton type="primary" size="small" data-testid="interface-save" :loading="service.state.value.phase === 'saving'" :disabled="!service.state.value.dirty || service.state.value.diagnostics.length > 0" @click="save"><NIcon><Save /></NIcon>保存</NButton>
        </header>
        <NAlert v-if="service.state.value.phase === 'conflict'" type="warning" class="surface-alert" data-testid="interface-conflict">
          此界面已在其他位置修改。你的草稿仍保留。
          <NButton size="small" data-testid="interface-reload-conflict" @click="service.reload"><NIcon><RefreshCw /></NIcon>载入最新版本</NButton>
        </NAlert>
        <NAlert v-else-if="service.state.value.error" type="error" class="surface-alert">{{ service.state.value.error }}</NAlert>
        <NAlert v-if="service.state.value.diagnostics.length" type="warning" class="surface-alert" data-testid="interface-diagnostics">
          {{ service.state.value.diagnostics[0]?.message }} · {{ service.state.value.diagnostics[0]?.path }}
        </NAlert>
        <nav class="page-tabs" aria-label="界面页面">
          <button v-for="page in definition.pages" :key="page.pageId" type="button" :class="{ active: activePageId === page.pageId }" @click="activePageId = page.pageId">{{ page.title }}</button>
          <NButton v-if="mode === 'edit'" quaternary size="tiny" aria-label="添加页面" data-testid="interface-add-page" @click="addPage"><NIcon><PlusCircle /></NIcon></NButton>
          <NButton v-if="mode === 'edit' && definition.pages.length > 1" quaternary size="tiny" type="error" aria-label="删除当前页面" @click="removePage"><NIcon><Trash2 /></NIcon></NButton>
        </nav>

        <div v-if="mode === 'edit'" class="builder-layout">
          <aside class="palette-panel">
            <small>元素</small>
            <button v-for="option in elementKindOptions" :key="option.value" type="button" :data-testid="`interface-add-${option.value}`" @click="addElement(option.value)"><Plus :size="13" />{{ option.label }}</button>
            <p v-if="childTargetId">下一元素将加入所选容器</p>
          </aside>
          <section class="builder-stage">
            <div class="builder-canvas" :class="`preview-${previewWidth}`">
              <InterfaceBuilderElement v-for="element in activePage?.elements ?? []" :key="element.elementId" :element="element" :selected-id="selectedElementId" @select="selectedElementId = $event" @remove="removeElement" @add-child="childTargetId = $event" />
              <NEmpty v-if="(activePage?.elements.length ?? 0) === 0" description="从左侧添加第一个元素" />
            </div>
          </section>
          <aside class="inspector-panel">
            <template v-if="selectedElement">
              <header><small>INSPECTOR</small><strong>{{ labelKind(selectedElement.kind) }}</strong></header>
              <label>文本<NInput size="small" :value="selectedElement.text ?? ''" @update:value="updateElement({ text: $event || null })" /></label>
              <label>宽度<NSelect size="small" :value="selectedElement.width" :options="widthOptions" @update:value="updateElement({ width: $event })" /></label>
              <label v-if="['metric','chart','record-list','record-detail','form'].includes(selectedElement.kind)">数据绑定<NSelect size="small" clearable data-testid="interface-element-binding" :value="selectedElement.bindingId" :options="bindingOptions" @update:value="updateElement({ bindingId: $event })" /></label>
              <label v-if="['form','button','navigation'].includes(selectedElement.kind)">动作<NSelect size="small" clearable data-testid="interface-element-action" :value="selectedElement.actionId" :options="actionOptions" @update:value="updateElement({ actionId: $event })" /></label>
            </template>
            <template v-else>
              <header><small>DATA BINDINGS</small><strong>数据源</strong></header>
              <label>数据表<NSelect size="small" filterable data-testid="interface-binding-source" :value="bindingSource" :options="collectionOptions" @update:value="loadBindingSchema" /></label>
              <label>字段<NSelect size="small" multiple filterable data-testid="interface-binding-fields" :loading="bindingLoading" :value="bindingFields" :options="fieldOptions" @update:value="bindingFields = $event" /></label>
              <NButton size="small" data-testid="interface-add-binding" :disabled="!bindingSource || bindingFields.length === 0" @click="addBinding"><NIcon><Plus /></NIcon>添加绑定</NButton>
              <InterfaceBindingEditor
                v-for="binding in definition.bindings"
                :key="binding.bindingId"
                :binding="binding"
                :bindings="definition.bindings"
                @update="updateBinding"
                @remove="removeBinding"
              />
              <header class="subhead"><small>ACTIONS</small><strong>动作</strong></header>
              <label>类型<NSelect size="small" v-model:value="actionKind" data-testid="interface-action-kind" :options="actionKindOptions" /></label>
              <label v-if="['record.create','record.update','binding.refresh'].includes(actionKind)">绑定<NSelect size="small" v-model:value="actionBindingId" data-testid="interface-action-binding" :options="bindingOptions" /></label>
              <label v-if="actionKind === 'navigate'">目标页面<NSelect size="small" v-model:value="actionTargetPageId" data-testid="interface-action-target-page" :options="pageOptions" /></label>
              <template v-if="actionKind === 'plugin'"><label>插件<NSelect size="small" v-model:value="actionPluginId" :options="pluginOptions" /></label><label>插件动作<NSelect size="small" v-model:value="actionPluginActionId" :options="pluginActionOptions" /></label></template>
              <NButton size="small" data-testid="interface-add-action" @click="addAction"><NIcon><Plus /></NIcon>添加动作</NButton>
              <div v-for="action in definition.actions" :key="action.actionId" class="definition-chip"><div><strong>{{ action.kind }}</strong><small>{{ action.actionId }}</small></div><NButton quaternary size="tiny" type="error" aria-label="删除动作" @click="removeAction(action.actionId)"><NIcon><Trash2 /></NIcon></NButton></div>
            </template>
          </aside>
        </div>
        <section v-else class="runtime-stage">
          <InterfaceRuntime :definition="definition" :active-page-id="activePageId" :preview-width="previewWidth" @navigate="activePageId = $event" />
        </section>
      </template>
      <div v-else-if="service.state.value.phase === 'loading'" class="surface-state"><NSpin />正在加载界面</div>
      <NEmpty v-else description="选择或创建一个界面" class="surface-state"><template #icon><NIcon><Box /></NIcon></template><NButton type="primary" data-testid="interface-create-empty" @click="createInterface"><NIcon><Plus /></NIcon>新建界面</NButton></NEmpty>
    </main>
  </div>
</template>

<style scoped>
.interface-workspace { display:flex; height:100%; min-height:0; background:var(--vt-bg-sunken); }
.interface-list { width:224px; flex:none; overflow:auto; border-right:1px solid var(--vt-border); background:var(--vt-bg); }.interface-list>header { display:flex; align-items:center; justify-content:space-between; padding:13px 10px 10px 14px; }.interface-list>header div { display:grid; }.interface-list small,.inspector-panel small,.palette-panel>small { color:var(--vt-fg-muted); font-size:9px; font-weight:700; letter-spacing:.11em; }.interface-list-item { display:grid; width:calc(100% - 12px); grid-template-columns:28px 1fr auto; align-items:center; gap:7px; margin:2px 6px; padding:8px; border:0; border-radius:7px; background:transparent; color:inherit; text-align:left; cursor:pointer; }.interface-list-item:hover,.interface-list-item.active { background:var(--vt-bg-subtle); }.interface-list-item>span { display:grid; width:28px; height:28px; place-items:center; border:1px solid var(--vt-border); border-radius:7px; }.interface-list-item div { display:grid; min-width:0; }.interface-list-item strong,.interface-list-item small { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }.interface-list-item small { letter-spacing:0; }.list-state { display:flex; justify-content:center; gap:7px; padding:24px; color:var(--vt-fg-muted); }
.interface-main { display:flex; min-width:0; flex:1; flex-direction:column; }.interface-toolbar { display:flex; min-height:48px; align-items:center; gap:8px; padding:7px 11px; border-bottom:1px solid var(--vt-border); background:var(--vt-bg); }.surface-name { width:min(320px,28vw); font-weight:700; }.dirty-dot { color:var(--vt-color-warning); font-size:11px; }.toolbar-segment { display:flex; gap:2px; padding:2px; border:1px solid var(--vt-border); border-radius:7px; }.preview-switcher { margin-left:auto; }.surface-alert { margin:7px 10px 0; }.page-tabs { display:flex; align-items:center; gap:2px; min-height:38px; padding:4px 10px; overflow-x:auto; border-bottom:1px solid var(--vt-border); background:var(--vt-bg); }.page-tabs>button { padding:6px 11px; border:0; border-radius:6px; background:transparent; color:var(--vt-fg-muted); cursor:pointer; white-space:nowrap; }.page-tabs>button.active { background:var(--vt-bg-subtle); color:var(--vt-fg); font-weight:700; }
.builder-layout { display:grid; min-height:0; flex:1; grid-template-columns:152px minmax(0,1fr) 260px; }.palette-panel,.inspector-panel { overflow:auto; background:var(--vt-bg); }.palette-panel { padding:11px 8px; border-right:1px solid var(--vt-border); }.palette-panel>button { display:flex; width:100%; align-items:center; gap:7px; margin-top:4px; padding:7px 8px; border:1px solid transparent; border-radius:6px; background:transparent; color:inherit; font-size:12px; cursor:pointer; }.palette-panel>button:hover { border-color:var(--vt-border); background:var(--vt-bg-subtle); }.palette-panel p { color:var(--vt-color-primary-600); font-size:10px; }.builder-stage { min-width:0; overflow:auto; padding:20px; background-image:radial-gradient(color-mix(in srgb,var(--vt-border) 58%,transparent) .7px,transparent .7px); background-size:12px 12px; }.builder-canvas { display:grid; width:100%; min-height:calc(100% - 2px); grid-template-columns:repeat(12,minmax(0,1fr)); align-content:start; gap:11px; margin:0 auto; padding:18px; border:1px solid var(--vt-border); border-radius:12px; background:var(--vt-bg-sunken); box-shadow:0 12px 36px rgb(15 23 42/.08); transition:max-width .2s; }.builder-canvas.preview-desktop { max-width:1180px; }.builder-canvas.preview-tablet { max-width:760px; }.builder-canvas.preview-mobile { max-width:390px; }.inspector-panel { padding:13px; border-left:1px solid var(--vt-border); }.inspector-panel header { display:grid; margin-bottom:12px; }.inspector-panel label { display:grid; gap:5px; margin-bottom:11px; color:var(--vt-fg-muted); font-size:11px; }.subhead { margin-top:22px; padding-top:13px; border-top:1px solid var(--vt-border); }.definition-chip { display:grid; grid-template-columns:1fr auto; align-items:center; gap:5px; margin-top:7px; padding:7px 5px 7px 8px; border:1px solid var(--vt-border); border-radius:6px; }.definition-chip div { display:grid; min-width:0; }.definition-chip strong,.definition-chip small { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }.definition-chip small { letter-spacing:0; }.runtime-stage { min-height:0; flex:1; overflow:auto; }.surface-state { display:flex; flex:1; align-items:center; justify-content:center; gap:9px; }
@media (max-width:960px) { .builder-layout { grid-template-columns:120px minmax(0,1fr); }.inspector-panel { position:absolute; z-index:10; right:0; width:260px; height:calc(100% - 86px); box-shadow:-10px 0 24px rgb(15 23 42/.12); }.interface-list { width:180px; } }
@media (max-width:680px) { .interface-list { width:58px; }.interface-list>header div,.interface-list-item div,.interface-list-item>svg { display:none; }.interface-list-item { grid-template-columns:1fr; }.builder-layout { grid-template-columns:1fr; }.palette-panel { display:flex; gap:4px; overflow-x:auto; border-right:0; border-bottom:1px solid var(--vt-border); }.palette-panel>small,.palette-panel p { display:none; }.palette-panel>button { width:auto; white-space:nowrap; }.preview-switcher { display:none; } }
</style>
