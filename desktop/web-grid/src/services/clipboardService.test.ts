import { describe, expect, it, vi } from "vitest";

import {
  BridgeRequestRetiredError,
  BridgeTimeoutError,
  type HostBridge,
} from "@/bridge/hostBridge";
import { ClipboardOperationError } from "@/contracts/clipboardContracts";
import {
  clipboardFailureReason,
  createClipboardService,
  isClipboardStaleFailure,
  type ClipboardBridge,
} from "./clipboardService";

function bridgeWith(
  impl: (type: string, payload: unknown) => unknown,
): ClipboardBridge {
  return { request: vi.fn(impl) as unknown as HostBridge["request"] };
}

describe("clipboardService", () => {
  it("resolves a successful read, including empty text", async () => {
    const bridge = bridgeWith((type) =>
      type === "clipboard.readText" ? { text: "" } : { written: true });
    const service = createClipboardService(bridge);

    expect(await service.readText()).toBe("");
  });

  it("writes text and validates the written acknowledgement", async () => {
    const bridge = bridgeWith((type, payload) =>
      type === "clipboard.writeText" && typeof (payload as { text?: string }).text === "string"
        ? { written: true as const }
        : { text: "" });
    const service = createClipboardService(bridge);

    await expect(service.writeText("a\tb")).resolves.toBeUndefined();
    expect(bridge.request).toHaveBeenCalledWith("clipboard.writeText", { text: "a\tb" });
  });

  it("maps distinct operation.failed codes onto typed reasons", () => {
    expect(clipboardFailureReason({ code: "CLIPBOARD_BUSY" })).toBe("busy");
    expect(clipboardFailureReason({ code: "CLIPBOARD_UNAVAILABLE" })).toBe("unavailable");
    expect(clipboardFailureReason({ code: "CLIPBOARD_TIMEOUT" })).toBe("timeout");
    expect(clipboardFailureReason({ code: "CLIPBOARD_TOO_LARGE" })).toBe("tooLarge");
    expect(clipboardFailureReason({ code: "MESSAGE_TOO_LARGE" })).toBe("tooLarge");
    expect(clipboardFailureReason({ code: "CLIPBOARD_FAILED" })).toBe("failed");
    expect(clipboardFailureReason(new Error("unexpected"))).toBe("failed");
  });

  it("passes through real bridge retirement classes without re-wrapping", async () => {
    // BridgeRequestRetiredError is a DOMException whose `name` is "AbortError";
    // only an instanceof match identifies it.
    const retired = new BridgeRequestRetiredError();
    expect(retired.name).toBe("AbortError");
    expect(isClipboardStaleFailure(retired)).toBe(true);
    const service = createClipboardService(bridgeWith(() => { throw retired; }));
    await expect(service.readText()).rejects.toBe(retired);
  });

  it("maps a real BridgeTimeoutError instance to the timeout reason", async () => {
    const timeout = new BridgeTimeoutError("clipboard.readText", "req-1", 10_000);
    expect(clipboardFailureReason(timeout)).toBe("timeout");
    const service = createClipboardService(bridgeWith(() => { throw timeout; }));
    await expect(service.writeText("x")).rejects.toMatchObject({
      name: "ClipboardOperationError",
      reason: "timeout",
    });
  });

  it("wraps operation failures as typed reasons", async () => {
    const busy = Object.assign(new Error("busy"), { code: "CLIPBOARD_BUSY" });
    const busyService = createClipboardService(bridgeWith(() => { throw busy; }));
    await expect(busyService.writeText("x")).rejects.toMatchObject({ reason: "busy" });
  });

  it("rejects malformed replies without exposing clipboard bodies", async () => {
    const service = createClipboardService(bridgeWith(() => ({ text: 42 })));
    await expect(service.readText()).rejects.toBeInstanceOf(ClipboardOperationError);

    const writeService = createClipboardService(bridgeWith(() => ({ written: "yes" })));
    await expect(writeService.writeText("x")).rejects.toBeInstanceOf(ClipboardOperationError);
  });
});
