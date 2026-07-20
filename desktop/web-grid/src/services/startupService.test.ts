import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useStartupService } from "./startupService";
import { useStartupStore } from "@/stores/startupStore";

function makeBridge(): { bridge: HostBridge; posted: unknown[]; emit: (message: unknown) => void } {
  const posted: unknown[] = [];
  let listener: ((event: { data: unknown }) => void) | null = null;
  const bridge = createHostBridge({
    webview: {
      postMessage: (message) => posted.push(message),
      addEventListener: (_type, next) => { listener = next; },
      removeEventListener: () => { listener = null; },
    },
  });
  bridge.start();
  return { bridge, posted, emit: (message) => listener?.({ data: message }) };
}

describe("startupService", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("uses startupStateChanged as the sole state authority", () => {
    const { bridge, emit } = makeBridge();
    setHostBridgeForTesting(bridge);
    const service = useStartupService();
    service.init();
    emit({ type: "host.startupStateChanged", payload: { phase: "ready", detail: "done" } });
    expect(useStartupStore().phase).toBe("ready");
    expect(useStartupStore().detail).toBe("done");
  });

  it("sends credentials with fire-and-forget notify envelopes", () => {
    const { bridge, posted } = makeBridge();
    setHostBridgeForTesting(bridge);
    const service = useStartupService();
    service.submitLogin({ email: "a@example.com", password: "secret", otp: "123456", rememberPassword: false, autoLogin: false });
    service.retry();
    expect(posted).toEqual([
      { type: "host.loginSubmitted", payload: { email: "a@example.com", password: "secret", otp: "123456", rememberPassword: false, autoLogin: false } },
      { type: "host.startupRetryRequested", payload: {} },
    ]);
    expect(posted.every((message) => !(message as { requestId?: string }).requestId)).toBe(true);
  });
});
