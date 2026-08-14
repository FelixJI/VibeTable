import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia, type Pinia } from "pinia";
import { NMessageProvider } from "naive-ui";
import { defineComponent, h } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { parseWirePanel } from "@/dashboard";
import {
  provideDashboardService,
  type DashboardService,
  type DashboardDrilldownResult,
} from "@/services/dashboardService";
import { useDashboardDraftStore, useDashboardStore } from "@/stores/dashboardStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import DashboardWorkspaceView from "./DashboardWorkspaceView.vue";

const exportsHarness = vi.hoisted(() => ({
  csv: vi.fn(),
  png: vi.fn(async () => undefined),
  print: vi.fn(),
}));

vi.mock("@/services/dashboardExportService", () => ({
  exportDashboardCsv: exportsHarness.csv,
  exportDashboardElementPng: exportsHarness.png,
  printDashboard: exportsHarness.print,
}));

const sidebarStub = defineComponent({
  name: "DashboardSidebar",
  props: ["dashboards", "selectedId"],
  emits: ["select", "create"],
  template: '<aside class="sidebar-stub" />',
});
const toolbarStub = defineComponent({
  name: "DashboardToolbar",
  props: ["name", "editing", "dirty", "saving", "refreshInterval"],
  emits: [
    "refresh", "edit", "copy", "delete", "save", "discard", "addPanel", "configure",
    "refreshInterval", "exportPng", "print",
  ],
  template: '<header class="toolbar-stub" />',
});
const filterStub = defineComponent({
  name: "DashboardFilterBar",
  props: ["filters", "values"],
  emits: ["change", "clear"],
  template: '<div class="filter-stub" />',
});
const gridStub = defineComponent({
  name: "DashboardGrid",
  props: ["panels", "data", "editing", "manifest"],
  emits: ["layout", "remove", "edit", "refresh", "exportCsv", "exportPng", "select", "visibility"],
  template: '<div class="grid-stub"><div v-for="panel in panels" :key="panel.id" :data-panel-id="panel.id" /></div>',
});
const createStub = defineComponent({
  name: "DashboardCreateModal",
  props: ["show"],
  emits: ["close", "create"],
  template: '<div class="create-stub" />',
});
const editorStub = defineComponent({
  name: "DashboardPanelEditor",
  props: ["show", "panel", "dashboardId", "collections", "allowedTypes", "manifest", "loadSchema"],
  emits: ["close", "submit"],
  template: '<div class="editor-stub" />',
});
const settingsStub = defineComponent({
  name: "DashboardSettingsDrawer",
  props: ["show", "name", "note", "config", "panels", "loadSchema"],
  emits: ["close", "submit"],
  template: '<div class="settings-stub" />',
});
const drilldownStub = defineComponent({
  name: "DashboardDrilldownDrawer",
  props: ["show", "title", "selection", "rows", "truncated", "loading", "error"],
  emits: ["close"],
  template: '<div class="drilldown-stub" />',
});

function wirePanel(id = "p1", patch: Record<string, unknown> = {}) {
  return {
    id,
    dashboardId: "d1",
    name: `面板 ${id}`,
    type: "bar",
    position: { x: 0, y: 0, width: 4, height: 3 },
    options: {},
    query: {
      kind: "aggregate",
      collection: "orders",
      dimensions: ["region"],
      measures: [{ key: "count", op: "count", field: null }],
    },
    ...patch,
  };
}

function seedDashboard(panelCount = 1): void {
  const store = useDashboardStore();
  const panels = Array.from({ length: panelCount }, (_, index) => wirePanel(`p${index + 1}`, {
    position: { x: 0, y: index * 3, width: 4, height: 3 },
  }));
  store.receiveList({ dashboards: [{ id: "d1", name: "运营", note: "每日", panels }] });
  store.receiveWorkspace({
    dashboard: { id: "d1", name: "运营", note: "每日", panels },
    revision: "dashboard-1",
    config: {
      configVersion: 1,
      refreshInterval: 30,
      globalFilters: [{
        key: "region",
        label: "区域",
        type: "enum",
        allowedFields: ["region"],
        targetPanels: [],
        fieldBindings: {},
      }],
      interactions: [],
    },
  });
  store.receiveManifest({
    manifest: {
      manifestVersion: "2",
      panels: [{
        type: "bar",
        rendererVersion: "2",
        minSize: { width: 2, height: 2 },
        optionsSchema: {},
      }],
    },
  });
  useWorkspaceStore().setOpened(
    [{ collection: "orders" }, { collection: "customers" }],
    { orders: "Orders", customers: "Customers" },
  );
}

