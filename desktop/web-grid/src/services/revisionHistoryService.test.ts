import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useRevisionHistoryService } from "./revisionHistoryService";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import type { BridgeMessage, HistoryPage, RestorePreview } from "@/contracts";

function setupBridge() {
  const posted: BridgeMessage[] = [];
  const listeners: Array<(event: { data: unknown }) => void> = [];
  const webview = {
    postMessage: (message: unknown) => posted.push(message as BridgeMessage),
    addEventListener: (_type: "message", listener: (event: { data: unknown }) => void) => listeners.push(listener),
    removeEventListener: (_type: "message", listener: (event: { data: unknown }) => void) => {
      const index = listeners.indexOf(listener);
      if (index >= 0) listeners.splice(index, 1);
    },
  };
  const bridge = createHostBridge({ webview, timeoutMs: 1_000 });
  bridge.start();
  return {
    bridge,
    posted,
    emit: (message: unknown) => listeners.forEach((listener) => listener({ data: message })),
  };
}

describe("revisionHistoryService", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("opens the selected cell scope and posts a server-side filtered query", () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const store = useRevisionHistoryStore();
    store.updateQuery({ search: "客户 A", actions: ["update"], actorId: "u1" });
    const service = useRevisionHistoryService();
    service.init();

    service.open({ scope: "cell", itemId: "42", field: "status" });

    expect(harness.posted.at(-1)).toMatchObject({
      type: "history.queryRequested",
      payload: {
        collection: "orders",
        scope: "cell",
        itemId: "42",
        field: "status",
        search: "客户 A",
        actorId: "u1",
        actions: ["update"],
        limit: 50,
        offset: 0,
      },
    });
    expect(store.phase).toBe("loading");
  });

  it("receives a page and requests the next offset without replacing prior entries", () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
    service.init();
    service.open({ scope: "table" });
    const payload: HistoryPage = {
      collection: "orders",
      scope: "table",
      changeSets: [{
        rootRevisionId: "r2",
        activityId: "a2",
        itemId: "42",
        action: "update",
        timestamp: "2026-07-22T08:00:00Z",
        actor: { userId: null, displayName: null },
        scalarChanges: [],
        relationChanges: [],
      }],
      total: 2,
      hasMore: true,
      capabilityHash: "cap",
      schemaRevision: "schema",
    };
    harness.emit({ type: "history.pageLoaded", payload });
    expect(store.changeSets).toHaveLength(1);

    service.loadMore();
    expect(harness.posted.at(-1)).toMatchObject({
      type: "history.queryRequested",
      payload: { offset: 1 },
    });
  });

  it("runs preview then apply with the short-lived token", () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
    service.init();
    service.open({ scope: "cell", itemId: "42", field: "status" });
    service.previewRestore({ itemId: "42", revisionId: "r1", field: "status" });
    expect(harness.posted.at(-1)).toMatchObject({
      type: "history.previewRestoreRequested",
      payload: { collection: "orders", itemId: "42", scope: "cell", field: "status", targetRevision: "r1" },
    });

    const preview: RestorePreview = {
      collection: "orders",
      itemId: "42",
      scope: "cell",
      field: "status",
      targetRevision: "r1",
      currentHash: "hash",
      schemaRevision: "schema",
      scalarChanges: [{ field: "status", before: "done", after: "new" }],
      relationChanges: [],
      diagnostics: [],
      canApply: true,
      token: "token-1",
      expiresAt: "2026-07-22T09:00:00Z",
    };
    harness.emit({ type: "history.restorePreviewReady", payload: preview });
    service.applyRestore();
    expect(harness.posted.at(-1)).toMatchObject({
      type: "history.applyRestoreRequested",
      payload: { collection: "orders", itemId: "42", token: "token-1" },
    });
    expect(store.restorePhase).toBe("applying");
  });

  it("surfaces capability and optimistic-lock failures in distinct states", () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
    service.init();
    service.open({ scope: "table" });
    harness.emit({ type: "operation.failed", payload: { message: "disabled", code: "history_not_allowed" } });
    expect(store.phase).toBe("unavailable");

    service.open({ scope: "row", itemId: "42" });
    service.previewRestore({ itemId: "42", revisionId: "r1" });
    harness.emit({ type: "operation.failed", payload: { message: "changed", code: "restore_conflict" } });
    expect(store.restorePhase).toBe("failed");
    expect(store.restoreErrorCode).toBe("restore_conflict");
  });

  it("does not request another page while one is already loading", () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
    service.init();
    store.hasMore = true;
    store.phase = "loadingMore";
    const before = harness.posted.length;
    service.loadMore();
    expect(harness.posted).toHaveLength(before);
    expect(vi.isMockFunction(harness.bridge.notify)).toBe(false);
  });
});
