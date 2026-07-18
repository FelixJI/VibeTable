import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useTheme } from "./useTheme";
import { useUiStore } from "@/stores/uiStore";

describe("useTheme", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    document.documentElement.className = "";
    // Force system dark = false for deterministic tests.
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: false,
      media: q,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
  });

  it("defaults to light when mode=system and system is light", () => {
    const ui = useUiStore();
    ui.setThemeMode("system");
    const { isDark } = useTheme();
    expect(isDark.value).toBe(false);
  });

  it("isDark true when mode=dark", () => {
    const ui = useUiStore();
    ui.setThemeMode("dark");
    const { isDark } = useTheme();
    expect(isDark.value).toBe(true);
  });

  it("toggles dark class on <html>", () => {
    const ui = useUiStore();
    ui.setThemeMode("dark");
    useTheme();
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });
});
