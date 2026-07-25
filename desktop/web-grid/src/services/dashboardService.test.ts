import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { flushPromises } from "@vue/test-utils";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useDashboardService } from "./dashboardService";
import { useDashboardDraftStore, useDashboardStore } from "@/stores/dashboardStore";

function harness(): { bridge: HostBridge; emit: (type: string, payload: unknown, requestId?: string) => void; posted: Array<Record<string, unknown>> } {
  let listener: ((event: { data: unknown }) => void) | null = null;
  const posted: Array<Record<string, unknown>> = [];
  const bridge = createHostBridge({
    timeoutMs: 1_000,
    webview: {
      addEventListener: (_type, fn) => { listener = fn; },
      removeEventListener: () => undefined,
      postMessage: (message) => { posted.push(message as Record<string, unknown>); },
    },
  });
  bridge.start();
  return {
    bridge,
    posted,
    emit: (type, payload, requestId) => listener?.({ data: JSON.stringify({ type, payload, requestId }) }),
  };
}

function reply(h: ReturnType<typeof harness>, requestType: string, responseType: string, payload: unknown): void {
  const request = [...h.posted].reverse().find((item) => item.type === requestType);
  expect(request).toBeTruthy();
  h.emit(responseType, payload, String(request!.requestId));
}

function replyManifest(h: ReturnType<typeof harness>, panelTypes = ["label", "metric", "bar"]): void {
  reply(h, "dashboard.manifestRequested", "dashboard.manifestLoaded", {
    manifest: {
      manifestVersion: "dashboard-panel-manifest.v1",
      queryContract: "product-query-port.v1",
      panels: panelTypes.map((type) => ({
        type,
        minSize: { x: 0, y: 0, width: 1, height: 1 },
        optionsSchema: {},
        rendererVersion: "1",
      })),
    },
    queryLimits: {
      maxConcurrentRequests: 6, maxSeriesPoints: 50_000, maxPanelPoints: 100_000,
      maxCategoryPoints: 5_000, defaultTopN: 100, maxPieSlices: 50, maxListRows: 100,
    },
  });
}

const panel = {
  id: "p1", dashboardId: "d1", name: "Revenue", type: "bar",
  position: { x: 0, y: 0, width: 6, height: 4 }, options: {},
  query: { kind: "aggregate", collection: "orders", dimensions: ["status"], measures: [{ key: "value", op: "sum", field: "amount" }], limit: 100 },
};

