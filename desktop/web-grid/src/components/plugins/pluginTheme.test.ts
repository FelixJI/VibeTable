import { afterEach, describe, expect, it, vi } from "vitest";

import { projectPluginTheme } from "./pluginTheme";

describe("projectPluginTheme", () => {
  afterEach(() => vi.restoreAllMocks());

  it("projects explicit CSS variables for a light plugin surface", () => {
    const root = document.createElement("div");
    root.style.setProperty("--vt-bg", "#fafafa");
    root.style.setProperty("--vt-color-primary-500", "#123456");

    const theme = projectPluginTheme({
      effectiveTheme: "light",
      locale: "zh-CN",
      density: "compact",
      root,
    });

    expect(theme.mode).toBe("light");
    expect(theme.variables["--vt-plugin-bg"]).toBe("#fafafa");
    expect(theme.variables["--vt-plugin-primary"]).toBe("#123456");
    expect(theme.variables["--vt-plugin-radius"]).toBe("6px");
  });

  it("uses the projected effective mode regardless of a stale DOM class", () => {
    const root = document.createElement("div");
    root.classList.add("dark");

    for (const effectiveTheme of ["dark", "light"] as const) {
      const theme = projectPluginTheme({
        effectiveTheme,
        locale: "en-US",
        density: "comfortable",
        root,
      });
      expect(theme.mode).toBe(effectiveTheme);
      expect(theme.variables["--vt-plugin-bg"]).toBe(effectiveTheme === "dark" ? "#17191f" : "#ffffff");
      expect(theme.variables["--vt-plugin-border"]).toBe(effectiveTheme === "dark" ? "#2a2e37" : "#e5e6eb");
    }
  });
});
