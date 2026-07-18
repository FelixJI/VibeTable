import { defineStore } from "pinia";
import { ref } from "vue";

export type ThemeMode = "light" | "dark" | "system";

const THEME_KEY = "vt:theme";

function loadThemeMode(): ThemeMode {
  const stored = localStorage.getItem(THEME_KEY) as ThemeMode | null;
  return stored === "light" || stored === "dark" || stored === "system"
    ? stored
    : "system";
}

export const useUiStore = defineStore("ui", () => {
  const createModalOpen = ref(false);
  const deleteModalOpen = ref(false);
  const deleteTarget = ref<string | null>(null);
  const pastePanelOpen = ref(false);
  const shortcutsOpen = ref(false);
  const themeMode = ref<ThemeMode>(loadThemeMode());

  function openCreate(): void {
    createModalOpen.value = true;
  }
  function closeCreate(): void {
    createModalOpen.value = false;
  }
  function openDelete(name: string): void {
    deleteTarget.value = name;
    deleteModalOpen.value = true;
  }
  function closeDelete(): void {
    deleteModalOpen.value = false;
    deleteTarget.value = null;
  }
  function openPastePanel(): void {
    pastePanelOpen.value = true;
  }
  function closePastePanel(): void {
    pastePanelOpen.value = false;
  }
  function openShortcuts(): void {
    shortcutsOpen.value = true;
  }
  function closeShortcuts(): void {
    shortcutsOpen.value = false;
  }
  function setThemeMode(m: ThemeMode): void {
    themeMode.value = m;
    try {
      localStorage.setItem(THEME_KEY, m);
    } catch {
      // ignore
    }
  }

  return {
    createModalOpen,
    deleteModalOpen,
    deleteTarget,
    pastePanelOpen,
    shortcutsOpen,
    themeMode,
    openCreate,
    closeCreate,
    openDelete,
    closeDelete,
    openPastePanel,
    closePastePanel,
    openShortcuts,
    closeShortcuts,
    setThemeMode,
  };
});
