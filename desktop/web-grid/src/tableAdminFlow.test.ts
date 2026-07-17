import { describe, expect, it } from "vitest";
import {
  applyCollectionsChanged,
  applyCreateFailed,
  applyCreateStarted,
  applyCreateSucceeded,
  applyDeleteFailed,
  applyDeleteStarted,
  applyDeleteSucceeded,
  initialTableAdminState,
  requestCreate,
  requestDelete,
} from "./tableAdminFlow";
import type { HostBridgeLike } from "./tableAdminFlow";

function makeBridge(): HostBridgeLike & {
  notifies: Array<{ type: string; payload: unknown }>;
} {
  const notifies: Array<{ type: string; payload: unknown }> = [];
  return {
    notifies,
    notify(type, payload) {
      notifies.push({ type, payload });
    },
  };
}

describe("reducers", () => {
  it("applyCollectionsChanged sets tables and clears status", () => {
    const next = applyCollectionsChanged(initialTableAdminState, ["a", "b"]);
    expect(next.collections).toEqual(["a", "b"]);
    expect(next.status).toBe("idle");
    expect(next.error).toBeNull();
  });

  it("create lifecycle: started → succeeded → idle", () => {
    const started = applyCreateStarted(initialTableAdminState);
    expect(started.status).toBe("creating");
    const succeeded = applyCreateSucceeded(started);
    expect(succeeded.status).toBe("idle");
  });

  it("createFailed stores the error message", () => {
    const started = applyCreateStarted(initialTableAdminState);
    const failed = applyCreateFailed(started, "boom");
    expect(failed.status).toBe("error");
    expect(failed.error).toBe("boom");
  });

  it("delete lifecycle mirrors create", () => {
    const started = applyDeleteStarted(initialTableAdminState);
    expect(started.status).toBe("deleting");
    const failed = applyDeleteFailed(started, "nope");
    expect(failed.status).toBe("error");
    expect(failed.error).toBe("nope");
    const succeeded = applyDeleteSucceeded(applyDeleteStarted(initialTableAdminState));
    expect(succeeded.status).toBe("idle");
  });
});

describe("orchestrators", () => {
  it("requestCreate notifies createRequested and dispatches only createStarted", () => {
    const bridge = makeBridge();
    const events: string[] = [];
    requestCreate(bridge, "projects", [{ key: "name", type: "string" }], (e) =>
      events.push(e.type),
    );
    // notify is fire-and-forget: only createStarted is dispatched here. Success
    // is observed indirectly via the host-pushed database.collectionsChanged,
    // and failure via an uncorrelated operation.failed broadcast (routed by
    // main.ts). The orchestrator must NOT dispatch createSucceeded/createFailed.
    expect(bridge.notifies).toEqual([
      { type: "tableAdmin.createRequested", payload: { name: "projects", fields: [{ key: "name", type: "string" }] } },
    ]);
    expect(events).toEqual(["createStarted"]);
  });

  it("requestDelete notifies deleteRequested and dispatches only deleteStarted", () => {
    const bridge = makeBridge();
    const events: string[] = [];
    requestDelete(bridge, "old_table", (e) => events.push(e.type));
    expect(bridge.notifies).toEqual([
      { type: "tableAdmin.deleteRequested", payload: { collection: "old_table" } },
    ]);
    expect(events).toEqual(["deleteStarted"]);
  });

  it("orchestrators never dispatch succeeded/failed (host broadcasts drive them)", () => {
    // Belt-and-braces: with notify there is no per-request result, so neither
    // orchestrator may emit createSucceeded/createFailed/deleteSucceeded/
    // deleteFailed. main.ts derives those from database.collectionsChanged and
    // the global operation.failed handler.
    const createEvents: string[] = [];
    requestCreate(makeBridge(), "x", [], (e) => createEvents.push(e.type));
    expect(createEvents).toEqual(["createStarted"]);

    const deleteEvents: string[] = [];
    requestDelete(makeBridge(), "y", (e) => deleteEvents.push(e.type));
    expect(deleteEvents).toEqual(["deleteStarted"]);
  });
});
