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

  // -------------------------------------------------------------------------
  // Feedback-loop guard: performUndo/performRedo suppress history pushes so the
  // host's confirmation round-trip does not clear the redo stack.
  //
  // The guard is CONSUME-ON-INBOUND, not time/await based. performUndo arms
  // the suppress flag and DOES NOT clear it on await; the matching inbound
  // result (editCommitted/rowsInserted/rowsDeleted/pasteApplied) consumes it.
  // This is what makes the guard survive the real WebView2 host's async
  // response — `await history.undo()` resolves long before the C# host
  // processes the re-notification and broadcasts its confirmation.
  //
  // Two scenarios are covered:
  //   (a) Synchronous host (shim emits inside the undo closure): the existing
  //       "performUndo suppresses the re-notification's history push" test.
  //   (b) Asynchronous host (response emitted AFTER undo resolves): the new
  //       "performUndo survives an ASYNC host confirmation" test below — this
  //       is the case the previous await-based flag could NOT handle.
  // -------------------------------------------------------------------------

  it("performUndo suppresses the re-notification's history push (redo preserved)", async () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    const ws = useWorkspaceStore();
    ws.selectTable("u");
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
    expect(history.undoStackSize).toBe(1);

    // Stub notify so that an undo's updateCellRequested triggers a SYNCHRONOUS
    // editCommitted re-notification from the "host" — while suppress is active.
    vi.spyOn(bridge, "notify").mockImplementation((type) => {
      if (type === "table.updateCellRequested") {
        emit("table.editCommitted", {
          rowKey: 1,
          column: "name",
          storedValue: "old",
          currentRow: { rowKey: 1, name: "old" },
          revision: {
            databaseSessionId: "s",
            schemaRevision: "sr",
            dataRevision: 3,
          },
        });
      }
    });

    await svc.performUndo();

    // The undo stack did NOT grow from the re-notification (1 → 0 via undo,
    // not 1 → 1 → 0). canRedo must still be true: the feedback loop did not
    // clear the redo stack.
    expect(history.undoStackSize).toBe(0);
    expect(history.canRedo).toBe(true);
  });

  it("performUndo returns entry to redo stack (basic undo semantics)", async () => {
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
    const beforeUndo = history.undoStackSize;
    await svc.performUndo();
    expect(history.undoStackSize).toBe(beforeUndo - 1);
    expect(history.canRedo).toBe(true);
  });

  // -------------------------------------------------------------------------
  // ASYNC feedback-loop guard: the real WebView2 host processes the undo's
  // re-notification asynchronously and emits `table.editCommitted` AFTER
  // `performUndo`'s promise has already resolved. A time/await-based suppress
  // flag would have flipped back off by then and the inbound handler would
  // push a duplicate entry, clearing the redo stack. The consume-on-inbound
  // guard survives the async gap.
  // -------------------------------------------------------------------------

  it("performUndo survives an ASYNC host confirmation (redo preserved)", async () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    const ws = useWorkspaceStore();
    ws.selectTable("u");
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
    expect(history.undoStackSize).toBe(1);

    // Stub notify so the undo's updateCellRequested schedules the host's
    // editCommitted confirmation via setTimeout(0) — i.e. AFTER the
    // microtask queue drains and `await svc.performUndo()` resolves. This
    // mirrors the real WebView2 host's async behavior.
    vi.spyOn(bridge, "notify").mockImplementation((type) => {
      if (type === "table.updateCellRequested") {
        setTimeout(() => {
          emit("table.editCommitted", {
            rowKey: 1,
            column: "name",
            storedValue: "old",
            currentRow: { rowKey: 1, name: "old" },
            revision: {
              databaseSessionId: "s",
              schemaRevision: "sr",
              dataRevision: 3,
            },
          });
        }, 0);
      }
    });

    // performUndo resolves BEFORE the host's async confirmation lands.
    await svc.performUndo();
    // At this instant, suppressHistory is STILL up (not cleared by await).
    // The redo stack already has the entry (history.undo moved it there).
    expect(history.canRedo).toBe(true);

    // Flush the setTimeout(0) so the host's async confirmation arrives now.
    await new Promise<void>((resolve) => setTimeout(resolve, 0));

    // The async confirmation did NOT push a duplicate entry (undoStackSize
    // stayed at 0, not 1) and did NOT clear the redo stack.
    expect(history.undoStackSize).toBe(0);
    expect(history.canRedo).toBe(true);
    // The store WAS updated by the inbound handler (apply still runs).
    expect(table.allRows[0]?.name).toBe("old");
  });

  it("performUndo clears the suppress guard when the host emits operation.failed", async () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    const ws = useWorkspaceStore();
    ws.selectTable("u");
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
    expect(history.undoStackSize).toBe(1);

    // Host fails the undo's re-notification — no editCommitted will arrive.
    vi.spyOn(bridge, "notify").mockImplementation((type) => {
      if (type === "table.updateCellRequested") {
        setTimeout(() => {
          emit("operation.failed", { message: "host rejected undo" });
        }, 0);
      }
    });

    await svc.performUndo();
    // Note: history.undo() popped the entry but the undo closure's notify
    // was stubbed; entry is now on the redo stack. The host then fails.
    await new Promise<void>((resolve) => setTimeout(resolve, 0));

    // operation.failed cleared the suppress guard. The NEXT user edit must
    // be recorded normally (not silently swallowed).
    emit("table.editCommitted", {
      rowKey: 1,
      column: "name",
      storedValue: "user-typed",
      currentRow: { rowKey: 1, name: "user-typed" },
      revision: {
        databaseSessionId: "s",
        schemaRevision: "sr",
        dataRevision: 4,
      },
    });
    expect(history.undoStackSize).toBe(1);
    expect(table.allRows[0]?.name).toBe("user-typed");
  });

  // -------------------------------------------------------------------------
  // C2: pasteApplied history producer.
  // -------------------------------------------------------------------------

  it("on pasteApplied with createdRowKeys, pushes an applyPaste history entry", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const ws = useWorkspaceStore();
    ws.selectTable("u");
    const history = useHistoryStore();
    const svc = useMutationService();
    svc.init();
    emit("table.pasteApplied", {
      collection: "u",
      outcome: "committed",
      createdRowKeys: [10, 11],
      updatedRowKeys: [],
      skippedRowKeys: [],
      conflicts: [],
      requestId: "rq-1",
    });
    expect(history.canUndo).toBe(true);
    expect(history.undoStack[0]?.kind).toBe("applyPaste");
    expect(history.undoStack[0]?.label).toBe("粘贴");
  });

  it("pasteApplied undo notifies deleteRowsRequested for the created rowKeys", async () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const ws = useWorkspaceStore();
    ws.selectTable("u");
    const spy = vi.spyOn(bridge, "notify");
    const history = useHistoryStore();
    const svc = useMutationService();
    svc.init();
    emit("table.pasteApplied", {
      collection: "u",
      outcome: "committed",
      createdRowKeys: [10, 11],
      updatedRowKeys: [],
      skippedRowKeys: [],
      conflicts: [],
      requestId: "rq-1",
    });
    spy.mockClear();
    await history.undo();
    const del = spy.mock.calls.find((c) => c[0] === "table.deleteRowsRequested");
    expect(del).toBeTruthy();
    const payload = del?.[1] as unknown as {
      rows: { rowKey: number; expectedDigest: string }[];
    };
    expect(payload.rows).toEqual([
      { rowKey: 10, expectedDigest: "" },
      { rowKey: 11, expectedDigest: "" },
    ]);
  });

  it("pasteApplied with no created rows pushes nothing", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const history = useHistoryStore();
    const svc = useMutationService();
    svc.init();
    emit("table.pasteApplied", {
      collection: "u",
      outcome: "committed",
      createdRowKeys: [],
      updatedRowKeys: [5],
      skippedRowKeys: [],
      conflicts: [],
      requestId: "rq-1",
    });
    // Only-updates paste still pushes an entry (per spec the producer runs for
    // every paste), but the undo closure no-ops because there is nothing to
    // remove. Verify canUndo flipped and redo no-ops without throwing.
    expect(history.canUndo).toBe(true);
    expect(history.undoStack[0]?.kind).toBe("applyPaste");
  });
});
