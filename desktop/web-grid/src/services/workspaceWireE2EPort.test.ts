import { afterEach, describe, expect, it, vi } from "vitest";
import { configureWorkspaceWire } from "@/services/workspaceWireAllocator";
import { createWorkspaceWireE2EPort } from "@/services/workspaceWireE2EPort";
import type { WorkspaceV2UiPort } from "@/services/workspaceV2UiPort";

const WORKSPACE_ID = "11111111-1111-4111-8111-111111111111";

describe("workspaceWireE2EPort", () => {
  const requestMock = vi.fn();
  const workspaceV2: WorkspaceV2UiPort = {
    request: requestMock as WorkspaceV2UiPort["request"],
  };

  afterEach(() => {
    configureWorkspaceWire(null, 0);
    vi.restoreAllMocks();
  });

  it("reserves raw-request scopes through the formal renderer allocator", () => {
    vi.spyOn(Date, "now").mockReturnValue(1_000);
    const port = createWorkspaceWireE2EPort(
      () => ({ workspaceId: WORKSPACE_ID, sessionEpoch: 7 }),
      workspaceV2,
    );

    const first = port.reserve("22222222-2222-4222-8222-222222222222");
    const second = port.reserve("33333333-3333-4333-8333-333333333333");

    expect(first).toMatchObject({
      scope: "workspace",
      workspaceId: WORKSPACE_ID,
      sessionEpoch: 7,
      operationId: "22222222-2222-4222-8222-222222222222",
    });
    expect(second.sequence - first.sequence).toBe(1_024);
  });

  it("fails closed until the formal workspace session is active", () => {
    const port = createWorkspaceWireE2EPort(() => null, workspaceV2);

    expect(() => port.reserve("22222222-2222-4222-8222-222222222222"))
      .toThrow("workspace wire allocator has no active session");
  });

  it("delegates workspace RPCs to the formal serialized UI port", async () => {
    requestMock.mockResolvedValueOnce({ policyRevision: 4 });
    const port = createWorkspaceWireE2EPort(
      () => ({ workspaceId: WORKSPACE_ID, sessionEpoch: 7 }),
      workspaceV2,
    );

    await expect(port.request({ method: "retention.get", params: {} }))
      .resolves.toEqual({ policyRevision: 4 });
    expect(requestMock).toHaveBeenCalledWith({ method: "retention.get", params: {} });
  });
});
