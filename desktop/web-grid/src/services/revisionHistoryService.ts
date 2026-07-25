import type {
  HistoryPage,
  HistoryApplyRestorePayload,
  HistoryPreviewRestorePayload,
  HistoryQueryPayload,
  RestorePreview,
  RestoreResult,
} from "@/contracts";
import { BridgeOperationError } from "@/bridge/hostBridge";
import { useRevisionHistoryStore, type OpenRevisionHistorySelection } from "@/stores/revisionHistoryStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHostBridge } from "./bridgeContext";

export interface RestoreTarget {
  readonly itemId: string;
  readonly revisionId: string;
  readonly field?: string | null;
}

/** Typed adapter for persistent server revision history (never the undo store). */
export function useRevisionHistoryService(): {
  invalidate: () => void;
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
  let queryGeneration = 0;
  let previewGeneration = 0;
  let applyGeneration = 0;

  function invalidate(): void {
    queryGeneration += 1;
    previewGeneration += 1;
    applyGeneration += 1;
  }

  function failure(error: unknown): { message: string; code: string | null } {
    if (error instanceof BridgeOperationError) {
      return { message: error.message, code: error.code ?? null };
    }
    return {
      message: error instanceof Error ? error.message : String(error),
      code: null,
    };
  }

  function open(selection: OpenRevisionHistorySelection | { scope: "archived" }): void {
    previewGeneration += 1;
    applyGeneration += 1;
    store.open(selection);
    void query(false);
  }

  function close(): void {
    invalidate();
    store.close();
  }

  async function query(append: boolean): Promise<void> {
    const collection = workspace.currentTable;
    if (!collection) {
      store.failLoad("请先选择一张数据表");
      return;
    }
    store.beginLoad(append);
    const generation = ++queryGeneration;
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
    try {
      const page = await bridge.request("history.queryRequested", payload) as HistoryPage;
      if (generation !== queryGeneration || workspace.currentTable !== collection) return;
      store.receivePage(page, append);
    } catch (error) {
      if (generation !== queryGeneration || workspace.currentTable !== collection) return;
      const mapped = failure(error);
      store.failLoad(mapped.message, mapped.code);
    }
  }

  function refresh(): void {
    void query(false);
  }

  function loadMore(): void {
    if (!store.hasMore || store.phase === "loading" || store.phase === "loadingMore") return;
    void query(true);
  }

  function previewRestore(target: RestoreTarget): void {
    const collection = workspace.currentTable;
    if (!collection) return;
    const restoreScope = store.scope === "cell"
      ? "cell"
      : store.scope === "archived" ? "archived" : "row";
    const field = restoreScope === "cell" ? target.field ?? store.field : undefined;
    store.beginPreview({ ...target, field });
    const generation = ++previewGeneration;
    const payload: HistoryPreviewRestorePayload = {
      collection,
      itemId: target.itemId,
      scope: restoreScope,
      field: field ?? undefined,
      targetRevision: target.revisionId,
    };
    void bridge.request("history.previewRestoreRequested", payload)
      .then((preview) => {
        if (generation !== previewGeneration || workspace.currentTable !== collection) return;
        store.receivePreview(preview as RestorePreview);
      })
      .catch((error: unknown) => {
        if (generation !== previewGeneration || workspace.currentTable !== collection) return;
        const mapped = failure(error);
        store.failRestore(mapped.message, mapped.code);
      });
  }

  function applyRestore(): void {
    const collection = workspace.currentTable;
    const itemId = store.previewItemId;
    const token = store.preview?.token;
    if (!store.canApply) return;
    if (!collection || !itemId || !token) {
      store.failRestore("恢复上下文已失效，请重新打开历史记录后再试。", "restore_context_invalid");
      return;
    }
    store.beginApply();
    const generation = ++applyGeneration;
    const payload: HistoryApplyRestorePayload = { collection, itemId, token };
    void bridge.request("history.applyRestoreRequested", payload)
      .then((result) => {
        if (generation !== applyGeneration || workspace.currentTable !== collection) return;
        store.completeRestore(result as RestoreResult);
      })
      .catch((error: unknown) => {
        if (generation !== applyGeneration || workspace.currentTable !== collection) return;
        const mapped = failure(error);
        store.failRestore(mapped.message, mapped.code);
      });
  }

  return { invalidate, open, close, refresh, loadMore, previewRestore, applyRestore };
}
