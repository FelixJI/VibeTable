import { describe, it, expect, beforeEach } from "vitest";
import { t, setLocale, getLocale } from "./index";

describe("i18n", () => {
  beforeEach(() => {
    setLocale("zh-CN");
  });

  it("returns the zh-CN message for a known key", () => {
    expect(t("app.title")).toBe("VibeTable");
  });

  it("falls back to key when message is missing", () => {
    expect(t("nonexistent.key")).toBe("nonexistent.key");
  });

  it("interpolates params", () => {
    expect(t("toolbar.rowCount", { count: 42 })).toBe("42 行");
  });

  it("switches locale to en-US", () => {
    setLocale("en-US");
    expect(getLocale()).toBe("en-US");
    expect(t("toolbar.rowCount", { count: 42 })).toBe("42 rows");
  });
});
