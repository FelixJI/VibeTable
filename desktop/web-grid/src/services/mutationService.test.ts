import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { createPinia, setActivePinia } from "pinia";
import { BridgeRequestNotSentError, createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useMutationService } from "./mutationService";
import { useTableService } from "./tableService";
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
  const postMessage = vi.fn((message: unknown) => { posted.push((typeof message === "string" ? JSON.parse(message) : message) as Envelope); });
  let id = 0;
  const bridge = createHostBridge({
    timeoutMs, generateRequestId: () => `r${++id}`, onDiagnostic: diagnostics,
    webview: {
      addEventListener: (_, fn) => { listener = fn; },
      removeEventListener: () => { listener = undefined; },
      postMessage,
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
  return { bridge, table, ws, history, service, posted, emit, reply, rejected, inserted, diagnostics, postMessage };
}
const cleanups: (() => void)[] = [];
const flush = async () => { for (let i = 0; i < 24; i++) await Promise.resolve(); };
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

  it.each(["rejection-first", "undo-first"] as const)("review16 transaction rejection preserves unrelated history and executable redo (%s)", async (order) => {
    const s = setup(); await seed(s);
    s.service.updateCell(2, "name", "two", "changed");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("changed", 2, "name", 3)); await flush();
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "new", "rejected"); const ordinary = s.posted.at(-1)!;
    const reject = async () => {
      s.reply(ordinary, "table.editRejected", { kind: "edit_conflict", code: "mutation.digest_conflict", message: "The row changed.", notSubmitted: false }); await flush();
    };
    const finishUndo = async () => { s.reply(inverse, "table.editCommitted", edit("two", 2, "name", 4)); await undo; };
    if (order === "rejection-first") { await reject(); await finishUndo(); }
    else { await finishUndo(); expect(s.history.canRedo).toBe(false); await reject(); }
    expect(s.history.writeUncertain).toBe(false); expect(s.history.undoStackSize).toBe(1);
    expect(s.rejected).toHaveBeenCalledWith(expect.objectContaining({ code: "mutation.digest_conflict", notSubmitted: false }));
    expect(s.history.canRedo).toBe(true);
    const redo = s.service.performRedo(); await flush();
    expect(s.posted.at(-1)!.payload).toMatchObject({ rowKey: 2, newValue: "changed" });
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("changed", 2, "name", 5)); await redo;
    expect(s.history.canRedo).toBe(false); expect(s.history.undoStackSize).toBe(2);
    expect(s.posted.filter((request) => request.payload.newValue === "rejected")).toHaveLength(1);
  });

  it.each([
    ["ordinary-first", 3, 4, true], ["undo-first", 3, 4, true],
    ["ordinary-first", 4, 3, false], ["undo-first", 4, 3, false],
  ] as const)("review15 redo follows commit revisions with %s replies (ordinary %s / undo %s)", async (order, ordinaryRevision, undoRevision, canRedo) => {
    const s = setup(); await seed(s);
    s.service.updateCell(2, "name", "two", "changed"); const ordinary = s.posted.at(-1)!;
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    const finishOrdinary = async () => { s.reply(ordinary, "table.editCommitted", edit("changed", 2, "name", ordinaryRevision)); await flush(); };
    const finishUndo = async () => { s.reply(inverse, "table.editCommitted", edit("old", 1, "name", undoRevision)); await undo; };
    if (order === "ordinary-first") { await finishOrdinary(); await finishUndo(); }
    else { await finishUndo(); expect(s.history.canRedo).toBe(false); await finishOrdinary(); }
    expect(s.history.undoStackSize).toBe(1); expect(s.history.canRedo).toBe(canRedo);
    if (canRedo) {
      const redo = s.service.performRedo(); await flush();
      expect(s.posted.at(-1)!.payload).toMatchObject({ rowKey: 1, newValue: "new" });
      s.reply(s.posted.at(-1)!, "table.editCommitted", edit("new", 1, "name", 5)); await redo;
    }
  });
  it("review15 an ordinary action dispatched later but committed earlier preserves the actual undo", async () => {
    const s = setup(); await seed(s); const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.service.updateCell(2, "name", "two", "changed"); const ordinary = s.posted.at(-1)!;
    s.reply(ordinary, "table.editCommitted", edit("changed", 2, "name", 3)); await flush();
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 4)); await undo;
    expect(s.history.canRedo).toBe(true); expect(s.history.undoStackSize).toBe(1);
  });
  it("review10 a proven zero-submission action cannot invalidate a pending undo's future redo", async () => {
    const s = setup(); await seed(s); const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.service.updateCell(2, "name", "two", "preflight fails"); const failed = s.posted.at(-1)!;
    s.reply(failed, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "parse failed" }); await flush();
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    expect(s.history.writeUncertain).toBe(false); expect(s.history.canRedo).toBe(true);
  });

  it.each(["undo-first", "rejection-first"])("review12 pending undo survives zero submission in %s order", async order => {
    const s = setup(); await seed(s); const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.service.updateCell(2, "name", "two", "preflight fails"); const failed = s.posted.at(-1)!;
    const reject = async () => { s.reply(failed, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "parse failed" }); await flush(); };
    const commitUndo = async () => { s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3)); await undo; };
    if (order === "undo-first") {
      await commitUndo(); expect(s.history.canRedo).toBe(false); await reject();
    } else { await reject(); await commitUndo(); }
    expect(s.history.writeUncertain).toBe(false); expect(s.history.canRedo).toBe(true);
    const redo = s.service.performRedo(); await flush();
    expect(s.posted.at(-1)!.payload).toMatchObject({ rowKey: 1, newValue: "new" });
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("new", 1, "name", 4)); await redo;
  });

  it.each([false, true])("review12 concurrent zero submissions restore redo regardless of rejection order (%s)", async reverse => {
    const s = setup(); await seed(s); const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.service.updateCell(1, "other", "before", "A"); const first = s.posted.at(-1)!;
    s.service.updateCell(2, "name", "two", "B"); const second = s.posted.at(-1)!;
    const requests = reverse ? [second, first] : [first, second];
    s.reply(requests[0]!, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "preflight" }); await flush();
    expect(s.history.canRedo).toBe(false);
    s.reply(requests[1]!, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "preflight" }); await flush();
    expect(s.history.canRedo).toBe(true); expect(s.history.redoStack).toHaveLength(1); expect(s.history.writeUncertain).toBe(false);
  });

  it.each([false, true])("review12 zero submission retains older redo without resurrecting a later consumed redo (%s)", async consumeNewer => {
    const s = setup(); await seed(s);
    s.service.updateCell(2, "name", "two", "changed"); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("changed", 2, "name", 3)); await flush();
    const firstUndo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("two", 2, "name", 4)); await firstUndo;
    const olderRedo = s.history.redoStack[0];
    s.service.updateCell(1, "other", "before", "preflight fails"); const failed = s.posted.at(-1)!;
    const secondUndo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 5)); await secondUndo;
    expect(s.history.canRedo).toBe(false);
    s.reply(failed, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "preflight" }); await flush();
    const newerRedo = s.history.redoStack[1]; expect(newerRedo).toBeDefined(); expect(newerRedo).not.toBe(olderRedo);
    if (consumeNewer) {
      const redo = s.service.performRedo(); await flush();
      expect(s.posted.at(-1)!.payload).toMatchObject({ rowKey: 1, newValue: "new" });
      s.reply(s.posted.at(-1)!, "table.editCommitted", edit("new", 1, "name", 6)); await redo;
    }
    s.reply(failed, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "preflight" }); await flush();
    expect(s.history.redoStack).toEqual(consumeNewer ? [olderRedo] : [olderRedo, newerRedo]);
  });

  it.each(["clear", "authority", "unknown"])("review12 late zero submission cannot revive redo after %s", async retirement => {
    const s = setup(); await seed(s); const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.service.updateCell(2, "name", "two", "preflight fails"); const failed = s.posted.at(-1)!;
    if (retirement === "clear") s.history.clear();
    else if (retirement === "authority") useWorkspaceSessionStore().sessionEpoch += 1;
    else {
      s.service.updateCell(1, "other", "before", "uncertain");
      s.reply(s.posted.at(-1)!, "table.editRejected", { kind: "unknown", message: "unconfirmed" }); await flush();
    }
    s.reply(failed, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "preflight" }); await flush();
    expect(s.history.redoStack).toHaveLength(0); expect(s.history.canRedo).toBe(false);
    expect(s.history.writeUncertain).toBe(retirement === "unknown");
  });

  it("review12 a synchronous bridge exception without a sent fact remains uncertain", async () => {
    const s = setup(); vi.spyOn(s.bridge, "requestWithHandle").mockImplementationOnce(() => { throw new Error("no submission fact"); });
    s.service.updateCell(2, "name", "two", "uncertain"); await flush();
    expect(s.history.writeUncertain).toBe(true);
  });

  it.each([false, true])("review12 synchronous paste bridge failure distinguishes proven notSent (%s)", async notSent => {
    const s = setup(); const paste = usePasteService(); cleanups.push(paste.dispose);
    await seed(s); const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    const cause = new Error("no submission fact");
    vi.spyOn(s.bridge, "requestWithHandle").mockImplementationOnce(() => {
      throw notSent ? new BridgeRequestNotSentError("table.applyPasteRequested", "unsent", cause) : cause;
    });
    const input = { collection: "u", token: "t", idempotencyKey: "k" };
    expect(() => paste.apply(input)).not.toThrow(); await flush();
    expect(s.history.writeUncertain).toBe(!notSent); expect(s.history.canRedo).toBe(notSent);
    expect(usePasteStore().phase).toBe("error");
    if (!notSent) { expect(usePasteStore().error).toContain("尚未确认"); s.history.captureWriteRefresh()(); }
    const count = s.posted.length; paste.apply(input); expect(s.posted).toHaveLength(count + 1);
  });
  it.each(["edit", "paste", "closed-edit", "closed-paste"])("review12 bridge admission rejects unsent %s without fencing its authority", async kind => {
    const s = setup(); const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    const workspaceId = "11111111-1111-4111-8111-111111111111";
    const closed = kind.startsWith("closed-");
    if (!closed) {
      session.setWorkspaces([{ contractVersion: "2.0", workspaceId, displayName: "test", selectedRoot: "D:/Workspaces/test", activityRoot: null,
        storageKind: "fixed", coordinationStrength: "strong", lastOpenedAt: null, lastKnownHealth: "healthy", lastSnapshotAt: null, lastSyncAt: null, pendingSync: false }]);
      session.applySession({ contractVersion: "2.0", workspaceId, sessionEpoch: 1, state: "openedWritable", openMode: "writable", writable: true, provisional: false, phase: "idle", errorCode: null });
      expect(session.hasOpenWorkspace).toBe(true);
      expect(session.beginSwitch("22222222-2222-4222-8222-222222222222")).toBe(true); session.reportTransitionPhase("draining");
    } else expect(session.hasOpenWorkspace).toBe(false);
    const paste = usePasteService(); cleanups.push(paste.dispose);
    s.history.push({ id: "kept", kind: "updateCell", label: "kept", timestamp: 0, undo: async () => {}, redo: async () => {} });
    const count = s.posted.length;
    if (kind.endsWith("edit")) s.service.updateCell(2, "name", "two", "unsent");
    else paste.apply({ collection: "u", token: "t", idempotencyKey: "k" });
    await flush();
    expect(s.posted).toHaveLength(count); expect(s.history.writeUncertain).toBe(false); expect(s.history.undoStackSize).toBe(1);
    if (!closed) { session.failSwitch("SWITCH_ABORTED"); await flush(); expect(session.sessionEpoch).toBe(1); expect(session.hasOpenWorkspace).toBe(true); }
    expect(s.history.writeUncertain).toBe(false);
  });

  it("review10 decoder rejects a non-boolean submission fact without settling its owner", async () => {
    const s = setup(); s.service.updateCell(1, "name", "old", "optimistic"); const request = s.posted.at(-1)!;
    s.reply(request, "table.editRejected", { kind: "unknown", notSubmitted: "true", message: "malformed fact" }); await flush();
    expect(s.rejected).not.toHaveBeenCalled(); expect(s.diagnostics).toHaveBeenCalledWith(expect.objectContaining({ kind: "malformed" }));
    s.reply(request, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "known preflight failure" }); await flush();
    expect(s.history.writeUncertain).toBe(false); expect(s.rejected).toHaveBeenCalledOnce();
  });

  it("review10 notSubmitted after history clear cannot roll back the refreshed row or erase its history", async () => {
    const s = setup(); s.service.updateCell(1, "name", "old", "old optimistic"); const write = s.posted.at(-1)!;
    s.history.clear(); s.table.rollbackCellEdit(1, "name", "refreshed authority");
    s.reply(write, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "preflight failed" }); await flush();
    expect(s.table.allRows[0]?.name).toBe("refreshed authority"); expect(s.history.writeUncertain).toBe(false); expect(s.rejected).not.toHaveBeenCalled();
  });

  it.each(["timeout", "unknown"])("review10 sent %s after history clear still fences its table", async failure => {
    vi.useFakeTimers(); const s = setup(); s.service.updateCell(1, "name", "old", "possibly committed"); const write = s.posted.at(-1)!;
    s.history.clear();
    if (failure === "timeout") await vi.advanceTimersByTimeAsync(5_000);
    else { s.reply(write, "table.editRejected", { kind: "unknown", message: "unconfirmed" }); await flush(); }
    expect(s.history.writeUncertain).toBe(true);
    const count = s.posted.length; s.service.insertRow({ name: "blind retry" }); expect(s.posted).toHaveLength(count);
  });

  it("review10 proven notSubmitted keeps unrelated history and rolls back only its edit", async () => {
    const s = setup(); await seed(s);
    s.service.updateCell(2, "name", "two", "optimistic"); const write = s.posted.at(-1)!; s.table.rollbackCellEdit(2, "name", "optimistic");
    s.reply(write, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "private preflight parse" }); await flush();
    expect(s.history.writeUncertain).toBe(false); expect(s.history.undoStackSize).toBe(1);
    expect(s.table.allRows[1]?.name).toBe("two"); expect(s.rejected).toHaveBeenCalledWith(expect.objectContaining({ notSubmitted: true }));
  });

  it.each([false, true])("review10 notSubmitted restores only the redo cleared by its own action (%s)", async newerAction => {
    const s = setup(); await seed(s); const undo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    expect(s.history.canRedo).toBe(true);
    s.service.updateCell(2, "name", "two", "preflight fails"); const failed = s.posted.at(-1)!;
    if (newerAction) { s.service.insertRow({ name: "newer" }); s.reply(s.posted.at(-1)!, "table.rowsInserted", { rowKey: 9, row: { id: 9, name: "newer" }, revision: revision(4) }); await flush(); }
    s.reply(failed, "table.editRejected", { kind: "unknown", notSubmitted: true, message: "parse failed" }); await flush();
    expect(s.history.writeUncertain).toBe(false); expect(s.history.canRedo).toBe(!newerAction);
  });

  it.each(["insert", "paste"] as const)("review7 %s uncertainty survives table/null switches and failed selection recovery", async kind => {
    const s = setup(); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    const paste = usePasteService(); cleanups.push(paste.dispose);
    const apply = () => kind === "insert" ? s.service.insertRow({ name: "new" })
      : paste.apply({ collection: "u", token: "t", idempotencyKey: "k" });
    apply(); const sent = s.posted.at(-1)!;
    if (kind === "insert") s.reply(sent, "table.editRejected", { kind: "unknown", message: "unconfirmed" });
    else s.reply(sent, "table.pasteApplied", pasteResult({ outcome: "pending" }));
    await flush(); expect(s.history.writeUncertain).toBe(true);
    s.ws.selectTable("other"); expect(s.history.writeUncertain).toBe(false);
    s.ws.clear(); s.ws.selectTable("u"); expect(s.history.writeUncertain).toBe(true);
    const count = s.posted.length; apply(); expect(s.posted).toHaveLength(count);
    tables.selectTable("u");
    s.emit("operation.failed", { operation: "table.selected", code: "WRITE_TERMINAL_UNCONFIRMED", message: "请重新打开工作区核实" });
    await flush(); expect(s.history.writeUncertain).toBe(true);
    const page = { table: "u", columns: [], rows: [], offset: 0, limit: 1, totalRows: 0, mode: "remote", revision: revision(4) };
    s.emit("table.datasetReady", page); await flush();
    const query = s.posted.at(-1)!; expect(query.type).toBe("table.queryRequested");
    s.reply(query, "table.pageLoaded", page); await flush(); expect(s.history.writeUncertain).toBe(false);
  });

  it.each(["insert", "paste"] as const)("review7 retiring a sent %s synchronously fences its original table", async kind => {
    const s = setup(); const paste = usePasteService(); cleanups.push(paste.dispose);
    const apply = () => kind === "insert" ? s.service.insertRow({ name: "new" })
      : paste.apply({ collection: "u", token: "t", idempotencyKey: "k" });
    apply(); const count = s.posted.length;
    s.ws.selectTable("other"); s.ws.selectTable("u");
    expect(s.history.writeUncertain).toBe(true);
    apply(); expect(s.posted).toHaveLength(count); await flush();
  });

  it("review7 a rejected chain cannot replace a separately captured external CAS baseline", async () => {
    const s = setup(); const externalDigest = `sha256:${"b".repeat(64)}`;
    s.service.updateCell(1, "name", "old", "invalid", digest); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "external", "corrected", externalDigest);
    s.reply(first, "table.editRejected", { kind: "mutation_validation", message: "invalid value" }); await flush();
    expect(s.posted.at(-1)!.payload).toMatchObject({ oldValue: "external", expectedDigest: externalDigest });
  });

  it("review7 a corrected queued edit uses the real unchanged baseline after validation rejection", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "invalid", digest); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "invalid", "corrected", digest);
    s.reply(first, "table.editRejected", { kind: "mutation_validation", message: "invalid value" }); await flush();
    const second = s.posted.at(-1)!;
    // Mirror the real Gateway old-value preflight and Go digest guard, rather than accepting any mocked write.
    expect(second.payload.oldValue).toBe("old"); expect(second.payload.expectedDigest).toBe(digest);
    s.reply(second, "table.editCommitted", edit("corrected")); await flush();
    expect(s.history.undoStackSize).toBe(1); const undo = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)!.payload.newValue).toBe("old");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
  });

  it("review4 queued edits use only their local receipt chain to advance real CAS guards", async () => {
    const s = setup(); let value: unknown = "old"; let authorityDigest = digest; let commit = 1;
    const requests: Envelope[] = [];
    s.postMessage.mockImplementation((message: unknown) => {
      const request = (typeof message === "string" ? JSON.parse(message) : message) as Envelope;
      s.posted.push(request); requests.push(request);
    });
    const apply = async (request: Envelope) => {
      if (request.payload.expectedDigest !== authorityDigest || request.payload.oldValue !== value) {
        s.reply(request, "table.editRejected", { kind: "edit_conflict", message: "real CAS rejected stale baseline" });
      } else {
        value = String(request.payload.newValue).trim(); commit += 1;
        authorityDigest = `sha256:${String(commit).repeat(64)}`;
        s.reply(request, "table.editCommitted", { ...edit(value, 1, "name", commit), currentRow: { rowKey: 1, name: value, __vibetableDigest: authorityDigest } });
      }
      await flush();
    };
    s.service.updateCell(1, "name", "old", " first ", digest);
    s.service.updateCell(1, "name", " first ", "second", digest);
    s.service.updateCell(1, "name", "second", "third", digest);
    await apply(requests[0]!); await apply(requests[1]!); await apply(requests[2]!);
    expect(value).toBe("third"); expect(s.rejected).not.toHaveBeenCalled(); expect(s.history.undoStackSize).toBe(3);
    const undo = s.service.performUndo(); await flush(); await apply(requests.at(-1)!); await undo;
    expect(value).toBe("second");
  });

  it.each([true, false])("review4 a guard outside the local receipt chain remains protected (%s)", async (validExternalGuard) => {
    const s = setup(); const externalDigest = `sha256:${"e".repeat(64)}`; const firstDigest = `sha256:${"b".repeat(64)}`;
    s.service.updateCell(1, "name", "old", "first", digest); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "external value", "next", externalDigest);
    s.reply(first, "table.editCommitted", { ...edit("first"), currentRow: { rowKey: 1, name: "first", __vibetableDigest: firstDigest } }); await flush();
    const next = s.posted.at(-1)!;
    expect(next.payload).toMatchObject({ oldValue: "external value", expectedDigest: externalDigest });
    const authorityDigest = validExternalGuard ? externalDigest : firstDigest;
    const authorityValue = validExternalGuard ? "external value" : "first";
    if (next.payload.expectedDigest === authorityDigest && next.payload.oldValue === authorityValue)
      s.reply(next, "table.editCommitted", { ...edit("next", 1, "name", 4), currentRow: { rowKey: 1, name: "next", __vibetableDigest: externalDigest } });
    else s.reply(next, "table.editRejected", { kind: "edit_conflict", message: "external CAS mismatch" });
    await flush(); expect(s.posted).toHaveLength(2);
    expect(s.history.undoStackSize).toBe(validExternalGuard ? 2 : 1);
    if (validExternalGuard) {
      const undo = s.service.performUndo(); await flush(); expect(s.posted.at(-1)?.payload.newValue).toBe("external value");
      s.reply(s.posted.at(-1)!, "table.editCommitted", edit("external value", 1, "name", 5)); await undo;
    } else expect(s.rejected).toHaveBeenCalledWith(expect.objectContaining({ kind: "edit_conflict" }));
  });

  it.each([false, true])("review4 an unconfirmed Host terminal exposes recovery only to its current query (%s)", async (retired) => {
    const s = setup(); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    s.history.markWriteUncertain(); const recovery = tables.reloadCurrentQuery(); const request = s.posted.at(-1)!;
    if (retired) { s.ws.selectTable("other"); s.ws.selectTable("u"); }
    s.reply(request, "operation.failed", { code: "WRITE_TERMINAL_UNCONFIRMED", message: "请重新打开工作区并核实数据，不要直接重试写入。" });
    await recovery;
    if (retired) expect(s.table.error).toBeNull();
    else { expect(s.table.error).toContain("重新打开工作区"); expect(s.history.writeUncertain).toBe(true); }
  });

  it("review4 Host terminal uncertainty retires write ownership even before renderer timeout", async () => {
    const s = setup(); await seed(s); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    s.service.updateCell(2, "name", "two", "possibly committed"); const write = s.posted.at(-1)!;
    s.table.rollbackCellEdit(2, "name", "possibly committed");
    const recovery = tables.reloadCurrentQuery(); const query = s.posted.at(-1)!;
    s.reply(query, "operation.failed", { code: "WRITE_TERMINAL_UNCONFIRMED", message: "写入结果尚未确认，请重新打开工作区核实。" }); await recovery;
    expect(s.history.writeUncertain).toBe(true); expect(s.history.canUndo).toBe(false);
    expect(s.table.error).toContain("重新打开工作区");
    s.reply(write, "table.editRejected", { kind: "mutation_validation", message: "late rejection" }); await flush();
    expect(s.table.allRows[1]?.name).toBe("possibly committed"); expect(s.rejected).not.toHaveBeenCalled();
    s.service.updateCell(2, "name", "possibly committed", "blind retry"); expect(s.posted).toHaveLength(3);
  });

  it("review3 serializes same-cell writes across gateway commit and delayed readback", async () => {
    const s = setup();
    let authority: unknown = "old";
    const reads: { request: Envelope; revision: number }[] = [];
    s.postMessage.mockImplementation((message: unknown) => {
      const request = (typeof message === "string" ? JSON.parse(message) : message) as Envelope;
      s.posted.push(request);
      if (request.type === "table.updateCellRequested") {
        // PocketBaseTableGateway applies first, then reads the current row separately.
        authority = request.payload.newValue;
        reads.push({ request, revision: reads.length + 2 });
      }
    });
    s.service.updateCell(1, "name", "old", "first", digest);
    s.table.setDatasetReady({ table: "u", columns: [], rows: [{ rowKey: 1, name: authority, __vibetableDigest: digest }], offset: 0, limit: 1, totalRows: 1, mode: "remote", revision: revision(2) });
    s.service.updateCell(1, "name", "first", "second", digest);
    expect(s.posted).toHaveLength(1); // B cannot alter A's pending readback.
    await s.service.performUndo(); expect(s.posted).toHaveLength(1);
    s.reply(reads[0]!.request, "table.editCommitted", edit(authority, 1, "name", reads[0]!.revision)); await flush();
    expect(s.posted).toHaveLength(2);
    expect(s.posted[1]?.payload.expectedDigest).toBe(digest);
    s.reply(reads[1]!.request, "table.editCommitted", edit(authority, 1, "name", reads[1]!.revision)); await flush();
    const undoSecond = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)?.payload.newValue).toBe("first");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit(authority, 1, "name", 4)); await undoSecond;
    const undoFirst = s.service.performUndo(); await flush();
    expect(s.posted.at(-1)?.payload.newValue).toBe("old");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit(authority, 1, "name", 5)); await undoFirst;
  });

  it("review3 real refresh preserves unknown until a post-unknown correlated authority query succeeds", async () => {
    const s = setup(); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    const paste = usePasteService(); paste.init(); cleanups.push(paste.dispose);
    s.service.updateCell(1, "name", "old", "unknown");
    s.reply(s.posted.at(-1)!, "table.editRejected", { kind: "unknown", message: "readback failed" }); await flush();
    tables.refresh();
    expect(s.history.writeUncertain).toBe(true);
    const attempts = () => {
      const count = s.posted.length;
      paste.apply({ collection: "u", token: "t", idempotencyKey: "k" });
      s.service.updateCell(1, "name", "visible", "blocked");
      expect(s.posted).toHaveLength(count);
    };
    attempts();
    s.emit("operation.failed", { operation: "table.selected", message: "refresh failed" }); await flush(); attempts();
    const page = { table: "u", columns: [], rows: [{ rowKey: 1, name: "authority", __vibetableDigest: digest }], offset: 0, limit: 1, totalRows: 1, mode: "remote", revision: revision(3) };
    s.emit("table.datasetReady", page); await flush();
    expect(s.history.writeUncertain).toBe(true);
    const query = s.posted.at(-1)!; expect(query.type).toBe("table.queryRequested");
    s.reply(query, "table.pageLoaded", page); await flush();
    expect(s.history.writeUncertain).toBe(false);
    s.table.setEditSchema([], revision(3));
    s.service.updateCell(1, "name", "authority", "explicit new write");
    expect(s.posted.at(-1)?.type).toBe("table.updateCellRequested");
  });

  it("review3 pre-unknown query cannot unlock writes when its page arrives afterward", async () => {
    const s = setup(); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    const oldQuery = tables.reloadCurrentQuery(); const request = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "old", "unknown");
    s.reply(s.posted.at(-1)!, "table.editRejected", { kind: "unknown", message: "readback failed" }); await flush();
    s.reply(request, "table.pageLoaded", { table: "u", columns: [], rows: [{ rowKey: 1, name: "old", __vibetableDigest: digest }], offset: 0, limit: 1, totalRows: 1, mode: "remote", revision: revision(2) });
    await oldQuery; expect(s.history.writeUncertain).toBe(true);
  });

  it("review3 definitely unsent normal write preserves an existing redo", async () => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.postMessage.mockImplementationOnce(() => { throw new Error("not sent"); });
    s.service.updateCell(2, "name", "two", "unsent"); await flush();
    expect(s.history.canRedo).toBe(true); expect(s.history.writeUncertain).toBe(false);
  });

  it("review3 retires queued and revision-waiting edits across unknown and a real refresh", async () => {
    vi.useFakeTimers(); const s = setup(); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    s.service.updateCell(1, "name", "old", "unknown"); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "unknown", "queued old action");
    s.table.setEditSchema([], { ...revision(), schemaRevision: "" });
    s.service.updateCell(2, "name", "two", "deferred old action");
    s.reply(first, "table.editRejected", { kind: "unknown", message: "readback failed" }); await flush();
    tables.refresh();
    const page = { table: "u", columns: [], rows: [{ rowKey: 2, name: "authority", __vibetableDigest: digest }], offset: 0, limit: 1, totalRows: 1, mode: "remote", revision: revision(3) };
    s.emit("table.datasetReady", page); await flush();
    s.reply(s.posted.at(-1)!, "table.pageLoaded", page); await flush();
    expect(s.history.writeUncertain).toBe(false);
    s.service.updateCell(2, "name", "authority", "explicit new action");
    await vi.advanceTimersByTimeAsync(25); await flush();
    expect(s.posted.filter((r) => r.type === "table.updateCellRequested").map((r) => r.payload.newValue)).toEqual(["unknown", "explicit new action"]);
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("explicit new action", 2, "name", 4)); await flush();
    const undo = s.service.performUndo(); await flush(); expect(s.posted.at(-1)?.payload.newValue).toBe("authority");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("authority", 2, "name", 5)); await undo;
  });

  it("review3 a failed recovery query keeps unknown latched until a newer refresh succeeds", async () => {
    const s = setup(); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    s.history.markWriteUncertain(); tables.refresh();
    const page = { table: "u", columns: [], rows: [], offset: 0, limit: 1, totalRows: 0, mode: "remote", revision: revision(3) };
    s.emit("table.datasetReady", page); await flush();
    s.reply(s.posted.at(-1)!, "operation.failed", { message: "authority query failed" }); await flush();
    expect(s.history.writeUncertain).toBe(true);
    const count = s.posted.length; s.service.updateCell(1, "name", "old", "blocked"); expect(s.posted).toHaveLength(count);
    tables.refresh(); s.emit("table.datasetReady", page); await flush();
    s.reply(s.posted.at(-1)!, "table.pageLoaded", page); await flush();
    expect(s.history.writeUncertain).toBe(false);
  });

  it("review3 normal same-cell edit waits for undo's complete readback", async () => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "new", "next"); expect(s.posted).toHaveLength(2);
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3)); await undo; await flush();
    expect(s.posted).toHaveLength(3); expect(s.history.canRedo).toBe(false);
    expect(s.posted.at(-1)?.payload).toMatchObject({ oldValue: "old", newValue: "next", expectedDigest: digest });
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("next", 1, "name", 4)); await flush();
    const undoNext = s.service.performUndo(); await flush(); expect(s.posted.at(-1)?.payload.newValue).toBe("old");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 5)); await undoNext;
  });

  it("review3 consecutive unsent actions preserve the original redo through both continuations", async () => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.postMessage.mockImplementationOnce(() => { throw new Error("first not sent"); });
    s.postMessage.mockImplementationOnce(() => { throw new Error("second not sent"); });
    s.service.updateCell(2, "name", "two", "unsent first"); s.service.insertRow({ name: "unsent second" }); await flush();
    expect(s.history.canRedo).toBe(true); expect(s.history.writeUncertain).toBe(false);
  });

  it("review3 an unsent normal edit preserves redo from a concurrently pending undo", async () => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.postMessage.mockImplementationOnce(() => { throw new Error("not sent"); });
    s.service.updateCell(2, "name", "two", "unsent"); await flush();
    s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    expect(s.history.canRedo).toBe(true); expect(s.history.writeUncertain).toBe(false);
  });

  it("review3 an unsent action cannot restore redo invalidated by a newer sent action", async () => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.postMessage.mockImplementationOnce(() => { throw new Error("not sent"); });
    s.service.updateCell(2, "name", "two", "unsent");
    s.service.insertRow({ name: "new action" }); const inserted = s.posted.at(-1)!;
    s.reply(inserted, "table.rowsInserted", { rowKey: 10, row: { rowKey: 10, name: "new action" }, revision: revision(4) }); await flush();
    expect(s.history.canRedo).toBe(false); expect(s.history.undoStackSize).toBe(1);
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

  it("unknown write fences a late rejection and blocks new optimistic writes until authoritative refresh", async () => {
    vi.useFakeTimers(); const s = setup(); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    s.service.updateCell(1, "name", "old", "possibly committed");
    await vi.advanceTimersByTimeAsync(1_000);
    s.service.updateCell(2, "name", "two", "second"); const second = s.posted.at(-1)!;
    s.table.rollbackCellEdit(2, "name", "second");
    await vi.advanceTimersByTimeAsync(4_000);
    s.reply(second, "table.editRejected", { kind: "mutation_validation", message: "old rejection" }); await flush();
    expect(s.table.allRows[1]?.name).toBe("second");
    expect(s.rejected).toHaveBeenCalledTimes(1); expect(s.rejected.mock.calls[0]?.[0].kind).toBe("unknown");
    s.table.rollbackCellEdit(2, "name", "attempt after unknown");
    s.service.updateCell(2, "name", "second", "attempt after unknown"); await flush();
    expect(s.posted).toHaveLength(2); expect(s.table.allRows[1]?.name).toBe("second");
    const reloaded = tables.reloadCurrentQuery();
    s.reply(s.posted.at(-1)!, "table.pageLoaded", { table: "u", columns: [], rows: [{ rowKey: 2, name: "authority", __vibetableDigest: digest }], offset: 0, limit: 1, totalRows: 1, mode: "remote", revision: revision(4) }); await reloaded;
    s.service.updateCell(2, "name", "authority", "new action"); expect(s.posted).toHaveLength(4);
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("new action", 2, "name", 5)); await flush();
    expect(s.history.canUndo).toBe(true);
  });

  it("a stale rejection remains fenced after refresh and cannot affect a new same-cell request", async () => {
    const s = setup(); const tables = useTableService(); tables.init(); cleanups.push(tables.dispose);
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.service.updateCell(2, "name", "two", "second"); const second = s.posted.at(-1)!;
    s.reply(first, "table.editRejected", { kind: "unknown", message: "unknown commit" }); await flush();
    const reloaded = tables.reloadCurrentQuery();
    s.reply(s.posted.at(-1)!, "table.pageLoaded", { table: "u", columns: [], rows: [{ rowKey: 2, name: "authority", __vibetableDigest: digest }], offset: 0, limit: 1, totalRows: 1, mode: "remote", revision: revision(4) }); await reloaded;
    s.service.updateCell(2, "name", "authority", "new action"); await flush();
    // An already-sent sibling remains serialized ahead of the new same-cell action.
    expect(s.posted).toHaveLength(3);
    s.reply(second, "table.editRejected", { kind: "edit_conflict", message: "stale rejection", currentRow: { name: "stale" } }); await flush();
    expect(s.table.allRows[0]?.name).toBe("authority"); expect(s.rejected).toHaveBeenCalledTimes(1);
    const current = s.posted.at(-1)!; expect(current.requestId).not.toBe(second.requestId);
    s.reply(current, "table.editCommitted", edit("new action", 2, "name", 5)); await flush();
    expect(s.history.canUndo).toBe(true);
    const undo = s.service.performUndo(); await flush(); expect(s.posted.at(-1)?.payload.newValue).toBe("authority");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("authority", 2, "name", 6)); await undo;
  });

  it("an unknown result queued between response continuations prevents a sibling rollback", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.service.updateCell(2, "name", "two", "second"); const second = s.posted.at(-1)!;
    s.table.rollbackCellEdit(2, "name", "second");
    s.reply(first, "table.editRejected", { kind: "unknown", message: "unknown commit" });
    queueMicrotask(() => s.reply(second, "table.editRejected", { kind: "edit_conflict", message: "late conflict" }));
    await flush(); expect(s.table.allRows[1]?.name).toBe("second");
    expect(s.rejected).toHaveBeenCalledTimes(1); expect(s.history.writeUncertain).toBe(true);
  });

  it("a pending same-cell write also fences an already published history item", async () => {
    const s = setup(); await seed(s);
    expect(s.history.canUndo).toBe(true); // Populate the computed cache before the pending Map changes.
    s.service.updateCell(1, "name", "new", "pending"); const pending = s.posted.at(-1)!;
    expect(s.history.canUndo).toBe(false); await s.service.performUndo(); expect(s.posted).toHaveLength(2);
    s.reply(pending, "table.editRejected", { kind: "mutation_validation", message: "invalid" }); await flush();
    expect(s.history.canUndo).toBe(true);
  });

  it("same-cell queue prevents early undo and preserves normalized authority values", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first input"); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "first visible", "second input");
    expect(s.posted).toHaveLength(1); await s.service.performUndo(); expect(s.posted).toHaveLength(1);
    s.reply(first, "table.editCommitted", edit("normalized first", 1, "name", 2)); await flush();
    expect(s.history.canUndo).toBe(false); expect(s.posted).toHaveLength(2);
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("normalized second", 1, "name", 3)); await flush();
    expect(s.history.undoStackSize).toBe(2);
    const undo = s.service.performUndo(); await flush(); expect(s.posted.at(-1)?.payload.newValue).toBe("normalized first");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("normalized first", 1, "name", 4)); await undo;
  });

  it("an unsent queued second edit restores the first confirmed value", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.table.rollbackCellEdit(1, "name", "second");
    s.postMessage.mockImplementationOnce(() => { throw new Error("not sent"); });
    s.service.updateCell(1, "name", "first", "second"); await flush();
    expect(s.table.allRows[0]?.name).toBe("second"); expect(s.history.canUndo).toBe(false);
    s.reply(first, "table.editCommitted", edit("first")); await flush();
    expect(s.table.allRows[0]?.name).toBe("first"); expect(s.history.undoStackSize).toBe(1); expect(s.history.canUndo).toBe(true);
  });

  it("a definitely unsent edit rolls back and preserves known history", async () => {
    const s = setup(); await seed(s);
    s.table.rollbackCellEdit(2, "name", "optimistic");
    s.postMessage.mockImplementationOnce(() => { throw new Error("transport unavailable"); });
    s.service.updateCell(2, "name", "two", "optimistic"); await flush();
    expect(s.table.allRows[1]?.name).toBe("two");
    expect(s.history.undoStackSize).toBe(1);
    expect(s.rejected).toHaveBeenCalledWith(expect.objectContaining({ kind: "backend_unavailable" }));
    expect(s.rejected).not.toHaveBeenCalledWith(expect.objectContaining({ kind: "unknown" }));
    s.postMessage.mockImplementationOnce(() => { throw new Error("undo not sent"); });
    await s.service.performUndo();
    expect(s.history.canUndo).toBe(true); expect(s.history.canRedo).toBe(false);
    const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.postMessage.mockImplementationOnce(() => { throw new Error("redo not sent"); });
    await s.service.performRedo();
    expect(s.history.canUndo).toBe(false); expect(s.history.canRedo).toBe(true);
    expect(s.history.lastError).toBe("redo not sent");
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
      s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 4));
      await undo;
      expect(s.history.canRedo).toBe(false);
      s.reply(ordinary, "table.editCommitted", edit("changed", 2, "name", 3));
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

  it("a failed pending redo cannot return after a new ordinary action was submitted", async () => {
    const s = setup(); await seed(s);
    const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    const redo = s.service.performRedo(); await flush(); const inverse = s.posted.at(-1)!;
    s.service.updateCell(2, "name", "two", "new action"); const ordinary = s.posted.at(-1)!;
    s.reply(inverse, "table.editRejected", { kind: "edit_conflict", message: "redo conflict" });
    await redo;
    s.reply(ordinary, "table.editCommitted", edit("new action", 2, "name", 4)); await flush();
    expect(s.history.canRedo).toBe(false);
    expect(s.history.undoStackSize).toBe(1);
    expect(s.history.lastError).toBe("redo conflict");
    expect(s.table.allRows[1]?.name).toBe("new action");
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
      s.reply(s.posted.at(-1)!, "table.editCommitted", edit(item.productValue, 1, item.field)); await flush();
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
    [1, "name", 1, "other"], [1, "name", 2, "name"],
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

  it("two rejected queued writes to one cell restore its true baseline", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "first", "second"); expect(s.posted).toHaveLength(1);
    s.reply(first, "table.editRejected", { kind: "mutation_validation", message: "invalid first" }); await flush();
    expect(s.posted).toHaveLength(2);
    s.reply(s.posted.at(-1)!, "table.editRejected", { kind: "mutation_validation", message: "invalid second" }); await flush();
    expect(s.table.allRows[0]?.name).toBe("old"); expect(s.history.canUndo).toBe(false);
    expect(s.rejected).toHaveBeenCalledTimes(2);
  });

  it("a rejected earlier optimistic value is not used as the next successful write's undo value", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.service.updateCell(1, "name", "first", "second");
    s.reply(first, "table.editRejected", { kind: "mutation_validation", message: "invalid" }); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("second", 1, "name", 3)); await flush();
    const undo = s.service.performUndo(); await flush(); expect(s.posted.at(-1)?.payload.newValue).toBe("old");
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 4)); await undo;
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

  it("does not overwrite a later authoritative page with an older response", async () => {
    const s = setup();
    s.service.updateCell(1, "name", "old", "first"); const first = s.posted.at(-1)!;
    s.table.setDatasetReady({ table: "u", columns: [], rows: [{ rowKey: 1, name: "authority", __vibetableDigest: digest }], offset: 0, limit: 1, totalRows: 1, mode: "remote", revision: revision(3) });
    s.reply(first, "table.editCommitted", edit("first", 1, "name", 2)); await flush();
    expect(s.table.allRows[0]?.name).toBe("authority"); expect(s.table.revision?.dataRevision).toBe(3);
    expect(s.history.undoStackSize).toBe(1);
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
    s.service.updateCell(2, "name", "two", "confirmed"); const sibling = s.posted.at(-1)!;
    await vi.advanceTimersByTimeAsync(4_000);
    expect(s.rejected).toHaveBeenCalledWith(expect.objectContaining({ kind: "unknown" }));
    s.reply(sibling, "table.editCommitted", edit("confirmed", 2, "name", 3)); await flush();
    expect(s.table.allRows[1]?.name).toBe("confirmed");
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
    expect(s.history.redoStack[0]?.dataRevision).toBe(3);
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
    expect(s.history.redoStack[0]?.dataRevision).toBe(3);
    const redo = s.service.performRedo(); await flush();
    expect(s.posted.at(-1)?.payload.rows).toEqual([{ rowKey: 22, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [22], revision: revision(4) }); await redo;
  });

  it.each([false, true])("review15 multi-row undo retains redo only without an intervening ordinary commit (%s)", async interleaved => {
    const s = setup(); s.service.deleteRows([{ rowKey: 1, expectedDigest: digest }, { rowKey: 2, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [1, 2], revision: revision(2) }); await flush();
    const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.rowsInserted", { rowKey: 11, row: { rowKey: 11, name: "old", __vibetableDigest: digest }, revision: revision(3) }); await flush();
    const secondRestore = s.posted.at(-1)!;
    if (interleaved) {
      s.service.updateCell(11, "name", "old", "intervening edit");
      s.reply(s.posted.at(-1)!, "table.editCommitted", edit("intervening edit", 11, "name", 4)); await flush();
    }
    s.reply(secondRestore, "table.rowsInserted", { rowKey: 22, row: { rowKey: 22, name: "two", __vibetableDigest: digest }, revision: revision(interleaved ? 5 : 4) }); await undo;
    expect(s.history.canRedo).toBe(!interleaved); expect(s.history.undoStackSize).toBe(interleaved ? 1 : 0);
    if (!interleaved) {
      expect(s.history.redoStack[0]?.dataRevision).toBe(4);
      const redo = s.service.performRedo(); await flush();
      expect(s.posted.at(-1)!.payload.rows).toEqual([{ rowKey: 11, expectedDigest: digest }, { rowKey: 22, expectedDigest: digest }]);
      s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [11, 22], revision: revision(5) }); await redo;
    }
  });
  it.each(["edit_conflict", "unknown"] as const)("partial multi-row delete undo never restores a blindly replayable entry after %s", async (kind) => {
    const s = setup(); s.service.deleteRows([{ rowKey: 1, expectedDigest: digest }, { rowKey: 2, expectedDigest: digest }]);
    s.reply(s.posted.at(-1)!, "table.rowsDeleted", { deletedRowKeys: [1, 2], revision: revision(2) }); await flush();
    const undo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.rowsInserted", { rowKey: 11, row: { rowKey: 11, name: "old" }, revision: revision(3) }); await flush();
    s.reply(s.posted.at(-1)!, "table.editRejected", { kind, message: "second failed" }); await undo;
    expect(s.history.canUndo).toBe(false); expect(s.history.canRedo).toBe(false);
    expect(s.table.allRows).toHaveLength(1);
    if (kind === "unknown") expect(s.history.writeUncertain).toBe(true);
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

  it.each(["conflict-first", "undo-first"] as const)("review16 rejected paste preserves the undo that invalidated its preview (%s)", async (order) => {
    const s = pasteSetup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); const inverse = s.posted.at(-1)!;
    s.paste.preview({ ...previewInput, selection: { ...previewInput.selection,
      dataRevision: 2, rowKeys: [1], querySnapshot: { ...previewInput.selection.querySnapshot, dataRevision: 2 },
    } });
    s.reply(s.posted.at(-1)!, "table.pastePreviewReady", { collection: "u", token: { token: input.token, expiresAt: 100, consumed: false }, overflow: false }); await flush();
    s.paste.apply(input); const apply = s.posted.at(-1)!;
    const conflict = async () => {
      s.reply(apply, "table.pasteApplied", pasteResult({ outcome: "conflict", createdRowKeys: [], updatedRowKeys: [], conflicts: [{ rowKey: 1, currentValue: { name: "old" } }] })); await flush();
    };
    const finishUndo = async () => { s.reply(inverse, "table.editCommitted", edit("old", 1, "name", 3)); await undo; };
    if (order === "conflict-first") { await conflict(); await finishUndo(); }
    else { await finishUndo(); expect(s.history.canRedo).toBe(false); await conflict(); }
    expect(s.store.result?.outcome).toBe("conflict"); expect(s.history.writeUncertain).toBe(false);
    expect(s.applied).not.toHaveBeenCalled(); expect(s.history.undoStackSize).toBe(0); expect(s.history.canRedo).toBe(true);
    const redo = s.service.performRedo(); await flush();
    expect(s.posted.at(-1)!.payload).toMatchObject({ rowKey: 1, newValue: "new" });
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("new", 1, "name", 4)); await redo;
    expect(s.history.canRedo).toBe(false); expect(s.history.undoStackSize).toBe(1);
    expect(s.posted.filter((request) => request.type === "table.applyPasteRequested")).toHaveLength(1);
  });

  it("review16 all-skip committed paste preserves executable redo and creates no empty history", async () => {
    const s = pasteSetup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.paste.apply(input); expect(s.history.canRedo).toBe(false);
    s.reply(s.posted.at(-1)!, "table.pasteApplied", pasteResult({ createdRowKeys: [], updatedRowKeys: [], skippedRowKeys: [1] })); await flush();
    expect(s.store.phase).toBe("applied"); expect(s.history.undoStackSize).toBe(0); expect(s.history.canRedo).toBe(true);
    const redo = s.service.performRedo(); await flush();
    expect(s.posted.at(-1)!.payload).toMatchObject({ rowKey: 1, newValue: "new" });
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("new", 1, "name", 4)); await redo;
    expect(s.history.undoStackSize).toBe(1); expect(s.history.canRedo).toBe(false);
  });

  it.each(["pending", "error"] as const)("review16 empty paste row arrays cannot prove zero writes for %s", async (outcome) => {
    const s = pasteSetup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.paste.apply(input); const apply = s.posted.at(-1)!;
    const emptyRows = { createdRowKeys: [], updatedRowKeys: [] };
    if (outcome === "pending") s.reply(apply, "table.pasteApplied", pasteResult({ ...emptyRows, outcome }));
    else s.reply(apply, "operation.failed", { ...emptyRows, message: "The paste outcome is unknown." });
    await flush();
    expect(s.history.writeUncertain).toBe(true); expect(s.history.canRedo).toBe(false);
    expect(s.history.undoStackSize).toBe(0); expect(s.applied).not.toHaveBeenCalled();
    const requests = s.posted.length; await s.service.performRedo(); expect(s.posted).toHaveLength(requests);
  });

  it("review15 committed paste without a revision retires overlapping redo and keeps other undo entries", async () => {
    const s = pasteSetup(); await seed(s);
    s.service.updateCell(2, "name", "two", "changed"); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("changed", 2, "name", 3)); await flush();
    s.paste.apply(input); const apply = s.posted.at(-1)!;
    const undo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("two", 2, "name", 4)); await undo;
    expect(s.history.canRedo).toBe(false);
    s.reply(apply, "table.pasteApplied", pasteResult({ createdRowKeys: [], updatedRowKeys: [2] })); await flush();
    expect(s.history.redoStack).toHaveLength(0); expect(s.history.undoStackSize).toBe(2); expect(s.history.writeUncertain).toBe(false);
    await s.service.performUndo(); // Paste only updates existing rows, so its established undo adds no redo.
    const laterUndo = s.service.performUndo(); await flush();
    s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 5)); await laterUndo;
    expect(s.history.canRedo).toBe(true);
  });
  it("review10 notSent paste remains definitely unsent even if history clears synchronously", async () => {
    const s = pasteSetup(); s.postMessage.mockImplementationOnce(() => { s.history.clear(); throw new Error("not posted"); });
    s.paste.apply(input); await flush();
    expect(s.history.writeUncertain).toBe(false); expect(s.store.error).not.toContain("尚未确认");
  });

  it("review10 sent paste timeout after history clear still fences its table", async () => {
    vi.useFakeTimers(); const s = pasteSetup(); s.paste.apply(input); s.history.clear();
    await vi.advanceTimersByTimeAsync(5_000); expect(s.history.writeUncertain).toBe(true);
    const count = s.posted.length; s.paste.apply(input); expect(s.posted).toHaveLength(count);
  });

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

  it("a definitely unsent paste preserves history and reports no unknown commit", async () => {
    const s = pasteSetup(); await seed(s);
    s.postMessage.mockImplementationOnce(() => { throw new Error("paste transport unavailable"); });
    s.paste.apply(input); await flush();
    expect(s.store.error).toContain("paste transport unavailable");
    expect(s.store.error).not.toContain("尚未确认");
    expect(s.history.undoStackSize).toBe(1);
    expect(s.applied).not.toHaveBeenCalled();
  });

  it.each([false, true])("review3 unsent paste restores redo only without a newer sent action (%s)", async (newerAction) => {
    const s = pasteSetup(); await seed(s);
    const undo = s.service.performUndo(); await flush(); s.reply(s.posted.at(-1)!, "table.editCommitted", edit("old", 1, "name", 3)); await undo;
    s.postMessage.mockImplementationOnce(() => { throw new Error("paste not sent"); });
    s.paste.apply(input);
    if (newerAction) s.service.updateCell(2, "name", "two", "sent action");
    await flush(); expect(s.history.canRedo).toBe(!newerAction); expect(s.history.writeUncertain).toBe(false);
    if (newerAction) {
      s.reply(s.posted.at(-1)!, "table.editCommitted", edit("sent action", 2, "name", 4)); await flush();
      expect(s.history.canRedo).toBe(false); expect(s.history.undoStackSize).toBe(1);
    }
  });

  it("review3 disposing a sibling service cannot confirm an unresolved write", async () => {
    const s = pasteSetup(); s.history.markWriteUncertain(); s.paste.dispose();
    expect(s.history.writeUncertain).toBe(true);
    s.service.updateCell(1, "name", "old", "blocked"); expect(s.posted).toHaveLength(0);
    s.service.dispose(); expect(s.history.writeUncertain).toBe(true);
  });

  it("an unknown paste fences an ordinary sibling rejection and further paste admission", async () => {
    const s = pasteSetup();
    s.service.updateCell(1, "name", "old", "pending"); const ordinary = s.posted.at(-1)!;
    s.table.rollbackCellEdit(1, "name", "pending");
    s.paste.apply(input); s.reply(s.posted.at(-1)!, "table.pasteApplied", pasteResult({ outcome: "pending" })); await flush();
    s.reply(ordinary, "table.editRejected", { kind: "edit_conflict", message: "late ordinary failure" }); await flush();
    expect(s.table.allRows[0]?.name).toBe("pending"); expect(s.rejected).not.toHaveBeenCalled();
    s.paste.apply({ ...input, idempotencyKey: "another" }); expect(s.posted).toHaveLength(2);
    expect(s.store.error).toContain("核实"); expect(s.history.writeUncertain).toBe(true);
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
