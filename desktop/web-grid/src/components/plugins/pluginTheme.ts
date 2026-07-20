import type { PluginSurfaceThemeSnapshot } from "@/contracts";
import type { DensityMode, ThemeMode } from "@/stores/uiStore";
import type { Locale } from "@/i18n";

export function projectPluginTheme(input: {
  readonly themeMode: ThemeMode;
  readonly locale: Locale;
  readonly density: DensityMode;
  readonly root?: HTMLElement;
}): PluginSurfaceThemeSnapshot {
  const root = input.root ?? document.documentElement;
  const style = getComputedStyle(root);
  const mode = input.themeMode === "dark"
    || (input.themeMode === "system" && root.classList.contains("dark")) ? "dark" : "light";
  const css = (name: string, fallback: string) => style.getPropertyValue(name).trim() || fallback;
  return {
    contract: "vibetable.plugin-theme.v1",
    mode,
    locale: input.locale,
    density: input.density,
    variables: {
      "--vt-plugin-bg": css("--vt-bg", mode === "dark" ? "#17191f" : "#ffffff"),
      "--vt-plugin-surface": css("--vt-bg-subtle", mode === "dark" ? "#1e2128" : "#f7f8fa"),
      "--vt-plugin-text": css("--vt-fg", mode === "dark" ? "#c9cdd4" : "#272e3b"),
      "--vt-plugin-text-muted": css("--vt-fg-muted", "#86909c"),
      "--vt-plugin-border": css("--vt-border", mode === "dark" ? "#2a2e37" : "#e5e6eb"),
      "--vt-plugin-primary": css("--vt-color-primary-500", mode === "dark" ? "#5b8bff" : "#3370ff"),
      "--vt-plugin-danger": css("--vt-color-danger", "#f54a45"),
      "--vt-plugin-radius": css("--vt-radius-md", "6px"),
      "--vt-plugin-space-unit": "4px",
    },
  };
}
