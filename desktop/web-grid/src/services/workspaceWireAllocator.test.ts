import { afterEach, describe, expect, it, vi } from "vitest";
import {
  configureWorkspaceWire,
  nextWorkspaceWire,
} from "@/services/workspaceWireAllocator";

const WORKSPACE_ID = "11111111-1111-4111-8111-111111111111";

describe("workspaceWireAllocator", () => {
  afterEach(() => {
    configureWorkspaceWire(null, 0);
    vi.restoreAllMocks();
  });

  it("leaves room for host reservations between queued renderer requests", () => {
    vi.spyOn(Date, "now").mockReturnValue(1_000);
    configureWorkspaceWire(WORKSPACE_ID, 7);

    const first = nextWorkspaceWire("22222222-2222-4222-8222-222222222222");
    const second = nextWorkspaceWire("33333333-3333-4333-8333-333333333333");

    expect(second.sequence - first.sequence).toBe(1_024);
    expect(second.sequence).toBeGreaterThan(first.sequence + 1);
  });
});
