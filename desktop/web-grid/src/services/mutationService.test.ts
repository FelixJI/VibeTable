import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useMutationService } from "./mutationService";
import { usePasteService } from "./pasteService";
import { usePasteStore } from "@/stores/pasteStore";
import { useErrorRouter } from "./errorRouter";
import type { ApplyPasteResult, PastePlan } from "@/contracts";

type Envelope = { type: string; payload: Record<string, unknown>; requestId: string };
const digest = `sha256:${"a".repeat(64)}`;
const revision = (dataRevision = 1, schemaRevision = "sr") => ({ databaseSessionId: "s", schemaRevision, dataRevision });
const edit = (storedValue: unknown, rowKey: number | string = 1, column = "name", dataRevision = 2) => ({
  rowKey, column, storedValue, currentRow: { rowKey, [column]: storedValue, __vibetableDigest: digest }, revision: revision(dataRevision),
});
const pasteResult = (overrides: Partial<ApplyPasteResult> = {}): ApplyPasteResult => ({
  collection: "u", outcome: "committed", createdRowKeys: [10], updatedRowKeys: [], skippedRowKeys: [],
  conflicts: [], requestId: "paste-result", ...overrides,
});

function setup(timeoutMs = 5_000) {
  let listener: ((event: { data: unknown }) => void) | undefined;
  const posted: Envelope[] = [];
  const diagnostics = vi.fn();
  let id = 0;
  const bridge = createHostBridge({
    timeoutMs, generateRequestId: () => `r${++id}`, onDiagnostic: diagnostics,
    webview: {
      addEventListener: (_, fn) => { listener = fn; },
      removeEventListener: () => { listener = undefined; },
      postMessage: (message) => { posted.push((typeof message === "string" ? JSON.parse(message) : message) as Envelope); },
    },
  });
  bridge.start();
  setHostBridgeForTesting(bridge);
  const table = useTableStore();
  const ws = useWorkspaceStore();
  ws.selectTable("u");
  table.beginLoad();
  table.appendPage({ table: "u", columns: [], rows: [
    { rowKey: 1, name: "old", other: "before", __vibetableDigest: digest },
    { rowKey: 2, name: "two", __vibetableDigest: digest },
  ], offset: 0, limit: 2, totalRows: 2, mode: "remote" });
  table.setEditSchema([{ name: "name", storageName: "name", dataType: "text", editable: true,
    nullable: true, primaryKey: false, editor: { kind: "text" }, validation: [] }], revision());
  const history = useHistoryStore();
  const rejected = vi.fn();
  const inserted = vi.fn();
  const service = useMutationService();
  service.init(rejected, inserted);
  const emit = (type: string, payload: unknown, requestId?: string) => listener?.({ data: { type, payload, ...(requestId ? { requestId } : {}) } });
  const reply = (request: Envelope, type: string, payload: unknown) => emit(type, payload, request.requestId);
  const dispose = () => { if ("dispose" in service) service.dispose(); bridge.stop(); };
  cleanups.push(dispose);
  return { bridge, table, ws, history, service, posted, emit, reply, rejected, inserted, diagnostics };
}
const cleanups: (() => void)[] = [];
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
async function seed(s: ReturnType<typeof setup>, oldValue: unknown = "old") {
  s.service.updateCell(1, "name", oldValue, "new");
  s.reply(s.posted.at(-1)!, "table.editCommitted", edit("new"));
  await flush();
}

