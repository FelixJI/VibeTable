import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";

import type { PluginSnapshot } from "@/contracts";
import { usePluginStore } from "@/stores/pluginStore";
import { useUiStore } from "@/stores/uiStore";
import { createPluginAppNavigation, derivePluginAppEntries } from "./pluginAppNavigation";

function appPlugin(overrides: Partial<PluginSnapshot> = {}): PluginSnapshot {
  return {
    projectKey: "local:default",
    pluginId: "com.example.tasks",
    version: "1.0.0",
    packageHash: "sha256:test",
    sourceType: "package",
    sourceLocation: "tasks.vtplugin",
    manifest: {
      $schema: "vibetable.plugin-manifest.v2",
      pluginId: "com.example.tasks",
      version: "1.0.0",
      displayName: { "zh-CN": "任务台" },
      description: {},
      compatibility: { pluginApi: "3.x" },
      permissions: {},
      actions: [
        {
          actionId: "open-task-desk",
          displayName: { "zh-CN": "打开任务台" },
          description: {},
          mode: "local",
          risk: "read",
          invocation: "manual",
          placements: [],
          requires: {},
          workerEntry: null,
          formSchema: null,
          inputSchema: null,
          outputSchema: null,
          presentation: "application",
          entry: "ui/index.html",
          surfaceSrc: "https://aaa.bbb.plugins.vibetable.local/ui/index.html",
          surfaceToken: "token-open",
          surfaceTitle: "任务台",
        },
        {
          actionId: "remember",
          displayName: { "zh-CN": "记住当前表" },
          description: {},
          mode: "local",
          risk: "read",
          invocation: "manual",
          placements: [],
          requires: {},
          workerEntry: null,
          formSchema: null,
          inputSchema: null,
          outputSchema: null,
          presentation: "headless",
          entry: "ui/run.js",
          surfaceSrc: "https://aaa.bbb.plugins.vibetable.local/__vibetable_host__/headless.html?entry=ui%2Frun.js",
          surfaceToken: "token-run",
        },
      ],
      ui: {},
    },
    schemas: {},
    status: "enabled",
    disabledReason: null,
    revision: 1,
    ...overrides,
  } satisfies PluginSnapshot;
}

describe("derivePluginAppEntries", () => {
  it("derives navigation only from enabled application actions with live surfaces", () => {
    const entries = derivePluginAppEntries(
      [
        appPlugin(),
        appPlugin({
          pluginId: "com.example.disabled",
          status: "disabled",
          manifest: {
            ...appPlugin().manifest,
            pluginId: "com.example.disabled",
          },
        }),
        // Surface projection missing (package unavailable): no dead entry.
        appPlugin({
          pluginId: "com.example.missing",
          manifest: {
            ...appPlugin().manifest,
            pluginId: "com.example.missing",
            actions: appPlugin().manifest.actions.map(action =>
              action.presentation === "application"
                ? { ...action, surfaceSrc: null, surfaceToken: null }
                : action),
          },
        }),
      ],
      "zh-CN",
    );
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({
      key: "com.example.tasks/open-task-desk",
      label: "任务台",
      src: "https://aaa.bbb.plugins.vibetable.local/ui/index.html",
      surfaceToken: "token-open",
    });
  });

  it("keeps display names distinguishable across duplicate labels", () => {
    const first = appPlugin();
    const second = appPlugin({
      pluginId: "com.example.other",
      manifest: { ...appPlugin().manifest, pluginId: "com.example.other" },
    });
    const entries = derivePluginAppEntries([first, second], "zh-CN");
    expect(entries.map(entry => entry.key)).toEqual([
      "com.example.tasks/open-task-desk",
      "com.example.other/open-task-desk",
    ]);
  });
});

describe("createPluginAppNavigation with ui store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("retires the active entry when the catalog drops it", async () => {
    const plugins = usePluginStore();
    const ui = useUiStore();
    plugins.setProjectContext("local:default", "1");
    plugins.replaceCatalog("local:default", [appPlugin()]);

    const navigation = createPluginAppNavigation(plugins, ui);
    ui.openPluginApp("com.example.tasks", "open-task-desk");
    expect(navigation.activeEntry.value?.surfaceToken).toBe("token-open");

    // Catalog revision removes the application surface (disable/uninstall).
    plugins.replaceCatalog("local:default", [
      appPlugin({ status: "disabled" }),
    ]);
    expect(navigation.activeEntry.value).toBeNull();

    // Switching workspaces clears the active app identity entirely.
    ui.openPluginApp("com.example.tasks", "open-task-desk");
    plugins.replaceCatalog("local:default", [appPlugin()]);
    expect(navigation.activeEntry.value).not.toBeNull();
    ui.setWorkspaceNamespace("other-workspace");
    expect(ui.activePluginApp).toBeNull();
    expect(navigation.activeEntry.value).toBeNull();
  });

  it("navigating to a platform view returns from the plugin app", () => {
    setActivePinia(createPinia());
    const ui = useUiStore();
    ui.openPluginApp("com.example.tasks", "open-task-desk");
    expect(ui.activePluginApp).toEqual({ pluginId: "com.example.tasks", actionId: "open-task-desk" });
    ui.navigate("tables");
    expect(ui.activePluginApp).toBeNull();
    ui.openPluginApp("com.example.tasks", "open-task-desk");
    ui.closePluginApp();
    expect(ui.activePluginApp).toBeNull();
  });
});
