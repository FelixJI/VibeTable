import { computed, ref, watchEffect } from "vue";
import { useUiStore } from "@/stores/uiStore";
import type { ThemeMode } from "@/stores/uiStore";

/** Reactive theme: follows system by default, manually overridable. */
export function useTheme() {
  const ui = useUiStore();
  const systemIsDark = ref(
    typeof matchMedia !== "undefined" &&
      matchMedia("(prefers-color-scheme: dark)").matches,
  );

  // Listen for system changes so 'system' mode tracks OS changes live.
  if (typeof matchMedia !== "undefined") {
    const mql = matchMedia("(prefers-color-scheme: dark)");
    mql.addEventListener("change", (e) => {
      systemIsDark.value = e.matches;
    });
  }

  const isDark = computed(() =>
    ui.themeMode === "system" ? systemIsDark.value : ui.themeMode === "dark",
  );

  watchEffect(() => {
    if (typeof document !== "undefined") {
      document.documentElement.classList.toggle("dark", isDark.value);
    }
  });

  function setMode(m: ThemeMode): void {
    ui.setThemeMode(m);
  }

  return { mode: computed(() => ui.themeMode), isDark, setMode };
}
