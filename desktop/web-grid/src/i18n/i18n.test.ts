import { describe, it, expect, beforeEach } from "vitest";
import { t, setLocale, getLocale } from "./index";
import { TABLE_FIELD_TYPES } from "@/contracts";

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

  it("localizes data import and export success notifications", () => {
    expect(t("dataIo.import.success", { count: 2 })).toBe("已导入 2 行。");
    expect(t("dataIo.export.success", {
      count: 3,
      name: "orders-export.csv",
    })).toBe("已导出 3 行至 orders-export.csv。");
  });

  it("switches locale to en-US", () => {
    setLocale("en-US");
    expect(getLocale()).toBe("en-US");
    expect(document.documentElement.lang).toBe("en-US");
    expect(t("toolbar.rowCount", { count: 42 })).toBe("42 rows");
  });

  it.each(["zh-CN", "en-US"] as const)(
    "translates every supported table field type in %s",
    (locale) => {
      setLocale(locale);
      for (const type of TABLE_FIELD_TYPES) {
        const key = `createTable.fieldType.${type}`;
        expect(t(key), key).not.toBe(key);
        expect(t(key), key).not.toContain("createTable.fieldType.");
      }
    },
  );

  it.each([
    ["zh-CN", "确定移除“收据.pdf”吗？此更改会立即保存。"],
    ["en-US", "Remove “收据.pdf”? This change is saved immediately."],
  ] as const)("localizes attachment removal confirmation in %s", (locale, expected) => {
    setLocale(locale);
    expect(t("attachment.remove.message", { name: "收据.pdf" })).toBe(expected);
    expect(t("attachment.remove.confirm")).not.toBe("attachment.remove.confirm");
    expect(t("attachment.remove.cancel")).not.toBe("attachment.remove.cancel");
  });

  it.each(["zh-CN", "en-US"] as const)(
    "provides safe, localized workspace attachment errors in %s",
    (locale) => {
      setLocale(locale);
      for (const suffix of ["timeout", "cancelled", "picker", "operation", "generic"]) {
        const key = `workspace.attachment.error.${suffix}`;
        const message = t(key);
        expect(message, key).not.toBe(key);
        expect(message).not.toMatch(/request\s*id|requestId|exception|stack/iu);
      }
    },
  );
});
