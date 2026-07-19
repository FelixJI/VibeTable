import { defineStore } from "pinia";
import { ref } from "vue";
import { getLocale, setLocale, type Locale } from "@/i18n";

export type ThemeMode = "light" | "dark" | "system";
export type AppView = "home" | "tables" | "settings";
export type StartupPage = "home" | "tables";
export type DensityMode = "comfortable" | "compact";

export interface RecentTable {
  readonly name: string;
  readonly openedAt: number;
}

const THEME_KEY = "vt:theme";
const STARTUP_KEY = "vt:startup-page";
const QUOTE_KEY = "vt:show-daily-quote";
const CALENDAR_KEY = "vt:show-mini-calendar";
const ADMIN_FLOATING_KEY = "vt:admin-floating-button";
const ADMIN_CONFIRM_CLOSE_KEY = "vt:admin-confirm-close";
const ADMIN_RELEASE_IDLE_KEY = "vt:admin-release-idle";
const DENSITY_KEY = "vt:density";
const RECENT_KEY = "vt:recent-tables";

function loadThemeMode(): ThemeMode {
  const stored = readStorage(THEME_KEY) as ThemeMode | null;
  return stored === "light" || stored === "dark" || stored === "system"
    ? stored
    : "system";
}

function readStorage(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Preferences remain valid for this session when storage is unavailable.
  }
}

function loadStartupPage(): StartupPage {
  return readStorage(STARTUP_KEY) === "tables" ? "tables" : "home";
}

function loadDensity(): DensityMode {
  return readStorage(DENSITY_KEY) === "compact" ? "compact" : "comfortable";
}

function loadRecentTables(): RecentTable[] {
  try {
    const parsed = JSON.parse(readStorage(RECENT_KEY) ?? "[]") as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter(
        (item): item is RecentTable =>
          typeof item === "object" &&
          item !== null &&
          typeof (item as RecentTable).name === "string" &&
          typeof (item as RecentTable).openedAt === "number",
      )
      .slice(0, 5);
  } catch {
    return [];
  }
}

export const useUiStore = defineStore("ui", () => {
  const createModalOpen = ref(false);
  const deleteModalOpen = ref(false);
  const deleteTarget = ref<string | null>(null);
  const pastePanelOpen = ref(false);
  const shortcutsOpen = ref(false);
  const themeMode = ref<ThemeMode>(loadThemeMode());
  const startupPage = ref<StartupPage>(loadStartupPage());
  const activeView = ref<AppView>(startupPage.value);
  const showDailyQuote = ref(readStorage(QUOTE_KEY) !== "false");
  const showMiniCalendar = ref(readStorage(CALENDAR_KEY) !== "false");
  const adminFloatingButton = ref(readStorage(ADMIN_FLOATING_KEY) !== "false");
  const adminConfirmClose = ref(readStorage(ADMIN_CONFIRM_CLOSE_KEY) !== "false");
  const adminReleaseWhenIdle = ref(readStorage(ADMIN_RELEASE_IDLE_KEY) !== "false");
  const density = ref<DensityMode>(loadDensity());
  const locale = ref<Locale>(getLocale());
  const recentTables = ref<RecentTable[]>(loadRecentTables());

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
    writeStorage(THEME_KEY, m);
  }
  function navigate(view: AppView): void {
    activeView.value = view;
  }
  function setStartupPage(page: StartupPage): void {
    startupPage.value = page;
    writeStorage(STARTUP_KEY, page);
  }
  function setShowDailyQuote(show: boolean): void {
    showDailyQuote.value = show;
    writeStorage(QUOTE_KEY, String(show));
  }
  function setShowMiniCalendar(show: boolean): void {
    showMiniCalendar.value = show;
    writeStorage(CALENDAR_KEY, String(show));
  }
  function setAdminFloatingButton(show: boolean): void {
    adminFloatingButton.value = show;
    writeStorage(ADMIN_FLOATING_KEY, String(show));
  }
  function setAdminConfirmClose(confirm: boolean): void {
    adminConfirmClose.value = confirm;
    writeStorage(ADMIN_CONFIRM_CLOSE_KEY, String(confirm));
  }
  function setAdminReleaseWhenIdle(release: boolean): void {
    adminReleaseWhenIdle.value = release;
    writeStorage(ADMIN_RELEASE_IDLE_KEY, String(release));
  }
  function setDensity(mode: DensityMode): void {
    density.value = mode;
    writeStorage(DENSITY_KEY, mode);
  }
  function setLanguage(next: Locale): void {
    locale.value = next;
    setLocale(next);
  }
  function rememberTable(name: string): void {
    const next = [
      { name, openedAt: Date.now() },
      ...recentTables.value.filter((item) => item.name !== name),
    ].slice(0, 5);
    recentTables.value = next;
    writeStorage(RECENT_KEY, JSON.stringify(next));
  }

  return {
    createModalOpen,
    deleteModalOpen,
    deleteTarget,
    pastePanelOpen,
    shortcutsOpen,
    themeMode,
    startupPage,
    activeView,
    showDailyQuote,
    showMiniCalendar,
    adminFloatingButton,
    adminConfirmClose,
    adminReleaseWhenIdle,
    density,
    locale,
    recentTables,
    openCreate,
    closeCreate,
    openDelete,
    closeDelete,
    openPastePanel,
    closePastePanel,
    openShortcuts,
    closeShortcuts,
    setThemeMode,
    navigate,
    setStartupPage,
    setShowDailyQuote,
    setShowMiniCalendar,
    setAdminFloatingButton,
    setAdminConfirmClose,
    setAdminReleaseWhenIdle,
    setDensity,
    setLanguage,
    rememberTable,
  };
});