describe("mutationService correlated writes", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => {
    cleanups.splice(0).forEach((cleanup) => cleanup());
    vi.useRealTimers();
    setHostBridgeForTesting(null);
  });

  it("response queued before a microtask table switch cannot apply afterward", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "old response");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old response"));
    queueMicrotask(() => queueMicrotask(() => {
      s.ws.selectTable("other"); s.ws.selectTable("u");
      s.table.rollbackCellEdit(1, "name", "new context");
    }));
    await flush();
    expect(s.table.allRows[0]?.name).toBe("new context");
    expect(s.history.canUndo).toBe(false);
  });

  it("ordinary confirmation cannot finish a pending inverse", async () => {
    const s = setup(); await seed(s);
    s.service.updateCell(2, "name", "two", "changed"); const ordinary = s.posted.at(-1)!;
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.reply(ordinary, "table.editCommitted", edit("changed", 2, "name", 3)); await flush();
    expect(s.history.busy).toBe(true);
    expect(s.history.undoStackSize).toBe(1);
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 4)); await undo;
    expect(s.history.canRedo).toBe(true);
  });

  it("older confirmations for a different row still project their committed value", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "one"); const first = s.posted.at(-1)!;
    s.service.updateCell(2, "name", "two", "second"); const second = s.posted.at(-1)!;
    s.reply(second, "table.editCommitted", edit("second", 2, "name", 3)); await flush();
    s.reply(first, "table.editCommitted", edit("one", 1, "name", 2)); await flush();
    expect(s.table.allRows[0]?.name).toBe("one"); expect(s.table.allRows[1]?.name).toBe("second");
  });

  it.each(["ordinary-first", "undo-first"])("keeps r2 ordinary history and r1 redo for %s confirmations", async (order) => {
    const s = setup();
    await seed(s);
    s.service.updateCell(2, "name", "two", "changed");
    const ordinary = s.posted.at(-1)!;
    const undo = s.service.performUndo();
    await flush();
    const inverse = s.posted.at(-1)!;
    expect(inverse.requestId).not.toBe(ordinary.requestId);
    if (order === "ordinary-first") {
      s.reply(ordinary, "table.editCommitted", edit("changed", 2, "name", 3));
      await flush();
      expect(s.history.busy).toBe(true);
      expect(s.history.undoStackSize).toBe(1);
      s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 4));
    } else {
      s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3));
      await undo;
      expect(s.history.canRedo).toBe(true);
      s.reply(ordinary, "table.editCommitted", edit("changed", 2, "name", 4));
    }
    await undo;
    await flush();
    expect(s.history.undoStackSize).toBe(1);
    expect(s.history.canRedo).toBe(true);
    const redo = s.service.performRedo();
    await flush();
    expect(s.posted.at(-1)?.payload).toMatchObject({ rowKey: 1, newValue: "new" });
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("new", 1, "name", 5));
    await redo;
    expect(s.table.allRows.find((r) => r.rowKey === 1)?.name).toBe("new");
    expect(s.history.undoStackSize).toBe(2);
    expect(s.history.canRedo).toBe(false);
  });

  it("a genuinely new write after undo clears redo at submission", async () => {
    const s = setup();
    await seed(s);
    const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3));
    await undo;
    expect(s.history.canRedo).toBe(true);
    s.service.updateCell(2, "name", "two", "changed");
    expect(s.history.canRedo).toBe(false);
  });

  it("forwards shared product-value corpus, old null and captured digest unchanged", async () => {
    const s = setup();
    const corpus = JSON.parse(readFileSync(resolve(process.cwd(), "../../contracts/schema-v2/fixtures/field-value-entry-corpus.json"), "utf8")) as {
      cases: { id: string; field: string; productValue: unknown }[];
    };
    for (const item of corpus.cases) {
      s.service.updateCell(1, item.field, null, item.productValue, digest);
      expect(s.posted.at(-1)?.payload, item.id).toMatchObject({ column: item.field, newValue: item.productValue, oldValue: null, expectedDigest: digest, schemaRevision: "sr" });
    }
    const s2 = setup();
    await seed(s2, null);
    s2.table.applyCellEdit(edit("new", 1, "name", 3));
    const undo = s2.service.performUndo(); await flush();
    expect(s2.posted.at(-1)?.payload.newValue).toBeNull();
    s2.reply(s2.posted.at(-1)!, "table.editCommitted", edit(null, 1, "name", 4));
    await undo;
    expect(s2.table.allRows[0]?.name).toBeNull();
  });

  it.each([
    [1, "name", 1, "name"], [1, "name", 1, "other"], [1, "name", 2, "name"],
  ] as const)("owns rejection for %s/%s independently of %s/%s", async (rowA, colA, rowB, colB) => {
    const s = setup();
    s.service.updateCell(rowA, colA, "old-A", "new-A");
    const a = s.posted.at(-1)!;
    s.service.updateCell(rowB, colB, "old-B", "new-B");
    const b = s.posted.at(-1)!;
    s.reply(b, "table.editCommitted", edit("new-B", rowB, colB, 3));
    await flush();
    s.reply(a, "table.editRejected", { kind: "edit_conflict", message: "A rejected", currentRow: { [colA]: "authority-A", [colB]: "stale-B" } });
    await flush();
    expect(s.history.undoStackSize).toBe(1);
    expect(s.table.allRows.find((r) => r.rowKey === rowB)?.[colB]).toBe("new-B");
    expect(s.rejected).toHaveBeenCalledOnce();
    s.reply(b, "table.editCommitted", edit("duplicate", rowB, colB, 4));
    s.reply(a, "table.editRejected", { kind: "edit_conflict", message: "duplicate" });
    await flush();
    expect(s.history.undoStackSize).toBe(1);
    expect(s.rejected).toHaveBeenCalledOnce();
  });

  it.each(["first-first", "second-first"])("two rejected writes to one cell restore its true baseline for %s", async (order) => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "first", "second"); const second = s.posted.at(-1)!;
    const rejection = { kind: "mutation_validation", message: "invalid" };
    s.reply(order === "first-first" ? first : second, "table.editRejected", rejection); await flush();
    s.reply(order === "first-first" ? second : first, "table.editRejected", rejection); await flush();
    expect(s.table.allRows[0]?.name).toBe("old"); expect(s.history.canUndo).toBe(false);
  });

  it("a rejected earlier optimistic value is not used as the next successful write's undo value", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "first", "second"); const second = s.posted.at(-1)!;
    s.reply(second, "table.editCommitted", edit("second", 1, "name", 2)); await flush();
    s.reply(first, "table.editRejected", { kind: "mutation_validation", message: "invalid first" }); await flush();
    const undo = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)?.payload.newValue).toBe("old");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
  });

  it("rolls back only the exact rejected cell and preserves unrelated optimistic columns", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "optimistic");
    s.reply(s.posted.at(-1)!, "table.editRejected", { kind: "edit_conflict", message: "conflict", currentRow: { name: "authority", other: "stale" } });
    await flush();
    expect(s.table.allRows[0]).toMatchObject({ name: "authority", other: "before" });
    expect(s.table.error).toBeNull();
    expect(s.history.canUndo).toBe(false);
  });

  it("does not overwrite a later committed projection with an older response", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "first", "second"); const second = s.posted.at(-1)!;
    s.reply(second, "table.editCommitted", edit("second", 1, "name", 3));
    s.reply(first, "table.editCommitted", edit("first", 1, "name", 2));
    await flush();
    expect(s.table.allRows[0]?.name).toBe("second");
    expect(s.history.undoStackSize).toBe(2);
    const undo = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)?.payload.newValue).toBe("first");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("first", 1, "name", 4)); await undo;
  });

  it.each(["insert", "delete"])("%s rejection cannot consume an inline edit or pending undo", async (kind) => {
    const s = setup(); await seed(s);
    s.service.updateCell(2, "name", "two", "pending");
    const editRequest = s.posted.at(-1)!;
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    if (kind === "insert") s.service.insertRow({ name: "insert" });
    else s.service.deleteRows([{ rowKey: 2, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.editRejected", { kind: "mutation_validation", message: `${kind} rejected` });
    await flush();
    expect(s.history.busy).toBe(true);
    expect(s.table.allRows[1]?.name).toBe("two");
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3));
    await undo;
    s.reply(editRequest, "table.editRejected", { kind: "edit_conflict", message: "edit rejected" });
    await flush();
    expect(s.rejected.mock.calls.map((c) => c[0].message)).toEqual([`${kind} rejected`, "edit rejected"]);
  });

  it("ignores uncorrelated and orphaned mutation results, failures and duplicates", async () => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.emit("table.editCommitted", edit("broadcast"));
    s.emit("table.editRejected", { kind: "edit_conflict", message: "broadcast" });
    s.emit("operation.failed", { message: "query failed", operation: "query.page" });
    s.emit("operation.failed", { message: "orphan" }, "absent");
    await flush();
    expect(s.history.busy).toBe(true);
    expect(s.rejected).not.toHaveBeenCalled();
    expect(s.table.allRows[0]?.name).toBe("new");
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3));
    await undo;
    s.reply(inverse, "table.editCommitted", edit("duplicate", 1, "name", 4));
    await flush();
    expect(s.history.canRedo).toBe(true);
    expect(s.table.allRows[0]?.name).toBe("old");
    expect(s.diagnostics).toHaveBeenCalledWith(expect.objectContaining({ kind: "orphaned-response" }));
  });

  it("a definite undo rejection restores its own entry for conflict resolution", async () => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editRejected", { kind: "edit_conflict", message: "undo conflict" });
    await undo;
    expect(s.history.canUndo).toBe(true);
    expect(s.history.canRedo).toBe(false);
    expect(s.history.lastError).toBe("undo conflict");
  });

  it("a confirmed sibling cannot rebuild replayable history after an unknown write", async () => {
    vi.useFakeTimers(); const s = setup();
    s.service.updateCell(1, "name", "old", "unknown");
    await vi.advanceTimersByTimeAsync(1_000);
    s.service.updateCell(1, "name", "unknown", "confirmed"); const sibling = s.posted.at(-1)!;
    await vi.advanceTimersByTimeAsync(4_000);
    expect(s.rejected).toHaveBeenCalledWith(expect.objectContaining({ kind: "unknown" }));
    s.reply(sibling, "table.editCommitted", edit("confirmed", 1, "name", 3)); await flush();
    expect(s.table.allRows[0]?.name).toBe("confirmed");
    expect(s.history.canUndo).toBe(false); expect(s.history.canRedo).toBe(false);
    expect(s.posted).toHaveLength(2);
  });

  it("sent undo timeout is unknown, cannot be retried and drops its late response", async () => {
    vi.useFakeTimers();
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    await vi.advanceTimersByTimeAsync(5_000); await undo;
    expect(s.rejected).toHaveBeenCalledWith(expect.objectContaining({ kind: "unknown", message: expect.stringContaining("核实") }));
    expect(s.history.canUndo).toBe(false);
    expect(s.history.canRedo).toBe(false);
    s.reply(inverse, "table.editCommitted", edit("late", 1, "name", 3)); await flush();
    expect(s.table.allRows[0]?.name).toBe("new");
    expect(s.posted).toHaveLength(2);
  });

  it.each(["table", "workspace", "epoch", "dispose", "session"])("retires in-flight commands on %s change without contaminating a new context", async (change) => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    if (change === "table") { s.ws.selectTable("other"); s.ws.selectTable("u"); }
    if (change === "workspace") useWorkspaceSessionStore().activeWorkspaceId = "another";
    if (change === "epoch") useWorkspaceSessionStore().sessionEpoch += 1;
    if (change === "dispose") s.service.dispose();
    if (change === "session") s.table.revision = { ...revision(), databaseSessionId: "another" };
    s.table.rollbackCellEdit(1, "name", "new context");
    s.reply(inverse, "table.editCommitted", edit("old context", 1, "name", 4));
    await undo; await flush();
    expect(s.table.allRows[0]?.name).toBe("new context");
    expect(s.history.undoStackSize).toBe(0);
    expect(s.history.canRedo).toBe(false);
    expect(s.rejected).not.toHaveBeenCalled();
  });

  it("coalesces unsent deferred edits with the original old value and guard", async () => {
    vi.useFakeTimers(); const s = setup(); s.table.reset();
    s.service.updateCell(1, "name", "old", "first", digest);
    s.service.updateCell(1, "name", "first", "second", "different");
    expect(s.posted).toHaveLength(0);
    s.table.setEditSchema([], revision());
    await vi.advanceTimersByTimeAsync(25);
    expect(s.posted).toHaveLength(1);
    expect(s.posted[0]?.payload).toMatchObject({ oldValue: "old", newValue: "second", expectedDigest: digest });
  });

  it("an unsent deferred timeout rolls back once and remains distinct from sent unknown", async () => {
    vi.useFakeTimers(); const s = setup(); s.table.setEditSchema([], { ...revision(), schemaRevision: "" });
    s.table.rollbackCellEdit(1, "name", "optimistic");
    s.service.updateCell(1, "name", "old", "first");
    s.service.updateCell(1, "name", "first", "second");
    await vi.advanceTimersByTimeAsync(5_000);
    expect(s.posted).toHaveLength(0);
    expect(s.table.allRows[0]?.name).toBe("old");
    expect(s.rejected).toHaveBeenCalledOnce();
    expect(s.rejected).toHaveBeenCalledWith(expect.objectContaining({ kind: "backend_unavailable" }));
  });

  it("dispose cancels revision timers and never sends into a later table", async () => {
    vi.useFakeTimers(); const s = setup(); s.table.reset();
    s.service.updateCell(1, "name", "old", "pending");
    s.service.dispose();
    s.table.setEditSchema([], revision());
    await vi.advanceTimersByTimeAsync(5_000);
    expect(s.posted).toHaveLength(0);
    expect(s.rejected).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(["ready", "schema", "empty-session"])("undo readiness handles %s before sending", async (mode) => {
    vi.useFakeTimers(); const s = setup(); await seed(s); s.table.reset();
    const undo = s.service.performUndo(); await flush();
    expect(s.posted).toHaveLength(1);
    s.table.setEditSchema([], mode === "ready" ? revision(2) : {
      ...revision(2, mode === "schema" ? "changed" : "sr"), databaseSessionId: "",
    });
    await vi.advanceTimersByTimeAsync(mode === "empty-session" ? 5_000 : 25);
    if (mode === "ready") {
      expect(s.posted).toHaveLength(2);
      s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3));
    }
    await undo;
    if (mode !== "ready") {
      expect(s.posted).toHaveLength(1);
      expect(s.history.lastError).toContain(mode === "schema" ? "schema changed" : "revision did not become ready");
    }
  });

  it("schema changes clear history at the confirmed revision boundary", async () => {
    const s = setup(); await seed(s);
    s.service.updateCell(1, "name", "new", "changed");
    s.reply(s.posted.at(-1)!, "table.editCommitted", { ...edit("changed"), revision: revision(3, "next") }); await flush();
    expect(s.history.canUndo).toBe(false);
  });

  it("insert eligibility permits readonly autoNumber, rejects readonly text and views", () => {
    const s = setup();
    s.table.setEditSchema([{ name: "n", storageName: "n", dataType: "autoNumber", editable: false,
      nullable: false, primaryKey: false, editor: { kind: "text" }, validation: [] }], revision());
    expect(s.service.canInsertRow()).toBe(true); s.service.insertRow({}); expect(s.posted).toHaveLength(1);
    s.table.setEditSchema([{ name: "n", storageName: "n", dataType: "text", editable: false,
      nullable: false, primaryKey: false, editor: { kind: "text" }, validation: [] }], revision());
    s.service.insertRow({}); expect(s.posted).toHaveLength(1);
    s.ws.setOpened([{ collection: "u", metadata: { kind: "view" } }], {});
    expect(s.service.canInsertRow()).toBe(false);
    s.table.reset(); expect(s.service.canInsertRow()).toBe(false);
  });

  it("insert confirmation, undo and redo retain the new authoritative row key", async () => {
    const s = setup(); s.service.insertRow({ name: "added" });
    s.reply(s.posted.at(-1)!, "table.rowsInserted", { rowKey: 10, row: { rowKey: 10, name: "added", __vibetableDigest: digest }, revision: revision(2) }); await flush();
    expect(s.inserted).toHaveBeenCalledOnce(); expect(s.history.undoStackSize).toBe(1);
    const undo = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)?.payload.rows).toEqual([{ rowKey: 10, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [10], revision: revision(3) }); await undo;
    const redo = s.service.performRedo(); await flush();
    s.reply(s.posted.at(-1)!, "table.rowsInserted", { rowKey: 11, row: { rowKey: 11, name: "added", __vibetableDigest: digest }, revision: revision(4) }); await redo;
    const again = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)?.payload.rows).toEqual([{ rowKey: 11, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [11], revision: revision(5) }); await again;
  });

  it("delete owns its snapshot and redoes using restored row identities", async () => {
    const s = setup(); s.service.deleteRows([{ rowKey: 2, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [2], revision: revision(2) }); await flush();
    expect(s.table.allRows).toHaveLength(1);
    const undo = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)?.payload.values).toMatchObject({ name: "two" });
    s.reply(s.posted.at(-1)!, "table.rowsInserted", { rowKey: 22, row: { rowKey: 22, name: "two", __vibetableDigest: digest }, revision: revision(3) }); await undo;
    const redo = s.service.performRedo(); await flush();
    expect(s.posted.at(-1)?.payload.rows).toEqual([{ rowKey: 22, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [22], revision: revision(4) }); await redo;
  });

  it("partial multi-row delete undo never restores a blindly replayable entry", async () => {
    const s = setup(); s.service.deleteRows([{ rowKey: 1, expectedDigest: digest }, { rowKey: 2, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [1, 2], revision: revision(2) }); await flush();
    const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.rowsInserted", { rowKey: 11, row: { rowKey: 11, name: "old" }, revision: revision(3) }); await flush();
    s.reply(s.posted.at(-1)!, "table.editRejected", { kind: "edit_conflict", message: "second failed" }); await undo;
    expect(s.history.canUndo).toBe(false); expect(s.history.canRedo).toBe(false);
    expect(s.table.allRows).toHaveLength(1);
  });

  it("paste history deletes only created rows with current digests and offers no redo", async () => {
    const s = setup(); s.service.recordPasteResult(pasteResult({ createdRowKeys: [2] }));
    const undo = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)?.payload.rows).toEqual([{ rowKey: 2, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [2], revision: revision(2) }); await undo;
    expect(s.history.canRedo).toBe(false);
    s.service.recordPasteResult(pasteResult({ createdRowKeys: [], updatedRowKeys: [1] }));
    await s.service.performUndo(); expect(s.posted).toHaveLength(1);
  });
});

