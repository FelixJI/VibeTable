import { describe, expect, it, vi } from "vitest";

import type { HostBridge } from "@/bridge/hostBridge";
import { SurfaceCursorController, type SurfaceCursorReadRequest } from "./surfaceCursor";

const request: SurfaceCursorReadRequest = {
  bindingId: "orders",
  tableId: "table-orders",
  initialCursor: null,
  query: { filters: [], sorts: [] },
  pageSize: 2,
};

function window(rows: string[], nextCursor: string | null) {
  return {
    rows: rows.map((rowKey) => ({ rowKey })),
    nextCursor,
    hasMore: nextCursor !== null,
    filteredRows: 3,
    totalRows: 3,
    querySnapshot: {} as never,
  };
}

describe("SurfaceCursorController", () => {
  it("opens, fetches, and revisits revision-bound cursor windows", async () => {
    const bridgeRequest = vi.fn()
      .mockResolvedValueOnce(window(["r1", "r2"], "cursor-2"))
      .mockResolvedValueOnce(window(["r3"], null))
      .mockResolvedValueOnce(window(["r1", "r2"], "cursor-2"));
    const controller = new SurfaceCursorController({ request: bridgeRequest } as unknown as HostBridge);
    const signal = new AbortController().signal;

    expect(await controller.read(request, signal)).toMatchObject({ offset: 0, filteredRows: 3 });
    expect(bridgeRequest).toHaveBeenNthCalledWith(1, "query.cursorOpen", {
      tableId: "table-orders",
      query: { filters: [], sorts: [], offset: 0, limit: 2 },
    });
    expect(controller.canNext("orders")).toBe(true);
    expect(controller.next("orders")).toBe(true);

    expect(await controller.read(request, signal)).toMatchObject({ offset: 2 });
    expect(bridgeRequest).toHaveBeenNthCalledWith(2, "query.cursorFetch", { cursor: "cursor-2" });
    expect(controller.canPrevious("orders")).toBe(true);
    expect(controller.canNext("orders")).toBe(false);
    expect(controller.previous("orders")).toBe(true);

    expect(await controller.read(request, signal)).toMatchObject({ offset: 0 });
    expect(bridgeRequest).toHaveBeenNthCalledWith(3, "query.cursorOpen", expect.any(Object));
  });

  it("reopens the canonical query once when a persisted or next cursor is stale", async () => {
    const bridgeRequest = vi.fn()
      .mockRejectedValueOnce(new Error("query.cursor_stale"))
      .mockResolvedValueOnce(window(["current"], null));
    const controller = new SurfaceCursorController({ request: bridgeRequest } as unknown as HostBridge);

    const result = await controller.read(
      { ...request, initialCursor: "persisted-stale" },
      new AbortController().signal,
    );

    expect(result).toMatchObject({ rows: [{ rowKey: "current" }], offset: 0 });
    expect(bridgeRequest.mock.calls).toEqual([
      ["query.cursorFetch", { cursor: "persisted-stale" }],
      ["query.cursorOpen", {
        tableId: "table-orders",
        query: { filters: [], sorts: [], offset: 0, limit: 2 },
      }],
    ]);
  });

  it("does not navigate outside known windows and resets selected bindings", async () => {
    const bridgeRequest = vi.fn().mockResolvedValue(window(["r1"], null));
    const controller = new SurfaceCursorController({ request: bridgeRequest } as unknown as HostBridge);
    await controller.read(request, new AbortController().signal);

    expect(controller.previous("orders")).toBe(false);
    expect(controller.next("orders")).toBe(false);
    controller.reset(new Set(["orders"]));
    expect(controller.canPrevious("orders")).toBe(false);
    controller.reset();
  });

  it("preserves abort and non-stale failures", async () => {
    const failed = new SurfaceCursorController({
      request: vi.fn().mockRejectedValue(new Error("query.failed")),
    } as unknown as HostBridge);
    await expect(failed.read(request, new AbortController().signal)).rejects.toThrow("query.failed");

    const aborted = new AbortController();
    aborted.abort();
    await expect(failed.read(request, aborted.signal)).rejects.toThrow();
  });

  it("recognizes structured stale codes and rejects malformed structured errors", async () => {
    const stale = new SurfaceCursorController({
      request: vi.fn()
        .mockRejectedValueOnce({ code: "query.cursor_stale" })
        .mockResolvedValueOnce(window(["current"], null)),
    } as unknown as HostBridge);
    await expect(stale.read(
      { ...request, initialCursor: "stale" },
      new AbortController().signal,
    )).resolves.toMatchObject({ rows: [{ rowKey: "current" }] });

    const malformed = new SurfaceCursorController({
      request: vi.fn().mockRejectedValue({ code: 42 }),
    } as unknown as HostBridge);
    await expect(malformed.read(request, new AbortController().signal))
      .rejects.toEqual({ code: 42 });
  });
});
