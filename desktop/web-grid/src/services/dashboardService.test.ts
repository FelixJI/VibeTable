import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
        optionsSchema: { type: "object", properties: {}, additionalProperties: false },
        rendererVersion: "2",
      })),
    },
    queryLimits: {
      maxConcurrentRequests: 6, maxSeriesPoints: 50_000, maxPanelPoints: 100_000,
      maxCategoryPoints: 5_000, defaultTopN: 100, maxPieSlices: 50, maxListRows: 100,
    },
  });
}

function replySchema(h: ReturnType<typeof harness>, collection = "orders"): void {
  const request = [...h.posted].reverse().find((item) => item.type === "schema.describe");
  expect(request).toBeTruthy();
  const requestGeneration = (request!.payload as { requestGeneration: number }).requestGeneration;
  const column = (
    name: string,
    dataType: "text" | "decimal" | "datetime",
    summaryOperations: readonly string[],
  ) => ({
    name, title: name, fieldId: `fld_${name}`, dataType, editable: true, nullable: true,
    filterOperators: [
      "eq", "ne", "in", "contains", "starts_with", "ends_with",
      "gt", "gte", "lt", "lte", "between", "is_null", "is_not_null",
    ],
    groupable: true, summaryOperations,
  });
  h.emit("schema.describe", {
    contract: "vibetable.schema-describe.v1", collection, requestGeneration,
    schema: {
      collection, primaryKey: "id", schemaRevision: "schema_7", permissionRevision: "schema_7",
      capabilityHash: "cap_7", lookupRevision: "lookup_7", normalizedRelations: [],
      columns: [
        column("id", "text", ["count", "countDistinct", "min", "max"]),
        column("status", "text", ["count", "countDistinct", "min", "max"]),
        column("region", "text", ["count", "countDistinct", "min", "max"]),
        column("region_code", "text", ["count", "countDistinct", "min", "max"]),
        column("amount", "decimal", ["count", "countDistinct", "sum", "avg", "min", "max"]),
        column("created_at", "datetime", ["count", "countDistinct", "min", "max"]),
      ],
    },
    capabilities: {
      contract: "vibetable.relation-capabilities.v1", relationReadV1: true,
      relationEditV1: true, lookupQueryV1: true,
    },
  }, String(request!.requestId));
}

const panel = {
  id: "p1", dashboardId: "d1", name: "Revenue", type: "bar",
  position: { x: 0, y: 0, width: 6, height: 4 }, options: {},
  query: { kind: "aggregate", collection: "orders", dimensions: ["status"], measures: [{ key: "value", op: "sum", field: "amount" }], limit: 100 },
};