describe("paste request ownership", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => {
    cleanups.splice(0).forEach((cleanup) => cleanup());
    vi.useRealTimers(); setHostBridgeForTesting(null);
  });
  function pasteSetup() {
    const s = setup();
    const paste = usePasteService();
    const applied = vi.fn(s.service.recordPasteResult);
    paste.init(applied);
    cleanups.push(paste.dispose);
    useErrorRouter().init();
    return { ...s, paste, applied, store: usePasteStore() };
  }
  const input = { collection: "u", token: "single-use", idempotencyKey: "logical-apply" };
  const previewInput = {
    collection: "u", schemaRevision: "sr", selection: { querySnapshot: { snapshotId: "q", digest: "q-digest", databaseId: "s", table: "u", schemaRevision: "sr", dataRevision: 1, normalizedQuery: {} }, dataRevision: 1, rowKeys: [] },
    startCell: { column: "name" }, cells: [],
  };

  it("unrelated query failures cannot settle undo or an applying paste", async () => {
    const s = pasteSetup(); await seed(s);
    s.paste.apply(input); const apply = s.posted.at(-1)!;
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    const query = s.bridge.request("table.queryRequested", { table: "u", query: { offset: 0, limit: 2 } });
    const rejection = query.catch((error: Error) => error);
    s.reply(s.posted.at(-1)!, "operation.failed", { operation: "query.page", message: "correlated query failed" });
    await rejection; await flush();
    expect(s.store.phase).toBe("applying"); expect(s.history.busy).toBe(true);
    expect(s.table.error).toBeNull();
    s.emit("operation.failed", { operation: "directory.list", message: "general diagnostic" }); await flush();
    expect(s.store.phase).toBe("applying"); expect(s.history.busy).toBe(true);
    expect(s.table.error).toBe("general diagnostic");
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.reply(apply, "table.pasteApplied", pasteResult()); await flush();
    expect(s.store.phase).toBe("applied"); expect(s.applied).toHaveBeenCalledOnce();
    expect(s.history.undoStack.filter((entry) => entry.kind === "applyPaste")).toHaveLength(1);
    s.reply(apply, "table.pasteApplied", pasteResult());
    s.reply(apply, "operation.failed", { message: "duplicate failed" });
    s.emit("table.pasteApplied", pasteResult());
    await flush(); expect(s.applied).toHaveBeenCalledOnce(); expect(s.store.phase).toBe("applied");
  });

  it("a late paste confirmation cannot rebuild history after another write becomes unknown", async () => {
    vi.useFakeTimers(); const s = pasteSetup();
    s.service.updateCell(1, "name", "old", "unknown");
    await vi.advanceTimersByTimeAsync(1_000);
    s.paste.apply(input); const apply = s.posted.at(-1)!;
    await vi.advanceTimersByTimeAsync(4_000);
    s.reply(apply, "table.pasteApplied", pasteResult()); await flush();
    expect(s.store.phase).toBe("applied");
    expect(s.applied).not.toHaveBeenCalled();
    expect(s.history.canUndo).toBe(false); expect(s.history.canRedo).toBe(false);
  });

  it("the exact paste failure is shown while a concurrent undo remains pending", async () => {
    const s = pasteSetup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.paste.apply(input); const apply = s.posted.at(-1)!;
    s.reply(apply, "operation.failed", { message: "paste backend unavailable" }); await flush();
    expect(s.store.error).toContain("paste backend unavailable");
    expect(s.store.error).toContain("核实");
    expect(s.history.busy).toBe(true);
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    expect(s.history.canUndo).toBe(false); expect(s.history.canRedo).toBe(false);
  });

  it("coalesces apply admission, preserves token/idempotency and does not retry timeout", async () => {
    vi.useFakeTimers(); const s = pasteSetup();
    s.paste.apply(input); const apply = s.posted.at(-1)!;
    s.paste.apply(input); s.paste.preview(previewInput);
    expect(s.posted).toHaveLength(1); expect(apply.payload).toEqual(input);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(s.store.error).toContain("核实"); expect(s.applied).not.toHaveBeenCalled();
    s.reply(apply, "table.pasteApplied", pasteResult()); await flush();
    expect(s.store.phase).toBe("error"); expect(s.posted).toHaveLength(1);
  });

  it.each(["conflict", "pending"] as const)("does not manufacture committed history for a %s paste", async (outcome) => {
    const s = pasteSetup(); s.paste.apply(input);
    s.reply(s.posted.at(-1)!, "table.pasteApplied", pasteResult({ outcome })); await flush();
    expect(s.applied).not.toHaveBeenCalled(); expect(s.history.canUndo).toBe(false);
    if (outcome === "pending") expect(s.store.error).toContain("核实");
    else expect(s.store.result?.outcome).toBe("conflict");
  });

  it.each(["table", "epoch", "dispose"])("discards old paste results and failures after %s retirement", async (kind) => {
    const s = pasteSetup(); s.paste.apply(input); const apply = s.posted.at(-1)!;
    if (kind === "table") { s.ws.selectTable("other"); s.ws.selectTable("u"); }
    if (kind === "epoch") useWorkspaceSessionStore().sessionEpoch += 1;
    if (kind === "dispose") s.paste.dispose();
    s.reply(apply, "table.pasteApplied", pasteResult()); await flush();
    s.reply(apply, "operation.failed", { message: "old rejection" }); await flush();
    expect(s.store.phase).toBe("idle"); expect(s.store.error).toBeNull();
    expect(s.applied).not.toHaveBeenCalled(); expect(s.history.canUndo).toBe(false);
  });

  it("accepts only the current correlated preview and ignores late preview failures", async () => {
    const s = pasteSetup();
    s.paste.preview(previewInput); const first = s.posted.at(-1)!;
    s.paste.preview(previewInput); const second = s.posted.at(-1)!;
    const plan = { collection: "u", token: { token: "second", expiresAt: 100, consumed: false }, overflow: false } as PastePlan;
    s.reply(second, "table.pastePreviewReady", plan); await flush();
    s.reply(first, "operation.failed", { message: "old preview failure" });
    s.emit("table.pastePreviewReady", { ...plan, token: { token: "broadcast" } }); await flush();
    expect(s.store.plan?.token.token).toBe("second"); expect(s.store.error).toBeNull();
    s.paste.preview(previewInput);
    s.reply(s.posted.at(-1)!, "operation.failed", { message: "current preview failure" }); await flush();
    expect(s.store.error).toBe("current preview failure");
    s.paste.dispose(); s.paste.preview(previewInput); s.paste.apply(input);
    expect(s.posted).toHaveLength(3);
  });
});
