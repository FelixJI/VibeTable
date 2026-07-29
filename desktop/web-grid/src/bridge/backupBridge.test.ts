import { describe, expect, it, vi } from "vitest";
import { createHostBridge } from "./hostBridge";

describe("backup HostBridge allowlist", () => {
  it("posts and correlates each closed backup request", async () => {
    let listener: ((event: { readonly data: unknown }) => void) | undefined;
    let sequence = 0;
    const webview = {
      postMessage: vi.fn(),
      addEventListener: vi.fn(
        (_type: "message", handler: (event: { readonly data: unknown }) => void) => {
          listener = handler;
        },
      ),
      removeEventListener: vi.fn(),
    };
    const bridge = createHostBridge({
      webview,
      generateRequestId: () => `backup-${++sequence}`,
    });
    bridge.start();

    const list = bridge.request("backup.list", {});
    expect(webview.postMessage).toHaveBeenLastCalledWith({
      type: "backup.list",
      requestId: "backup-1",
      payload: {},
    });
    listener?.({
      data: {
        type: "backup.list",
        requestId: "backup-1",
        payload: { backups: [] },
      },
    });
    await expect(list).resolves.toEqual({ backups: [] });

    const create = bridge.request("backup.create", { name: "safe.zip" });
    listener?.({
      data: {
        type: "backup.create",
        requestId: "backup-2",
        payload: {
          backup: {
            name: "safe.zip",
            size: 1,
            modified: "2026-07-24T10:15:00Z",
            sha256: "a".repeat(64),
          },
          integrityValid: true,
          receipt: "vbr1.test-receipt",
        },
      },
    });
    await expect(create).resolves.toMatchObject({ integrityValid: true });

    const restore = bridge.request("backup.restore", {
      name: "safe.zip",
      confirmed: true,
    });
    listener?.({
      data: {
        type: "backup.restore",
        requestId: "backup-3",
        payload: { status: "restarting" },
      },
    });
    await expect(restore).resolves.toEqual({ status: "restarting" });
  });
});
