import { useHostBridge } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHistoryStore } from "@/stores/historyStore";
import type {
  ApplyPasteResult,
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
   * Feedback-loop guard: while true, the NEXT matching inbound mutation result
   * still applies to the store (and runs the schema-clear check) but skips
   * pushing a new history entry, then CONSUMES the flag (sets it back to
   * false).
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
   * Stale-flag recovery: if the host never replies (it fails the operation),
   * no editCommitted/rowsInserted/rowsDeleted/pasteApplied ever arrives. The
   * `operation.failed` subscription below clears the flag so the NEXT user
   * edit is not wrongly suppressed. As a defensive backstop, a 5s timer also
   * clears it (covers a host that neither confirms nor fails — should not
   * happen, but a stuck suppressHistory would silently swallow every edit
   * afterward).
   *
   * Module-level inside the closure so it is shared across all inbound
   * handlers of this service instance.
   */
  let suppressHistory = false;
  /** Defensive backstop timer that clears a stuck `suppressHistory`. */
  let suppressHistoryTimer: ReturnType<typeof setTimeout> | null = null;
  /** How long the suppress guard may stay up before being force-cleared. */
  const SUPPRESS_HISTORY_TIMEOUT_MS = 5_000;

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
    // Capture BEFORE apply: this inbound result is the one expected from an
    // undo/redo round-trip if and only if the guard is up. We consume the
    // flag (clear it) when we see it — one undo/redo yields exactly one
    // inbound result.
    const wasSuppressing = suppressHistory;
    apply();
    if (prevSchemaRev !== null && prevSchemaRev !== newSchemaRev) {
      // Schema changed across this mutation — undo/redo is no longer safe.
      // NOTE: schema-clear intentionally runs even while suppressing, because
      // a real schema change during a re-notification must still invalidate
      // history. We still consume the suppress flag below.
      history.clear();
      if (wasSuppressing) clearSuppressHistory();
    } else if (wasSuppressing) {
      // Undo/redo round-trip confirmation: apply already ran above, just drop
      // the guard without pushing a duplicate entry.
      clearSuppressHistory();
    } else {
      pushEntry();
    }
  }

  /**
   * Drop the suppress guard and cancel its backstop timer. Called when an
   * inbound mutation result arrives (the round-trip completed) OR when
   * `operation.failed` arrives (the round-trip will never complete).
   */
  function clearSuppressHistory(): void {
    suppressHistory = false;
    if (suppressHistoryTimer !== null) {
      clearTimeout(suppressHistoryTimer);
      suppressHistoryTimer = null;
    }
  }

  /**
   * Arm the suppress guard and a defensive 5s backstop. The backstop ensures
   * that if the host neither confirms nor fails the operation, a stuck guard
   * does not silently swallow every subsequent user edit.
   */
  function armSuppressHistory(): void {
    clearSuppressHistory();
    suppressHistory = true;
    suppressHistoryTimer = setTimeout(() => {
      suppressHistoryTimer = null;
      suppressHistory = false;
    }, SUPPRESS_HISTORY_TIMEOUT_MS);
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

    bridge.on("table.pasteApplied", (r: ApplyPasteResult) => {
      // ApplyPasteResult only carries createdRowKeys / updatedRowKeys /
      // skippedRowKeys (keys only, NO full row data) — there is not enough
      // information here to mutate tableStore directly. The host emits a
      // subsequent `table.datasetReady` (or pageLoaded) refresh after a paste,
      // which is what actually updates the store's rows. So this handler only
      // owns the history entry. Skip the apply/store update and skip the schema
      // clear path (no revision on ApplyPasteResult); push directly.
      //
      // Suppress-consume: if this paste confirmation is the inbound result of
      // a redo (paste redo is a no-op, but defensively), drop the guard
      // without pushing a duplicate entry.
      if (suppressHistory) {
        clearSuppressHistory();
        return;
      }
      const created = r.createdRowKeys as readonly (number | string)[];
      const createdCount = created.length;
      history.push({
        id: crypto.randomUUID(),
        kind: "applyPaste",
        label: "粘贴",
        timestamp: Date.now(),
        undo: async () => {
          // Undo a paste by deleting any created rows. Updated rows are
          // NOT reverted here (their pre-paste values are not available on the
          // web side; the host would need a restore primitive). expectedDigest
          // is required by the wire contract but ignored by the backend; "" is
          // a stable filler that mirrors the insertRow undo closure above.
          if (created.length === 0) return;
          bridge.notify("table.deleteRowsRequested", {
            table: ws.currentTable ?? "",
            rows: created.map((k) => ({ rowKey: k, expectedDigest: "" })),
            schemaRevision: currentSchemaRev(),
          });
        },
        redo: async () => {
          // Paste tokens are single-use: the host consumed `token` during the
          // original apply and will reject a replay. We cannot re-issue the
          // paste without re-running the preview flow, so redo is a no-op.
          // Keeping a closure (rather than throwing) means the entry cleanly
          // returns to the redo stack without surfacing an error toast.
          void createdCount;
        },
      });
    });

    // Stale-guard recovery: if an undo/redo's re-notification fails on the
    // host side, the host broadcasts `operation.failed` (handled centrally by
    // errorRouter) instead of any editCommitted/rowsInserted/rowsDeleted/
    // pasteApplied confirmation. Without this subscription, suppressHistory
    // would stay up forever (until the 5s backstop fires) and silently
    // swallow the next user edit. Drop the guard here so the next genuine
    // user edit is recorded normally.
    bridge.on("operation.failed", () => {
      if (suppressHistory) clearSuppressHistory();
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

  /**
   * Run `history.undo()` with the consume-on-inbound suppress guard armed.
   *
   * The undo closure re-notifies the host (e.g. `updateCellRequested` with
   * `oldValue`). The notify returns immediately (postMessage is sync from the
   * web side), so `await history.undo()` resolves before the C# host has even
   * processed the request. The host emits `table.editCommitted`
   * ASYNCHRONOUSLY later — by which point a time/await-based guard would have
   * already flipped back off, and the inbound handler would push a duplicate
   * entry and clear the redo stack.
   *
   * Instead we arm the guard here and leave it up. The matching inbound
   * mutation handler consumes it (see `applyAndMaybeClear` /
   * `pasteApplied`). If the host fails instead, the `operation.failed`
   * subscription drops the guard. A 5s backstop also clears it as a last
   * resort so a stuck guard cannot silently swallow future edits.
   */
  async function performUndo(): Promise<void> {
    armSuppressHistory();
    await history.undo();
  }

  /** Redo counterpart to {@link performUndo}. */
  async function performRedo(): Promise<void> {
    armSuppressHistory();
    await history.redo();
  }

  return { init, updateCell, insertRow, deleteRows, performUndo, performRedo };
}

/** Local shape mirror of `DeleteRowRequestItem` to keep the public surface typed. */
interface DeleteRowReqItem {
  readonly rowKey: number | string;
  readonly expectedDigest: string;
}
