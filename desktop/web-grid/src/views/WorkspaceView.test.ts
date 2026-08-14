import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia, type Pinia } from "pinia";
import { defineComponent, h } from "vue";
import { NDropdown, NMessageProvider } from "naive-ui";

import WorkspaceView from "./WorkspaceView.vue";
import GridHost from "@/components/grid/GridHost.vue";
import DataSourceViewBar from "@/components/grid/DataSourceViewBar.vue";
import RelationEditorPanel from "@/components/grid/RelationEditorPanel.vue";
import ContentRecordPanel from "@/content/ContentRecordPanel.vue";
import FileWorkspaceView from "./FileWorkspaceView.vue";
import ManagedAttachmentCell from "@/components/attachments/ManagedAttachmentCell.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useTableStore } from "@/stores/tableStore";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import { useSurfaceStore } from "@/stores/surfaceStore";
import { usePresetVersionStore } from "@/stores/presetVersionStore";
import { setLocale } from "@/i18n";
import {
  setWorkspaceV2UiPort,
  type WorkspaceV2UiPort,
} from "@/services/workspaceV2UiPort";
import type {
  NormalizedRelationDescriptor,
  PastePlan,
  PasteSummary,
  RelationTargetRef,
  PresetEntry,
} from "@/contracts";

/**
 * WorkspaceView integration tests. WorkspaceView is the CONTAINER that wires
 * child-component emits to service calls and runs service.init() on mount. We
 * verify the emit -> service wiring by inspecting the resulting store / UI
 * state changes (the services are the bridge between emits and stores).
 *
 * Tabulator (mounted by GridHost) does not run in jsdom, so we mock createGrid
 * to avoid layout-dependent failures (mirrors useTabulator.test.ts).
 */

interface Outbound {
  type: string;
  payload: unknown;
  requestId?: string;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function makeRecordingBridge(): {
  bridge: HostBridge;
  posted: Outbound[];
  emit: (message: unknown) => void;
} {
  const posted: Outbound[] = [];
  const listeners: Array<(event: { data: unknown }) => void> = [];
  const shim = {
    addEventListener: (_type: "message", listener: (event: { data: unknown }) => void) => listeners.push(listener),
    removeEventListener: (_type: "message", listener: (event: { data: unknown }) => void) => {
      const index = listeners.indexOf(listener);
      if (index >= 0) listeners.splice(index, 1);
    },
    postMessage: (msg: unknown) => {
      // The bridge posts a BridgeMessage envelope object (not a string) in
      // unit-test shims (the C# host in production posts via PostWebMessageAsString,
      // which would arrive as JSON at the host side; here we capture pre-string form).
      if (typeof msg === "string") {
        try {
          const parsed = JSON.parse(msg) as { type: string; payload?: unknown; requestId?: string };
          posted.push({ type: parsed.type, payload: parsed.payload, requestId: parsed.requestId });
          return;
        } catch {
          posted.push({ type: msg, payload: undefined });
          return;
        }
      }
      const env = msg as { type?: string; payload?: unknown; requestId?: string };
      posted.push({ type: env.type ?? "(unknown)", payload: env.payload, requestId: env.requestId });
    },
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return {
    bridge,
    posted,
    emit: (message: unknown) => listeners.forEach((listener) => listener({ data: message })),
  };
}

/**
 * The Tabulator instance returned by the mocked createGrid. Tests can mutate
 * `mockTabulatorRef.current` (e.g. install a fresh `getRanges` stub) BEFORE
 * mounting WorkspaceView so the GridHost's provide/inject sees the new value.
 */
const mockTabulatorRef: {
  current: {
    setData: ReturnType<typeof vi.fn>;
    setColumns: ReturnType<typeof vi.fn>;
    destroy: ReturnType<typeof vi.fn>;
    getRanges: () => unknown[];
  };
} = {
  current: {
    setData: vi.fn().mockResolvedValue(undefined),
    setColumns: vi.fn(),
    destroy: vi.fn(),
    getRanges: () => [],
  },
};

vi.mock("@/grid/createGrid", () => ({
  createGrid: () => mockTabulatorRef.current,
  buildTabulatorColumns: () => [],
  ROW_NUMBER_FIELD: "__vt_row_number",
}));

let testPinia: Pinia;

function mountView({ realTransitions = false }: { realTransitions?: boolean } = {}) {
  // WorkspaceView.setup() calls useMessage() (to surface history.lastError),
  // which requires an ancestor NMessageProvider. Mirror App.vue's wrapper here.
  // We mount the wrapper with attachTo so queries against document.body still
  // work (e.g. [data-testid="sidebar-table-name"]).
  const Host = defineComponent({
    components: { NMessageProvider, WorkspaceView },
    render() {
      return h(NMessageProvider, () => h(WorkspaceView));
    },
  });
  const wrapper = mount(Host, {
    attachTo: document.body,
    global: {
      plugins: [testPinia],
      stubs: realTransitions ? { transition: false } : {},
    },
  });
  mountedViews.push(wrapper);
  return wrapper;
}

const mountedViews: ReturnType<typeof mount>[] = [];

/** Build a valid `PastePlan` carrying a non-consumed token for apply tests. */
function makePlan(token = "tok-xyz"): PastePlan {
  const summary: PasteSummary = {
    updateRows: 1,
    insertRows: 0,
    skipRows: 0,
    errorCount: 0,
    warningCount: 0,
  };
  return {
    collection: "users",
    schemaRevision: "schema-1",
    capabilityHash: "cap-1",
    summary,
    rows: [],
    diagnostics: [],
    token: { token, expiresAt: 0, consumed: false },
    overflow: false,
  };
}

describe("WorkspaceView", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    setLocale("zh-CN");
    testPinia = createPinia();
    setActivePinia(testPinia);
    // Reset the mocked Tabulator instance between tests (in particular,
    // restore getRanges to "no selection" so shortcut tests start clean).
    mockTabulatorRef.current = {
      setData: vi.fn().mockResolvedValue(undefined),
      setColumns: vi.fn(),
      destroy: vi.fn(),
      getRanges: () => [],
    };
  });

  afterEach(() => {
    for (const wrapper of mountedViews.splice(0)) wrapper.unmount();
    document.body.innerHTML = "";
    setHostBridgeForTesting(null);
    setWorkspaceV2UiPort(null);
    vi.restoreAllMocks();
  });