describe("dashboardService", () => {
  beforeEach(() => { setActivePinia(createPinia()); Object.defineProperty(document, "hidden", { configurable: true, value: false }); });
  afterEach(() => {
    vi.useRealTimers();
    setHostBridgeForTesting(null);
  });

  it("loads the completed Dashboard surface without a runtime feature flag", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const service = useDashboardService(); service.init();
    h.emit("database.opened", { tables: [], views: [] });
    await flushPromises();
    expect(h.posted.some((item) => item.type === "dashboard.listRequested")).toBe(true);
    expect(h.posted.some((item) => item.type === "dashboard.manifestRequested")).toBe(true);
    service.dispose();
  });

  it("loads, queries, and applies session filters without persisting their values", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const service = useDashboardService(); service.init();
    h.emit("database.opened", { tables: ["orders"], views: [] });
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
    replySchema(h);
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

  it("does not let refresh supersede a pending dashboard selection", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "old", name: "Old", note: "", panels: [] },
      config: {}, revision: "r0", queryLimits: {},
    });
    const service = useDashboardService();

    const selecting = service.select("d1");
    await flushPromises();
    await service.refresh();
    reply(h, "dashboard.readRequested", "dashboard.loaded", {
      dashboard: { id: "d1", name: "Ops", note: "", panels: [] },
      config: {}, revision: "r1", queryLimits: {},
    });
    await selecting;

    expect(store.phase).toBe("ready");
    expect(store.current?.id).toBe("d1");
    service.dispose();
  });

  it("recovers a loaded dashboard after a refreshable failure", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [] },
      config: {}, revision: "r1", queryLimits: {},
    });
    store.fail("temporary failure");
    const service = useDashboardService();

    await service.refresh();

    expect(store.phase).toBe("ready");
    expect(store.error).toBeNull();
    expect(store.lastRefreshAt).not.toBeNull();
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
    h.emit("database.opened", { tables: [], views: [] });
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
    replySchema(h);
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

  it("does not execute bindings for panels outside the visible window", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [panel, { ...panel, id: "p2", name: "Hidden" }] },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    service.setVisiblePanels(["p1"]);
    await flushPromises();
    replySchema(h);
    await flushPromises();
    const requests = h.posted.filter((item) => item.type === "dashboard.queryRequested");
    expect(requests).toHaveLength(1);
    expect(requests[0]?.payload).toMatchObject({ query: { collection: "orders" } });
    h.emit("dashboard.queryLoaded", { rows: [], truncated: false, maxPoints: 100 }, String(requests[0]!.requestId));
    await flushPromises();
    expect(store.panelData.p2?.state).toBe("idle");
  });

  it("uses clientPanelIds to remove draft references from the saved workspace", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [panel] },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    store.receiveManifest({
      manifest: {
        manifestVersion: "dashboard-panel-manifest.v2",
        panels: ["bar", "label"].map((type) => ({
          type, minSize: { x: 0, y: 0, width: 1, height: 1 },
          optionsSchema: { type: "object", properties: {}, additionalProperties: false },
          rendererVersion: "2",
        })),
      }, queryLimits: {},
    });
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
    replySchema(h);
    await flushPromises();
    const linkedRequest = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
    expect(linkedRequest.payload).toMatchObject({ query: { filters: [
      { field: "region_code", operator: "eq", value: "east" },
    ] } });
    h.emit("dashboard.queryLoaded", { rows: [], truncated: false }, String(linkedRequest.requestId));
    await flushPromises();
  });

  it("reacts to relevant realtime, offline, online, and visibility lifecycle changes", async () => {
    vi.useFakeTimers();
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [panel] },
      config: { refreshInterval: 30 }, revision: "r1", queryLimits: {},
    });
    store.setPanelState("p1", { state: "ready", error: null });
    const service = useDashboardService(); service.init();

    h.emit("data.changed", { tableId: "unrelated" });
    expect(store.panelData.p1?.state).not.toBe("stale");
    h.emit("data.changed", { tableId: "orders" });
    expect(store.panelData.p1?.state).toBe("stale");
    await vi.advanceTimersByTimeAsync(350);
    await flushPromises();
    replySchema(h);
    await flushPromises();
    const realtimeQuery = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
    h.emit("dashboard.queryLoaded", { rows: [], truncated: false }, String(realtimeQuery.requestId));
    await flushPromises();

    window.dispatchEvent(new Event("offline"));
    expect(store.offline).toBe(true);
    window.dispatchEvent(new Event("online"));
    expect(store.offline).toBe(false);
    await flushPromises();
    const onlineQuery = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested");
    if (onlineQuery && onlineQuery.requestId !== realtimeQuery.requestId) {
      h.emit("dashboard.queryLoaded", { rows: [], truncated: false }, String(onlineQuery.requestId));
    }

    Object.defineProperty(document, "hidden", { configurable: true, value: true });
    document.dispatchEvent(new Event("visibilitychange"));
    Object.defineProperty(document, "hidden", { configurable: true, value: false });
    document.dispatchEvent(new Event("visibilitychange"));
    service.dispose();
    vi.useRealTimers();
  });

  it("keeps save and delete failures fail-closed, including an edit conflict", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveManifest({
      manifest: {
        manifestVersion: "dashboard-panel-manifest.v2",
        panels: [{
          type: "bar", minSize: { x: 0, y: 0, width: 1, height: 1 },
          optionsSchema: { type: "object", properties: {}, additionalProperties: false },
          rendererVersion: "2",
        }],
      }, queryLimits: {},
    });
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [panel] },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    service.beginEdit();
    useDashboardDraftStore().rename("Changed", "");
    const saving = service.save();
    await flushPromises();
    const saveRequest = [...h.posted].reverse().find((item) => item.type === "dashboard.saveRequested")!;
    h.emit("operation.failed", { message: "another editor won", code: "dashboard_edit_conflict" }, String(saveRequest.requestId));
    await saving;
    expect(useDashboardDraftStore().conflict?.message).toContain("another editor won");
    expect(store.error).toContain("another editor won");

    const deleting = service.deleteCurrent();
    await flushPromises();
    reply(h, "dashboard.deleteRequested", "dashboard.deleted", { deleted: "wrong-id" });
    await deleting;
    expect(store.error).toContain("invalid result");
  });

  it("creates every supported template and refuses save responses without a workspace", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveManifest({
      manifest: {
        manifestVersion: "dashboard-panel-manifest.v2",
        panels: ["label", "metric", "donut", "time-series", "list", "line", "bar", "metric-list"].map((type) => ({
          type, minSize: { x: 0, y: 0, width: 1, height: 1 },
          optionsSchema: { type: "object", properties: {}, additionalProperties: false },
          rendererVersion: "2",
        })),
      }, queryLimits: {},
    });
    const service = useDashboardService();
    for (const template of ["blank", "operations-overview", "trend-analysis", "detail-monitoring"] as const) {
      service.createFromTemplate(template, template);
      expect(useDashboardDraftStore().draft?.name).toBe(template);
      useDashboardDraftStore().stop();
    }
    service.createFromTemplate("operations-overview", "Save me");
    const saving = service.save();
    await flushPromises();
    reply(h, "dashboard.saveRequested", "dashboard.saved", { workspace: null, clientPanelIds: {} });
    await saving;
    expect(store.error).toContain("no workspace");
  });

  it("normalizes record and aggregate query edge values at the authoritative bridge", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    const recordWire = {
      ...panel,
      id: "records",
      type: "list",
      options: { collection: "orders" },
      query: {
        kind: "records", collection: "orders", fields: ["id", "status"], limit: 999,
        filters: [
          { field: "status", operator: "contains", value: "paid" },
        ],
        sorts: [{ field: "status", direction: "desc" }],
      },
    };
    const aggregateWire = {
      ...panel,
      id: "aggregate",
      query: {
        kind: "aggregate", collection: "orders", dimensions: ["status"],
        measures: [{ key: "measure1", op: "count", field: null }, { key: "measure3", op: "max", field: "amount" }],
        timeBucket: { field: "created_at", unit: "month" }, limit: -1, topN: 99_999,
      },
    };
    store.receiveWorkspace({
      dashboard: { id: "d1", name: "Ops", note: "", panels: [recordWire, aggregateWire] },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    const recordPanel = store.current!.panels[0]!;
    expect(recordPanel.editable).toBe(true);
    expect(recordPanel.productType).toBe("list");
    const recordPreview = service.previewPanel(recordPanel);
    await flushPromises();
    replySchema(h);
    await flushPromises();
    const recordRequest = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
    expect(store.panelData.records?.error).toBeNull();
    expect(recordRequest.payload).toMatchObject({ query: {
      kind: "records", collection: "orders", fields: ["id", "status"], limit: 100,
      filters: [{ field: "status", operator: "contains", value: "paid" }],
      sorts: [{ field: "status", direction: "desc" }],
    } });
    h.emit("dashboard.queryLoaded", { rows: [], truncated: false }, String(recordRequest.requestId));
    await recordPreview;

    const aggregatePanel = store.current!.panels[1]!;
    const aggregatePreview = service.previewPanel(aggregatePanel);
    await flushPromises();
    const aggregateRequest = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
    expect(aggregateRequest.payload).toMatchObject({ query: {
      dimensions: ["status"],
      measures: [{ key: "measure1", op: "count", field: null }, { key: "measure3", op: "max", field: "amount" }],
      timeBucket: { field: "created_at", unit: "month", timezone: "UTC" },
      limit: 1, topN: 1,
    } });
    h.emit("dashboard.queryLoaded", { rows: [], truncated: false }, String(aggregateRequest.requestId));
    await aggregatePreview;
  });

  it("preserves every closed filter, aggregate, sort, and time-bucket variant", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    const operators = [
      "eq", "ne", "in", "contains", "starts_with", "ends_with",
      "gt", "gte", "lt", "lte", "between", "is_null", "is_not_null",
    ] as const;
    const recordWire = {
      ...panel,
      id: "all-record-operators",
      type: "list",
      query: {
        kind: "records", collection: "orders", fields: ["id", 7, "status"],
        filters: [
          ...operators.map((operator) => ({ field: "status", operator, value: operator === "in" ? ["paid"] : "paid" })),
          null, { field: 7, operator: "eq", value: "bad" }, { field: "status", operator: "legacy", value: "bad" },
        ],
        sorts: [{ field: "status", direction: "asc" }, { field: "id", direction: "desc" }, null],
        limit: 20.5,
      },
    };
    const aggregate = (unit: string, id: string) => ({
      ...panel,
      id,
      query: {
        kind: "aggregate", collection: "orders", dimensions: ["status", null],
        measures: [
          { key: "count", op: "count", field: null },
          { key: "distinct", op: "countDistinct", field: "id" },
          { key: "sum", op: "sum", field: "amount" },
          { key: "avg", op: "avg", field: "amount" },
          { key: "min", op: "min", field: "amount" },
          { op: "max", field: "amount" },
          null, { key: "bad", op: "median", field: "amount" },
        ],
        filters: null,
        timeBucket: { field: "created_at", unit },
        limit: "bad",
        topN: "bad",
      },
    });
    store.receiveWorkspace({
      dashboard: {
        id: "d1", name: "Ops", note: "",
        panels: [recordWire, aggregate("day", "day"), aggregate("week", "week"), aggregate("month", "month")],
      },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    for (const current of store.current!.panels) {
      const preview = service.previewPanel(current);
      await flushPromises();
      if (h.posted.some((item) => item.type === "schema.describe")) replySchema(h);
      await flushPromises();
      const request = [...h.posted].reverse().find((item) => item.type === "dashboard.queryRequested")!;
      expect(request).toBeTruthy();
      h.emit("dashboard.queryLoaded", { rows: [], truncated: false }, String(request.requestId));
      await preview;
      h.posted.splice(0);
    }
    expect(store.panelData["all-record-operators"]?.error).toBeNull();
    expect(store.panelData.day?.error).toBeNull();
  });

  it("does not query incomplete or unknown panel queries", async () => {
    const h = harness(); setHostBridgeForTesting(h.bridge);
    const store = useDashboardStore();
    store.receiveWorkspace({
      dashboard: {
        id: "d1", name: "Invalid", note: "", panels: [
          { ...panel, id: "no-collection", options: { collection: "orders" }, query: { kind: "records", fields: ["id"] } },
          { ...panel, id: "empty-records", query: { kind: "records", collection: "orders", fields: null } },
          { ...panel, id: "empty-aggregate", query: { kind: "aggregate", collection: "orders", measures: [null, { op: "median" }] } },
          { ...panel, id: "unknown-query", query: { kind: "legacy", collection: "orders" } },
          { ...panel, id: "static", type: "label", query: {} },
        ],
      },
      config: {}, revision: "r1", queryLimits: {},
    });
    const service = useDashboardService();
    for (const current of store.current!.panels) await service.previewPanel(current);
    expect(h.posted.some((item) => item.type === "dashboard.queryRequested")).toBe(false);
  });
});
