import { useHostBridge } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHistoryStore } from "@/stores/historyStore";
import type {
  ApplyPasteResult,
  DeleteRowsResult,
  InsertRowResult,
  MutationErrorPayload,
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
  init: (onEditRejected?: (error: MutationErrorPayload) => void) => void;
  updateCell: (
    rowKey: number | string,
    column: string,
    oldValue: unknown,
    newValue: unknown,
    expectedDigest?: string | null,
  ) => void;
  insertRow: (values: Readonly<Record<string, unknown>>) => void;
  deleteRows: (rows: readonly DeleteRowReqItem[]) => void;
  /**
   * Undo the latest entry with the feedback-loop guard active: while the
   * undo closure runs (it re-notifies the host), inbound `editCommitted` /
   * `rowsInserted` / `rowsDeleted` / `pasteApplied` events are NOT pushed onto
   * the history stack — otherwise the host's confirmation would push a NEW
   * entry that clears the redo stack, breaking redo. Use this (not
   * `history.undo()` directly) from the keyboard shortcut handler.
   */
  performUndo: () => Promise<void>;
  /** Redo counterpart to {@link performUndo}. */
  performRedo: () => Promise<void>;
} {
  const bridge = useHostBridge();
  const table = useTableStore();
  const ws = useWorkspaceStore();
  const history = useHistoryStore();

  /**
   * A pending history round-trip applies the matching inbound mutation result
   * to the store without pushing a duplicate entry. Its Promise resolves only
   * after host confirmation, so historyStore moves stacks at the truthful
   * commit boundary and can restore the entry when the host rejects it.
   *
   * Lifecycle (consume-on-inbound, NOT time/await based):
   *   1. `performUndo`/`performRedo` set this to true BEFORE calling
   *      `history.undo()`/`redo()`. They do NOT clear it in a finally block.
   *   2. The undo/redo closure re-notifies the host (e.g.
   *      `updateCellRequested` with `oldValue`). The notify round-trip is
   *      synchronous from the web side, but the C# host processes the request
   *      ASYNCHRONOUSLY and emits `editCommitted` / `rowsInserted` /
   *      `rowsDeleted` / `pasteApplied` LATER — after `await history.undo()`
   *      has already resolved. So the flag MUST stay up across that async gap.
   *   3. When the matching inbound result arrives, the handler applies the
   *      change to the store, SKIPS `history.push`, and sets the flag back to
   *      false (one undo/redo == one outbound request == one inbound result).
   *
   * `table.editRejected`/`operation.failed` reject the Promise, while a 5s
   * timeout prevents an unresponsive host from locking history indefinitely.
   *
   * Module-level inside the closure so it is shared across all inbound
   * handlers of this service instance.
   */
  type HistoryResultType =
    | "table.editCommitted"
    | "table.rowsInserted"
    | "table.rowsDeleted";
  interface PendingHistoryRoundTrip {
    readonly expectedType: HistoryResultType;
    readonly resolve: () => void;
    readonly reject: (error: Error) => void;
    readonly timer: ReturnType<typeof setTimeout>;
  }
  let pendingHistoryRoundTrip: PendingHistoryRoundTrip | null = null;
  const HISTORY_ROUND_TRIP_TIMEOUT_MS = 5_000;

  /** Caches row snapshots for pending deleteRows, keyed by stringified keys. */
  const pendingDeleteSnapshot = new Map<string, Record<string, unknown>[]>();
  const pendingCellEdits: Array<{
    readonly rowKey: number | string;
    readonly column: string;
    readonly oldValue: unknown;
  }> = [];
  let editRejectedHandler:
    | ((error: MutationErrorPayload) => void)
    | undefined;

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
    resultType: HistoryResultType,
    newSchemaRev: string,
    apply: () => void,
    pushEntry: () => void,
  ): void {
    const prevSchemaRev = table.revision?.schemaRevision ?? null;
    const isHistoryConfirmation =
      pendingHistoryRoundTrip?.expectedType === resultType;
    apply();
    if (prevSchemaRev !== null && prevSchemaRev !== newSchemaRev) {
      // Schema changed across this mutation — undo/redo is no longer safe.
      // NOTE: schema-clear intentionally runs even while suppressing, because
      // a real schema change during a re-notification must still invalidate
      // history.
      history.clear();
    } else if (!isHistoryConfirmation) {
      pushEntry();
    }
    if (isHistoryConfirmation) resolveHistoryRoundTrip();
  }

  function resolveHistoryRoundTrip(): void {
    const pending = pendingHistoryRoundTrip;
    if (!pending) return;
    pendingHistoryRoundTrip = null;
    clearTimeout(pending.timer);
    pending.resolve();
  }

  function rejectHistoryRoundTrip(message: string): void {
    const pending = pendingHistoryRoundTrip;
    if (!pending) return;
    pendingHistoryRoundTrip = null;
    clearTimeout(pending.timer);
    pending.reject(new Error(message));
  }

  function runHistoryRoundTrip(
    expectedType: HistoryResultType,
    send: () => void,
  ): Promise<void> {
    if (pendingHistoryRoundTrip) {
      return Promise.reject(new Error("Another undo or redo is still pending."));
    }
    return new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        rejectHistoryRoundTrip("The host did not confirm the undo or redo.");
      }, HISTORY_ROUND_TRIP_TIMEOUT_MS);
      pendingHistoryRoundTrip = { expectedType, resolve, reject, timer };
      try {
        send();
      } catch (error) {
        rejectHistoryRoundTrip(
          error instanceof Error ? error.message : "Unable to send undo or redo.",
        );
      }
    });
  }

  function init(
    onEditRejected?: (error: MutationErrorPayload) => void,
  ): void {
    editRejectedHandler = onEditRejected ?? editRejectedHandler;
    bridge.on("table.editRejected", (error: MutationErrorPayload) => {
      const pending = takeRejectedCellEdit(error);
      if (pending) {
        table.rollbackCellEdit(
          pending.rowKey,
          pending.column,
          pending.oldValue,
          error.currentRow,
        );
      }
      editRejectedHandler?.(error);
      rejectHistoryRoundTrip(error.message);
    });

    bridge.on("table.editCommitted", (r: UpdateCellResult) => {
      // Capture the old value BEFORE applying (apply overwrites the row).
      const pending = takePendingCellEdit(r.rowKey, r.column);
      const oldValue = pending?.oldValue ?? findCellValue(r.rowKey, r.column);
      applyAndMaybeClear(
        "table.editCommitted",
        r.revision.schemaRevision,
        () => table.applyCellEdit(r),
        () => {
          history.push({
            id: crypto.randomUUID(),
            kind: "updateCell",
            label: `编辑 ${r.column}`,
            timestamp: Date.now(),
            undo: async () => {
              await runHistoryRoundTrip("table.editCommitted", () => {
                bridge.notify("table.updateCellRequested", {
                  table: ws.currentTable ?? "",
                  rowKey: r.rowKey,
                  column: r.column,
                  oldValue: r.storedValue,
                  newValue: oldValue,
                  expectedDigest: findRowDigest(r.rowKey),
                  schemaRevision: currentSchemaRev(),
                });
              });
            },
            redo: async () => {
              await runHistoryRoundTrip("table.editCommitted", () => {
                bridge.notify("table.updateCellRequested", {
                  table: ws.currentTable ?? "",
                  rowKey: r.rowKey,
                  column: r.column,
                  oldValue: oldValue,
                  newValue: r.storedValue,
                  expectedDigest: findRowDigest(r.rowKey),
                  schemaRevision: currentSchemaRev(),
                });
              });
            },
          });
        },
      );
    });

    bridge.on("table.rowsInserted", (r: InsertRowResult) => {
      applyAndMaybeClear(
        "table.rowsInserted",
        r.revision.schemaRevision,
        () => table.applyInsert(r),
        () => {
          history.push({
            id: crypto.randomUUID(),
            kind: "insertRow",
            label: "插入行",
            timestamp: Date.now(),
            undo: async () => {
              const expectedDigest = findRowDigest(r.rowKey);
              if (!expectedDigest) {
                throw new Error("The row changed and cannot be undone safely.");
              }
              await runHistoryRoundTrip("table.rowsDeleted", () => {
                bridge.notify("table.deleteRowsRequested", {
                  table: ws.currentTable ?? "",
                  rows: [{ rowKey: r.rowKey, expectedDigest }],
                  schemaRevision: currentSchemaRev(),
                });
              });
            },
            redo: async () => {
              await runHistoryRoundTrip("table.rowsInserted", () => {
                bridge.notify("table.insertRowRequested", {
                  table: ws.currentTable ?? "",
                  values: r.row,
                  schemaRevision: currentSchemaRev(),
                });
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
        "table.rowsDeleted",
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
                await runHistoryRoundTrip("table.rowsInserted", () => {
                  bridge.notify("table.insertRowRequested", {
                    table: ws.currentTable ?? "",
                    values: row,
                    schemaRevision: currentSchemaRev(),
                  });
                });
              }
            },
            redo: async () => {
              const guardedRows = r.deletedRowKeys.flatMap((rowKey) => {
                const expectedDigest = findRowDigest(rowKey);
                return expectedDigest ? [{ rowKey, expectedDigest }] : [];
              });
              if (guardedRows.length !== r.deletedRowKeys.length) {
                throw new Error("The rows changed and cannot be redone safely.");
              }
              await runHistoryRoundTrip("table.rowsDeleted", () => {
                bridge.notify("table.deleteRowsRequested", {
                  table: ws.currentTable ?? "",
                  rows: guardedRows,
                  schemaRevision: currentSchemaRev(),
                });
              });
            },
          });
        },
      );
    });

    bridge.on("table.pasteApplied", (r: ApplyPasteResult) => {
      // ApplyPasteResult only carries createdRowKeys / updatedRowKeys /
      // skippedRowKeys (keys only, NO full row data) — there is not enough
      // information here to mutate tableStore directly. The host emits a
      // subsequent `table.datasetReady` (or pageLoaded) refresh after a paste,
      // which is what actually updates the store's rows. So this handler only
      // owns the history entry. Skip the apply/store update and skip the schema
      // clear path (no revision on ApplyPasteResult); push directly.
      //
      const created = r.createdRowKeys as readonly (number | string)[];
      history.push({
        id: crypto.randomUUID(),
        kind: "applyPaste",
        label: "粘贴",
        timestamp: Date.now(),
        undo: async () => {
          // Undo a paste by deleting any created rows. Updated rows are
          // NOT reverted here (their pre-paste values are not available on the
          // web side; the refreshed authoritative rows do carry QueryPort
          // digests, so capture those immediately before the guarded delete.
          if (created.length === 0) return;
          const guardedRows = created.flatMap((rowKey) => {
            const expectedDigest = findRowDigest(rowKey);
            return expectedDigest ? [{ rowKey, expectedDigest }] : [];
          });
          if (guardedRows.length !== created.length) {
            throw new Error("The pasted rows changed and cannot be undone safely.");
          }
          await runHistoryRoundTrip("table.rowsDeleted", () => {
            bridge.notify("table.deleteRowsRequested", {
              table: ws.currentTable ?? "",
              rows: guardedRows,
              schemaRevision: currentSchemaRev(),
            });
          });
        },
      });
    });

    // A host-side failure rejects the pending history Promise. historyStore
    // then restores the entry to its original stack for a truthful retry.
    bridge.on("operation.failed", (failure) => {
      const message =
        typeof failure === "object"
        && failure !== null
        && "message" in failure
        && typeof failure.message === "string"
          ? failure.message
          : "The host rejected the undo or redo.";
      rejectHistoryRoundTrip(message);
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

  function findRowDigest(rowKey: number | string): string | null {
    const row = table.allRows.find((item) => item.rowKey === rowKey);
    const value = row?.__vibetableDigest;
    return typeof value === "string" && /^sha256:[0-9a-f]{64}$/u.test(value)
      ? value
      : null;
  }

  function updateCell(
    rowKey: number | string,
    column: string,
    oldValue: unknown,
    newValue: unknown,
    expectedDigest: string | null = null,
  ): void {
    pendingCellEdits.push({ rowKey, column, oldValue });
    bridge.notify("table.updateCellRequested", {
      table: ws.currentTable ?? "",
      rowKey,
      column,
      oldValue,
      newValue,
      expectedDigest: expectedDigest ?? findRowDigest(rowKey),
      schemaRevision: currentSchemaRev(),
    });
  }

  function takePendingCellEdit(
    rowKey: number | string,
    column: string,
  ): { readonly rowKey: number | string; readonly column: string; readonly oldValue: unknown } | undefined {
    const index = pendingCellEdits.findIndex(
      (pending) => pending.rowKey === rowKey && pending.column === column,
    );
    if (index < 0) return undefined;
    return pendingCellEdits.splice(index, 1)[0];
  }

  function takeRejectedCellEdit(
    error: MutationErrorPayload,
  ): { readonly rowKey: number | string; readonly column: string; readonly oldValue: unknown } | undefined {
    const conflicts = new Set(error.conflictingRowKeys ?? []);
    const index = conflicts.size > 0
      ? pendingCellEdits.findIndex((pending) => conflicts.has(pending.rowKey))
      : 0;
    if (index < 0 || pendingCellEdits.length === 0) return undefined;
    return pendingCellEdits.splice(index, 1)[0];
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

  /**
   * Run `history.undo()`. Each mutation closure waits for the matching host
   * confirmation before this Promise resolves.
   */
  async function performUndo(): Promise<void> {
    await history.undo();
  }

  /** Redo counterpart to {@link performUndo}. */
  async function performRedo(): Promise<void> {
    await history.redo();
  }

  return { init, updateCell, insertRow, deleteRows, performUndo, performRedo };
}

/** Local shape mirror of `DeleteRowRequestItem` to keep the public surface typed. */
interface DeleteRowReqItem {
  readonly rowKey: number | string;
  readonly expectedDigest: string;
}
