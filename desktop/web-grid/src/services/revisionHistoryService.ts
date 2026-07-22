import type {
  HistoryApplyRestorePayload,
  HistoryPreviewRestorePayload,
  HistoryQueryPayload,
  OperationFailedPayload,
} from "@/contracts";
import { useRevisionHistoryStore, type OpenRevisionHistorySelection } from "@/stores/revisionHistoryStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHostBridge } from "./bridgeContext";

const HISTORY_FAILURE_CODES = new Set([
  "history_not_allowed",
  "history_field_unreadable",
  "archive_not_supported",
  "restore_not_allowed",
  "restore_token_unknown",
  "restore_token_expired",
  "restore_scope_mismatch",
  "restore_conflict",
  "schema_drift",
  "restore_no_fields",
  "target_revision_invalid",
  "relation_target_unavailable",
  "revision_not_created",
  "history_cancelled",
  "history_backend_unavailable",
  "history_query_failed",
  "history_preview_failed",
  "history_apply_failed",
  "history_operation_failed",
]);

export interface RestoreTarget {
  readonly itemId: string;
  readonly revisionId: string;
  readonly field?: string | null;
}

/** Typed adapter for persistent server revision history (never the undo store). */
export function useRevisionHistoryService(): {
  init: () => void;
  open: (selection: OpenRevisionHistorySelection | { scope: "archived" }) => void;
  close: () => void;
  refresh: () => void;
  loadMore: () => void;
  previewRestore: (target: RestoreTarget) => void;
  applyRestore: () => void;
} {
  const bridge = useHostBridge();
  const store = useRevisionHistoryStore();
  const workspace = useWorkspaceStore();
  let initialized = false;

  function init(): void {
    if (initialized) return;
    initialized = true;
    bridge.on("history.pageLoaded", (payload) => {
      const append = store.phase === "loadingMore";
      store.receivePage(payload, append);
    });
    bridge.on("history.restorePreviewReady", (payload) => {
      store.receivePreview(payload);
    });
    bridge.on("history.restoreApplied", (payload) => {
      store.completeRestore(payload);
    });
    bridge.on("operation.failed", (payload) => routeFailure(payload));
  }

  function routeFailure(payload: OperationFailedPayload): void {
    const code = payload.code ?? null;
    if (code && !HISTORY_FAILURE_CODES.has(code)) return;
    if (store.restorePhase !== "idle") {
      store.failRestore(payload.message, code);
      return;
    }
    if (store.phase === "loading" || store.phase === "loadingMore") {
      store.failLoad(payload.message, code);
    }
  }

  function open(selection: OpenRevisionHistorySelection | { scope: "archived" }): void {
    store.open(selection);
    query(false);
  }

  function close(): void {
    store.close();
  }

  function query(append: boolean): void {
    const collection = workspace.currentTable;
    if (!collection) {
      store.failLoad("请先选择一张数据表");
      return;
    }
    store.beginLoad(append);
    const filters = store.query;
    const payload: HistoryQueryPayload = {
      collection,
      scope: store.scope,
      itemId: store.itemId ?? undefined,
      field: store.field ?? (filters.field || undefined),
      search: filters.search.trim() || undefined,
      dateFrom: filters.dateFrom || undefined,
      dateTo: filters.dateTo || undefined,
      actorId: filters.actorId.trim() || undefined,
      actions: filters.actions.length ? [...filters.actions] : undefined,
      recordId: filters.recordId.trim() || undefined,
      limit: store.limit,
      offset: append ? store.offset : 0,
    };
    bridge.notify("history.queryRequested", payload);
  }

  function refresh(): void {
    query(false);
  }

  function loadMore(): void {
    if (!store.hasMore || store.phase === "loading" || store.phase === "loadingMore") return;
    query(true);
  }

  function previewRestore(target: RestoreTarget): void {
    const collection = workspace.currentTable;
    if (!collection) return;
    const restoreScope = store.scope === "cell"
      ? "cell"
      : store.scope === "archived" ? "archived" : "row";
    const field = restoreScope === "cell" ? target.field ?? store.field : undefined;
    store.beginPreview({ ...target, field });
    const payload: HistoryPreviewRestorePayload = {
      collection,
      itemId: target.itemId,
      scope: restoreScope,
      field: field ?? undefined,
      targetRevision: target.revisionId,
    };
    bridge.notify("history.previewRestoreRequested", payload);
  }

  function applyRestore(): void {
    const collection = workspace.currentTable;
    const itemId = store.previewItemId;
    const token = store.preview?.token;
    if (!collection || !itemId || !token || !store.canApply) return;
    store.beginApply();
    const payload: HistoryApplyRestorePayload = { collection, itemId, token };
    bridge.notify("history.applyRestoreRequested", payload);
  }

  return { init, open, close, refresh, loadMore, previewRestore, applyRestore };
}
