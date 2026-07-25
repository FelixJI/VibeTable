import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h } from "vue";
import { NDropdown, NMessageProvider } from "naive-ui";

import WorkspaceView from "./WorkspaceView.vue";
import GridHost from "@/components/grid/GridHost.vue";
import RelationEditorPanel from "@/components/grid/RelationEditorPanel.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useTableStore } from "@/stores/tableStore";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import { setLocale } from "@/i18n";
import type {
  NormalizedRelationDescriptor,
  PastePlan,
  PasteSummary,
  RelationTargetRef,
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

function mountView() {
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
  return mount(Host, { attachTo: document.body });
}

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
    setActivePinia(createPinia());
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
    setHostBridgeForTesting(null);
    vi.restoreAllMocks();
  });

  it("mounts and calls service.init() for every service without errors", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.find(".workspace").exists()).toBe(true);
  });

  it("shows a localized non-blocking recovery path for stale edits", async () => {
    const { bridge, emit, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }]);
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
    workspace.setOpened([{ collection: "orders" }]);
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();

    emit({
      type: "task.changed",
      payload: {
        contractVersion: "1.0",
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
    workspace.setOpened([{ collection: "orders" }]);
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();

    emit({
      type: "task.changed",
      payload: {
        contractVersion: "1.0",
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
    workspace.setOpened([{ collection: "orders" }]);
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
      .find((candidate) => candidate.props("trigger") === "manual")!;
    expect(dropdown.props("show")).toBe(true);

    dropdown.vm.$emit("update:show", false);
    await flushPromises();
    expect(dropdown.props("show")).toBe(false);
  });

  it("runs export through the renderer-host task bridge", async () => {
    const { bridge, posted, emit } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }]);
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
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const workspace = useWorkspaceStore();
    const table = useTableStore();
    workspace.setOpened([{ collection: "orders" }]);
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
        token: { token: "import-token", expiresAt: 9999999999, consumed: false },
        summary: { validRows: 2, errorRows: 0, warningRows: 0, totalRows: 2 },
        rows: [],
        diagnostics: [],
      },
    });
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
        result: { createdCount: 2, updatedCount: 0, skippedCount: 0 },
        error: null,
      },
    });
    await flushPromises();

    expect(posted.map((item) => item.type)).toEqual(expect.arrayContaining([
      "data.importSourceRequested",
      "data.previewImport",
      "task.create",
    ]));
  });

  it("opens relation and Lookup field management from the table toolbar", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }, { collection: "contracts" }]);
    workspace.selectTable("orders");
    mountView();
    await flushPromises();

    const trigger = document.body.querySelector('[data-testid="toolbar-field-manager"]') as HTMLElement;
    expect(trigger).toBeTruthy();
    trigger.click();
    await flushPromises();

    expect(document.body.textContent).toContain("关系与 Lookup 字段");
    expect(document.body.textContent).toContain("orders · schema");
  });

  it("applies relation.updateSingle current as one target object", async () => {
    const descriptor: NormalizedRelationDescriptor = {
      relationId: "orders.contract", fieldRef: "contract", sourceCollection: "orders", kind: "m2o",
      relatedCollection: "contracts", allowedCollections: [], unique: false, nullable: true,
      onDelete: "nullify", preset: "standard", selfRelation: false, managed: true, state: "valid",
      diagnostics: [],
    };
    const target: RelationTargetRef = {
      collection: "contracts", itemId: "contract-7", label: "CT-0007", junctionValues: {},
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
    workspace.setOpened([{ collection: "orders" }]);
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
    editor.vm.$emit("select", target);
    await flushPromises();

    expect(table.allRows[0]?.contract).toEqual(target);
    expect(Array.isArray(table.allRows[0]?.contract)).toBe(false);
    wrapper.unmount();
  });

  it("routes grid sort/filter/group intent to the standard full-dataset table query", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders" }]);
    workspace.selectTable("orders");
    const wrapper = mountView();
    await flushPromises();
    posted.length = 0;

    wrapper.findComponent(GridHost).vm.$emit("viewQueryChange", {
      filters: [{ field: "status", operator: "eq", value: "signed", logic: "AND" }],
      sorts: [{ field: "contract_price", direction: "desc", nullsLast: true }],
      groups: [{ fieldRef: "customer", direction: "asc" }],
    });
    await flushPromises();

    expect(posted).toContainEqual({
      type: "table.queryRequested",
      payload: {
        table: "orders",
        query: {
          filters: [{ field: "status", operator: "eq", value: "signed", logic: "AND" }],
          sorts: [{ field: "contract_price", direction: "desc", nullsLast: true }],
          offset: 0,
          limit: 500,
        },
      },
    });
  });

  it("opens whole-table history from the toolbar when there is no row or cell selection", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceStore().selectTable("orders");
    mountView();
    await flushPromises();

    const button = document.body.querySelector('[data-testid="toolbar-history"]') as HTMLElement;
    expect(button).toBeTruthy();
    button.click();
    await flushPromises();

    const request = posted.find((item) => item.type === "history.queryRequested");
    expect(request?.payload).toMatchObject({ collection: "orders", scope: "table", limit: 50, offset: 0 });
    expect(useRevisionHistoryStore().panelOpen).toBe(true);
  });

  it("uses the exact single-cell selection for the toolbar history scope", async () => {
    const { bridge, posted } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceStore().selectTable("orders");
    const revisions = useRevisionHistoryStore();
    revisions.setSelection({ scope: "cell", itemId: "42", field: "status" });
    mountView();
    await flushPromises();

    (document.body.querySelector('[data-testid="toolbar-history"]') as HTMLElement).click();
    await flushPromises();

    const request = posted.find((item) => item.type === "history.queryRequested");
    expect(request?.payload).toMatchObject({
      collection: "orders",
      scope: "cell",
      itemId: "42",
      field: "status",
    });
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
    workspace.setOpened([{ collection: "users", metadata: {} }]);

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
    ]);
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
    workspace.setOpened([{ collection: "orders", metadata: {} }]);
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
      loadedRows: 0,
      mode: "client",
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
    workspace.setOpened([{ collection: "orders", metadata: {} }]);

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
    workspace.setOpened([{ collection: "users", metadata: {} }]);
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
      mode: "client",
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
    ]);

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
    workspace.setOpened([{ collection: "items" }]);
    workspace.selectTable("items");
    ui.navigate("tables");
    const trigger = document.createElement("button");
    trigger.textContent = "Open JSON";
    document.body.append(trigger);
    trigger.focus();

    const wrapper = mountView();
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
    await flushPromises();

    const dialog = document.body.querySelector<HTMLElement>(
      '[data-testid="json-editor-modal"]',
    );
    expect(dialog?.getAttribute("role")).toBe("dialog");
    expect(dialog?.getAttribute("aria-modal")).toBe("true");
    expect(dialog?.getAttribute("aria-labelledby")).toBe("json-editor-title");

    document.body.querySelector<HTMLElement>(
      '[data-testid="json-editor-close"]',
    )?.click();
    await flushPromises();
    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(
      document.body.querySelector<HTMLElement>(
        '[data-testid="json-editor-modal"]',
      )?.style.display,
    ).toBe("none");
    expect(document.activeElement).toBe(trigger);
  });
});