function createService(
  drilldown: () => Promise<DashboardDrilldownResult> = async () => ({
    rows: [{ id: "r1", region: "North" }],
    truncated: true,
  }),
): DashboardService {
  const store = useDashboardStore();
  const draft = useDashboardDraftStore();
  return {
    init: vi.fn(),
    dispose: vi.fn(),
    list: vi.fn(async () => undefined),
    select: vi.fn(async () => undefined),
    refresh: vi.fn(async () => undefined),
    beginEdit: vi.fn(() => {
      if (store.current) draft.begin(store.current, store.config, store.revision);
    }),
    discardEdit: vi.fn(() => draft.stop()),
    createFromTemplate: vi.fn((_templateId, name) => {
      draft.begin({ id: "draft:new", name, note: "", panels: [] }, {
        configVersion: 1,
        globalFilters: [],
        interactions: [],
        refreshInterval: 0,
      }, null);
      draft.dirty = true;
    }),
    copyCurrent: vi.fn(() => 1),
    save: vi.fn(async () => undefined),
    deleteCurrent: vi.fn(async () => undefined),
    queryAllPanels: vi.fn(async () => undefined),
    previewPanel: vi.fn(async () => undefined),
    refreshDraft: vi.fn(async () => undefined),
    selectPanelValue: vi.fn(),
    drilldown: vi.fn(drilldown),
    describeCollection: vi.fn(async () => ({ collectionId: "orders", revision: "1", fields: [] })),
    setVisiblePanels: vi.fn(),
  };
}

function mountView(pinia: Pinia, service: DashboardService) {
  const Host = defineComponent({
    setup() {
      provideDashboardService(service);
      return () => h(NMessageProvider, null, { default: () => h(DashboardWorkspaceView) });
    },
  });
  return mount(Host, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      stubs: {
        DashboardSidebar: sidebarStub,
        DashboardToolbar: toolbarStub,
        DashboardFilterBar: filterStub,
        DashboardGrid: gridStub,
        DashboardCreateModal: createStub,
        DashboardPanelEditor: editorStub,
        DashboardSettingsDrawer: settingsStub,
        DashboardDrilldownDrawer: drilldownStub,
      },
    },
  });
}

