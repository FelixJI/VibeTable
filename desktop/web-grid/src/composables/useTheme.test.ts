import { describe, it, expect, beforeEach, vi } from "vitest";
import { nextTick } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { useTheme } from "./useTheme";
import { useUiStore } from "@/stores/uiStore";

describe("useTheme", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    localStorage.clear();
    document.documentElement.className = "";
  });

  it("stays light until the Host projects a dark system theme", () => {
    const { isDark } = useTheme();
    expect(isDark.value).toBe(false);
    useUiStore().applyThemeProjection({
      preference: "system",
      effective: "dark",
      highContrast: false,
    });
    expect(isDark.value).toBe(true);
  });

  it("explicit light/dark overrides the projected system theme", () => {
    const ui = useUiStore();
    const { isDark } = useTheme();
    ui.applyThemeProjection({ preference: "dark", effective: "light", highContrast: false });
    expect(isDark.value).toBe(true);
    ui.applyThemeProjection({ preference: "light", effective: "dark", highContrast: false });
    expect(isDark.value).toBe(false);
  });

  it("toggles dark and high-contrast classes on <html> with live projections", async () => {
    const ui = useUiStore();
    useTheme();
    ui.applyThemeProjection({ preference: "system", effective: "dark", highContrast: false });
    await nextTick();
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(document.documentElement.classList.contains("vt-high-contrast")).toBe(false);
    ui.applyThemeProjection({ preference: "system", effective: "light", highContrast: true });
    await nextTick();
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(document.documentElement.classList.contains("vt-high-contrast")).toBe(true);
  });

  it("never reads matchMedia and stays light until a dark snapshot", () => {
    const matchMediaSpy = vi.fn();
    vi.stubGlobal("matchMedia", matchMediaSpy);
    const ui = useUiStore();
    const { isDark, systemTheme, highContrast } = useTheme();
    expect(isDark.value).toBe(false);
    expect(systemTheme.value).toBeNull();
    expect(highContrast.value).toBe(false);
    ui.applyThemeProjection({ preference: null, effective: "dark", highContrast: true });
    expect(isDark.value).toBe(true);
    expect(highContrast.value).toBe(true);
    expect(matchMediaSpy).not.toHaveBeenCalled();
  });

  it("keeps the DOM class in sync without persisting anything", () => {
    const { mode, isDark } = useTheme();
    useUiStore().setThemeMode("dark");
    expect(mode.value).toBe("dark");
    expect(isDark.value).toBe(true);
    expect(localStorage.getItem("vt:theme")).toBeNull();
  });
});
