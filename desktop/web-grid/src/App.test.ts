import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import {
  NConfigProvider,
  NMessageProvider,
  dateEnUS,
  dateZhCN,
  enUS,
  zhCN,
} from "naive-ui";

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

  it("mounts the v2 shell before business runtime readiness when capability is active", () => {
    const bridge = createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } });
    setHostBridgeForTesting(bridge);
    const pinia = createPinia();
    setActivePinia(pinia);
    useWorkspaceSessionStore().configureCapabilities(["workspace.session.v2"]);

    const wrapper = mount(App, { global: { plugins: [pinia] } });

    expect(wrapper.find('[data-testid="startup-gate"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="workspace-stub"]').exists()).toBe(true);
  });

  it("keeps Naive UI component and date locales in sync with the app language", async () => {
    const bridge = createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } });
    setHostBridgeForTesting(bridge);
    const pinia = createPinia();
    setActivePinia(pinia);
    const wrapper = mount(App, { global: { plugins: [pinia] } });
    const provider = wrapper.getComponent(NConfigProvider);

    expect(provider.props("locale")).toBe(zhCN);
    expect(provider.props("dateLocale")).toBe(dateZhCN);

    useUiStore().setLanguage("en-US");
    await wrapper.vm.$nextTick();
    expect(provider.props("locale")).toBe(enUS);
    expect(provider.props("dateLocale")).toBe(dateEnUS);
  });

  it("keeps notifications away from headers without stacking", () => {
    const bridge = createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } });
    setHostBridgeForTesting(bridge);
    const pinia = createPinia();
    setActivePinia(pinia);
    const wrapper = mount(App, { global: { plugins: [pinia] } });
    const provider = wrapper.getComponent(NMessageProvider);

    expect(provider.props("placement")).toBe("bottom");
    expect(provider.props("max")).toBe(1);
  });

  it("switches theme tokens and suppresses only native file navigation", async () => {
    const bridge = createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } });
    setHostBridgeForTesting(bridge);
    const pinia = createPinia();
    setActivePinia(pinia);
    const wrapper = mount(App, { global: { plugins: [pinia] } });
    const provider = wrapper.getComponent(NConfigProvider);

    useUiStore().setThemeMode("dark");
    await wrapper.vm.$nextTick();
    expect(provider.props("theme")).not.toBeNull();
    expect(provider.props("themeOverrides")).toBeTruthy();

    const dispatchDrop = (types?: string[]) => {
      const event = new Event("drop", { bubbles: true, cancelable: true });
      if (types) Object.defineProperty(event, "dataTransfer", { value: { types } });
      window.dispatchEvent(event);
      return event.defaultPrevented;
    };
    expect(dispatchDrop()).toBe(false);
    expect(dispatchDrop(["text/plain"])).toBe(false);
    expect(dispatchDrop(["Files"])).toBe(true);
    wrapper.unmount();
  });
});