describe("DashboardWorkspaceView", () => {
  let pinia: Pinia;

  beforeEach(() => {
    document.body.innerHTML = "";
    pinia = createPinia();
    setActivePinia(pinia);
    vi.clearAllMocks();
    vi.stubGlobal("CSS", { escape: (value: string) => value });
  });

  afterEach(() => {
    document.body.innerHTML = "";
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("creates a dashboard from the empty state and closes the modal", async () => {
    const service = createService();
    const wrapper = mountView(pinia, service);
    expect(wrapper.text()).toContain("新建仪表盘");

    await wrapper.get('[data-testid="dashboard-create-empty"]').trigger("click");
    const modal = wrapper.findComponent(createStub);
    expect(modal.props("show")).toBe(true);
    modal.vm.$emit("create", "blank", "新工作台");
    await flushPromises();
    expect(service.createFromTemplate).toHaveBeenCalledWith("blank", "新工作台");
    expect(useDashboardDraftStore().draft?.name).toBe("新工作台");
    expect(wrapper.findComponent(createStub).props("show")).toBe(false);
    expect(wrapper.findComponent(toolbarStub).exists()).toBe(true);
    wrapper.unmount();
  });

  it("routes dashboard edit, panel, filter, drilldown, export, and visibility workflows", async () => {
    seedDashboard();
    const service = createService();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const wrapper = mountView(pinia, service);
    await flushPromises();
    const sidebar = wrapper.findComponent(sidebarStub);
    const toolbar = wrapper.findComponent(toolbarStub);

    toolbar.vm.$emit("refresh");
    toolbar.vm.$emit("copy");
    toolbar.vm.$emit("exportPng");
    toolbar.vm.$emit("print");
    toolbar.vm.$emit("edit");
    await flushPromises();
    expect(service.refresh).toHaveBeenCalled();
    expect(service.copyCurrent).toHaveBeenCalled();
    expect(exportsHarness.png).toHaveBeenCalledWith(expect.any(HTMLElement), "运营");
    expect(exportsHarness.print).toHaveBeenCalled();
    expect(useDashboardDraftStore().editing).toBe(true);

    toolbar.vm.$emit("addPanel");
    await wrapper.vm.$nextTick();
    const editor = wrapper.findComponent(editorStub);
    expect(editor.props("show")).toBe(true);
    const newPanel = parseWirePanel(wirePanel("draft:p2", {
      dashboardId: "d1",
      position: { x: 9, y: 0, width: 3, height: 2 },
    }));
    editor.vm.$emit("submit", newPanel);
    await flushPromises();
    expect(useDashboardDraftStore().draft?.panels.at(-1)?.position).toMatchObject({ x: 0, y: 3 });
    expect(service.previewPanel).toHaveBeenCalledWith(newPanel);

    const grid = wrapper.findComponent(gridStub);
    const first = useDashboardDraftStore().draft!.panels[0]!;
    grid.vm.$emit("layout", first.id, { x: 2, y: 4, width: 5, height: 3 });
    grid.vm.$emit("edit", first);
    grid.vm.$emit("refresh", first.id);
    grid.vm.$emit("exportCsv", first);
    grid.vm.$emit("exportPng", first);
    grid.vm.$emit("visibility", [first.id]);
    await flushPromises();
    expect(useDashboardDraftStore().draft!.panels[0]!.position).toMatchObject({ x: 2, y: 4 });
    expect(wrapper.findComponent(editorStub).props("panel")).toMatchObject({ id: first.id });
    expect(service.previewPanel).toHaveBeenCalledWith(expect.objectContaining({ id: first.id }));
    expect(exportsHarness.csv).toHaveBeenCalledWith(first.name, expect.objectContaining({ state: "idle" }));
    expect(exportsHarness.png).toHaveBeenCalledWith(expect.any(HTMLElement), first.name);
    expect(service.setVisiblePanels).toHaveBeenCalledWith([first.id]);

    toolbar.vm.$emit("configure");
    await wrapper.vm.$nextTick();
    const settings = wrapper.findComponent(settingsStub);
    expect(settings.props("show")).toBe(true);
    settings.vm.$emit("submit", "新名称", "说明", {
      ...useDashboardDraftStore().config,
      refreshInterval: 60,
    });
    await wrapper.vm.$nextTick();
    expect(useDashboardDraftStore().draft?.name).toBe("新名称");
    expect(useDashboardDraftStore().config.refreshInterval).toBe(60);

    const filter = wrapper.findComponent(filterStub);
    filter.vm.$emit("change", "region", ["North"]);
    filter.vm.$emit("clear");
    await flushPromises();
    expect(useDashboardStore().sessionFilterValues).toEqual({});
    expect(service.refreshDraft).toHaveBeenCalledTimes(2);

    grid.vm.$emit("select", first, { primaryValue: "North" });
    await flushPromises();
    expect(service.selectPanelValue).toHaveBeenCalledWith(first, { primaryValue: "North" });
    const drawer = wrapper.findComponent(drilldownStub);
    expect(drawer.props()).toMatchObject({ show: true, loading: false, truncated: true });
    expect(drawer.props("rows")).toEqual([{ id: "r1", region: "North" }]);
    drawer.vm.$emit("close");
    await wrapper.vm.$nextTick();
    expect(wrapper.findComponent(drilldownStub).props("show")).toBe(false);

    toolbar.vm.$emit("save");
    toolbar.vm.$emit("discard");
    await flushPromises();
    expect(service.save).toHaveBeenCalled();
    expect(useDashboardDraftStore().editing).toBe(false);

    service.beginEdit();
    useDashboardDraftStore().rename("本地草稿", "");
    confirm.mockReturnValueOnce(false);
    sidebar.vm.$emit("select", "other");
    await flushPromises();
    expect(service.select).not.toHaveBeenCalled();
    confirm.mockReturnValueOnce(true);
    sidebar.vm.$emit("select", "other");
    await flushPromises();
    expect(service.select).toHaveBeenCalledWith("other");

    toolbar.vm.$emit("delete");
    await flushPromises();
    expect(service.deleteCurrent).toHaveBeenCalled();
    expect(confirm).toHaveBeenCalled();
    wrapper.unmount();
  });

  it("shows loading/offline/error/conflict states and keeps failed drilldown deterministic", async () => {
    seedDashboard();
    const service = createService(async () => { throw "query.offline"; });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const store = useDashboardStore();
    const draft = useDashboardDraftStore();
    draft.begin(store.current!, store.config, store.revision);
    draft.setConflict("changed elsewhere", "dashboard-2");
    store.offline = true;
    store.fail("dashboard.failed");
    const wrapper = mountView(pinia, service);
    await flushPromises();

    expect(wrapper.text()).toContain("changed elsewhere");
    expect(wrapper.text()).toContain("离线");
    expect(wrapper.text()).toContain("dashboard.failed");
    await wrapper.get('[data-testid="dashboard-reload-conflict"]').trigger("click");
    await flushPromises();
    expect(service.select).toHaveBeenCalledWith("d1");
    expect(draft.editing).toBe(false);
    expect(confirm).toHaveBeenCalled();

    draft.begin(store.current!, store.config, store.revision);
    const first = draft.draft!.panels[0]!;
    wrapper.findComponent(gridStub).vm.$emit("select", first, "North");
    await flushPromises();
    expect(wrapper.findComponent(drilldownStub).props()).toMatchObject({
      show: true,
      loading: false,
      error: "query.offline",
    });

    store.beginLoad();
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain("正在加载");
    wrapper.unmount();
  });
});
