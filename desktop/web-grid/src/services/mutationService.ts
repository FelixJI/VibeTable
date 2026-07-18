import { useHostBridge } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHistoryStore } from "@/stores/historyStore";
import type {
  DeleteRowsResult,
  InsertRowResult,
  UpdateCellResult,
} from "@/contracts";

/**
 * mutationService — the single mutation entry point for the web layer.
 *
 * Responsibilities:
 *   (a) Outbound: send `table.updateCellRequested` / `table.insertRowRequested`
 *       / `table.deleteRowsRequested` notifications (fire-and-forget; the host
 *       broadcasts the outcome via the inbound events below).
 *   (b) Inbound: subscribe to `table.editCommitted` / `table.rowsInserted` /
 *       `table.rowsDeleted` and apply each result to `tableStore`.
 *   (c) History: push an undo/redo `HistoryEntry` producer for each applied
 *       mutation. The undo closures re-notify the host with the inverse
 *       operation; they do NOT mutate the store directly (the host's resulting
 *       inbound event does).
 *
 * Schema-clear nuance: when an inbound result carries a different
 * `schemaRevision` than the one currently in `tableStore.revision`, the entire
 * undo/redo history is cleared — undoing across a schema change is unsafe
 * (columns/dataTypes may no longer match). The ORDERING here is critical:
 * `applyCellEdit`/`applyInsert`/`applyDelete` UPDATE `tableStore.revision` to
 * the new revision, so we must capture `prevSchemaRev` BEFORE calling apply,
 * then compare against the result's new schemaRevision AFTER.
 *
 * deleteRows snapshot caching: before sending `deleteRowsRequested`, we snapshot
 * the full row data via `tableStore.snapshotRows` and cache it keyed by
 * `JSON.stringify(deletedRowKeys)`. When the matching `rowsDeleted` inbound
 * event arrives, we retrieve the snapshot and embed it in the undo closure so
 * the user can restore the deleted rows.
 */
