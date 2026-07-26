import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useDataRootService } from "./dataRootService";

describe("dataRootService", () => {
  const request = vi.fn();

  beforeEach(() => {
    request.mockReset();
    setHostBridgeForTesting({ request } as unknown as HostBridge);
  });

  afterEach(() => setHostBridgeForTesting(null));

  it("uses only the closed status and native migration-selection requests", async () => {
    request
      .mockResolvedValueOnce({
        dataRoot: "D:\\Apps\\VibeTable\\VibeTableData",
        defaultDataRoot: "D:\\Apps\\VibeTable\\VibeTableData",
        migrationPending: false,
        pendingDataRoot: null,
      })
      .mockResolvedValueOnce({
        selected: true,
        targetDataRoot: "E:\\VibeTableData",
        requiresRestart: true,
      });
    const service = useDataRootService();

    await expect(service.getStatus()).resolves.toMatchObject({
      migrationPending: false,
    });
    await expect(service.chooseMigration()).resolves.toMatchObject({
      requiresRestart: true,
    });
    expect(request.mock.calls).toEqual([
      ["dataRoot.get", {}],
      ["dataRoot.chooseMigrationRequested", {}],
    ]);
  });

  it("fails closed on malformed host responses", async () => {
    request.mockResolvedValueOnce({
      dataRoot: "",
      defaultDataRoot: "",
      migrationPending: "no",
    });
    await expect(useDataRootService().getStatus()).rejects.toThrow(
      /data-root status/i,
    );
  });
});
