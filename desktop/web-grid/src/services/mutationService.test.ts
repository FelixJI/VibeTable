import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useMutationService } from "./mutationService";

// Build a bridge with a controllable inbound shim (same shape as
// errorRouter.test.ts). The shim captures the message listener so tests can
// drive inbound events via `emit`.
function makeShimBridge(): {
  bridge: HostBridge;
  emit: (type: string, payload: unknown) => void;
} {
  let listener: ((e: { data: unknown }) => void) | null = null;
  const shim = {
    addEventListener: (_: string, fn: (e: { data: unknown }) => void) => {
      listener = fn;
    },
    removeEventListener: (
      _: string,
      fn: (e: { data: unknown }) => void,
    ) => {
      if (listener === fn) listener = null;
    },
    postMessage: () => {},
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return {
    bridge,
    emit: (type, payload) =>
      listener?.({ data: JSON.stringify({ type, payload }) }),
  };
}

describe("mutationService", () => {
  beforeEach(() => setActivePinia(createPinia()));

  // CRITICAL: `useHostBridge` is a module singleton. Reset to null so the fake
  // bridge does not leak into other test files.
  afterEach(() => setHostBridgeForTesting(null));

  it("updateCell notifies table.updateCellRequested with schemaRevision", () => {
    const { bridge } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const spy = vi.spyOn(bridge, "notify");
    const table = useTableStore();
    const ws = useWorkspaceStore();
    ws.selectTable("users");
    table.setEditSchema([], {
      databaseSessionId: "s",
      schemaRevision: "sr1",
      dataRevision: 1,
    });
    const svc = useMutationService();
    svc.updateCell(5, "name", "old", "new");
    expect(spy).toHaveBeenCalledWith("table.updateCellRequested", {
      table: "users",
      rowKey: 5,
      column: "name",
      oldValue: "old",
      newValue: "new",
      schemaRevision: "sr1",
    });
  });

  it("on editCommitted, applies edit + pushes undoable history entry", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "u",
      columns: [],
      rows: [{ rowKey: 1, name: "old" }],
      offset: 0,
      limit: 1,
      totalRows: 1,
      mode: "client",
    });
    const history = useHistoryStore();
    const svc = useMutationService();
    svc.init();
    emit("table.editCommitted", {
      rowKey: 1,
      column: "name",
      storedValue: "new",
      currentRow: { rowKey: 1, name: "new" },
      revision: { databaseSessionId: "s", schemaRevision: "sr", dataRevision: 2 },
    });
    expect(table.allRows[0]?.name).toBe("new");
    expect(history.canUndo).toBe(true);
  });

  it("on rowsDeleted, applies delete + pushes history with cached snapshot", async () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "u",
      columns: [],
      rows: [
        { rowKey: 1, name: "a" },
        { rowKey: 2, name: "b" },
      ],
      offset: 0,
      limit: 2,
      totalRows: 2,
      mode: "client",
    });
    const history = useHistoryStore();
    const ws = useWorkspaceStore();
    ws.selectTable("u");
    table.setEditSchema([], {
      databaseSessionId: "s",
      schemaRevision: "sr",
      dataRevision: 1,
    });
    const svc = useMutationService();
    svc.init();
    // deleteRows caches the snapshot BEFORE sending the request.
    svc.deleteRows([{ rowKey: 2, expectedDigest: "d" }]);
    emit("table.rowsDeleted", {
      deletedRowKeys: [2],
      revision: { databaseSessionId: "s", schemaRevision: "sr", dataRevision: 2 },
    });
    expect(table.allRows).toHaveLength(1);
    expect(history.canUndo).toBe(true);
    // undo re-inserts the deleted row (via insertRowRequested notify).
    await history.undo();
    // The undo handler issues a notify; it does NOT directly mutate the store.
    // After undo, the history entry has moved to the redo stack.
    expect(history.canRedo).toBe(true);
  });

  it("clears history on schema change (revision.schemaRevision differs)", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({
      table: "u",
      columns: [],
      rows: [{ rowKey: 1 }],
      offset: 0,
      limit: 1,
      totalRows: 1,
      mode: "client",
    });
    const history = useHistoryStore();
    const svc = useMutationService();
    svc.init();
    emit("table.editCommitted", {
      rowKey: 1,
      column: "x",
      storedValue: 1,
      currentRow: { rowKey: 1, x: 1 },
      revision: { databaseSessionId: "s", schemaRevision: "sr", dataRevision: 2 },
    });
    expect(history.canUndo).toBe(true);
    // Second commit arrives with a DIFFERENT schemaRevision — history must be
    // cleared because undo/redo across a schema change is unsafe.
    emit("table.editCommitted", {
      rowKey: 1,
      column: "x",
      storedValue: 2,
      currentRow: { rowKey: 1, x: 2 },
      revision: {
        databaseSessionId: "s",
        schemaRevision: "CHANGED",
        dataRevision: 3,
      },
    });
    expect(history.canUndo).toBe(false);
  });
});
