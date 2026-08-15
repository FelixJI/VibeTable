import { describe, expect, it, vi } from "vitest";
import type { HostBridge } from "@/bridge/hostBridge";
import { SurfaceHostRepository } from "./surfaceHostRepository";

function bridge(result: unknown): { bridge: HostBridge; notify: ReturnType<typeof vi.fn> } {
  const notify = vi.fn();
  return {
    notify,
    bridge: {
      requestWithHandle: vi.fn(() => ({ requestId: "surface-request-1", promise: Promise.resolve(result) })),
      notify,
    } as unknown as HostBridge,
  };
}

describe("SurfaceHostRepository", () => {
  it("maps the generated list envelope without exposing a generic RPC method", async () => {
    const host = bridge({ items: [{ interfaceId: "if-1", name: "Orders", revision: "rev-1" }] });
    const repository = new SurfaceHostRepository(host.bridge);

    await expect(repository.list(new AbortController().signal)).resolves.toEqual([
      { interfaceId: "if-1", name: "Orders", revision: "rev-1" },
    ]);
    expect(host.bridge.requestWithHandle).toHaveBeenCalledWith("interface.listRequested", {});
  });

  it("translates AbortSignal to the correlated native cancel use case", async () => {
    let resolve!: (value: unknown) => void;
    const promise = new Promise((done) => { resolve = done; });
    const notify = vi.fn();
    const host = {
      requestWithHandle: vi.fn(() => ({ requestId: "slow-interface", promise })),
      notify,
    } as unknown as HostBridge;
    const controller = new AbortController();
    const pending = new SurfaceHostRepository(host).load("if-1", controller.signal);

    controller.abort();
    expect(notify).toHaveBeenCalledWith(
      "interface.cancelRequested",
      { targetRequestId: "slow-interface" },
    );
    resolve({ definition: {}, revision: "rev" });
    await pending;
  });

  it("fails closed when delete acknowledgement names another Interface", async () => {
    const host = bridge({ interfaceId: "if-other" });
    const repository = new SurfaceHostRepository(host.bridge);
    await expect(repository.delete("if-1", "rev-1", new AbortController().signal))
      .rejects.toMatchObject({ code: "surface.response_invalid" });
  });
});
