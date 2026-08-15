import { defineStore } from "pinia";
import { ref } from "vue";
import { getLocale, setLocale, type Locale } from "@/i18n";

export type ThemeMode = "light" | "dark" | "system";
export type AppView = "home" | "tables" | "dashboard" | "interfaces" | "files" | "search" | "conflicts" | "plugins" | "settings";
export type StartupPage = "home" | "tables";
export type WorkspaceStartupPolicy = "lastWorkspace" | "workspaceCenter";
export type DensityMode = "comfortable" | "compact";
export type DailyQuoteSource = "hitokoto" | "jinrishici" | "quotable" | "builtin";
export type DailyQuoteStyle = "mixed" | "inspiring" | "literary" | "philosophy" | "poetry" | "lighthearted";

export const QUOTE_STYLES_BY_SOURCE: Readonly<Record<DailyQuoteSource, readonly DailyQuoteStyle[]>> = {
  hitokoto: ["mixed", "inspiring", "literary", "philosophy", "poetry", "lighthearted"],
  jinrishici: ["poetry"],
  quotable: ["mixed", "inspiring", "philosophy"],
  builtin: ["mixed", "inspiring"],
};

export interface RecentTable {
  readonly name: string;
  readonly openedAt: number;
}

const THEME_KEY = "vt:theme";
const STARTUP_KEY = "vt:startup-page";
const WORKSPACE_STARTUP_POLICY_KEY = "vt:workspace-startup-policy";
const QUOTE_KEY = "vt:show-daily-quote";
const QUOTE_SOURCE_KEY = "vt:daily-quote-source";
const QUOTE_STYLE_KEY = "vt:daily-quote-style";
const CALENDAR_KEY = "vt:show-mini-calendar";
const ADMIN_FLOATING_KEY = "vt:admin-floating-button";
const ADMIN_CONFIRM_CLOSE_KEY = "vt:admin-confirm-close";
const ADMIN_RELEASE_IDLE_KEY = "vt:admin-release-idle";
const DENSITY_KEY = "vt:density";
const LEGACY_RECENT_KEY = "vt:recent-tables";

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

function loadWorkspaceStartupPolicy(): WorkspaceStartupPolicy {
  return readStorage(WORKSPACE_STARTUP_POLICY_KEY) === "workspaceCenter"
    ? "workspaceCenter"
    : "lastWorkspace";
}

function loadDensity(): DensityMode {
  return readStorage(DENSITY_KEY) === "compact" ? "compact" : "comfortable";
}

function loadQuoteSource(): DailyQuoteSource {
  const stored = readStorage(QUOTE_SOURCE_KEY);
  if (stored === "hitokoto" || stored === "jinrishici" || stored === "quotable" || stored === "builtin") {
    return stored;
  }
  // A fresh/offline-first workspace must not contact a third-party service
  // before the user explicitly opts into an online quote source.
  return "builtin";
}

function loadQuoteStyle(source: DailyQuoteSource): DailyQuoteStyle {
  const stored = readStorage(QUOTE_STYLE_KEY) as DailyQuoteStyle | null;
  return stored && QUOTE_STYLES_BY_SOURCE[source].includes(stored)
    ? stored
    : QUOTE_STYLES_BY_SOURCE[source][0];
}

function recentKey(workspaceId: string | null): string {
  return workspaceId ? `vt:${workspaceId}:recent-tables` : LEGACY_RECENT_KEY;
}

function loadRecentTables(workspaceId: string | null): RecentTable[] {
  try {
    const parsed = JSON.parse(readStorage(recentKey(workspaceId)) ?? "[]") as unknown;
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
  const workspaceStartupPolicy = ref<WorkspaceStartupPolicy>(
    loadWorkspaceStartupPolicy(),
  );
  const activeView = ref<AppView>(startupPage.value);
  const showDailyQuote = ref(readStorage(QUOTE_KEY) !== "false");
  const dailyQuoteSource = ref<DailyQuoteSource>(loadQuoteSource());
  const dailyQuoteStyle = ref<DailyQuoteStyle>(loadQuoteStyle(dailyQuoteSource.value));
  const showMiniCalendar = ref(readStorage(CALENDAR_KEY) !== "false");
  const adminFloatingButton = ref(readStorage(ADMIN_FLOATING_KEY) !== "false");
  const adminConfirmClose = ref(readStorage(ADMIN_CONFIRM_CLOSE_KEY) !== "false");
  const adminReleaseWhenIdle = ref(readStorage(ADMIN_RELEASE_IDLE_KEY) !== "false");
  const density = ref<DensityMode>(loadDensity());
  const locale = ref<Locale>(getLocale());
  const workspaceNamespace = ref<string | null>(null);
  const recentTables = ref<RecentTable[]>(loadRecentTables(null));

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
  function setWorkspaceStartupPolicy(policy: WorkspaceStartupPolicy): void {
    workspaceStartupPolicy.value = policy;
    writeStorage(WORKSPACE_STARTUP_POLICY_KEY, policy);
  }
  function setShowDailyQuote(show: boolean): void {
    showDailyQuote.value = show;
    writeStorage(QUOTE_KEY, String(show));
  }
  function setDailyQuoteSource(source: DailyQuoteSource): void {
    dailyQuoteSource.value = source;
    writeStorage(QUOTE_SOURCE_KEY, source);
    if (!QUOTE_STYLES_BY_SOURCE[source].includes(dailyQuoteStyle.value)) {
      setDailyQuoteStyle(QUOTE_STYLES_BY_SOURCE[source][0]);
    }
  }
  function setDailyQuoteStyle(style: DailyQuoteStyle): void {
    if (!QUOTE_STYLES_BY_SOURCE[dailyQuoteSource.value].includes(style)) return;
    dailyQuoteStyle.value = style;
    writeStorage(QUOTE_STYLE_KEY, style);
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
  function setWorkspaceNamespace(workspaceId: string | null): void {
    workspaceNamespace.value = workspaceId;
    recentTables.value = loadRecentTables(workspaceId);
  }
  function rememberTable(name: string): void {
    const next = [
      { name, openedAt: Date.now() },
      ...recentTables.value.filter((item) => item.name !== name),
    ].slice(0, 5);
    recentTables.value = next;
    writeStorage(recentKey(workspaceNamespace.value), JSON.stringify(next));
  }

  return {
    createModalOpen,
    deleteModalOpen,
    deleteTarget,
    pastePanelOpen,
    shortcutsOpen,
    themeMode,
    startupPage,
    workspaceStartupPolicy,
    activeView,
    showDailyQuote,
    dailyQuoteSource,
    dailyQuoteStyle,
    showMiniCalendar,
    adminFloatingButton,
    adminConfirmClose,
    adminReleaseWhenIdle,
    density,
    locale,
    workspaceNamespace,
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
    setWorkspaceStartupPolicy,
    setShowDailyQuote,
    setDailyQuoteSource,
    setDailyQuoteStyle,
    setShowMiniCalendar,
    setAdminFloatingButton,
    setAdminConfirmClose,
    setAdminReleaseWhenIdle,
    setDensity,
    setLanguage,
    setWorkspaceNamespace,
    rememberTable,
  };
});
