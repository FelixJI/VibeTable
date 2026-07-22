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
  activeBridges.push(bridge);
  return {
    bridge,
    posted,
    emit: (message: unknown) => listeners.forEach((listener) => listener({ data: message })),
  };
}

const activeBridges: ReturnType<typeof createHostBridge>[] = [];

function replyToLast(
  harness: ReturnType<typeof setupBridge>,
  type: string,
  payload: unknown,
): void {
  const requestId = harness.posted.at(-1)?.requestId;
  expect(typeof requestId).toBe("string");
  harness.emit({ type, requestId, payload });
}

describe("revisionHistoryService", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => {
    for (const bridge of activeBridges.splice(0)) bridge.stop();
    setHostBridgeForTesting(null);
  });

  it("opens the selected cell scope and posts a server-side filtered query", () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const store = useRevisionHistoryStore();
    store.updateQuery({ search: "客户 A", actions: ["update"], actorId: "u1" });
    const service = useRevisionHistoryService();

    service.open({ scope: "cell", itemId: "42", field: "status" });

    expect(harness.posted.at(-1)).toMatchObject({
      type: "history.queryRequested",
      requestId: expect.any(String),
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

  it("receives a page and requests the next offset without replacing prior entries", async () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
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
    replyToLast(harness, "history.pageLoaded", payload);
    await vi.waitFor(() => expect(store.changeSets).toHaveLength(1));

    service.loadMore();
    expect(harness.posted.at(-1)).toMatchObject({
      type: "history.queryRequested",
      payload: { offset: 1 },
    });
  });

  it("runs preview then apply with the short-lived token", async () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
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
    replyToLast(harness, "history.restorePreviewReady", preview);
    await vi.waitFor(() => expect(store.restorePhase).toBe("ready"));
    service.applyRestore();
    expect(harness.posted.at(-1)).toMatchObject({
      type: "history.applyRestoreRequested",
      payload: { collection: "orders", itemId: "42", token: "token-1" },
    });
    expect(store.restorePhase).toBe("applying");
  });

  it("surfaces capability and optimistic-lock failures in distinct states", async () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
    service.open({ scope: "table" });
    replyToLast(harness, "operation.failed", { message: "disabled", code: "history_not_allowed" });
    await vi.waitFor(() => expect(store.phase).toBe("unavailable"));

    service.open({ scope: "row", itemId: "42" });
    service.previewRestore({ itemId: "42", revisionId: "r1" });
    replyToLast(harness, "operation.failed", { message: "changed", code: "restore_conflict" });
    await vi.waitFor(() => expect(store.restorePhase).toBe("failed"));
    expect(store.restoreErrorCode).toBe("restore_conflict");
  });

  it("does not request another page while one is already loading", () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
    store.hasMore = true;
    store.phase = "loadingMore";
    const before = harness.posted.length;
    service.loadMore();
    expect(harness.posted).toHaveLength(before);
    expect(vi.isMockFunction(harness.bridge.request)).toBe(false);
  });

  it("ignores an older query response after a newer refresh completes", async () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
    service.open({ scope: "table" });
    const oldRequestId = harness.posted.at(-1)?.requestId;
    service.refresh();
    const newRequestId = harness.posted.at(-1)?.requestId;
    const freshPage: HistoryPage = {
      collection: "orders",
      scope: "table",
      changeSets: [],
      total: 0,
      hasMore: false,
      capabilityHash: "fresh",
      schemaRevision: "schema",
    };
    harness.emit({ type: "history.pageLoaded", requestId: newRequestId, payload: freshPage });
    await vi.waitFor(() => expect(store.capabilityHash).toBe("fresh"));
    harness.emit({
      type: "history.pageLoaded",
      requestId: oldRequestId,
      payload: { ...freshPage, capabilityHash: "stale" },
    });
    await Promise.resolve();
    expect(store.capabilityHash).toBe("fresh");
  });

  it("ignores a response after invalidation even when the same table is reselected", async () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const store = useRevisionHistoryStore();
    const service = useRevisionHistoryService();
    service.open({ scope: "table" });
    const requestId = harness.posted.at(-1)?.requestId;

    service.invalidate();
    workspace.selectTable("orders");
    store.reset();
    harness.emit({
      type: "history.pageLoaded",
      requestId,
      payload: {
        collection: "orders",
        changeSets: [],
        total: 0,
        capabilityHash: "stale",
        schemaRevision: "schema",
      },
    });
    await Promise.resolve();

    expect(store.phase).toBe("idle");
    expect(store.capabilityHash).toBe("");
  });

  it("settles the store for an unknown correlated error code", async () => {
    const harness = setupBridge();
    setHostBridgeForTesting(harness.bridge);
    useWorkspaceStore().selectTable("orders");
    const store = useRevisionHistoryStore();
    useRevisionHistoryService().open({ scope: "table" });

    replyToLast(harness, "operation.failed", { message: "unexpected", code: "future_code" });
    await vi.waitFor(() => expect(store.phase).toBe("failed"));
    expect(store.lastErrorCode).toBe("future_code");
  });
});
