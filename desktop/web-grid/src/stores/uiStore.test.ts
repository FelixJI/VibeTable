import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useUiStore } from "./uiStore";

describe("uiStore", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("starts with all panels closed and theme=system", () => {
    const s = useUiStore();
    expect(s.createModalOpen).toBe(false);
    expect(s.deleteModalOpen).toBe(false);
    expect(s.pastePanelOpen).toBe(false);
    expect(s.shortcutsOpen).toBe(false);
    expect(s.themeMode).toBe("system");
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

  it("setThemeMode persists to localStorage", () => {
    const s = useUiStore();
    s.setThemeMode("dark");
    expect(s.themeMode).toBe("dark");
    expect(localStorage.getItem("vt:theme")).toBe("dark");
  });

  it("starts on Home and persists product preferences", () => {
    const s = useUiStore();
    expect(s.activeView).toBe("home");
    s.navigate("settings");
    s.setStartupPage("tables");
    s.setShowDailyQuote(false);
    s.setShowMiniCalendar(false);
    s.setAdminFloatingButton(false);
    s.setAdminConfirmClose(false);
    s.setAdminReleaseWhenIdle(false);
    s.setDensity("compact");
    expect(s.activeView).toBe("settings");
    expect(localStorage.getItem("vt:startup-page")).toBe("tables");
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
