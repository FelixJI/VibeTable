import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";

vi.mock("@/views/WorkspaceView.vue", () => ({
  default: { template: '<div data-testid="workspace-stub">workspace</div>' },
}));

import App from "./App.vue";

describe("App startup gate", () => {
  afterEach(() => setHostBridgeForTesting(null));

  it("does not mount the workspace until the host reports ready", async () => {
    const listeners: Array<(event: { data: unknown }) => void> = [];
    const bridge = createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: (_type, next) => { listeners.push(next); },
      removeEventListener: () => { listeners.length = 0; },
    } });
    bridge.start();
    setHostBridgeForTesting(bridge);
    const pinia = createPinia();
    setActivePinia(pinia);
    const wrapper = mount(App, { global: { plugins: [pinia] } });
    expect(wrapper.find('[data-testid="startup-gate"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="workspace-stub"]').exists()).toBe(false);

    listeners[0]?.({ data: { type: "host.startupStateChanged", payload: { phase: "ready" } } });
    await flushPromises();
    expect(wrapper.find('[data-testid="startup-gate"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="workspace-stub"]').exists()).toBe(true);
  });
});