export function useMutationService(): {
  init: () => void;
  updateCell: (
    rowKey: number | string,
    column: string,
    oldValue: unknown,
    newValue: unknown,
  ) => void;
  insertRow: (values: Readonly<Record<string, unknown>>) => void;
  deleteRows: (rows: readonly DeleteRowReqItem[]) => void;
} {
  const bridge = useHostBridge();
  const table = useTableStore();
  const ws = useWorkspaceStore();
  const history = useHistoryStore();

  /** Caches row snapshots for pending deleteRows, keyed by stringified keys. */
  const pendingDeleteSnapshot = new Map<string, Record<string, unknown>[]>();

  function currentSchemaRev(): string {
    return table.revision?.schemaRevision ?? "";
  }

  /**
   * Apply a mutation result to the store and decide whether to push a history
   * entry or clear the stack. The `apply` callback MUST mutate the store (and
   * update `tableStore.revision`); `pushEntry` runs only when the schema is
   * unchanged (so the entry is safe to undo/redo).
   *
   * Ordering: read `prevSchemaRev` BEFORE `apply()` runs, because `apply()`
   * overwrites `tableStore.revision` with the result's revision. If we read it
   * after, prev and new would always match and history would never clear.
   */
  function applyAndMaybeClear(
    newSchemaRev: string,
    apply: () => void,
    pushEntry: () => void,
  ): void {
    const prevSchemaRev = table.revision?.schemaRevision ?? null;
    apply();
    if (prevSchemaRev !== null && prevSchemaRev !== newSchemaRev) {
      // Schema changed across this mutation — undo/redo is no longer safe.
      history.clear();
    } else {
      pushEntry();
    }
  }

  function init(): void {
    bridge.on("table.editCommitted", (r: UpdateCellResult) => {
      // Capture the old value BEFORE applying (apply overwrites the row).
      const oldValue = findCellValue(r.rowKey, r.column);
      applyAndMaybeClear(
        r.revision.schemaRevision,
        () => table.applyCellEdit(r),
        () => {
          history.push({
            id: crypto.randomUUID(),
            kind: "updateCell",
            label: `编辑 ${r.column}`,
            timestamp: Date.now(),
            undo: async () => {
              bridge.notify("table.updateCellRequested", {
                table: ws.currentTable ?? "",
                rowKey: r.rowKey,
                column: r.column,
                oldValue: r.storedValue,
                newValue: oldValue,
                schemaRevision: currentSchemaRev(),
              });
            },
            redo: async () => {
              bridge.notify("table.updateCellRequested", {
                table: ws.currentTable ?? "",
                rowKey: r.rowKey,
                column: r.column,
                oldValue: oldValue,
                newValue: r.storedValue,
                schemaRevision: currentSchemaRev(),
              });
            },
          });
        },
      );
    });

    bridge.on("table.rowsInserted", (r: InsertRowResult) => {
      applyAndMaybeClear(
        r.revision.schemaRevision,
        () => table.applyInsert(r),
        () => {
          history.push({
            id: crypto.randomUUID(),
            kind: "insertRow",
            label: "插入行",
            timestamp: Date.now(),
            undo: async () => {
              bridge.notify("table.deleteRowsRequested", {
                table: ws.currentTable ?? "",
                rows: [{ rowKey: r.rowKey, expectedDigest: "" }],
                schemaRevision: currentSchemaRev(),
              });
            },
            redo: async () => {
              bridge.notify("table.insertRowRequested", {
                table: ws.currentTable ?? "",
                values: r.row,
                schemaRevision: currentSchemaRev(),
              });
            },
          });
        },
      );
    });

    bridge.on("table.rowsDeleted", (r: DeleteRowsResult) => {
      // Retrieve (and drop) the cached snapshot. Keyed by stringified keys in
      // arrival order of the original deleteRows call.
      const key = JSON.stringify(r.deletedRowKeys);
      const snapshot = pendingDeleteSnapshot.get(key) ?? [];
      pendingDeleteSnapshot.delete(key);
      applyAndMaybeClear(
        r.revision.schemaRevision,
        () => table.applyDelete(r),
        () => {
          history.push({
            id: crypto.randomUUID(),
            kind: "deleteRows",
            label: `删除 ${r.deletedRowKeys.length} 行`,
            timestamp: Date.now(),
            undo: async () => {
              for (const row of snapshot) {
                bridge.notify("table.insertRowRequested", {
                  table: ws.currentTable ?? "",
                  values: row,
                  schemaRevision: currentSchemaRev(),
                });
              }
            },
            redo: async () => {
              bridge.notify("table.deleteRowsRequested", {
                table: ws.currentTable ?? "",
                rows: r.deletedRowKeys.map((k) => ({
                  rowKey: k,
                  expectedDigest: "",
                })),
                schemaRevision: currentSchemaRev(),
              });
            },
          });
        },
      );
    });
  }

  /** Look up the current stored value of (rowKey, column) in tableStore. */
  function findCellValue(
    rowKey: number | string,
    column: string,
  ): unknown {
    for (const row of table.allRows) {
      if (row.rowKey === rowKey) {
        return row[column];
      }
    }
    return undefined;
  }

  function updateCell(
    rowKey: number | string,
    column: string,
    oldValue: unknown,
    newValue: unknown,
  ): void {
    bridge.notify("table.updateCellRequested", {
      table: ws.currentTable ?? "",
      rowKey,
      column,
      oldValue,
      newValue,
      schemaRevision: currentSchemaRev(),
    });
  }

  function insertRow(values: Readonly<Record<string, unknown>>): void {
    bridge.notify("table.insertRowRequested", {
      table: ws.currentTable ?? "",
      values,
      schemaRevision: currentSchemaRev(),
    });
  }

  function deleteRows(rows: readonly DeleteRowReqItem[]): void {
    // Cache the row snapshot BEFORE sending (the data is still in the store
    // now; once the host confirms the delete the rows will be gone). Keyed by
    // the deleted rowKeys so the inbound rowsDeleted handler can retrieve it.
    const keys = rows.map((r) => r.rowKey);
    const snapshot = table.snapshotRows(keys);
    pendingDeleteSnapshot.set(JSON.stringify(keys), snapshot);
    bridge.notify("table.deleteRowsRequested", {
      table: ws.currentTable ?? "",
      rows,
      schemaRevision: currentSchemaRev(),
    });
  }

  return { init, updateCell, insertRow, deleteRows };
}

/** Local shape mirror of `DeleteRowRequestItem` to keep the public surface typed. */
interface DeleteRowReqItem {
  readonly rowKey: number | string;
  readonly expectedDigest: string;
}
