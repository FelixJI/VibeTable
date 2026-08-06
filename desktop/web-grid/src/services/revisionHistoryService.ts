import type {
  HistoryPage,
  HistoryApplyRestorePayload,
  HistoryPreviewRestorePayload,
  HistoryQueryPayload,
  RestorePreview,
  RestoreResult,
} from "@/contracts";
import { watch } from "vue";
import { BridgeOperationError } from "@/bridge/hostBridge";
import { useRevisionHistoryStore, type OpenRevisionHistorySelection } from "@/stores/revisionHistoryStore";
import {
  registerWorkspaceEpochReset,
  useWorkspaceSessionStore,
} from "@/stores/workspaceSessionStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHostBridge } from "./bridgeContext";
import { requestWorkspaceV2UiAction } from "./workspaceV2UiPort";

export interface RestoreTarget {
  readonly itemId: string;
  readonly revisionId: string;
  readonly field?: string | null;
}

/** Typed adapter for persistent server revision history (never the undo store). */
export function useRevisionHistoryService(): {
  invalidate: () => void;
  dispose: () => void;
  open: (selection: OpenRevisionHistorySelection | { scope: "archived" }) => void;
  close: () => void;
  refresh: () => void;
  loadMore: () => void;
  previewRestore: (target: RestoreTarget) => void;
  applyRestore: () => void;
} {
  const bridge = useHostBridge();
  const store = useRevisionHistoryStore();
  const session = useWorkspaceSessionStore();
  const workspace = useWorkspaceStore();
  let queryGeneration = 0;
  let previewGeneration = 0;
  let applyGeneration = 0;
  let pendingEpochQuery = false;

  function invalidate(): void {
    pendingEpochQuery = false;
    queryGeneration += 1;
    previewGeneration += 1;
    applyGeneration += 1;
  }

  function failure(error: unknown): { message: string; code: string | null } {
    if (error instanceof BridgeOperationError) {
      return { message: error.message, code: error.code ?? null };
    }
    if (
      error instanceof Error
      && "code" in error
      && typeof error.code === "string"
    ) {
      return { message: error.message, code: error.code };
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
    if (session.enabled && (!session.hasOpenWorkspace || session.isTransitioning)) {
      pendingEpochQuery = true;
      store.beginLoad(false);
      return;
    }
    pendingEpochQuery = false;
    void query(false);
  }

  function close(): void {
    pendingEpochQuery = false;
    invalidate();
    store.close();
  }

  function currentSelection(): OpenRevisionHistorySelection | { scope: "archived" } {
    if (store.scope === "archived") return { scope: "archived" };
    if (store.scope === "cell") {
      return {
        scope: "cell",
        itemId: store.itemId ?? undefined,
        field: store.field ?? undefined,
      };
    }
    if (store.scope === "row") {
      return { scope: "row", itemId: store.itemId ?? undefined };
    }
    return { scope: "table" };
  }

  const unregisterEpochReset = registerWorkspaceEpochReset(
    "revision-history-service",
    ({ previousWorkspaceId, nextWorkspaceId }) => {
      const retainedSelection = store.panelOpen
        && previousWorkspaceId !== null
        && previousWorkspaceId === nextWorkspaceId
        ? currentSelection()
        : null;
      invalidate();
      pendingEpochQuery = retainedSelection !== null;
      store.reset();
      if (retainedSelection) {
        store.open(retainedSelection);
        store.beginLoad(false);
      }
    },
  );

  const stopSessionWatch = watch(
    () => [
      session.enabled,
      session.hasOpenWorkspace,
      session.isTransitioning,
      session.sessionEpoch,
      workspace.currentTable,
    ] as const,
    () => {
      if (!pendingEpochQuery || !workspace.currentTable) return;
      if (session.enabled && (!session.hasOpenWorkspace || session.isTransitioning)) return;
      pendingEpochQuery = false;
      void query(false);
    },
  );

  function dispose(): void {
    pendingEpochQuery = false;
    unregisterEpochReset();
    stopSessionWatch();
    invalidate();
  }

  async function query(append: boolean, allowFieldFallback = true): Promise<void> {
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
      const request = session.hasOpenWorkspace
        ? session.historyRestoreEnabled
          ? requestWorkspaceV2UiAction({
              method: "history.query",
              params: {
                collection,
                scope: store.scope,
                itemId: store.itemId,
                field: store.field ?? (filters.field || null),
                search: filters.search.trim(),
                dateFrom: filters.dateFrom || null,
                dateTo: filters.dateTo || null,
                actorId: filters.actorId.trim() || null,
                actions: [...filters.actions],
                recordId: filters.recordId.trim() || null,
                limit: store.limit,
                offset: append ? store.offset : 0,
              },
            })
          : Promise.reject(new Error("workspace.history_restore_unavailable"))
        : bridge.request("history.queryRequested", payload);
      const page = await request as HistoryPage;
      if (generation !== queryGeneration || workspace.currentTable !== collection) return;
      store.receivePage(page, append);
    } catch (error) {
      if (generation !== queryGeneration || workspace.currentTable !== collection) return;
      const mapped = failure(error);
      if (
        allowFieldFallback
        && mapped.code === "history.field_not_found"
        && (store.scope === "cell" || store.field !== null || store.query.field !== "")
      ) {
        store.setSelection({ scope: "table" });
        store.open({ scope: "table" });
        store.updateQuery({ field: "" });
        await query(false, false);
        return;
      }
      store.failLoad(mapped.message, mapped.code);
    }
  }

  function refresh(): void {
    if (pendingEpochQuery) return;
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
    const request = session.hasOpenWorkspace
      ? session.historyRestoreEnabled
        ? requestWorkspaceV2UiAction({
            method: "history.previewRestore",
            params: {
              collection,
              itemId: target.itemId,
              scope: restoreScope,
              field: field ?? null,
              targetRevision: target.revisionId,
            },
          })
        : Promise.reject(new Error("workspace.history_restore_unavailable"))
      : bridge.request("history.previewRestoreRequested", payload);
    void request
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
    const request = session.hasOpenWorkspace
      ? session.historyRestoreEnabled
        ? requestWorkspaceV2UiAction({
            method: "history.applyRestore",
            params: { collection, itemId, token },
          })
        : Promise.reject(new Error("workspace.history_restore_unavailable"))
      : bridge.request("history.applyRestoreRequested", payload);
    void request
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

  return { invalidate, dispose, open, close, refresh, loadMore, previewRestore, applyRestore };
}
