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
  requests: Array<{ type: string; payload: unknown }>;
  rejectNextWith?: Error;
} {
  const requests: Array<{ type: string; payload: unknown }> = [];
  return {
    requests,
    async request(type, payload) {
      requests.push({ type, payload });
      return undefined;
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
  it("requestCreate posts createRequested and resolves on success", async () => {
    const bridge = makeBridge();
    const events: string[] = [];
    await requestCreate(bridge, "projects", [{ key: "name", type: "string" }], (e) =>
      events.push(e.type),
    );
    expect(bridge.requests).toEqual([
      { type: "tableAdmin.createRequested", payload: { name: "projects", fields: [{ key: "name", type: "string" }] } },
    ]);
    expect(events).toEqual(["createStarted", "createSucceeded"]);
  });

  it("requestCreate dispatches createFailed on rejection", async () => {
    const bridge = makeBridge();
    bridge.request = async () => {
      throw new Error("backend said no");
    };
    const events: string[] = [];
    await requestCreate(bridge, "x", [], (e) => events.push(e.type));
    expect(events).toEqual(["createStarted", "createFailed"]);
  });

  it("requestDelete posts deleteRequested", async () => {
    const bridge = makeBridge();
    const events: string[] = [];
    await requestDelete(bridge, "old_table", (e) => events.push(e.type));
    expect(bridge.requests).toEqual([
      { type: "tableAdmin.deleteRequested", payload: { collection: "old_table" } },
    ]);
    expect(events).toEqual(["deleteStarted", "deleteSucceeded"]);
  });
});
