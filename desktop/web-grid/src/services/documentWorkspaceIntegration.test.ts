import { flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { HostBridge } from "@/bridge/hostBridge";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import { setHostBridgeForTesting } from "./bridgeContext";
import {
  createDocumentWorkspaceService,
  defaultDocumentQuery,
  useDocumentWorkspaceService,
  type DocumentWorkspaceIntent,
} from "./documentWorkspaceService";

const entry = (patch: Record<string, unknown> = {}) => ({
  documentId: "11111111-1111-4111-8111-111111111111",
  entryHandle: "entry-1",
  displayName: "报告.pdf",
  relativePath: "docs/报告.pdf",
  extension: ".pdf",
  availability: "available",
  mimeType: "application/pdf",
  sizeBytes: 20,
  effectiveRevisionCreatedAt: "2026-08-12T00:00:00Z",
  formalVersion: 1,
  status: "active",
  currentRevision: "r2",
  effectiveRevisionId: "22222222-2222-4222-8222-222222222222",
  capabilities: ["open", "preview", "history", "relocate", "unknown"],
  ...patch,
});

describe("document workspace bridge integration", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.spyOn(crypto, "randomUUID").mockReturnValue("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa");
  });

  afterEach(() => {
    setHostBridgeForTesting(null);
    vi.restoreAllMocks();
  });

  it("loads/reloads cursor pages and maps the canonical capability model", async () => {
    const handlers = new Map<string, (payload: never) => void>();
    const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
      if (type === "document.listRequested") {
        const query = payload.query as { cursor: string | null };
        return query.cursor
          ? {
              entries: [entry({ displayName: "报告 v2.pdf" })],
              nextCursor: null,
              topologyRevision: 3,
            }
          : {
              entries: [entry()],
              nextCursor: "cursor-2",
              topologyRevision: 2,
            };
      }
      return {};
    });
    const bridge = {
      request,
      notify: vi.fn(),
      notifyWithAdditionalObjects: vi.fn(() => false),
      on: vi.fn((type: string, handler: (payload: never) => void) => {
        handlers.set(type, handler);
        return () => handlers.delete(type);
      }),
    } as unknown as HostBridge;
    setHostBridgeForTesting(bridge);
    const service = useDocumentWorkspaceService();

    service.dispatch({
      type: "document.listRequested",
      scope: { kind: "record", collection: "orders", itemId: 7 },
      authority: "workspace",
      query: defaultDocumentQuery(" 报告 "),
    });
    await flushPromises();
    const store = useDocumentWorkspaceStore();
    expect(store.entries[0]).toMatchObject({
      displayName: "报告.pdf",
      authority: "workspace",
      versionLabel: "r2",
      capabilities: ["open", "preview", "history", "relink"],
    });
    expect(store.nextCursor).toBe("cursor-2");
    expect(request).toHaveBeenCalledWith("document.listRequested", expect.objectContaining({
      query: expect.objectContaining({
        filters: expect.arrayContaining([
          { field: "displayName", operator: "contains", value: "报告" },
        ]),
      }),
    }));

    service.dispatch({
      type: "document.listRequested",
      scope: { kind: "record", collection: "orders", itemId: 7 },
      authority: "workspace",
      query: defaultDocumentQuery("报告", "cursor-2"),
    });
    await flushPromises();
    expect(store.entries).toHaveLength(1);
    expect(store.entries[0]?.displayName).toBe("报告 v2.pdf");
    expect(store.topologyRevision).toBe(3);

    handlers.get("document.workspaceChanged")?.({} as never);
    await flushPromises();
    expect(request.mock.calls.at(-1)?.[1]).toMatchObject({
      scope: { kind: "record", collection: "orders", itemId: 7 },
    });
  });

  it("ignores an older file-list response that completes after a newer query", async () => {
    let resolveOlder!: (value: unknown) => void;
    const older = new Promise((resolve) => { resolveOlder = resolve; });
    const request = vi.fn()
      .mockImplementationOnce(() => older)
      .mockResolvedValueOnce({
        entries: [entry({ displayName: "newer.pdf" })],
        nextCursor: null,
        topologyRevision: 3,
      });
    setHostBridgeForTesting({
      request,
      notify: vi.fn(),
      notifyWithAdditionalObjects: vi.fn(() => false),
      on: vi.fn(() => () => undefined),
    } as unknown as HostBridge);
    const service = useDocumentWorkspaceService();

    service.dispatch({
      type: "document.listRequested",
      scope: { kind: "global" },
      authority: "workspace",
      query: defaultDocumentQuery("older"),
    });
    service.dispatch({
      type: "document.listRequested",
      scope: { kind: "global" },
      authority: "workspace",
      query: defaultDocumentQuery("newer"),
    });
    await flushPromises();
    expect(useDocumentWorkspaceStore().entries[0]?.displayName).toBe("newer.pdf");

    resolveOlder({
      entries: [entry({ displayName: "older.pdf" })],
      nextCursor: null,
      topologyRevision: 2,
    });
    await flushPromises();
    expect(useDocumentWorkspaceStore().entries[0]?.displayName).toBe("newer.pdf");
    expect(useDocumentWorkspaceStore().topologyRevision).toBe(3);
  });

  it("routes all notification/request intents and keeps failures stable", async () => {
    const notify = vi.fn();
    const request = vi.fn(async (type: string) => {
      if (type === "document.previewRequested") throw "preview.offline";
      return {};
    });
    setHostBridgeForTesting({
      request,
      notify,
      notifyWithAdditionalObjects: vi.fn(() => false),
      on: vi.fn(() => () => undefined),
    } as unknown as HostBridge);
    const service = useDocumentWorkspaceService();

    for (const intent of [
      { type: "document.importRequested", scope: { kind: "global" } },
      { type: "document.dragOutRequested", handle: "drag-1" },
      { type: "document.relinkRequested", handle: "relink-1" },
      { type: "document.openRequested", entryHandle: "entry-1" },
      { type: "document.previewRequested", entryHandle: "entry-1" },
      { type: "document.revealRequested", entryHandle: "entry-1" },
    ] as DocumentWorkspaceIntent[]) service.dispatch(intent);
    await flushPromises();

    expect(notify).toHaveBeenCalledWith("document.importRequested", { scope: { kind: "global" } });
    expect(notify).toHaveBeenCalledWith("document.dragOutRequested", { handle: "drag-1" });
    expect(notify).toHaveBeenCalledWith("document.relinkRequested", { handle: "relink-1" });
    expect(request).toHaveBeenCalledWith("document.openRequested", { entryHandle: "entry-1" });
    expect(request).toHaveBeenCalledWith("document.revealRequested", { entryHandle: "entry-1" });
    expect(useDocumentWorkspaceStore()).toMatchObject({ phase: "failed", lastError: "preview.offline" });
  });

  it("completes, rejects, cancels, and fails revision diff operations", async () => {
    const valid = {
      entryHandle: "entry-1",
      historicalRevisionId: "33333333-3333-4333-8333-333333333333",
      effectiveRevisionId: "22222222-2222-4222-8222-222222222222",
      outcome: "changedWithDetails",
      failure: null,
      addedLines: 4,
      removedLines: 2,
    };
    let result: unknown = valid;
    const request = vi.fn(async (type: string) => {
      if (type === "document.diffRequested") return result;
      if (type === "document.diffCancelRequested") return {};
      return {};
    });
    setHostBridgeForTesting({
      request,
      notify: vi.fn(),
      notifyWithAdditionalObjects: vi.fn(() => false),
      on: vi.fn(() => () => undefined),
    } as unknown as HostBridge);
    const service = useDocumentWorkspaceService();
    const store = useDocumentWorkspaceStore();

    service.dispatch({
      type: "document.diffRequested",
      entryHandle: "entry-1",
      operationId: "op-1",
      historicalRevisionId: valid.historicalRevisionId,
      expectedEffectiveRevisionId: valid.effectiveRevisionId,
    });
    await flushPromises();
    expect(store.diffPhase).toBe("ready");
    expect(store.diffResult).toEqual(valid);

    result = { ...valid, unexpected: true };
    service.dispatch({
      type: "document.diffRequested",
      entryHandle: "entry-1",
      operationId: "op-2",
      historicalRevisionId: valid.historicalRevisionId,
      expectedEffectiveRevisionId: valid.effectiveRevisionId,
    });
    await flushPromises();
    expect(store.diffPhase).toBe("failed");
    expect(store.diffError).toContain("invalid result");

    service.dispatch({ type: "document.diffCancelRequested", entryHandle: "entry-1", operationId: "op-2" });
    await flushPromises();
    expect(store.diffPhase).toBe("idle");
    expect(request).toHaveBeenCalledWith("document.diffCancelRequested", {
      entryHandle: "entry-1",
      operationId: "op-2",
    });

    request.mockRejectedValueOnce(new Error("diff.io"));
    service.dispatch({
      type: "document.diffRequested",
      entryHandle: "entry-1",
      operationId: "op-3",
      historicalRevisionId: valid.historicalRevisionId,
      expectedEffectiveRevisionId: valid.effectiveRevisionId,
    });
    await flushPromises();
    expect(store.diffError).toBe("diff.io");
  });

  it("exposes preview/reveal/external-drop intents and ignores cancellation without an operation", () => {
    const intents: DocumentWorkspaceIntent[] = [];
    const service = createDocumentWorkspaceService((intent) => intents.push(intent));
    const file = new File(["x"], "x.txt");
    service.preview("entry-1");
    service.reveal("entry-1");
    service.externalDrop({ kind: "global" }, [file]);
    service.cancelDiff("missing");
    service.cancelDiff("entry-1", "explicit-op");
    expect(intents).toEqual([
      { type: "document.previewRequested", entryHandle: "entry-1" },
      { type: "document.revealRequested", entryHandle: "entry-1" },
      { type: "document.externalDropRequested", scope: { kind: "global" }, files: [file] },
      { type: "document.diffCancelRequested", entryHandle: "entry-1", operationId: "explicit-op" },
    ]);
  });

  it("rejects non-canonical document identities before they enter the store", async () => {
    setHostBridgeForTesting({
      request: vi.fn(async () => ({
        entries: [entry({ documentId: "not-a-uuid" })],
        nextCursor: null,
        topologyRevision: 1,
      })),
      on: vi.fn(() => () => undefined),
    } as unknown as HostBridge);
    useDocumentWorkspaceService().dispatch({
      type: "document.listRequested",
      scope: { kind: "global" },
      authority: "workspace",
      query: defaultDocumentQuery(),
    });
    await flushPromises();
    expect(useDocumentWorkspaceStore()).toMatchObject({
      phase: "failed",
      lastError: "document.listLoaded returned a non-canonical documentId",
    });
  });
});