describe("dashboardService", () => {
  beforeEach(() => { setActivePinia(createPinia()); Object.defineProperty(document, "hidden", { configurable: true, value: false }); });
  afterEach(() => setHostBridgeForTesting(null));

  it("is fail-closed until the host explicitly enables dashboards", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const service = useDashboardService(); service.init();
    h.emit("database.opened", { tables: [], views: [], features: { dashboards: false } });
    await flushPromises();
    expect(useDashboardStore().featureEnabled).toBe(false);
    expect(h.posted.some((item) => item.type === "dashboard.listRequested")).toBe(false);
    service.dispose();
  });

  it("loads, queries, and applies session filters without persisting their values", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const service = useDashboardService(); service.init();
    h.emit("database.opened", { tables: ["orders"], views: [], features: { dashboards: true } });
    await flushPromises();
    replyManifest(h);
    reply(h, "dashboard.listRequested", "dashboard.listLoaded", { dashboards: [{ id: "d1", name: "Ops", note: "", panels: [panel] }] });
    await flushPromises();
    expect(useDashboardStore().list[0]).toMatchObject({ id: "d1", panelCount: 1 });

    const selecting = service.select("d1");
    await flushPromises();
    reply(h, "dashboard.readRequested", "dashboard.loaded", {
      dashboard: { id: "d1", name: "Ops", note: "", panels: [panel] }, revision: "r1",
      config: { configVersion: 1, refreshInterval: 0, globalFilters: [{ key: "region", label: "Region", type: "enum", allowedFields: ["legacy_region", "region"], targetPanels: ["p1"], fieldBindings: { p1: "region" } }], interactions: [] },
      queryLimits: {}, atomicSaveEndpoint: "/vibetable-bulk-mutation/dashboard/apply",
    });
    await flushPromises();
    useDashboardStore().setFilterValue("region", ["east"]);
    const queryRequest = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested");
    expect(queryRequest).toBeTruthy();
    reply(h, "dashboard.queryRequested", "dashboard.queryLoaded", { rows: [{ status: "paid", sum: { amount: 42 } }], truncated: false, maxPoints: 100 });
    await selecting;

    const refreshing = service.refresh();
    await flushPromises();
    const filtered = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
    expect(filtered.payload).toMatchObject({ query: { filters: [{ field: "region", operator: "in", value: ["east"] }] } });
    reply(h, "dashboard.queryRequested", "dashboard.queryLoaded", { rows: [], truncated: false, maxPoints: 100 });
    await refreshing;
    expect(useDashboardStore().config.globalFilters[0]).not.toHaveProperty("value");
    service.dispose();
  });

  it("copies a dashboard as a new draft and reports unsupported custom panels", () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Vendor", note: "", panels: [panel, { ...panel, id: "custom", type: "vendor-heatmap", options: { _vibetable: { productType: "vendor-heatmap" } }, query: {} }] },
      config: {
        globalFilters: [{ key: "status", label: "Status", type: "enum", allowedFields: ["status"], targetPanels: ["p1"], fieldBindings: { p1: "status" } }],
        interactions: [{ sourcePanelId: "p1", targetPanelIds: ["p1"], targetField: "status" }],
      }, revision: "r1", queryLimits: {},
    });
    const skipped = useDashboardService().copyCurrent("Vendor copy");
    const copy = useDashboardDraftStore().draft;
    expect(copy).toMatchObject({ name: "Vendor copy" });
    expect(copy?.id).toMatch(/^draft:/);
    expect(skipped).toBe(1);
    expect(copy?.panels).toHaveLength(1);
    const copiedId = copy!.panels[0]!.id;
    expect(useDashboardDraftStore().config.globalFilters[0]).toMatchObject({
      targetPanels: [copiedId], fieldBindings: { [copiedId]: "status" },
    });
    expect(useDashboardDraftStore().config.interactions[0]).toMatchObject({
      sourcePanelId: copiedId, targetPanelIds: [copiedId],
    });
  });

  it("loads the runtime manifest and keeps panel creation fail-closed to its allowlist", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const service = useDashboardService(); service.init();
    h.emit("database.opened", { tables: [], views: [], features: { dashboards: true } });
    await flushPromises();
    replyManifest(h, ["label"]);
    reply(h, "dashboard.listRequested", "dashboard.listLoaded", { dashboards: [] });
    await flushPromises();
    expect(useDashboardStore().allowedPanelTypes).toEqual(["label"]);
    expect(useDashboardStore().limits.maxPanelPoints).toBe(100_000);
    service.createFromTemplate("operations-overview", "Blocked");
    expect(useDashboardDraftStore().draft).toBeNull();
    service.dispose();
  });

  it("ignores a stale preview response for the same panel", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [panel] },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    const first = service.previewPanel(store.current!.panels[0]!);
    await flushPromises();
    const firstRequest = h.posted.find((item) => item.type === "dashboard.queryRequested")!;
    const second = service.previewPanel(store.current!.panels[0]!);
    await flushPromises();
    const requests = h.posted.filter((item) => item.type === "dashboard.queryRequested");
    const secondRequest = requests.at(-1)!;
    expect(secondRequest.requestId).not.toBe(firstRequest.requestId);
    h.emit("dashboard.queryLoaded", { rows: [{ status: "new", value: 2 }], truncated: false, maxPoints: 100 }, String(secondRequest.requestId));
    await flushPromises();
    h.emit("dashboard.queryLoaded", { rows: [{ status: "old", value: 1 }], truncated: false, maxPoints: 100 }, String(firstRequest.requestId));
    await Promise.all([first, second]);
    expect(store.panelData.p1?.rows).toEqual([{ status: "new", value: 2 }]);
  });

  it("uses clientPanelIds to remove draft references from the saved workspace", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [panel] },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    service.beginEdit();
    const draftStore = useDashboardDraftStore();
    const clientId = "draft:new-panel";
    draftStore.addPanel({
      ...draftStore.draft!.panels[0]!, id: clientId, dashboardId: "d1", name: "New",
    });
    draftStore.updateConfig({
      ...draftStore.config,
      globalFilters: [{
        key: "status", label: "Status", type: "enum", allowedFields: ["status"],
        targetPanels: [clientId], fieldBindings: { [clientId]: "status" },
      }],
      interactions: [{ sourcePanelId: "p1", targetPanelIds: [clientId], targetField: "status" }],
    });

    const saving = service.save();
    await flushPromises();
    const saveRequest = h.posted.find((item) => item.type === "dashboard.saveRequested")!;
    h.emit("dashboard.saved", {
      workspace: {
        dashboard: {
          id: "d1", name: "Ops", note: "", panels: [
            { ...panel, type: "label", query: {} },
            { ...panel, id: "p2", name: "New", type: "label", query: {} },
          ],
        },
        config: {
          configVersion: 1, refreshInterval: 0,
          globalFilters: [{ key: "status", label: "Status", type: "enum", allowedFields: ["status"], targetPanels: [clientId], fieldBindings: { [clientId]: "status" } }],
          interactions: [{ sourcePanelId: "p1", targetPanelIds: [clientId], targetField: "status" }],
        },
        revision: "r2", queryLimits: {},
      },
      clientPanelIds: { [clientId]: "p2" },
      atomic: true,
    }, String(saveRequest.requestId));
    await flushPromises();
    expect(store.error).toBeNull();
    await saving;
    expect(store.config.globalFilters[0]).toMatchObject({
      targetPanels: ["p2"], fieldBindings: { p2: "status" },
    });
    expect(store.config.interactions[0]).toMatchObject({ targetPanelIds: ["p2"] });
  });

  it("deletes the current dashboard through the atomic bridge and reloads the list", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.setFeatureEnabled(true);
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [] },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    const deleting = service.deleteCurrent();
    await flushPromises();
    reply(h, "dashboard.deleteRequested", "dashboard.deleted", { deleted: "d1" });
    await flushPromises();
    replyManifest(h);
    reply(h, "dashboard.listRequested", "dashboard.listLoaded", { dashboards: [] });
    await deleting;
    expect(store.current).toBeNull();
    expect(store.list).toEqual([]);
  });

  it("runs a permission-checked record drilldown for the selected dimension", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [panel] },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    const loading = service.drilldown(store.current!.panels[0]!, "paid");
    await flushPromises();
    const request = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
    expect(request.payload).toMatchObject({
      panelType: "list",
      query: {
        kind: "records",
        collection: "orders",
        fields: ["id", "status", "amount"],
        filters: [{ field: "status", operator: "eq", value: "paid" }],
        limit: 100,
      },
    });
    h.emit("dashboard.queryLoaded", { rows: [{ id: "1", status: "paid", amount: 42 }], truncated: false, maxPoints: 100 }, String(request.requestId));
    await expect(loading).resolves.toEqual({
      rows: [{ id: "1", status: "paid", amount: 42 }],
      truncated: false,
    });
  });

  it("applies all selected dimensions to drilldown and uses interaction sourceField", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const multidimensional = {
      ...panel,
      type: "time-series",
      query: {
        ...panel.query,
        dimensions: ["region", "status"],
        timeBucket: { field: "created_at", unit: "day" },
      },
    };
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [multidimensional] },
      config: {
        interactions: [{
          sourcePanelId: "p1", targetPanelIds: ["p1"], targetField: "region_code", sourceField: "region",
        }, {
          sourcePanelId: "p1", targetPanelIds: ["p1"], targetField: "must_not_filter", sourceField: "missing",
        }],
      },
      revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    const selection = {
      primaryField: "created_at",
      primaryValue: "2026-07-01T00:00:00.000Z",
      values: { created_at: "2026-07-01T00:00:00.000Z", region: "east", status: "paid" },
    };
    const loading = service.drilldown(store.current!.panels[0]!, selection);
    await flushPromises();
    const drilldownRequest = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
    expect(drilldownRequest.payload).toMatchObject({ query: { filters: [
      { field: "created_at", operator: "gte", value: "2026-07-01T00:00:00.000Z" },
      { field: "created_at", operator: "lt", value: "2026-07-02T00:00:00.000Z" },
      { field: "region", operator: "eq", value: "east" },
      { field: "status", operator: "eq", value: "paid" },
    ] } });
    h.emit("dashboard.queryLoaded", { rows: [], truncated: false }, String(drilldownRequest.requestId));
    await loading;

    service.selectPanelValue(store.current!.panels[0]!, selection);
    await flushPromises();
    const linkedRequest = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
    expect(linkedRequest.payload).toMatchObject({ query: { filters: [
      { field: "region_code", operator: "eq", value: "east" },
    ] } });
    h.emit("dashboard.queryLoaded", { rows: [], truncated: false }, String(linkedRequest.requestId));
    await flushPromises();
  });
});
