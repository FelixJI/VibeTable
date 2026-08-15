import { afterEach, describe, expect, it, vi } from "vitest";

import { projectPluginTheme } from "./pluginTheme";

describe("projectPluginTheme", () => {
  afterEach(() => vi.restoreAllMocks());

  it("projects explicit CSS variables for a light plugin surface", () => {
    const root = document.createElement("div");
    root.style.setProperty("--vt-bg", "#fafafa");
    root.style.setProperty("--vt-color-primary-500", "#123456");

    const theme = projectPluginTheme({
      themeMode: "light",
      locale: "zh-CN",
      density: "compact",
      root,
    });

    expect(theme.mode).toBe("light");
    expect(theme.variables["--vt-plugin-bg"]).toBe("#fafafa");
    expect(theme.variables["--vt-plugin-primary"]).toBe("#123456");
    expect(theme.variables["--vt-plugin-radius"]).toBe("6px");
  });

  it("uses dark fallbacks for explicit and system dark modes", () => {
    const root = document.createElement("div");
    root.classList.add("dark");

    for (const themeMode of ["dark", "system"] as const) {
      const theme = projectPluginTheme({
        themeMode,
        locale: "en-US",
        density: "comfortable",
        root,
      });
      expect(theme.mode).toBe("dark");
      expect(theme.variables["--vt-plugin-bg"]).toBe("#17191f");
      expect(theme.variables["--vt-plugin-border"]).toBe("#2a2e37");
    }
  });
});
