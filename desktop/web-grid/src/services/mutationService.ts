import { useHostBridge } from "./bridgeContext";
import { useMutationScope } from "./mutationScope";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHistoryStore } from "@/stores/historyStore";
import type {
  ApplyPasteResult, DeleteRowsResult, InsertRowResult, MutationErrorPayload,
  MutationRevision, UpdateCellResult, WebPayloadMap,
} from "@/contracts";

type MutationType = "table.updateCellRequested" | "table.insertRowRequested" | "table.deleteRowsRequested";
type MutationResult = UpdateCellResult | InsertRowResult | DeleteRowsResult;
const UNKNOWN_MESSAGE = "写入结果尚未确认，请刷新并核实权威数据后继续；不要直接重试。";

/** Every local write owns its response, rollback snapshot and history entry. */
export function useMutationService() {
  const bridge = useHostBridge();
  const table = useTableStore();
  const ws = useWorkspaceStore();
  const history = useHistoryStore();
  let editRejectedHandler: ((error: MutationErrorPayload) => void) | undefined;
  let rowInsertedHandler: ((result: InsertRowResult) => void) | undefined;
  const waiting = new Set<() => void>();
  const deferred = new Map<string, { newValue: unknown }>();
  const cellStates = new Map<string, {
    latest: number;
    pending: number;
    baseValue: unknown;
    historyIsCurrent: () => boolean;
    committed: { result: UpdateCellResult; oldValue: unknown }[];
  }>();
  let cellSequence = 0;
  const rowRevisions = new Map<number | string, number>();
  let projectionRevision = table.revision;
  let refreshFloor = table.revision?.dataRevision ?? -1;
  const scope = useMutationScope(() => {
    history.clear();
    for (const cancel of [...waiting]) cancel();
    deferred.clear();
    cellStates.clear();
    rowRevisions.clear();
    projectionRevision = table.revision;
    refreshFloor = table.revision?.dataRevision ?? -1;
  });

  function init(
    onEditRejected?: (error: MutationErrorPayload) => void,
    onRowInserted?: (result: InsertRowResult) => void,
  ): void {
    editRejectedHandler = onEditRejected ?? editRejectedHandler;
    rowInsertedHandler = onRowInserted ?? rowInsertedHandler;
  }

  function report(error: MutationErrorPayload): void {
    if (editRejectedHandler) editRejectedHandler(error);
    else table.setError(error.message);
  }

  function findRowDigest(rowKey: number | string): string | null {
    const value = table.allRows.find((row) => row.rowKey === rowKey)?.__vibetableDigest;
    return typeof value === "string" && /^sha256:[0-9a-f]{64}$/u.test(value) ? value : null;
  }

  function guardedRows(keys: readonly (number | string)[]) {
    return keys.map((rowKey) => {
      const expectedDigest = findRowDigest(rowKey);
      if (!expectedDigest) throw new Error("The rows changed and cannot be replayed safely.");
      return { rowKey, expectedDigest };
    });
  }

  function readyRevision(
    isCurrent: () => boolean,
    expected?: MutationRevision,
  ): Promise<string> {
    const startedAt = Date.now();
    return new Promise((resolve, reject) => {
      let timer: ReturnType<typeof setTimeout> | undefined;
      const finish = (error?: Error, revision?: string): void => {
        clearTimeout(timer);
        waiting.delete(cancel);
        if (error) reject(error);
        else resolve(revision!);
      };
      const cancel = (): void => finish(new Error("The mutation context changed before sending."));
      waiting.add(cancel);
      const check = (): void => {
        if (!isCurrent()) { cancel(); return; }
        const revision = table.revision;
        if (expected && revision?.schemaRevision && revision.schemaRevision !== expected.schemaRevision) {
          finish(new Error("The table schema changed before replaying history."));
          return;
        }
        if (expected && revision?.databaseSessionId
          && revision.databaseSessionId !== expected.databaseSessionId) {
          finish(new Error("The database session changed before replaying history."));
          return;
        }
        if (revision?.schemaRevision && (!expected || revision.databaseSessionId)) {
          finish(undefined, revision.schemaRevision);
        } else if (Date.now() - startedAt >= 5_000) {
          finish(new Error("The table revision did not become ready in time."));
        } else {
          timer = setTimeout(check, 25);
        }
      };
      check();
    });
  }

  async function send<K extends MutationType>(
    type: K,
    payload: WebPayloadMap[K],
    isCurrent: () => boolean,
    normal: boolean,
    onRejected?: (error: MutationErrorPayload) => void,
  ): Promise<MutationResult> {
    if (!isCurrent()) throw new Error("The mutation context changed before sending.");
    // A new action invalidates existing redo at submission, not at late confirmation.
    if (normal) history.beginAction();
    const expectedSession = table.revision?.databaseSessionId;
    let result: unknown;
    try {
      let cancel!: () => void;
      const retired = new Promise<never>((_, reject) => {
        cancel = () => reject(new Error("The mutation context retired after sending."));
        waiting.add(cancel);
      });
      try {
        result = await Promise.race([bridge.request(type, payload), retired]);
      } finally {
        waiting.delete(cancel);
      }
    } catch (error) {
      if (isCurrent()) {
        history.clear();
        report({ kind: "unknown", message: error instanceof Error ? `${error.message} ${UNKNOWN_MESSAGE}` : UNKNOWN_MESSAGE });
      }
      throw new Error(error instanceof Error ? `${error.message} ${UNKNOWN_MESSAGE}` : UNKNOWN_MESSAGE);
    }
    if (!isCurrent()) throw new Error("The mutation context changed after sending.");
    if (typeof result === "object" && result !== null && "kind" in result) {
      const rejection = result as MutationErrorPayload;
      if (rejection.kind === "unknown") {
        history.clear();
        report({ ...rejection, message: UNKNOWN_MESSAGE });
      } else {
        onRejected?.(rejection);
        report(rejection);
      }
      throw new Error(rejection.message);
    }
    const committed = result as MutationResult;
    if (expectedSession && committed.revision.databaseSessionId !== expectedSession) {
      history.clear();
      report({ kind: "unknown", message: UNKNOWN_MESSAGE });
      throw new Error(UNKNOWN_MESSAGE);
    }
    return committed;
  }

  function applyResult(result: MutationResult, push?: () => void, historyIsCurrent: () => boolean = () => true): void {
    const previous = table.revision;
    const schemaChanged = previous && previous.schemaRevision !== result.revision.schemaRevision;
    if (schemaChanged) {
      rowRevisions.clear();
      refreshFloor = -1;
    } else if (previous && previous !== projectionRevision) {
      // External authoritative refreshes cover every row; local commits cover their own rows only.
      refreshFloor = Math.max(refreshFloor, previous.dataRevision);
    }
    const keys = "deletedRowKeys" in result ? result.deletedRowKeys : [result.rowKey];
    const freshKeys = keys.filter((key) => result.revision.dataRevision >= Math.max(
      refreshFloor, rowRevisions.get(key) ?? -1,
    ));
    if (freshKeys.length > 0) {
      if ("storedValue" in result) table.applyCellEdit(result);
      else if ("row" in result) table.applyInsert(result);
      else table.applyDelete({ ...result, deletedRowKeys: freshKeys });
      for (const key of freshKeys) rowRevisions.set(key, result.revision.dataRevision);
    }
    projectionRevision = table.revision;
    if (schemaChanged) history.clear();
    else if (historyIsCurrent()) push?.();
  }
  async function replay<K extends MutationType>(
    type: K,
    payload: Omit<WebPayloadMap[K], "schemaRevision">,
    expected: MutationRevision,
    isCurrent: () => boolean,
  ): Promise<MutationResult> {
    const schemaRevision = await readyRevision(isCurrent, expected);
    const result = await send(type, { ...payload, schemaRevision } as WebPayloadMap[K], isCurrent, false);
    if (!isCurrent()) throw new Error("The mutation context changed before applying its response.");
    applyResult(result);
    return result;
  }

  function cellHistory(r: UpdateCellResult, oldValue: () => unknown, tableId: string, isCurrent: () => boolean): void {
    let dataRevision = r.revision.dataRevision;
    const change = async (from: unknown, to: unknown): Promise<void> => {
      const result = await replay("table.updateCellRequested", {
        table: tableId, rowKey: r.rowKey, column: r.column,
        oldValue: from, newValue: to, expectedDigest: findRowDigest(r.rowKey),
      }, r.revision, isCurrent);
      dataRevision = result.revision.dataRevision;
    };
    history.push({
      id: crypto.randomUUID(), kind: "updateCell", label: `编辑 ${r.column}`, timestamp: Date.now(),
      get dataRevision() { return dataRevision; },
      undo: () => change(r.storedValue, oldValue()), redo: () => change(oldValue(), r.storedValue),
    }, true);
  }

  function insertHistory(r: InsertRowResult, tableId: string, isCurrent: () => boolean): void {
    let rowKey = r.rowKey;
    let dataRevision = r.revision.dataRevision;
    history.push({
      id: crypto.randomUUID(), kind: "insertRow", label: "插入行", timestamp: Date.now(),
      get dataRevision() { return dataRevision; },
      undo: async () => {
        await replay("table.deleteRowsRequested", {
          table: tableId, rows: guardedRows([rowKey]),
        }, r.revision, isCurrent);
      },
      redo: async () => {
        const inserted = await replay("table.insertRowRequested", {
          table: tableId, values: r.row,
        }, r.revision, isCurrent) as InsertRowResult;
        rowKey = inserted.rowKey;
        dataRevision = inserted.revision.dataRevision;
      },
    }, true);
  }

  function updateCell(
    rowKey: number | string, column: string, oldValue: unknown, newValue: unknown,
    expectedDigest: string | null = null,
  ): void {
    const key = JSON.stringify([typeof rowKey, rowKey, column]);
    const existing = deferred.get(key);
    if (existing) { existing.newValue = newValue; return; }
    const sequence = ++cellSequence;
    const previousState = cellStates.get(key);
    const state = previousState?.historyIsCurrent() ? previousState : {
      latest: sequence, pending: 0, baseValue: oldValue, committed: [],
      historyIsCurrent: history.captureGeneration(),
    };
    state.latest = sequence;
    state.pending += 1;
    cellStates.set(key, state);
    const isCurrent = scope.capture();
    const tableId = ws.currentTable ?? "";
    const digest = expectedDigest ?? findRowDigest(rowKey);
    const submit = async (schemaRevision: string, value: unknown): Promise<void> => {
      try {
        const r = await send("table.updateCellRequested", {
          table: tableId, rowKey, column, oldValue, newValue: value,
          expectedDigest: digest, schemaRevision,
        }, isCurrent, true, (error) => {
          if (state.latest !== sequence) return;
          // A conflict row may contain stale values for other pending columns.
          const currentRow = error.currentRow && Object.hasOwn(error.currentRow, column)
            ? { [column]: error.currentRow[column] } : undefined;
          const storedValue = state.committed.length > 0
            ? state.committed.at(-1)!.result.storedValue : state.baseValue;
          table.rollbackCellEdit(rowKey, column, storedValue, currentRow);
        }) as UpdateCellResult;
        if (!isCurrent()) return;
        const committed = { result: r, oldValue };
        state.committed.push(committed);
        state.committed.sort((a, b) => a.result.revision.dataRevision - b.result.revision.dataRevision);
        let before = state.baseValue;
        for (const entry of state.committed) {
          entry.oldValue = before;
          before = entry.result.storedValue;
        }
        applyResult(r, () => cellHistory(r, () => committed.oldValue, tableId, isCurrent), state.historyIsCurrent);
      } finally {
        state.pending -= 1;
        if (state.pending === 0 && cellStates.get(key) === state) cellStates.delete(key);
      }
    };
    if (table.revision?.schemaRevision) {
      void submit(table.revision.schemaRevision, newValue).catch(() => {});
      return;
    }
    const item = { newValue };
    deferred.set(key, item);
    void readyRevision(isCurrent).then((revision) => {
      deferred.delete(key);
      return submit(revision, item.newValue);
    }, (error: Error) => {
      deferred.delete(key);
      if (cellStates.get(key) === state) cellStates.delete(key);
      if (!isCurrent()) return;
      table.rollbackCellEdit(rowKey, column, oldValue);
      report({ kind: "backend_unavailable", message: error.message, conflictingRowKeys: [rowKey] });
    }).catch(() => {});
  }

  function canInsertRow(): boolean {
    return !!ws.currentTable
      && !ws.collections.some((collection) => collection.collection === ws.currentTable
        && collection.metadata?.kind === "view")
      && !!table.revision?.schemaRevision
      && !!table.editSchema?.some((column) => column.editable || column.dataType === "autoNumber");
  }

  function insertRow(values: Readonly<Record<string, unknown>>): void {
    if (!canInsertRow()) return;
    const isCurrent = scope.capture();
    const tableId = ws.currentTable ?? "";
    const historyIsCurrent = history.captureGeneration();
    void send("table.insertRowRequested", {
      table: tableId, values, schemaRevision: table.revision!.schemaRevision,
    }, isCurrent, true).then((result) => {
      if (!isCurrent()) return;
      const r = result as InsertRowResult;
      applyResult(r, () => insertHistory(r, tableId, isCurrent), historyIsCurrent);
      rowInsertedHandler?.(r);
    }).catch(() => {});
  }

  function deleteRows(rows: readonly DeleteRowReqItem[]): void {
    const tableId = ws.currentTable ?? "";
    const isCurrent = scope.capture();
    const snapshot = table.snapshotRows(rows.map((row) => row.rowKey));
    const historyIsCurrent = history.captureGeneration();
    void send("table.deleteRowsRequested", {
      table: tableId, rows, schemaRevision: table.revision?.schemaRevision ?? "",
    }, isCurrent, true).then((result) => {
      if (!isCurrent()) return;
      const r = result as DeleteRowsResult;
      let restoredKeys = [...r.deletedRowKeys];
      let dataRevision = r.revision.dataRevision;
      applyResult(r, () => history.push({
        id: crypto.randomUUID(), kind: "deleteRows", label: `删除 ${r.deletedRowKeys.length} 行`, timestamp: Date.now(),
        get dataRevision() { return dataRevision; },
        undo: async () => {
          const insertedKeys: (number | string)[] = [];
          try {
            for (const row of snapshot) {
              const inserted = await replay("table.insertRowRequested", {
                table: tableId, values: row,
              }, r.revision, isCurrent) as InsertRowResult;
              insertedKeys.push(inserted.rowKey);
            }
            restoredKeys = insertedKeys;
          } catch (error) {
            // Partial multi-row undo has committed writes and is never safe to replay wholesale.
            if (insertedKeys.length > 0 && isCurrent()) history.clear();
            throw error;
          }
        },
        redo: async () => {
          const deleted = await replay("table.deleteRowsRequested", {
            table: tableId, rows: guardedRows(restoredKeys),
          }, r.revision, isCurrent);
          dataRevision = deleted.revision.dataRevision;
        },
      }, true), historyIsCurrent);
    }).catch(() => {});
  }

  function recordPasteResult(r: ApplyPasteResult): void {
    if (r.collection !== ws.currentTable) return;
    const isCurrent = scope.capture();
    const revision = table.revision;
    history.push({
      id: crypto.randomUUID(), kind: "applyPaste", label: "粘贴", timestamp: Date.now(),
      undo: async () => {
        if (r.createdRowKeys.length === 0) return;
        if (!revision) throw new Error("The table revision was unavailable when the paste completed.");
        await replay("table.deleteRowsRequested", {
          table: r.collection, rows: guardedRows(r.createdRowKeys),
        }, revision, isCurrent);
      },
    }, true);
  }

  return {
    init, updateCell, canInsertRow, insertRow, deleteRows, recordPasteResult,
    performUndo: () => history.undo(), performRedo: () => history.redo(), dispose: scope.dispose,
  };
}

interface DeleteRowReqItem {
  readonly rowKey: number | string;
  readonly expectedDigest: string;
}
