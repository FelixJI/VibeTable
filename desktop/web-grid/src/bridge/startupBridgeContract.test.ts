import { describe, expect, it, vi } from "vitest";
import { createHostBridge } from "./hostBridge";

describe("startup bridge whitelist", () => {
  it("accepts startup state and permits only typed fire-and-forget startup actions", () => {
    const listeners: Array<(event: { data: unknown }) => void> = [];
    const postMessage = vi.fn();
    const bridge = createHostBridge({ webview: {
      postMessage,
      addEventListener: (_type, next) => { listeners.push(next); },
      removeEventListener: () => { listeners.length = 0; },
    } });
    const handler = vi.fn();
    bridge.on("host.startupStateChanged", handler);
    bridge.start();
    listeners[0]?.({ data: { type: "host.startupStateChanged", payload: { phase: "login", email: "a@example.com" } } });
    expect(handler).toHaveBeenCalledWith({ phase: "login", email: "a@example.com" });

    bridge.notify("host.firstRunSubmitted", { email: "a@example.com", password: "x", managedLogin: true, rememberPassword: false, autoLogin: false });
    bridge.notify("host.loginSubmitted", { email: "a@example.com", password: "x", rememberPassword: false, autoLogin: false });
    bridge.notify("host.startupRetryRequested", {});
    bridge.notify("host.startupCancelRequested", {});
    expect(postMessage.mock.calls.map((call) => call[0].type)).toEqual([
      "host.firstRunSubmitted",
      "host.loginSubmitted",
      "host.startupRetryRequested",
      "host.startupCancelRequested",
    ]);
    expect(postMessage.mock.calls.every((call) => call[0].requestId === undefined)).toBe(true);
  });
});
