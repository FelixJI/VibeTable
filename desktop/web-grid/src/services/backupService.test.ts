import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useBackupService } from "./backupService";

describe("backupService", () => {
  const request = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    setHostBridgeForTesting({ request } as unknown as HostBridge);
  });

  afterEach(() => {
    setHostBridgeForTesting(null);
    vi.useRealTimers();
  });

  it("uses only the three closed backup RPC names and payloads", async () => {
    request
      .mockResolvedValueOnce({
        backups: [{
          name: "manual_20260724_101500.zip",
          size: 8192,
          modified: "2026-07-24T10:15:00Z",
          sha256: "a".repeat(64),
        }],
      })
      .mockResolvedValueOnce({
        backup: {
          name: "manual_20260724_101500.zip",
          size: 8192,
          modified: "2026-07-24T10:15:00Z",
          sha256: "a".repeat(64),
        },
        integrityValid: true,
      })
      .mockResolvedValueOnce({ status: "restarting" });
    const service = useBackupService();

    const listed = await service.listBackups();
    const created = await service.createBackup(new Date("2026-07-24T10:15:00Z"));
    const restored = await service.restoreBackup(
      "manual_20260724_101500.zip",
      true,
    );

    expect(listed.backups).toHaveLength(1);
    expect(created.integrityValid).toBe(true);
    expect(restored.status).toBe("restarting");
    expect(request.mock.calls).toEqual([
      ["backup.list", {}],
      ["backup.create", { name: "manual_20260724_101500.zip" }],
      [
        "backup.restore",
        { name: "manual_20260724_101500.zip", confirmed: true },
      ],
    ]);
  });

  it("rejects unsafe archive names and unconfirmed restore before crossing the bridge", async () => {
    const service = useBackupService();

    await expect(
      service.restoreBackup("../data.db.zip", true),
    ).rejects.toThrow(/archive name/i);
    await expect(
      service.restoreBackup("safe.zip", false as true),
    ).rejects.toThrow(/confirmation/i);
    expect(request).not.toHaveBeenCalled();
  });

  it("fails closed on malformed host results", async () => {
    request.mockResolvedValueOnce({
      backups: [{ name: "safe.zip", size: -1, modified: "", sha256: "bad" }],
    });
    const service = useBackupService();

    await expect(service.listBackups()).rejects.toThrow(/backup response/i);
  });

  it("preserves the host-sanitized product error for retry messaging", async () => {
    request.mockResolvedValueOnce({
      error: {
        code: "backup.storage_failed",
        path: "",
        message: "backup storage is unavailable",
        details: null,
        retryable: true,
      },
    });
    const service = useBackupService();

    await expect(service.listBackups()).rejects.toMatchObject({
      message: "backup storage is unavailable",
      code: "backup.storage_failed",
      retryable: true,
    });
  });
});
