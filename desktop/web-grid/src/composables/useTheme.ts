import { computed, watchEffect } from "vue";
import { useUiStore } from "@/stores/uiStore";
import type { SystemBaseTheme, ThemeMode } from "@/stores/uiStore";

/**
 * Theme presentation projection. The Host owns both the persisted preference
 * (AppPreferences theme) and the observed system appearance (initial
 * snapshot + change notifications applied through the theme projection
 * service); this composable only renders that projected state. It never
 * reads matchMedia — the renderer must not become a second system-appearance
 * authority. Durable persistence flows through the theme projection service
 * (Settings awaits it and surfaces failures).
 */
export function useTheme() {
  const ui = useUiStore();

  // Explicit light/dark wins; "system" (and the pre-snapshot display
  // default) follows the Host-projected system theme, defaulting to light
  // until the first snapshot arrives.
  const isDark = computed(() => ui.effectiveTheme === "dark");

  watchEffect(() => {
    if (typeof document === "undefined") return;
    document.documentElement.classList.toggle("dark", isDark.value);
    // Forced-colors sync: the Host's high-contrast projection drives the same
    // state the browser would derive from the OS, exposed as a hook for
    // high-contrast token adjustments.
    document.documentElement.classList.toggle("vt-high-contrast", ui.highContrast);
  });

  return {
    mode: computed<ThemeMode>(() => ui.themeMode),
    isDark,
    systemTheme: computed<SystemBaseTheme | null>(() => ui.systemTheme),
    highContrast: computed(() => ui.highContrast),
  };
}
