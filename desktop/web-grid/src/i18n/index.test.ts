import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { currentLocale, getLocale, initLocale, setLocale, t } from "./index";

describe("i18n closed locale boundary", () => {
  beforeEach(() => {
    currentLocale.value = "zh-CN";
    localStorage.clear();
  });

  afterEach(() => vi.restoreAllMocks());

  it("initializes only a supported persisted locale and syncs the document", () => {
    localStorage.setItem("vt:locale", "en-US");
    initLocale();
    expect(getLocale()).toBe("en-US");
    expect(document.documentElement.lang).toBe("en-US");

    localStorage.setItem("vt:locale", "unsupported");
    initLocale();
    expect(getLocale()).toBe("en-US");
  });

  it("ignores unsupported setters and tolerates unavailable local storage", () => {
    setLocale("unsupported" as "zh-CN");
    expect(getLocale()).toBe("zh-CN");

    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("blocked");
    });
    expect(() => setLocale("en-US")).not.toThrow();
    expect(getLocale()).toBe("en-US");
  });

  it("uses locale, Chinese, and key fallbacks while preserving missing placeholders", () => {
    setLocale("en-US");
    expect(t("files.selected", { count: 2 })).toContain("2");
    expect(t("files.selected", {})).toContain("{count}");
    expect(t("files.selected")).toContain("{count}");
    expect(t("contract.only.in.zh" )).toBe("contract.only.in.zh");
    expect(t("missing.translation")).toBe("missing.translation");
  });
});