  it("mounts and calls service.init() for every service without errors", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.find(".workspace").exists()).toBe(true);
  });

  it("lets navigation leave the workspace center before a workspace is open", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceSessionStore().configureCapabilities(["workspace.session.v2"]);
    const wrapper = mountView();
    await flushPromises();

    expect(wrapper.find('[data-testid="workspace-center"]').exists()).toBe(true);

    await wrapper.get('[data-testid="nav-settings"]').trigger("click");
    await flushPromises();
    expect(wrapper.find('[data-testid="workspace-center"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="settings-view"]').isVisible()).toBe(true);

    await wrapper.get('[data-testid="nav-files"]').trigger("click");
    await flushPromises();
    expect(wrapper.find('[data-testid="workspace-center"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="file-workspace"]').exists()).toBe(true);

    await wrapper.get('[data-testid="nav-tables"]').trigger("click");
    await flushPromises();
    expect(wrapper.find('[data-testid="workspace-center"]').exists()).toBe(false);
    expect(wrapper.get(".tables-view").isVisible()).toBe(true);
  });

  it("renders every top-level product workspace from the shared navigation state", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceSessionStore().configureCapabilities(["conflict.center.v2"]);
    const ui = useUiStore();
    const wrapper = mountView();
    await flushPromises();

    const cases = [
      ["home", ".home-view"],
      ["tables", ".tables-view"],
      ["dashboard", null],
      ["interfaces", null],
      ["files", "[data-testid='file-workspace']"],
      ["search", "[data-testid='workspace-search-view']"],
      ["conflicts", "[data-testid='conflict-center']"],
      ["plugins", ".plugin-center"],
      ["settings", "[data-testid='settings-view']"],
    ] as const;
    for (const [view, selector] of cases) {
      await wrapper.get(`[data-testid="nav-${view}"]`).trigger("click");
      await flushPromises();
      expect(ui.activeView).toBe(view);
      if (selector === null) continue;
      await vi.waitFor(() => {
        expect(wrapper.find(selector).exists(), `${view} should render`).toBe(true);
      });
    }
  });

  it("passes remembered document labels to a Content panel after WorkspaceView remounts", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const documents = useDocumentWorkspaceStore();
    const documentId = "11111111-1111-4111-8111-111111111111";
    documents.setPage([{
      documentId,
      entryHandle: "entry-1",
      displayName: "content-reference-a.md",
      relativePath: "content-reference-a.md",
      extension: "md",
      authority: "workspace",
      availability: "available",
      mimeType: "text/markdown",
      sizeBytes: 71,
      effectiveRevisionCreatedAt: "2026-08-13T00:00:00Z",
      formalVersion: 1,
      status: "active",
      capabilities: [],
    }], null, 14, false);
    documents.setPage([], null, 15, false);

    const firstView = mountView();
    await flushPromises();
    expect(firstView.findComponent(ContentRecordPanel).props("documentLabels")).toEqual({
      [documentId]: "content-reference-a.md",
    });
    firstView.unmount();

    const reopenedView = mountView();
    await flushPromises();
    expect(reopenedView.findComponent(ContentRecordPanel).props("documentLabels")).toEqual({
      [documentId]: "content-reference-a.md",
    });
  });

  it("projects a successful unlink out of the active Content document set", async () => {
    const handlers = new Map<string, (payload: never) => void>();
    const documentId = "11111111-1111-4111-8111-111111111111";
    const activeDocument = {
      documentId,
      entryHandle: "entry-1",
      displayName: "content-reference-a.md",
      relativePath: "content-reference-a.md",
      extension: "md",
      availability: "available",
      mimeType: "text/markdown",
      sizeBytes: 71,
      effectiveRevisionCreatedAt: "2026-08-13T00:00:00Z",
      formalVersion: 1,
      status: "active",
      currentRevision: "revision-1",
      effectiveRevisionId: "22222222-2222-4222-8222-222222222222",
      capabilities: ["open", "unlink"],
    };
    setHostBridgeForTesting({
      request: vi.fn((type: string) => type === "document.listRequested"
        ? Promise.resolve({ entries: [activeDocument], nextCursor: null, topologyRevision: 14 })
        : new Promise(() => undefined)),
      notify: vi.fn(),
      on: vi.fn((type: string, handler: (payload: never) => void) => {
        handlers.set(type, handler);
        return () => handlers.delete(type);
      }),
    } as unknown as HostBridge);
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2", "fileHistory.tree.v2"]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "33333333-3333-4333-8333-333333333333",
      displayName: "测试工作区",
      selectedRoot: "D:\\Workspace",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: "2026-08-13T00:00:00Z",
      lastKnownHealth: "healthy",
      lastSnapshotAt: null,
      lastSyncAt: null,
      pendingSync: false,
    }]);
    session.applySession({
      contractVersion: "2.0",
      workspaceId: "33333333-3333-4333-8333-333333333333",
      sessionEpoch: 1,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    });
    setWorkspaceV2UiPort({
      request: vi.fn(async () => ({
        ...activeDocument,
        contractVersion: "2.0",
        status: "deleted",
      })),
    } as unknown as WorkspaceV2UiPort);
    useUiStore().navigate("files");
    const wrapper = mountView();
    await flushPromises();

    wrapper.findComponent(FileWorkspaceView).vm.$emit("intent", {
      type: "document.listRequested",
      scope: { kind: "global" },
      authority: "workspace",
      query: {
        logic: "and",
        filters: [{ field: "status", operator: "eq", value: "active" }],
        sort: [],
        limit: 100,
        cursor: null,
      },
    });
    await flushPromises();
    const documents = useDocumentWorkspaceStore();
    expect(documents.entries[0]?.status).toBe("active");
    expect(documents.documentLabels[documentId]).toBe("content-reference-a.md");

    wrapper.findComponent(FileWorkspaceView).vm.$emit("workspaceV2Action", {
      method: "fileHistory.unlink",
      params: {
        documentId,
        expectedEffectiveRevisionId: activeDocument.effectiveRevisionId,
      },
    });
    await flushPromises();

    expect(documents.entries).toEqual([]);
    expect(documents.documentLabels[documentId]).toBe("content-reference-a.md");
    expect(wrapper.findComponent(ContentRecordPanel).props("documents")).toEqual([]);
  });

  it("switches the active data source through every supported record presentation", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders", metadata: {} }], { orders: "Orders" });
    workspace.selectTable("orders");
    useUiStore().navigate("tables");
    const table = useTableStore();
    table.setDatasetReady({
      table: "orders",
      columns: [
        { name: "title", title: "标题", dataType: "text", editable: true, nullable: true },
        { name: "status", title: "状态", dataType: "text", editable: true, nullable: true },
        { name: "start", title: "开始", dataType: "date", editable: true, nullable: true },
        { name: "end", title: "结束", dataType: "date", editable: true, nullable: true },
        { name: "cover", title: "封面", dataType: "text", editable: true, nullable: true },
      ],
      rows: [{ rowKey: "1", title: "任务", status: "进行中", start: "2026-08-12", end: "2026-08-13", cover: null }],
      offset: 0,
      limit: 100,
      totalRows: 1,
      mode: "remote",
      revision: { databaseSessionId: "session-1", schemaRevision: "schema-1", dataRevision: 1 },
    });
    const presets = usePresetVersionStore();
    const entries = (["table", "calendar", "timeline", "kanban", "gallery"] as const)
      .map((kind, index): PresetEntry => ({
        id: `view-${kind}`,
        collection: "orders",
        name: kind,
        scope: "personal",
        revision: `revision-${index}`,
        emittedEvents: [],
        view: {
          kind,
          layout: kind,
          filters: [],
          sorts: [],
          search: "",
          visibleFields: ["title", "status", "start", "end", "cover"],
          dateField: "start",
          endDateField: "end",
          titleField: "title",
          groupField: "status",
          coverField: "cover",
        },
      }));
    presets.receivePresets({ collection: "orders", presets: entries });
    const wrapper = mountView();
    await flushPromises();
    workspace.selectTable("orders");
    useUiStore().navigate("tables");
    table.setDatasetReady({
      table: "orders",
      columns: table.schema ?? [],
      rows: [{ rowKey: "1", title: "任务", status: "进行中", start: "2026-08-12", end: "2026-08-13", cover: null }],
      offset: 0,
      limit: 100,
      totalRows: 1,
      mode: "remote",
      revision: { databaseSessionId: "session-1", schemaRevision: "schema-1", dataRevision: 1 },
    });
    presets.receivePresets({ collection: "orders", presets: entries });
    await flushPromises();

    const selectors: Record<(typeof entries)[number]["view"]["kind"] & string, string> = {
      table: ".grid-host",
      calendar: "[data-testid='record-calendar-view']",
      timeline: "[data-testid='record-timeline-view']",
      kanban: "[data-testid='record-kanban-view']",
      gallery: "[data-testid='record-gallery-view']",
    };
    for (const entry of entries) {
      presets.activatePreset(entry.id);
      await flushPromises();
      expect(
        wrapper.find(selectors[entry.view.kind!]).exists(),
        `${entry.view.kind} should render`,
      ).toBe(true);
    }
  });

  it("manages persisted views through create, duplicate, rename, default, switch, save, and delete", async () => {
    const { bridge, posted, emit } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders", metadata: {} }], { orders: "Orders" });
    workspace.selectTable("orders");
    useUiStore().navigate("tables");
    const table = useTableStore();
    table.setDatasetReady({
      table: "orders",
      columns: [
        { name: "title", title: "标题", dataType: "text", editable: true, nullable: true },
        { name: "status", title: "状态", dataType: "text", editable: true, nullable: true },
        { name: "start", title: "开始", dataType: "date", editable: true, nullable: true },
      ],
      rows: [{ rowKey: "1", title: "任务", status: "进行中", start: "2026-08-12" }],
      offset: 0, limit: 100, totalRows: 1, mode: "remote",
      revision: { databaseSessionId: "session-1", schemaRevision: "schema-1", dataRevision: 1 },
    });
    const entry = (id: string, name: string, isDefault = false): PresetEntry => ({
      id, collection: "orders", name, scope: "personal", revision: `revision-${id}`,
      emittedEvents: [],
      view: {
        kind: "table", layout: "table", filters: [], sorts: [], search: "",
        visibleFields: ["title", "status", "start"], isDefault,
      },
    });
    const wrapper = mountView();
    await flushPromises();
    workspace.selectTable("orders");
    table.setDatasetReady({
      table: "orders", columns: table.schema ?? [],
      rows: [{ rowKey: "1", title: "任务", status: "进行中", start: "2026-08-12" }],
      offset: 0, limit: 100, totalRows: 1, mode: "remote",
      revision: { databaseSessionId: "session-1", schemaRevision: "schema-1", dataRevision: 1 },
    });
    await flushPromises();
    const listRequest = [...posted].reverse().find((item) => item.type === "preset.list")!;
    const first = entry("view-1", "默认", true);
    const second = entry("view-2", "备选");
    emit({ type: "preset.list", requestId: listRequest.requestId, payload: { collection: "orders", presets: [first, second] } });
    await flushPromises();

    const bar = wrapper.findComponent(DataSourceViewBar);
    expect(bar.exists()).toBe(true);
    const answerSave = async (saved: PresetEntry) => {
      await flushPromises();
      const request = [...posted].reverse().find((item) => item.type === "preset.save")!;
      emit({ type: "preset.save", requestId: request.requestId, payload: saved });
      await flushPromises();
    };

    bar.vm.$emit("create", {
      name: "日历", kind: "calendar", dateField: "start", endDateField: null,
      titleField: "title", groupField: null, coverField: null,
    });
    const created = { ...entry("view-3", "日历"), view: { ...entry("view-3", "日历").view, kind: "calendar" as const, layout: "calendar" as const, dateField: "start", titleField: "title" } };
    await answerSave(created);
    expect(usePresetVersionStore().activePresetId).toBe("view-3");

    bar.vm.$emit("duplicate", created, "日历副本");
    const duplicated = { ...created, id: "view-4", name: "日历副本", revision: "revision-view-4", view: { ...created.view, isDefault: false } };
    await answerSave(duplicated);
    bar.vm.$emit("rename", duplicated, "迭代日历");
    const renamed = { ...duplicated, name: "迭代日历", revision: "revision-renamed" };
    await answerSave(renamed);

    bar.vm.$emit("setDefault", renamed);
    const promoted = { ...renamed, view: { ...renamed.view, isDefault: true } };
    await answerSave(promoted);
    await answerSave({ ...first, view: { ...first.view, isDefault: false } });
    expect(usePresetVersionStore().activePresetId).toBe("view-4");

    bar.vm.$emit("switch", second);
    await answerSave(promoted);
    expect(usePresetVersionStore().activePresetId).toBe("view-2");
    bar.vm.$emit("save", second);
    await answerSave({ ...second, revision: "revision-saved" });

    bar.vm.$emit("delete", second);
    await flushPromises();
    const deleteRequest = [...posted].reverse().find((item) => item.type === "preset.delete")!;
    emit({ type: "preset.delete", requestId: deleteRequest.requestId, payload: { deleted: second.id } });
    await flushPromises();
    expect(usePresetVersionStore().presets.some((item) => item.id === second.id)).toBe(false);
  });

  it("opens Interfaces and guards its unsaved draft before leaving", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    vi.spyOn(window, "prompt").mockReturnValue("Operations");
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const wrapper = mountView();
    await flushPromises();

    await wrapper.get('[data-testid="nav-interfaces"]').trigger("click");
    await flushPromises();
    await vi.waitFor(() => {
      expect(wrapper.find('[data-testid="interface-workspace"]').exists()).toBe(true);
    });
    expect(posted.some((item) => item.type === "interface.listRequested")).toBe(true);

    await wrapper.get('[data-testid="interface-create"]').trigger("click");
    expect(useSurfaceStore().dirty).toBe(true);
    await wrapper.get('[data-testid="nav-files"]').trigger("click");
    expect(confirm).toHaveBeenCalledOnce();
    expect(useUiStore().activeView).toBe("interfaces");

    confirm.mockReturnValue(true);
    await wrapper.get('[data-testid="nav-files"]').trigger("click");
    await flushPromises();
    expect(useUiStore().activeView).toBe("files");
    expect(useSurfaceStore().dirty).toBe(false);
  });

  it("shows a localized non-blocking recovery path for stale edits", async () => {
    const { bridge, emit, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();

    emit({
      type: "table.editRejected",
      payload: {
        kind: "edit_conflict",
        message: "The row changed before the edit could be applied.",
        conflictingRowKeys: ["row-1"],
      },
    });
    await flushPromises();

    const notice = wrapper.get('[data-testid="edit-rejection-notice"]');
    expect(notice.text()).toContain("数据已在其他位置更新");
    expect(notice.text()).not.toContain("The row changed");
    expect(useTableStore().error).toBeNull();
    expect(wrapper.find('[data-testid="table-error-overlay"]').exists()).toBe(false);

    const refreshCount = posted.filter((item) => item.type === "table.selected").length;
    await wrapper.get('[data-testid="edit-rejection-reload"]').trigger("click");
    expect(
      posted.filter((item) => item.type === "table.selected"),
    ).toHaveLength(refreshCount + 1);
    expect(wrapper.find('[data-testid="edit-rejection-notice"]').exists()).toBe(false);
  });

  it("shows authoritative formula backfill progress from task.changed", async () => {
    const { bridge, emit } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();

    emit({
      type: "task.changed",
      payload: {
        contractVersion: "2.0",
        topic: "task.changed",
        eventId: "backfill-42",
        sequence: 42,
        occurredAt: "2026-07-24T08:30:00Z",
        taskId: "formula-orders",
        taskType: "formulaBackfill",
        state: "running",
        progress: 0.64,
        cursor: "6400",
        error: null,
      },
    });
    await flushPromises();

    const status = wrapper.get('[data-testid="realtime-task-progress"]');
    expect(status.text()).toContain("64%");
    expect(status.get('[role="progressbar"]').attributes("aria-valuenow")).toBe("64");
  });

  it("does not label unrelated background tasks as formula backfills", async () => {
    const { bridge, emit } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();

    emit({
      type: "task.changed",
      payload: {
        contractVersion: "2.0",
        topic: "task.changed",
        eventId: "export-7",
        sequence: 7,
        occurredAt: "2026-07-24T08:30:00Z",
        taskId: "export-orders",
        taskType: "export",
        state: "running",
        progress: 0.3,
        cursor: "3000",
        error: null,
      },
    });
    await flushPromises();

    expect(wrapper.find('[data-testid="realtime-task-progress"]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain("旧值将在重算完成后刷新");
  });

  it("closes the controlled grid context menu when Esc requests show=false", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();

    wrapper.findComponent(GridHost).vm.$emit("rowContext", {
      rowKey: "row-1",
      field: "status",
      x: 20,
      y: 30,
    });
    await flushPromises();
    const dropdown = wrapper.findAllComponents(NDropdown)
      .find((candidate) =>
        candidate.props("trigger") === "manual"
        && candidate.props("show") === true)!;
    expect(dropdown.props("show")).toBe(true);

    dropdown.vm.$emit("update:show", false);
    await flushPromises();
    expect(dropdown.props("show")).toBe(false);
  });

  it("runs export through the renderer-host task bridge", async () => {
    const { bridge, posted, emit } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();
    posted.length = 0;

    wrapper.findComponent(AppToolbar).vm.$emit("exportData");
    await flushPromises();
    const grantRequest = posted.at(-1)!;
    expect(grantRequest.type).toBe("data.exportTargetRequested");
    emit({
      type: "data.exportTargetRequested",
      requestId: grantRequest.requestId,
      payload: { grantId: "grant-export", displayName: "orders-export.csv" },
    });
    await flushPromises();

    const taskRequest = posted.at(-1)!;
    expect(taskRequest.type).toBe("task.create");
    emit({
      type: "task.create",
      requestId: taskRequest.requestId,
      payload: {
        taskId: "task-export",
        kind: "data.export",
        state: "succeeded",
        progress: 1,
        message: "done",
        result: {
          grantId: "grant-export",
          rowsWritten: 3,
          outputDisplayName: "orders-export.csv",
          format: "csv",
          lookupRevision: null,
        },
        error: null,
      },
    });
    await flushPromises();

    expect(posted.map((item) => item.type)).toEqual([
      "data.exportTargetRequested",
      "task.create",
    ]);
    expect(document.body.textContent).toContain("已导出 3 行至 orders-export.csv。");
  });

  it("runs a validated import and refreshes the active table", async () => {
    const { bridge, posted, emit } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    table.revision = {
      databaseSessionId: "session-1",
      schemaRevision: "schema-1",
      dataRevision: 1,
    };
    const wrapper = mountView();
    await flushPromises();
    posted.length = 0;

    wrapper.findComponent(AppToolbar).vm.$emit("importData");
    await flushPromises();
    let request = posted.at(-1)!;
    emit({
      type: request.type,
      requestId: request.requestId,
      payload: { grantId: "grant-import", displayName: "orders.csv" },
    });
    await flushPromises();

    request = posted.at(-1)!;
    expect(request.type).toBe("data.previewImport");
    emit({
      type: request.type,
      requestId: request.requestId,
      payload: {
        collection: "orders",
        schemaRevision: "schema-1",
        capabilityHash: "cap-1",
        sourceHash: "source-1",
        token: { token: "import-token", expiresAt: 9999999999, consumed: false },
        summary: {
          validRows: 2, errorRows: 0, warningRows: 0, totalRows: 2,
          errorCount: 0, warningCount: 0,
        },
        rows: [{
          sourceRow: 2,
          values: { number: "A-1" },
          diagnostics: [],
          relationResolutions: [],
        }],
        unmatchedColumns: [],
        diagnostics: [],
      },
    });
    await flushPromises();

    expect(document.body.querySelector('[data-testid="import-preview-panel"]')).toBeTruthy();
    expect(posted.at(-1)?.type).toBe("data.previewImport");
    (document.body.querySelector('[data-testid="import-confirm"]') as HTMLElement).click();
    await flushPromises();

    request = posted.at(-1)!;
    expect(request.type).toBe("task.create");
    emit({
      type: request.type,
      requestId: request.requestId,
      payload: {
        taskId: "task-import",
        kind: "data.import",
        state: "succeeded",
        progress: 1,
        message: "done",
        result: {
          collection: "orders",
          createdCount: 2,
          updatedCount: 0,
          failedRows: [],
          chunks: [],
          requestIds: [],
        },
        error: null,
      },
    });
    await flushPromises();

    expect(posted.map((item) => item.type)).toEqual(expect.arrayContaining([
      "data.importSourceRequested",
      "data.previewImport",
      "task.create",
    ]));
    expect(document.body.querySelector('[data-testid="import-preview-panel"]')).toBeFalsy();
    expect(document.body.textContent).toContain("已导入 2 行。");
  });

  it("opens the unified relation field settings drawer from the table toolbar", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened(
      [{ collection: "orders" }, { collection: "contracts" }],
      { orders: "Orders", contracts: "Contracts" },
    );
    workspace.selectTable("orders");
    mountView();
    await flushPromises();

    const trigger = document.body.querySelector('[data-testid="toolbar-field-manager"]') as HTMLElement;
    expect(trigger).toBeTruthy();
    trigger.click();
    await flushPromises();

    expect(document.body.textContent).toContain("SCHEMA V2");
    expect(document.body.textContent).toContain("正在读取字段能力与推荐设置");
    expect(posted).toEqual(expect.arrayContaining([
      expect.objectContaining({
        type: "field.settings.describe",
        payload: { tableId: "orders" },
      }),
    ]));
  });

  it("creates a visual relation target and applies it as one target object", async () => {
    const descriptor: NormalizedRelationDescriptor = {
      relationId: "orders.contract", fieldRef: "contract", sourceCollection: "orders", kind: "m2o",
      relatedCollection: "contracts", unique: false, nullable: true,
      onDelete: "nullify", preset: "standard", selfRelation: false, managed: true, state: "valid",
      diagnostics: [],
    };
    const target: RelationTargetRef = {
      collection: "contracts", itemId: "contract-7", label: "CT-0007",
    };
    const request = vi.fn(async (method: string, payload: unknown) => {
      if (method === "schema.describe") {
        return {
          contract: "vibetable.schema-describe.v1",
          collection: "orders",
          requestGeneration: (payload as { requestGeneration: number }).requestGeneration,
          schema: {
            collection: "orders", primaryKey: "id",
            columns: [{
              name: "contract", title: "Contract", fieldId: "orders.contract", kind: "relation",
              relationId: "orders.contract", dataType: "text", editable: true, nullable: true,
            }],
            normalizedRelations: [descriptor], schemaRevision: "schema-1",
            permissionRevision: "permission-1", capabilityHash: "capability-1", lookupRevision: "lookup-1",
          },
          capabilities: {
            contract: "vibetable.relation-capabilities.v1",
            relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
          },
        };
      }
      if (method === "lookup.list") {
        return { collection: "orders", definitions: [], lookupRevision: "lookup-1" };
      }
      if (method === "relation.searchTargets") return { items: [target], total: 1 };
      if (method === "relation.createTarget") {
        return { outcome: "committed", target, requestId: "create-1" };
      }
      if (method === "relation.updateSingle") {
        return { outcome: "committed", current: target, schemaRevision: "schema-1", requestId: "update-1" };
      }
      throw new Error(`unexpected request: ${method}`);
    });
    setHostBridgeForTesting({
      request,
      on: vi.fn(() => vi.fn()),
      notify: vi.fn(),
      notifyWithAdditionalObjects: vi.fn(() => false),
      start: vi.fn(),
      stop: vi.fn(),
    } as unknown as HostBridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    const table = useTableStore();
    table.appendPage({
      table: "orders",
      columns: [{
        name: "contract", title: "Contract", fieldId: "orders.contract", kind: "relation",
        relationId: "orders.contract", dataType: "text", editable: true, nullable: true,
      }],
      rows: [{ rowKey: "order-1", contract: null }], offset: 0, limit: 1, totalRows: 1, mode: "remote",
    });
    const wrapper = mountView();
    await flushPromises();

    wrapper.findComponent(GridHost).vm.$emit("relationEdit", {
      rowKey: "order-1", field: "contract", descriptor, value: null,
    });
    await flushPromises();
    const editor = wrapper.findComponent(RelationEditorPanel);
    expect(editor.exists()).toBe(true);
    editor.vm.$emit("create", "CT-0007");
    await flushPromises();

    expect(table.allRows[0]?.contract).toEqual(target);
    expect(Array.isArray(table.allRows[0]?.contract)).toBe(false);
    expect(request).toHaveBeenCalledWith("relation.createTarget", expect.objectContaining({
      relationId: "orders.contract", label: "CT-0007",
    }));
    wrapper.unmount();
  });

  it("ignores a nested relation search response from a previously closed editor", async () => {
    const mainRelation = (field: string, targetCollection: string): NormalizedRelationDescriptor => ({
      relationId: `orders.${field}`, fieldRef: field, sourceCollection: "orders", kind: "m2o",
      relatedCollection: targetCollection, unique: false, nullable: true,
      onDelete: "nullify", preset: "standard", selfRelation: false, managed: true,
      quickCreateEligible: false, state: "valid", diagnostics: [],
    });
    const nestedRelation = (collection: string): NormalizedRelationDescriptor => ({
      relationId: `${collection}.region`, fieldRef: "region", sourceCollection: collection,
      kind: "m2o", relatedCollection: "regions", unique: false,
      nullable: false, onDelete: "restrict", preset: "standard", selfRelation: false,
      managed: true, quickCreateEligible: true, state: "valid", diagnostics: [],
    });
    const relationA = mainRelation("customer", "customers");
    const relationB = mainRelation("vendor", "vendors");
    const stale = deferred<{ items: RelationTargetRef[]; total: number }>();
    const current = deferred<{ items: RelationTargetRef[]; total: number }>();
    const productTable = (collection: string) => ({
      contract: "vibetable.product-table.v1",
      tableId: collection,
      displayName: collection,
      primaryDisplayFieldId: "name_id",
      fields: [
        {
          fieldId: "name_id", physicalName: "name", displayName: "名称",
          kind: "scalar", dataType: "shortText", storageType: "text",
          nullable: false, defaultValue: null, constraints: [],
          editor: { kind: "text", config: {} }, readOnly: false,
          formula: null, relation: null, lookup: null, attachmentPolicy: null,
        },
        {
          fieldId: "region_id", physicalName: "region", displayName: "区域",
          kind: "relation", dataType: "relation", storageType: "relation",
          nullable: false, defaultValue: null, constraints: [],
          editor: { kind: "relation", config: {} }, readOnly: false,
          formula: null, relation: {}, lookup: null, attachmentPolicy: null,
        },
      ],
    });
    const request = vi.fn(async (method: string, payload: unknown) => {
      const params = payload as Record<string, unknown>;
      if (method === "schema.getTable") return productTable(String(params.tableId));
      if (method === "schema.describe") {
        const collection = String(params.collection);
        const relations = collection === "orders"
          ? [relationA, relationB]
          : [nestedRelation(collection)];
        return {
          contract: "vibetable.schema-describe.v1", collection,
          requestGeneration: params.requestGeneration,
          schema: {
            collection, primaryKey: "id", primaryDisplayFieldId: "name_id", columns: [],
            normalizedRelations: relations, schemaRevision: `schema-${collection}`,
            permissionRevision: "permission-1", capabilityHash: "capability-1",
            lookupRevision: "lookup-1",
          },
          capabilities: {
            contract: "vibetable.relation-capabilities.v1",
            relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
          },
        };
      }
      if (method === "lookup.list") {
        return { collection: String(params.collection), definitions: [], lookupRevision: "lookup-1" };
      }
      if (method === "relation.searchTargets") {
        if (params.query === "旧区域") return stale.promise;
        if (params.query === "新区域") return current.promise;
        return { items: [], total: 0 };
      }
      throw new Error(`unexpected request: ${method}`);
    });
    setHostBridgeForTesting({
      request,
      on: vi.fn(() => vi.fn()),
      notify: vi.fn(),
      notifyWithAdditionalObjects: vi.fn(() => false),
      start: vi.fn(),
      stop: vi.fn(),
    } as unknown as HostBridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    const table = useTableStore();
    table.appendPage({
      table: "orders",
      columns: [
        { name: "customer", title: "客户", fieldId: "customer_id", kind: "relation",
          relationId: relationA.relationId, dataType: "text", editable: true, nullable: true },
        { name: "vendor", title: "供应商", fieldId: "vendor_id", kind: "relation",
          relationId: relationB.relationId, dataType: "text", editable: true, nullable: true },
      ],
      rows: [{ rowKey: "order-1", customer: null, vendor: null }],
      offset: 0, limit: 1, totalRows: 1, mode: "remote",
    });
    const wrapper = mountView();
    await flushPromises();

    wrapper.findComponent(GridHost).vm.$emit("relationEdit", {
      rowKey: "order-1", field: "customer", descriptor: relationA, value: null,
    });
    await flushPromises();
    let editor = wrapper.findComponent(RelationEditorPanel);
    editor.vm.$emit("searchCreateRelation", "region", "旧区域");
    await flushPromises();
    editor.vm.$emit("close");
    await flushPromises();

    wrapper.findComponent(GridHost).vm.$emit("relationEdit", {
      rowKey: "order-1", field: "vendor", descriptor: relationB, value: null,
    });
    await flushPromises();
    editor = wrapper.findComponent(RelationEditorPanel);
    editor.vm.$emit("searchCreateRelation", "region", "新区域");
    current.resolve({
      items: [{ collection: "regions", itemId: "same-id", label: "新区域" }],
      total: 1,
    });
    await flushPromises();
    expect(editor.props("targetRelationOptions")).toEqual({
      region: [{ collection: "regions", itemId: "same-id", label: "新区域" }],
    });

    stale.resolve({
      items: [{ collection: "regions", itemId: "same-id", label: "旧区域" }],
      total: 1,
    });
    await flushPromises();
    expect(editor.props("targetRelationOptions")).toEqual({
      region: [{ collection: "regions", itemId: "same-id", label: "新区域" }],
    });
  });

  it("routes grid sort/filter/group intent to the standard full-dataset table query", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }], { orders: "Orders" });
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();
    posted.length = 0;

    wrapper.findComponent(GridHost).vm.$emit("viewQueryChange", {
      headerFilters: [{ field: "status", operator: "eq", value: "signed", logic: "AND" }],
      sorts: [{ field: "contract_price", direction: "desc", nullsLast: true }],
      groups: [{ field: "customer", direction: "asc", bucket: "value" }],
    });
    await flushPromises();

    expect(posted).toContainEqual({
      type: "table.queryRequested",
      payload: {
        table: "orders",
        query: {
          filters: [{ field: "status", operator: "eq", value: "signed", logic: "AND" }],
          sorts: [{ field: "contract_price", direction: "desc", nullsLast: true }],
          groups: [{ field: "customer", direction: "asc", bucket: "value" }],
          offset: 0,
          limit: 500,
          groupOffset: 0,
          groupLimit: 100,
        },
      },
    });
  });

  it("opens whole-table history from the toolbar when there is no row or cell selection", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceStore().selectTable("orders");
    const revisions = useRevisionHistoryStore(testPinia);
    const wrapper = mountView();
    await flushPromises();

    wrapper.findComponent(AppToolbar).vm.$emit("openHistory");
    await flushPromises();

    const request = posted.find((item) => item.type === "history.queryRequested");
    expect(request?.payload).toMatchObject({ collection: "orders", scope: "table", limit: 50, offset: 0 });
    expect(revisions.panelOpen).toBe(true);
  });

  it("uses the exact single-cell selection for the toolbar history scope", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceStore().selectTable("orders");
    const revisions = useRevisionHistoryStore(testPinia);
    revisions.setSelection({ scope: "cell", itemId: "42", field: "status" });
    const wrapper = mountView();
    await flushPromises();

    wrapper.findComponent(AppToolbar).vm.$emit("openHistory");
    await flushPromises();

    const request = posted.find((item) => item.type === "history.queryRequested");
    expect(request?.payload).toMatchObject({
      collection: "orders",
      scope: "cell",
      itemId: "42",
      field: "status",
    });
  });

  it("falls back to table history when a restored dataset retires the selected field", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceStore().selectTable("orders");
    useTableStore().appendPage({
      table: "orders",
      columns: [{
        name: "current_status",
        title: "Status",
        dataType: "text",
        editable: true,
        nullable: true,
      }],
      rows: [{ rowKey: "42", current_status: "open" }],
      offset: 0,
      limit: 1,
      totalRows: 1,
      mode: "remote",
    });
    const revisions = useRevisionHistoryStore(testPinia);
    revisions.setSelection({ scope: "cell", itemId: "42", field: "retired_status" });
    const wrapper = mountView();
    await flushPromises();

    wrapper.findComponent(AppToolbar).vm.$emit("openHistory");
    await flushPromises();

    const request = posted.find((item) => item.type === "history.queryRequested");
    expect(request?.payload).toMatchObject({ collection: "orders", scope: "table" });
    expect(revisions.selection).toEqual({ scope: "table" });
  });

  it("refreshes the table and audit timeline after restore without creating a Ctrl+Z entry", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceStore().selectTable("orders");
    const undo = useHistoryStore();
    const revisions = useRevisionHistoryStore();
    revisions.open({ scope: "row", itemId: "42" });
    mountView();
    await flushPromises();
    posted.length = 0;

    // Correlated restore responses are consumed by the service, which commits
    // the validated result to the store. Exercise the container reaction from
    // that boundary instead of simulating an obsolete broadcast response.
    revisions.completeRestore({
      collection: "orders",
      itemId: "42",
      restoredToRevision: "r1",
      newRevisionId: "r3",
      item: { id: 42, status: "new" },
    });
    await flushPromises();

    expect(posted.some((item) => item.type === "table.selected")).toBe(true);
    expect(posted.some((item) => item.type === "history.queryRequested")).toBe(true);
    expect(undo.canUndo).toBe(false);
  });

  it("wires sidebar select -> tableService.selectTable (table.selected posted)", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "users", metadata: {} }], { users: "Users" });

    mountView();
    await flushPromises();

    const row = document.body.querySelector('[data-testid="sidebar-table-name"]');
    expect(row).toBeTruthy();
    (row as HTMLElement).click();
    await flushPromises();

    expect(workspace.currentTable).toBe("users");
    expect(posted.some((p) => p.type === "table.selected")).toBe(true);
  });

  it("wires the compact toolbar switcher through the same table selection service", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([
      { collection: "orders" },
      { collection: "users" },
    ], { orders: "Orders", users: "Users" });
    workspace.selectTable("orders");

    const wrapper = mountView();
    await flushPromises();
    posted.length = 0;

    wrapper.findComponent(AppToolbar).vm.$emit("selectTable", "users");
    await flushPromises();

    expect(workspace.currentTable).toBe("users");
    expect(posted).toContainEqual({
      type: "table.selected",
      payload: { table: "users" },
      requestId: undefined,
    });
  });

  it("wires sidebar newTable -> admin.openCreate + ui.openCreate", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const ui = useUiStore();
    const admin = useTableAdminStore();

    mountView();
    await flushPromises();

    const btn = document.body.querySelector('[data-testid="sidebar-new-table"]');
    expect(btn).toBeTruthy();
    (btn as HTMLElement).click();
    await flushPromises();

    expect(admin.phase).toBe("creating");
    expect(ui.createModalOpen).toBe(true);
  });

  it("routes the zero-row CTA through the existing insert-row mutation", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    workspace.setOpened([{ collection: "orders", metadata: {} }], { orders: "Orders" });
    workspace.selectTable("orders");
    table.setEditSchema([{
      name: "name",
      storageName: "name",
      dataType: "text",
      editable: true,
      nullable: true,
      primaryKey: false,
      editor: { kind: "text" },
      validation: [],
    }], {
      databaseSessionId: "session-1",
      schemaRevision: "schema-1",
      dataRevision: 1,
    });
    table.setDatasetReady({
      table: "orders",
      columns: [{
        name: "name",
        title: "Name",
        dataType: "text",
        editable: true,
        nullable: true,
      }],
      rows: [],
      offset: 0,
      limit: 100,
      totalRows: 0,
      mode: "remote",
      revision: {
        databaseSessionId: "session-1",
        schemaRevision: "schema-1",
        dataRevision: 1,
      },
    });

    const wrapper = mountView();
    await flushPromises();
    await wrapper.get('[data-testid="grid-add-first-row"]').trigger("click");

    expect(posted).toContainEqual({
      type: "table.insertRowRequested",
      payload: {
        table: "orders",
        values: {},
        schemaRevision: "schema-1",
      },
      requestId: undefined,
    });
  });

  it("wires sidebar requestDelete -> ui.openDelete(name)", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const ui = useUiStore();
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders", metadata: {} }], { orders: "Orders" });

    mountView();
    await flushPromises();

    const delBtn = document.body.querySelector('[data-testid="sidebar-request-delete"]');
    expect(delBtn).toBeTruthy();
    (delBtn as HTMLElement).click();
    await flushPromises();

    expect(ui.deleteModalOpen).toBe(true);
    expect(ui.deleteTarget).toBe("orders");
  });

  it("wires sidebar openAdmin -> tableAdminService.openAdmin (admin.openRequested posted)", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);

    mountView();
    await flushPromises();

    const adminBtn = document.body.querySelector('[data-testid="nav-admin"]');
    expect(adminBtn).toBeTruthy();
    (adminBtn as HTMLElement).click();
    await flushPromises();

    expect(posted.some((p) => p.type === "admin.openRequested")).toBe(true);
  });

  it("keeps delete confirmation visible until the host reports success", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const ui = useUiStore();
    ui.openDelete("orders");

    mountView();
    await flushPromises();

    const confirmBtn = document.body.querySelector('[data-testid="delete-confirm-ok"]');
    expect(confirmBtn).toBeTruthy();
    (confirmBtn as HTMLElement).click();
    await flushPromises();

    // The host was notified; the modal stays visible so an operation.failed
    // message has somewhere to render and the user can retry.
    expect(posted.some((p) => p.type === "tableAdmin.deleteRequested")).toBe(true);
    expect(ui.deleteModalOpen).toBe(true);
  });

  it("wires paste-confirm -> pasteService.apply (table.applyPasteRequested with token + idempotencyKey)", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const paste = usePasteStore();
    const workspace = useWorkspaceStore();
    const ui = useUiStore();

    // Stage the paste flow: a current collection + a previewed plan + open panel.
    workspace.setOpened([{ collection: "users", metadata: {} }], { users: "Users" });
    workspace.selectTable("users");
    paste.setPlan(makePlan("tok-xyz"));
    paste.toggleAck(); // canConfirm requires phase=previewing && acked
    ui.openPastePanel();

    mountView();
    await flushPromises();

    const confirmBtn = document.body.querySelector('[data-testid="paste-confirm"]');
    expect(confirmBtn).toBeTruthy();
    (confirmBtn as HTMLElement).click();
    await flushPromises();

    // Bridge received `table.applyPasteRequested` with collection, the non-empty
    // single-use token from the plan, and a fresh non-empty idempotencyKey.
    const apply = posted.find((p) => p.type === "table.applyPasteRequested");
    expect(apply).toBeTruthy();
    const payload = apply?.payload as {
      collection: string;
      token: string;
      idempotencyKey: string;
    };
    expect(payload.collection).toBe("users");
    expect(payload.token).toBe("tok-xyz");
    expect(payload.idempotencyKey).toBeTruthy();
    // idempotencyKey should be unique per call (UUID shape).
    expect(payload.idempotencyKey).not.toBe("tok-xyz");

    // pasteService.apply flips the store to "applying" synchronously.
    expect(paste.phase).toBe("applying");
  });

  it("on paste-confirm with no plan or no table, does NOT post applyPasteRequested", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);

    mountView();
    await flushPromises();

    // No plan staged and no table selected: clicking confirm (if rendered)
    // must not produce an apply notification.
    const confirmBtn = document.body.querySelector('[data-testid="paste-confirm"]');
    if (confirmBtn) {
      (confirmBtn as HTMLElement).click();
      await flushPromises();
    }
    expect(posted.some((p) => p.type === "table.applyPasteRequested")).toBe(false);
  });

  // -------------------------------------------------------------------------
  // Task M5: keyboard shortcut wiring (copy/paste/delete/refresh/newTable).
  // -------------------------------------------------------------------------

  /**
   * Dispatch a keydown for a single key + modifier set on document (the same
   * surface useKeyboard listens on). Mirrors the helper in useKeyboard.test.ts.
   */
  function fireKey(key: string, init: KeyboardEventInit = {}): void {
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }),
    );
  }

  it("Delete shortcut with an active range posts table.deleteRowsRequested (no confirm dialog)", async () => {
    const digestA = `sha256:${"a".repeat(64)}`;
    const digestB = `sha256:${"b".repeat(64)}`;
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const tableStore = useTableStore();
    const ui = useUiStore();
    ui.navigate("tables");
    workspace.selectTable("users");
    // Seed a page so useTabulator's init watch fires and the mocked Tabulator
    // instance (with our getRanges stub) is instantiated + provided.
    tableStore.beginLoad();
    tableStore.appendPage({
      table: "users",
      columns: [{ name: "name", title: "Name", dataType: "text", editable: true, nullable: true }],
      rows: [
        { rowKey: 7, name: "a", __vibetableDigest: digestA },
        { rowKey: 11, name: "b", __vibetableDigest: digestB },
      ],
      offset: 0,
      limit: 2,
      totalRows: 2,
      mode: "remote",
    });

    // Stage an active Tabulator range with two selected rows. mutationService
    // uses ws.currentTable as the `table` field; the bridge call should carry
    // the rowKeys with QueryPort-issued authoritative digests.
    mockTabulatorRef.current = {
      setData: vi.fn().mockResolvedValue(undefined),
      setColumns: vi.fn(),
      destroy: vi.fn(),
      getRanges: () => [
        {
          getRows: () => [
            { getData: () => ({ rowKey: 7, name: "a", __vibetableDigest: digestA }) },
            { getData: () => ({ rowKey: 11, name: "b", __vibetableDigest: digestB }) },
          ],
          getColumns: () => [{ getField: () => "name" }],
        },
      ],
    };

    mountView();
    await flushPromises();

    fireKey("Delete");
    await flushPromises();

    const del = posted.find((p) => p.type === "table.deleteRowsRequested");
    expect(del).toBeTruthy();
    const payload = del?.payload as {
      table: string;
      rows: { rowKey: number; expectedDigest: string }[];
      schemaRevision: string;
    };
    expect(payload.table).toBe("users");
    expect(payload.rows).toEqual([
      { rowKey: 7, expectedDigest: digestA },
      { rowKey: 11, expectedDigest: digestB },
    ]);
  });

  it("Delete shortcut with NO active range does not post deleteRowsRequested", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const ui = useUiStore();
    ui.navigate("tables");
    workspace.selectTable("users");

    // Default mock has getRanges -> []; no range active.
    mountView();
    await flushPromises();

    fireKey("Delete");
    await flushPromises();

    expect(posted.some((p) => p.type === "table.deleteRowsRequested")).toBe(false);
  });

  it("Ctrl+R shortcut posts table.selected (refresh re-uses selectTable channel)", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const ui = useUiStore();
    ui.navigate("tables");
    workspace.selectTable("orders");

    mountView();
    await flushPromises();
    posted.length = 0; // ignore the mount-time notifications

    fireKey("r", { ctrlKey: true });
    await flushPromises();

    // tableService.refresh re-posts `table.selected` for the current table.
    const sel = posted.find((p) => p.type === "table.selected");
    expect(sel).toBeTruthy();
    expect((sel?.payload as { table: string }).table).toBe("orders");
  });

  it("Ctrl+N shortcut opens the create-table modal (admin + ui)", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const ui = useUiStore();
    const admin = useTableAdminStore();

    mountView();
    await flushPromises();

    fireKey("n", { ctrlKey: true });
    await flushPromises();

    expect(admin.phase).toBe("creating");
    expect(ui.createModalOpen).toBe(true);
  });

  it("'?' shortcut opens the shortcuts help page", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const ui = useUiStore();

    mountView();
    await flushPromises();

    fireKey("?");
    await flushPromises();

    expect(ui.shortcutsOpen).toBe(true);
  });

  it("Ctrl+Z routes through mutationService.performUndo (entry moves to redo)", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const history = useHistoryStore();
    const ui = useUiStore();
    ui.navigate("tables");
    let undoCalled = 0;
    // Seed an entry whose undo closure is observable. WorkspaceView's onUndo
    // calls mutationService.performUndo -> history.undo -> entry.undo().
    history.push({
      id: "seed",
      kind: "updateCell",
      label: "seed",
      timestamp: 0,
      undo: async () => {
        undoCalled++;
      },
      redo: async () => {},
    });

    mountView();
    await flushPromises();

    fireKey("z", { ctrlKey: true });
    await flushPromises();

    // The undo closure ran (proving the shortcut reached history.undo via
    // performUndo), and the entry has moved to the redo stack.
    expect(undoCalled).toBe(1);
    expect(history.canRedo).toBe(true);
    expect(history.canUndo).toBe(false);
  });

  it("switching tables (sidebar select) clears the undo history", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const history = useHistoryStore();

    workspace.setOpened([
      { collection: "users", metadata: {} },
      { collection: "orders", metadata: {} },
    ], { users: "Users", orders: "Orders" });

    // Seed the history with a fake entry so we can observe the clear.
    history.push({
      id: "seed",
      kind: "updateCell",
      label: "seed",
      timestamp: 0,
      undo: async () => {},
      redo: async () => {},
    });
    expect(history.canUndo).toBe(true);

    mountView();
    await flushPromises();

    // Click the first table row to trigger onSelect -> tableService.selectTable
    // -> history.clear() (now lives inside the service, not onSelect).
    const row = document.body.querySelector('[data-testid="sidebar-table-name"]');
    expect(row).toBeTruthy();
    (row as HTMLElement).click();
    await flushPromises();

    expect(history.canUndo).toBe(false);
  });

  it("Ctrl+R refresh clears the undo history (service owns the clear)", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const history = useHistoryStore();
    const ui = useUiStore();
    ui.navigate("tables");
    workspace.selectTable("orders");

    // Seed history; refresh must clear it (tableService.refresh now clears).
    history.push({
      id: "seed",
      kind: "updateCell",
      label: "seed",
      timestamp: 0,
      undo: async () => {},
      redo: async () => {},
    });
    expect(history.canUndo).toBe(true);

    mountView();
    await flushPromises();
    posted.length = 0;

    fireKey("r", { ctrlKey: true });
    await flushPromises();

    expect(history.canUndo).toBe(false);
  });

  it("gives structured editors dialog semantics and restores keyboard focus", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    const ui = useUiStore();
    workspace.setOpened([{ collection: "items" }], { items: "Items" });
    workspace.selectTable("items");
    ui.navigate("tables");
    const trigger = document.createElement("button");
    trigger.textContent = "Open JSON";
    document.body.append(trigger);
    trigger.focus();

    const wrapper = mountView({ realTransitions: true });
    await flushPromises();
    trigger.focus();
    wrapper.findComponent(GridHost).vm.$emit("jsonEdit", {
      rowKey: "row-1",
      column: {
        name: "metadata",
        title: "Metadata",
        dataType: "json",
        editable: true,
        nullable: true,
      },
      value: { approved: true },
    });
    expect(document.activeElement).not.toBe(trigger);
    await flushPromises();

    const dialog = document.body.querySelector<HTMLElement>(
      '[data-testid="json-editor-modal"]',
    );
    expect(dialog?.getAttribute("role")).toBe("dialog");
    expect(dialog?.getAttribute("aria-modal")).toBe("true");
    expect(dialog?.getAttribute("aria-labelledby")).toBe("json-editor-title");
    await vi.waitFor(() => {
      expect(
        dialog?.contains(document.activeElement),
        document.activeElement instanceof HTMLElement ? document.activeElement.outerHTML : "",
      ).toBe(true);
    });
    expect(document.activeElement?.closest('[aria-hidden="true"]')).toBeNull();

    document.body.querySelector<HTMLElement>(
      '[data-testid="json-editor-close"]',
    )?.click();
    await flushPromises();
    await vi.waitFor(() => {
      expect(document.activeElement).toBe(trigger);
    });
  });

  it("releases attachment gridcell focus before hiding the background for NModal", async () => {
    setHostBridgeForTesting({
      request: vi.fn(async (method: string) => {
        if (method === "file.list") return { attachments: [] };
        throw new Error(`unexpected request: ${method}`);
      }),
      notify: vi.fn(),
      notifyWithAdditionalObjects: vi.fn(() => false),
      on: vi.fn(() => vi.fn()),
      start: vi.fn(),
      stop: vi.fn(),
    } as unknown as HostBridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "items" }], { items: "Items" });
    workspace.selectTable("items");
    useUiStore().navigate("tables");

    const wrapper = mountView({ realTransitions: true });
    await flushPromises();
    const trigger = document.createElement("div");
    trigger.tabIndex = 0;
    trigger.setAttribute("role", "gridcell");
    document.body.append(trigger);
    trigger.focus();

    wrapper.findComponent(GridHost).vm.$emit("attachmentOpen", {
      rowKey: "row-1",
      column: {
        name: "photos",
        title: "Photos",
        fieldId: "photos-id",
        dataType: "file",
        editable: true,
        nullable: true,
        attachmentPolicy: {
          maxFiles: 3,
          maxBytesPerFile: 1024,
          allowedMimeTypes: ["image/png"],
          thumbnailVariants: [],
          protected: false,
        },
      },
    });

    expect(document.activeElement).not.toBe(trigger);
    await flushPromises();
    const dialog = document.body.querySelector<HTMLElement>(
      '[data-testid="attachment-panel"]',
    );
    await vi.waitFor(() => {
      expect(dialog?.contains(document.activeElement)).toBe(true);
    });
    expect(document.activeElement?.closest('[aria-hidden="true"]')).toBeNull();

    document.body.querySelector<HTMLButtonElement>(
      '[data-testid="attachment-panel"] header button',
    )?.click();
    await flushPromises();
    await vi.waitFor(() => {
      expect(document.activeElement).toBe(trigger);
    });
  });

  it("focuses the lookup source dialog through its public grid event", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "items" }], { items: "Items" });
    workspace.selectTable("items");
    useUiStore().navigate("tables");

    const wrapper = mountView({ realTransitions: true });
    await flushPromises();
    const trigger = document.createElement("div");
    trigger.tabIndex = 0;
    trigger.setAttribute("role", "gridcell");
    document.body.append(trigger);
    trigger.focus();

    wrapper.findComponent(GridHost).vm.$emit("lookupSourcePage", {
      fieldRef: "customer-name",
      sourceRecordId: "order-1",
      cell: {
        state: "ok",
        value: "Customer one",
        diagnostic: null,
        provenance: [{
          collection: "customers",
          collectionLabel: "Customers",
          itemId: "customer-1",
          recordLabel: "Customer one",
          fieldId: "name-id",
          fieldLabel: "Name",
          value: "Customer one",
        }],
        provenanceTotal: 1,
        provenanceTotalKnown: true,
        provenanceOffset: 0,
        provenanceLimit: 100,
        provenanceHasMore: false,
      },
    });
    await flushPromises();

    const dialog = document.body.querySelector<HTMLElement>(
      '[data-testid="lookup-sources-panel"]',
    );
    await vi.waitFor(() => {
      expect(dialog?.contains(document.activeElement)).toBe(true);
    });
    expect(document.activeElement?.closest('[aria-hidden="true"]')).toBeNull();
  });

  it("routes managed attachment actions through opaque host commands", async () => {
    const digest = `sha256:${"a".repeat(64)}`;
    const file = {
      contractVersion: "2.0" as const,
      tableId: "items",
      recordId: "row-1",
      fieldId: "photos-id",
      storedName: "stored.png",
      originalName: "photo.png",
      mimeType: "image/png",
      size: 12,
      sha256: `sha256:${"b".repeat(64)}`,
      downloadCapability: "download-1",
      thumbnails: [],
    };
    const request = vi.fn(async (method: string) => {
      if (method === "file.list") return { attachments: [file] };
      if (method === "file.replaceRequested" || method === "file.uploadRequested") {
        return { status: "applied" };
      }
      if (method === "file.removeRequested") return { status: "applied" };
      throw new Error(`unexpected request: ${method}`);
    });
    const notify = vi.fn();
    setHostBridgeForTesting({
      request,
      notify,
      notifyWithAdditionalObjects: vi.fn(() => false),
      on: vi.fn(() => vi.fn()),
      start: vi.fn(),
      stop: vi.fn(),
    } as unknown as HostBridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "items" }], { items: "Items" });
    workspace.selectTable("items");
    useUiStore().navigate("tables");
    useTableStore().setDatasetReady({
      table: "items",
      columns: [],
      rows: [{ rowKey: "row-1", __vibetableDigest: digest }],
      offset: 0,
      limit: 1,
      totalRows: 1,
      mode: "remote",
      revision: {
        databaseSessionId: "session-1",
        schemaRevision: "schema-1",
        dataRevision: 1,
      },
    });
    const wrapper = mountView();
    await flushPromises();

    const open = async () => {
      wrapper.findComponent(GridHost).vm.$emit("attachmentOpen", {
        rowKey: "row-1",
        column: {
          name: "photos",
          title: "Photos",
          fieldId: "photos-id",
          dataType: "file",
          editable: true,
          nullable: true,
          attachmentPolicy: {
            maxFiles: 3,
            maxBytesPerFile: 1024,
            allowedMimeTypes: ["image/png"],
            thumbnailVariants: [],
            protected: false,
          },
        },
      });
      await flushPromises();
      return wrapper.findComponent(ManagedAttachmentCell);
    };

    let panel = await open();
    expect(panel.exists()).toBe(true);
    panel.vm.$emit("preview", "stored.png");
    panel.vm.$emit("download", "stored.png");
    expect(notify).toHaveBeenCalledWith("file.previewRequested", expect.objectContaining({
      storedName: "stored.png",
    }));
    expect(notify).toHaveBeenCalledWith("file.downloadRequested", expect.objectContaining({
      originalName: "photo.png",
    }));

    panel.vm.$emit("replace", "stored.png");
    await flushPromises();
    expect(request).toHaveBeenCalledWith("file.replaceRequested", expect.objectContaining({
      expectedDigest: digest,
      schemaRevision: "schema-1",
    }));

    panel = await open();
    panel.vm.$emit("remove", "stored.png");
    await flushPromises();
    expect(request).toHaveBeenCalledWith("file.removeRequested", expect.objectContaining({
      storedName: "stored.png",
    }));

    panel = await open();
    panel.vm.$emit("upload");
    await flushPromises();
    expect(request).toHaveBeenCalledWith("file.uploadRequested", expect.objectContaining({
      recordId: "row-1",
      fieldId: "photos-id",
    }));
  });

  it("commits valid JSON through the table mutation seam and blocks invalid JSON", async () => {
    const notify = vi.fn();
    const request = vi.fn(async (method: string) => {
      throw new Error(`unexpected request: ${method}`);
    });
    setHostBridgeForTesting({
      request,
      notify,
      notifyWithAdditionalObjects: vi.fn(() => false),
      on: vi.fn(() => vi.fn()),
      start: vi.fn(),
      stop: vi.fn(),
    } as unknown as HostBridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "items" }], { items: "Items" });
    workspace.selectTable("items");
    useUiStore().navigate("tables");
    useTableStore().setDatasetReady({
      table: "items",
      columns: [],
      rows: [{ rowKey: "row-1", metadata: { approved: true } }],
      offset: 0,
      limit: 1,
      totalRows: 1,
      mode: "remote",
      revision: {
        databaseSessionId: "session-1",
        schemaRevision: "schema-1",
        dataRevision: 1,
      },
    });
    const wrapper = mountView();
    await flushPromises();
    wrapper.findComponent(GridHost).vm.$emit("jsonEdit", {
      rowKey: "row-1",
      column: {
        name: "metadata",
        title: "Metadata",
        dataType: "json",
        editable: true,
        nullable: true,
      },
      value: { approved: true },
      expectedDigest: null,
    });
    await flushPromises();

    const input = document.body.querySelector<HTMLTextAreaElement>(
      '[data-testid="json-editor-input"]',
    );
    expect(input).not.toBeNull();
    input!.value = "{";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();
    const save = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="json-editor-save"]',
    );
    expect(save?.disabled).toBe(true);
    expect(notify).not.toHaveBeenCalledWith("table.updateCellRequested", expect.anything());

    input!.value = '{"approved":false}';
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();
    const validSave = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="json-editor-save"]',
    );
    expect(validSave?.disabled).toBe(false);
    validSave?.click();
    await flushPromises();
    expect(notify).toHaveBeenCalledWith("table.updateCellRequested", expect.objectContaining({
      table: "items",
      rowKey: "row-1",
      column: "metadata",
      newValue: { approved: false },
      schemaRevision: "schema-1",
    }));
  });
});
