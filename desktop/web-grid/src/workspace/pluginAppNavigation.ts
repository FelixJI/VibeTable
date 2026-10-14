import type { ComputedRef } from "vue";
import { computed } from "vue";
import type { PluginSnapshot } from "@/contracts";
import type { useUiStore } from "@/stores/uiStore";
import type { usePluginStore } from "@/stores/pluginStore";

type PluginStorePort = Pick<ReturnType<typeof usePluginStore>, "plugins">;
type UiStorePort = Pick<ReturnType<typeof useUiStore>, "locale" | "activePluginApp">;

export interface PluginAppEntry {
  /** Stable plugin-app identity key: `${pluginId}/${actionId}`. */
  readonly key: string;
  readonly pluginId: string;
  readonly actionId: string;
  readonly label: string;
  readonly src: string;
  readonly surfaceToken: string;
  readonly revision: number;
}

/**
 * Derives the plugin application navigation from the current workspace
 * catalog (AC1): only installed, enabled v2 plugins whose application action
 * has a live host-projected surface contribute an entry. There is no second
 * Interface entity — the identity is pluginId + contributed action id.
 */
export function derivePluginAppEntries(
  plugins: readonly PluginSnapshot[],
  locale: string,
): PluginAppEntry[] {
  const entries: PluginAppEntry[] = [];
  for (const plugin of plugins) {
    if (plugin.status !== "enabled") continue;
    if (plugin.manifest.compatibility.pluginApi !== "3.x") continue;
    for (const action of plugin.manifest.actions) {
      if (action.presentation !== "application") continue;
      if (!action.surfaceSrc || !action.surfaceToken) continue;
      entries.push({
        key: `${plugin.pluginId}/${action.actionId}`,
        pluginId: plugin.pluginId,
        actionId: action.actionId,
        label: action.surfaceTitle
          ?? action.displayName[locale]
          ?? action.displayName["zh-CN"]
          ?? action.displayName["en-US"]
          ?? plugin.manifest.displayName["zh-CN"]
          ?? action.actionId,
        src: action.surfaceSrc,
        surfaceToken: action.surfaceToken,
        revision: plugin.revision,
      });
    }
  }
  return entries;
}

export interface PluginAppNavigation {
  readonly entries: ComputedRef<readonly PluginAppEntry[]>;
  readonly entryByKey: (key: string) => PluginAppEntry | null;
  /** Resolves the live entry for the active app, or null when it went stale. */
  readonly activeEntry: ComputedRef<PluginAppEntry | null>;
}

export function createPluginAppNavigation(
  plugins: PluginStorePort,
  ui: UiStorePort,
): PluginAppNavigation {
  const entries = computed(() => derivePluginAppEntries(plugins.plugins, ui.locale));
  const entryByKey = (key: string): PluginAppEntry | null =>
    entries.value.find(entry => entry.key === key) ?? null;
  const activeEntry = computed(() => {
    const active = ui.activePluginApp;
    if (!active) return null;
    return entries.value.find(
      entry => entry.pluginId === active.pluginId && entry.actionId === active.actionId,
    ) ?? null;
  });
  return { entries, entryByKey, activeEntry };
}
