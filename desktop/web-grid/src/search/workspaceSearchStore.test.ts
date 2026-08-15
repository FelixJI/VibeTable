import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { SearchHit } from "@/contracts/generated/workbench";
import {
  setWorkspaceV2UiPort,
  type WorkspaceV2UiPort,
} from "@/services/workspaceV2UiPort";
import { useWorkspaceSearchStore } from "./workspaceSearchStore";

const hit = (id: string): SearchHit => ({
  contractVersion: "1.0",
  hitId: id,
  kind: "file",
  canonicalId: id,
  title: id,
  snippet: id,
  highlights: ["value"],
  sourceRevision: `revision-${id}`,
  score: 1,
  revisionTime: "2026-08-12T00:00:00Z",
  metadata: [],
  openTarget: {
    kind: "file",
    tableId: null,
    recordId: null,
    fieldId: null,
    documentId: id,
  },
});

describe("workspaceSearchStore", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    setWorkspaceV2UiPort(null);
  });

  it("sends the closed query and appends cursor pages without duplicate hits", async () => {
    const request = vi.fn(async (action: { method: string; params: Record<string, unknown> }) => {
      if (action.method === "workspaceSearch.query") {
        return action.params.cursor
          ? { hits: [hit("b"), hit("c")], nextCursor: null, generation: 3 }
          : { hits: [hit("a"), hit("b")], nextCursor: "cursor-2", generation: 3 };
      }
      throw new Error("unexpected method");
    });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const store = useWorkspaceSearchStore();
    store.query = "  财务  ";
    store.logic = "or";
    store.scope = "history";
    store.filters = [{ field: "kind", operator: "eq", value: "file" }];

    await store.search();
    await store.search({ append: true });

    expect(request.mock.calls[0]?.[0]).toEqual({
      method: "workspaceSearch.query",
      params: expect.objectContaining({
        contractVersion: "1.0",
        query: "财务",
        logic: "or",
        scope: "history",
        cursor: null,
        limit: 50,
      }),
    });
    expect(request.mock.calls[1]?.[0].params.cursor).toBe("cursor-2");
    expect(store.hits.map((item) => item.hitId)).toEqual(["a", "b", "c"]);
    expect(store.nextCursor).toBeNull();
  });

  it("exposes rebuild lifecycle and retains a stable failure code", async () => {
    const request = vi.fn(async (action: { method: string }) => {
      if (action.method === "workspaceSearch.rebuild") throw new Error("workspace_search.disk_full");
      throw new Error("unexpected method");
    });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const store = useWorkspaceSearchStore();
    await store.rebuild();
    expect(store.status.state).toBe("failed");
    expect(store.errorCode).toBe("workspace_search.disk_full");
    expect(store.rebuilding).toBe(false);
  });

  it("replaces typed filter slots and removes blank values", () => {
    const store = useWorkspaceSearchStore();

    store.setFilter("tableId", "contains", " table-orders ");
    store.setFilter("sizeBytes", "gte", 1024);
    store.setFilter("revisionTime", "after", "2026-08-12T00:00:00.000Z");
    store.setFilter("status", "eq", "indexed");
    store.setFilter("sizeBytes", "gte", 2048);

    expect(store.filters).toEqual([
      { field: "tableId", operator: "contains", value: "table-orders" },
      { field: "revisionTime", operator: "after", value: "2026-08-12T00:00:00.000Z" },
      { field: "status", operator: "eq", value: "indexed" },
      { field: "sizeBytes", operator: "gte", value: 2048 },
    ]);
    expect(store.filterValue("sizeBytes", "gte")).toBe(2048);

    store.setFilter("tableId", "contains", "  ");
    expect(store.filterValue("tableId", "contains")).toBeNull();
  });

  it("guards invalid searches and retains prior results on append failure", async () => {
    const request = vi.fn(async () => {
      throw "workspace_search.busy";
    });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const store = useWorkspaceSearchStore();

    await store.search();
    store.query = "value";
    await store.search({ append: true });
    store.searching = true;
    await store.search();
    expect(request).not.toHaveBeenCalled();

    store.searching = false;
    store.hits = [hit("kept")];
    store.nextCursor = "cursor";
    await store.search({ append: true });
    expect(store.hits).toHaveLength(1);
    expect(store.errorCode).toBe("workspace_search.busy");

    await store.search();
    expect(store.hits).toEqual([]);
  });

  it("loads status, degrades on failure, and resets every request-owned field", async () => {
    const request = vi.fn()
      .mockResolvedValueOnce({
        state: "ready",
        generation: 7,
        checkpoint: "14",
        processed: 4,
        total: 4,
        errorCode: null,
      })
      .mockRejectedValueOnce(new Error("workspace_search.corrupt"));
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const store = useWorkspaceSearchStore();

    await store.refreshStatus();
    expect(store.status.state).toBe("ready");
    await store.refreshStatus();
    expect(store.status).toMatchObject({
      state: "degraded",
      errorCode: "workspace_search.corrupt",
    });

    store.query = "query";
    store.logic = "or";
    store.scope = "history";
    store.hits = [hit("old")];
    store.nextCursor = "cursor";
    store.generation = 7;
    store.searching = true;
    store.rebuilding = true;
    store.errorCode = "failed";
    store.reset();
    expect(store.$state).toMatchObject({
      query: "",
      logic: "and",
      scope: "current",
      hits: [],
      nextCursor: null,
      generation: 0,
      searching: false,
      rebuilding: false,
      errorCode: null,
    });
  });

  it("polls a rebuild to ready, reruns the active query, and supports cancel", async () => {
    vi.useFakeTimers();
    const methods: string[] = [];
    let statusCalls = 0;
    const request = vi.fn(async (action: { method: string }) => {
      methods.push(action.method);
      if (action.method === "workspaceSearch.rebuild") {
        return { state: "building", generation: 8, checkpoint: null, processed: 0, total: 2, errorCode: null };
      }
      if (action.method === "workspaceSearch.status") {
        statusCalls += 1;
        return { state: "ready", generation: 8, checkpoint: "20", processed: 2, total: 2, errorCode: null };
      }
      if (action.method === "workspaceSearch.query") {
        return { hits: [hit("rebuilt")], nextCursor: null, generation: 8 };
      }
      if (action.method === "workspaceSearch.cancel") {
        return { state: "degraded", generation: 8, checkpoint: "20", processed: 1, total: 2, errorCode: "workspace_search.cancelled" };
      }
      throw new Error("unexpected method");
    });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const store = useWorkspaceSearchStore();
    store.query = "rebuild me";

    const rebuilding = store.rebuild();
    await vi.advanceTimersByTimeAsync(250);
    await rebuilding;
    expect(statusCalls).toBe(1);
    expect(methods).toEqual([
      "workspaceSearch.rebuild",
      "workspaceSearch.status",
      "workspaceSearch.query",
    ]);
    expect(store.hits[0]?.hitId).toBe("rebuilt");

    store.status = { ...store.status, state: "building" };
    await store.cancelRebuild();
    expect(store.status.state).toBe("degraded");
    vi.useRealTimers();
  });

  it("guards duplicate rebuild/cancel calls and exposes cancel failures", async () => {
    const request = vi.fn(async () => {
      throw new Error("workspace_search.storage_failed");
    });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const store = useWorkspaceSearchStore();

    store.rebuilding = true;
    await store.rebuild();
    store.rebuilding = false;
    await store.cancelRebuild();
    expect(request).not.toHaveBeenCalled();

    store.status = { ...store.status, state: "building" };
    await store.cancelRebuild();
    expect(store.errorCode).toBe("workspace_search.storage_failed");
  });

  it("re-reads a hit from authority and replaces a stale search result before open", async () => {
    const stale = hit("document-1");
    const refreshed = {
      ...stale,
      hitId: "document-1-current",
      title: "Current title",
      sourceRevision: "revision-current",
    };
    const request = vi.fn(async (action: { method: string; params: Record<string, unknown> }) => {
      if (action.method !== "workspaceSearch.resolveHit") throw new Error("unexpected method");
      return { status: "stale", hit: refreshed };
    });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const store = useWorkspaceSearchStore();
    store.scope = "current";
    store.hits = [stale];

    const resolved = await store.resolveHit(stale);

    expect(request).toHaveBeenCalledWith({
      method: "workspaceSearch.resolveHit",
      params: { contractVersion: "1.0", scope: "current", hit: stale },
    });
    expect(resolved).toBeNull();
    expect(store.hits).toEqual([refreshed]);
    expect(store.resolvingHitId).toBeNull();
  });

  it("removes an authority-missing hit and suppresses navigation", async () => {
    const missing = hit("missing");
    setWorkspaceV2UiPort({
      request: vi.fn(async () => {
        throw new Error("workspace_search.hit_missing");
      }) as WorkspaceV2UiPort["request"],
    });
    const store = useWorkspaceSearchStore();
    store.hits = [missing];

    expect(await store.resolveHit(missing)).toBeNull();
    expect(store.hits).toEqual([]);
    expect(store.errorCode).toBe("workspace_search.hit_missing");
  });

  it("suppresses an older authority result after a newer hit starts resolving", async () => {
    const older = hit("older");
    const newer = hit("newer");
    let finishOlder!: (value: unknown) => void;
    const request = vi.fn()
      .mockImplementationOnce(() => new Promise((resolve) => { finishOlder = resolve; }))
      .mockResolvedValueOnce({ status: "current", hit: newer });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const store = useWorkspaceSearchStore();
    store.hits = [older, newer];

    const first = store.resolveHit(older);
    expect(await store.resolveHit(newer)).toEqual(newer);
    finishOlder({ status: "stale", hit: { ...older, title: "outdated refresh" } });
    expect(await first).toBeNull();
    expect(store.hits[0]?.title).not.toBe("outdated refresh");
    expect(store.resolvingHitId).toBeNull();
  });
});
