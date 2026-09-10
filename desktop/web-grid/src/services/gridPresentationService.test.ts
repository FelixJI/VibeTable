import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { reactive } from "vue";
import { cloneFilterExpressions } from "@/stores/viewQueryStore";
import { createHostBridge, type WebViewLike } from "@/bridge/hostBridge";
import type { BridgeMessage, GridState, GridStateResult } from "@/contracts";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { setHostBridgeForTesting } from "./bridgeContext";
import { createGridPresentationPersistence, useGridPresentationService } from "./gridPresentationService";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}
beforeEach(() => setActivePinia(createPinia()));
afterEach(() => setHostBridgeForTesting(null));

it("uses the real correlated Host bridge with workspace scope and complete state", async () => {
  const session = useWorkspaceSessionStore();
  session.configureCapabilities(["workspace.session.v2"]);
  session.setWorkspaces([{ contractVersion: "2.0", workspaceId: "11111111-1111-4111-8111-111111111111",
    displayName: "Local", selectedRoot: "D:\\Local", activityRoot: null, storageKind: "fixed",
    coordinationStrength: "strong", lastOpenedAt: null, lastKnownHealth: "healthy", lastSnapshotAt: null,
    lastSyncAt: null, pendingSync: false }]);
  session.applySession({ contractVersion: "2.0", workspaceId: "11111111-1111-4111-8111-111111111111",
    sessionEpoch: 7, state: "openedWritable", openMode: "writable", writable: true,
    provisional: false, phase: "idle", errorCode: null });
  let listener: Parameters<WebViewLike["addEventListener"]>[1] | null = null;
  const posted: BridgeMessage[] = [];
  const bridge = createHostBridge({ webview: {
    postMessage(message) { posted.push(message as BridgeMessage); },
    addEventListener(_type, callback) { listener = callback; },
    removeEventListener() { listener = null; },
  } });
  bridge.start(); setHostBridgeForTesting(bridge);
  const state: GridState = { columns: [{ name: "title", width: 220, order: 0, frozen: true, visible: false }],
    filters: [{ groupLogic: "OR", filters: [{ field: "title", operator: "eq", value: "" }] }],
    sorts: [{ field: "title", direction: "desc" }], keyword: "needle", density: "compact", forcedRemote: true,
    presetId: "p", presetRevision: "p1" };
  const service = useGridPresentationService();
  function respond(result: GridStateResult) {
    const request = posted.at(-1)!;
    listener!({ data: { type: request.type, requestId: request.requestId, payload: result } });
  }
  try {
    const read = service.read("orders");
    expect(posted[0]).toMatchObject({ type: "gridState.get", payload: { table: "orders" },
      scope: { workspaceId: session.activeWorkspaceId, sessionEpoch: 7 } });
    respond({ state, revision: "r1", conflict: false });
    expect(await read).toEqual({ state, revision: "r1", conflict: false });
    const save = service.save("orders", state, "r1");
    expect(posted.at(-1)).toMatchObject({ type: "gridState.save", payload: { table: "orders", state, revision: "r1" } });
    respond({ state, revision: "r2", conflict: false });
    expect((await save).revision).toBe("r2");

    const persistence = createGridPresentationPersistence(service, () => "workspace:7", vi.fn());
    const opening = persistence.open("orders");
    await Promise.resolve();
    const request = posted.at(-1)!;
    listener!({ data: `{"type":"gridState.get","requestId":"${request.requestId}","payload":{"state":{"filters":[{"field":"total","operator":"eq","value":9007199254740993}]},"revision":"r2","conflict":false}}` });
    const restored = (await opening)!;
    const reactiveState = reactive({ ...restored, filters: cloneFilterExpressions(restored.filters ?? []) });
    persistence.save(reactiveState);
    expect(JSON.stringify(posted.at(-1))).toContain('"value":9007199254740993');
    respond({ state: reactiveState, revision: "r3", conflict: false });
    await persistence.flush();

    const nativeJSON = JSON;
    const withoutSource = (text: string, reviver?: (key: string, value: unknown) => unknown): unknown =>
      nativeJSON.parse(text, reviver ? (key, value: unknown) => reviver(key, value) : undefined);
    for (const unsupportedJSON of [
      { parse: nativeJSON.parse, stringify: nativeJSON.stringify },
      { parse: withoutSource, stringify: nativeJSON.stringify,
        rawJSON: (nativeJSON as JSON & { rawJSON: (text: string) => object }).rawJSON },
    ]) {
      vi.stubGlobal("JSON", unsupportedJSON);
      try {
        const report = vi.fn();
        const unsupported = createGridPresentationPersistence(service, () => "workspace:7", report);
        const failedOpen = unsupported.open("orders");
        await Promise.resolve();
        const failedRequest = posted.at(-1)!;
        listener!({ data: `{"type":"gridState.get","requestId":"${failedRequest.requestId}","payload":{"state":{"filters":[{"field":"total","operator":"eq","value":9007199254740993}]},"revision":"r3","conflict":false}}` });
        expect(await failedOpen).toBeNull();
        expect(report).toHaveBeenCalledWith(expect.objectContaining({ name: "GridStateNumberError" }));
        const sent = posted.length;
        unsupported.save({ keyword: "must not overwrite unparsed state" });
        await unsupported.flush();
        expect(posted).toHaveLength(sent);
      } finally { vi.stubGlobal("JSON", nativeJSON); }
    }
  } finally { bridge.stop(); }
});

