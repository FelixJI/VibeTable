import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useUiStore } from "./uiStore";

describe("uiStore", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("starts with all panels closed and an unprojected theme default", () => {
    const s = useUiStore();
    expect(s.createModalOpen).toBe(false);
    expect(s.deleteModalOpen).toBe(false);
    expect(s.pastePanelOpen).toBe(false);
    expect(s.shortcutsOpen).toBe(false);
    // "system" is the display default before any Host snapshot; the Host
    // owns the durable preference and the system appearance projection.
    expect(s.themeMode).toBe("system");
    expect(s.systemTheme).toBeNull();
    expect(s.highContrast).toBe(false);
    expect(s.hostThemeSynced).toBe(false);
    expect(localStorage.getItem("vt:theme")).toBeNull();
  });

  it("openCreate/closeCreate toggles createModalOpen", () => {
    const s = useUiStore();
    s.openCreate();
    expect(s.createModalOpen).toBe(true);
    s.closeCreate();
    expect(s.createModalOpen).toBe(false);
  });

  it("openDelete/closeDelete toggles deleteModalOpen", () => {
    const s = useUiStore();
    s.openDelete("users");
    expect(s.deleteModalOpen).toBe(true);
    expect(s.deleteTarget).toBe("users");
    s.closeDelete();
    expect(s.deleteModalOpen).toBe(false);
  });

  it("openShortcuts/closeShortcuts toggles shortcutsOpen", () => {
    const s = useUiStore();
    s.openShortcuts();
    expect(s.shortcutsOpen).toBe(true);
    s.closeShortcuts();
    expect(s.shortcutsOpen).toBe(false);
  });

  it("openSettings lands on the settings view with the requested section and retires plugin apps", () => {
    const s = useUiStore();
    s.openPluginApp("com.example.erp", "app");
    s.openSettings("versions");
    expect(s.activeView).toBe("settings");
    expect(s.activePluginApp).toBeNull();
    expect(s.requestedSettingsSection).toBe("versions");
    // 消费后清除；普通 navigate 不携带分区意图。
    s.settleSettingsSection();
    expect(s.requestedSettingsSection).toBeNull();
    s.openSettings("storage");
    s.navigate("tables");
    expect(s.requestedSettingsSection).toBeNull();
    s.openSettings("storage");
    // 工作区切换时残留意图不得存活。
    s.setWorkspaceNamespace("11111111-1111-4111-8111-111111111111");
    expect(s.requestedSettingsSection).toBeNull();
  });

  it("touchTable persists local last-modified times per workspace namespace", () => {
    const s = useUiStore();
    expect(s.tableModified).toEqual({});
    const at = 1_780_000_000_000;
    s.touchTable("vt_t_01abc", at);
    expect(s.tableModified["vt_t_01abc"]).toBe(at);
    expect(JSON.parse(localStorage.getItem("vt:table-modified")!)).toEqual({ "vt_t_01abc": at });

    const workspaceId = "11111111-1111-4111-8111-111111111111";
    s.setWorkspaceNamespace(workspaceId);
    expect(s.tableModified).toEqual({});
    s.touchTable("vt_t_02def", at + 5);
    expect(JSON.parse(localStorage.getItem(`vt:${workspaceId}:table-modified`)!))
      .toEqual({ "vt_t_02def": at + 5 });
    // 非法输入不落盘。
    s.touchTable("", at);
    expect(Object.keys(s.tableModified)).toEqual(["vt_t_02def"]);
  });

  it("setThemeMode is a local projection and never writes storage", () => {
    const s = useUiStore();
    s.setThemeMode("dark");
    expect(s.themeMode).toBe("dark");
    expect(localStorage.getItem("vt:theme")).toBeNull();

    // A pre-existing legacy value must not be overwritten by local picks.
    localStorage.setItem("vt:theme", "light");
    s.setThemeMode("system");
    expect(s.themeMode).toBe("system");
    expect(localStorage.getItem("vt:theme")).toBe("light");
  });

  it("applyThemeProjection maps Host snapshots onto presentation state", () => {
    const s = useUiStore();
    // Unconfigured Host preference keeps the "system" display default.
    s.applyThemeProjection({ preference: null, effective: "dark", highContrast: false });
    expect(s.themeMode).toBe("system");
    expect(s.systemTheme).toBe("dark");
    expect(s.highContrast).toBe(false);
    expect(s.hostThemeSynced).toBe(true);

    // Explicit values and high contrast arrive the same way.
    s.applyThemeProjection({ preference: "dark", effective: "light", highContrast: true });
    expect(s.themeMode).toBe("dark");
    expect(s.systemTheme).toBe("light");
    expect(s.highContrast).toBe(true);
  });

  it("starts on Home and persists product preferences", () => {
    const s = useUiStore();
    expect(s.activeView).toBe("home");
    expect(s.workspaceStartupPolicy).toBe("lastWorkspace");
    expect(s.dailyQuoteSource).toBe("builtin");
    s.navigate("settings");
    s.setStartupPage("tables");
    s.setWorkspaceStartupPolicy("workspaceCenter");
    s.setShowDailyQuote(false);
    s.setShowMiniCalendar(false);
    s.setAdminFloatingButton(false);
    s.setAdminConfirmClose(false);
    s.setAdminReleaseWhenIdle(false);
    s.setDensity("compact");
    expect(s.activeView).toBe("settings");
    expect(localStorage.getItem("vt:startup-page")).toBe("tables");
    expect(localStorage.getItem("vt:workspace-startup-policy")).toBe("workspaceCenter");
    expect(localStorage.getItem("vt:show-daily-quote")).toBe("false");
    expect(localStorage.getItem("vt:show-mini-calendar")).toBe("false");
    expect(localStorage.getItem("vt:admin-floating-button")).toBe("false");
    expect(localStorage.getItem("vt:admin-confirm-close")).toBe("false");
    expect(localStorage.getItem("vt:admin-release-idle")).toBe("false");
    expect(localStorage.getItem("vt:density")).toBe("compact");
  });

  it("keeps five unique recently opened tables", () => {
    const s = useUiStore();
    for (const name of ["a", "b", "c", "d", "e", "f", "c"]) s.rememberTable(name);
    expect(s.recentTables.map((item) => item.name)).toEqual(["c", "f", "e", "d", "b"]);
  });

  it("persists quote source and keeps its style compatible", () => {
    const s = useUiStore();
    s.setDailyQuoteSource("jinrishici");
    expect(s.dailyQuoteStyle).toBe("poetry");
    s.setDailyQuoteStyle("mixed");
    expect(s.dailyQuoteStyle).toBe("poetry");

    s.setDailyQuoteSource("hitokoto");
    s.setDailyQuoteStyle("lighthearted");
    expect(s.dailyQuoteSource).toBe("hitokoto");
    expect(s.dailyQuoteStyle).toBe("lighthearted");
    expect(localStorage.getItem("vt:daily-quote-source")).toBe("hitokoto");
    expect(localStorage.getItem("vt:daily-quote-style")).toBe("lighthearted");
  });
});
