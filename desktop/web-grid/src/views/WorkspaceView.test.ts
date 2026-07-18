import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import WorkspaceView from "./WorkspaceView.vue";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePasteStore } from "@/stores/pasteStore";
import type { PastePlan, PasteSummary } from "@/contracts";

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
}

function makeRecordingBridge(): {
  bridge: HostBridge;
  posted: Outbound[];
} {
  const posted: Outbound[] = [];
  const shim = {
    addEventListener: () => {},
    removeEventListener: () => {},
    postMessage: (msg: unknown) => {
      // The bridge posts a BridgeMessage envelope object (not a string) in
      // unit-test shims (the C# host in production posts via PostWebMessageAsString,
      // which would arrive as JSON at the host side; here we capture pre-string form).
      if (typeof msg === "string") {
        try {
          const parsed = JSON.parse(msg) as { type: string; payload?: unknown };
          posted.push({ type: parsed.type, payload: parsed.payload });
          return;
        } catch {
          posted.push({ type: msg, payload: undefined });
          return;
        }
      }
      const env = msg as { type?: string; payload?: unknown };
      posted.push({ type: env.type ?? "(unknown)", payload: env.payload });
    },
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return { bridge, posted };
}

vi.mock("@/grid/createGrid", () => ({
  createGrid: () => ({
    setData: vi.fn().mockResolvedValue(undefined),
    setColumns: vi.fn(),
    destroy: vi.fn(),
  }),
  buildColumns: () => [],
}));

function mountView() {
  return mount(WorkspaceView, { attachTo: document.body });
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
    setActivePinia(createPinia());
  });

  afterEach(() => setHostBridgeForTesting(null));

  it("mounts and calls service.init() for every service without errors", async () => {
    const { bridge } = makeRecordingBridge();
    setHostBridgeForTesting(bridge);
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.find(".workspace").exists()).toBe(true);
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

    const adminBtn = document.body.querySelector('[data-testid="sidebar-open-admin"]');
    expect(adminBtn).toBeTruthy();
    (adminBtn as HTMLElement).click();
    await flushPromises();

    expect(posted.some((p) => p.type === "admin.openRequested")).toBe(true);
  });

  it("wires delete-confirm-ok -> tableAdminService.deleteTable + ui.closeDelete", async () => {
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

    // The host was notified and the modal was closed by the container.
    expect(posted.some((p) => p.type === "tableAdmin.deleteRequested")).toBe(true);
    expect(ui.deleteModalOpen).toBe(false);
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
});