it("serializes CAS and keeps the newest unsent complete snapshot", async () => {
  const first = deferred<GridStateResult>();
  const save = vi.fn().mockReturnValueOnce(first.promise)
    .mockImplementation(async (_table: string, state: GridState) => ({ state, revision: "r3", conflict: false }));
  const persistence = createGridPresentationPersistence({
    read: async () => ({ state: {}, revision: "r1", conflict: false }), save,
  }, () => "workspace:7", vi.fn());
  await persistence.open("orders");
  persistence.save({ keyword: "first" });
  persistence.save({ keyword: "intermediate" });
  persistence.save({ keyword: "latest", density: "compact" });
  expect(save).toHaveBeenCalledTimes(1);
  first.resolve({ state: {}, revision: "r2", conflict: false });
  await persistence.flush();
  expect(save.mock.calls).toEqual([
    ["orders", { keyword: "first" }, "r1"],
    ["orders", { keyword: "latest", density: "compact" }, "r2"],
  ]);
});

it("stops on conflict without overwriting either winner or pending edits", async () => {
  const report = vi.fn();
  const save = vi.fn(async () => ({ state: { keyword: "other window" }, revision: "r2", conflict: true }));
  const persistence = createGridPresentationPersistence({ read: async () => ({ state: {}, revision: "r1", conflict: false }), save }, () => "workspace:7", report);
  await persistence.open("orders");
  persistence.save({ keyword: "my edit" }); await persistence.flush();
  persistence.save({ keyword: "newer edit" }); await persistence.flush();
  expect(save).toHaveBeenCalledTimes(1);
  expect(report).toHaveBeenCalledOnce();
});

it("discards an old epoch load and retires queued writes before a replacement epoch", async () => {
  const read = deferred<GridStateResult>();
  let identity = "workspace:7";
  const save = vi.fn();
  const persistence = createGridPresentationPersistence({ read: () => read.promise, save }, () => identity, vi.fn());
  const opening = persistence.open("orders");
  identity = "workspace:8";
  read.resolve({ state: { keyword: "stale" }, revision: "r1", conflict: false });
  expect(await opening).toBeNull();
  persistence.save({ keyword: "stale write" }); await persistence.flush();
  expect(save).not.toHaveBeenCalled();
});

it("discards pending old-epoch edits even when an in-flight save later succeeds", async () => {
  const first = deferred<GridStateResult>();
  let identity: string | null = "workspace:7";
  const save = vi.fn(() => first.promise);
  const persistence = createGridPresentationPersistence({
    read: async () => ({ state: {}, revision: "r1", conflict: false }), save,
  }, () => identity, vi.fn());
  await persistence.open("orders");
  persistence.save({ keyword: "already sent" });
  persistence.save({ keyword: "must not enter replacement epoch" });
  identity = null;
  persistence.retire();
  identity = "workspace:8";
  await persistence.open("orders");
  first.resolve({ state: {}, revision: "old-r2", conflict: false });
  await Promise.resolve();
  await persistence.flush();
  expect(save).toHaveBeenCalledExactlyOnceWith("orders", { keyword: "already sent" }, "r1");
});
