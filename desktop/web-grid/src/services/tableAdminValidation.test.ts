import { describe, expect, it } from "vitest";
import {
  TABLE_FIELD_TYPES,
  TABLE_NAME_PATTERN,
  validateFields,
  validateTableName,
} from "./tableAdminValidation";

describe("validateTableName", () => {
  it("accepts Unicode display names", () => {
    expect(validateTableName("projects")).toBeNull();
    expect(validateTableName("2026年客户清单 ✅")).toBeNull();
  });

  it("rejects empty/whitespace", () => {
    expect(validateTableName("")).not.toBeNull();
    expect(validateTableName("   ")).not.toBeNull();
  });

  it("accepts names starting with a digit or underscore", () => {
    expect(validateTableName("1月订单")).toBeNull();
    expect(validateTableName("_草稿")).toBeNull();
  });

  it("rejects names over 64 chars", () => {
    expect(validateTableName("表".repeat(129))).not.toBeNull();
    expect(validateTableName("表".repeat(128))).toBeNull();
  });
});

describe("validateFields", () => {
  it("skips rows whose key is blank", () => {
    const result = validateFields([
      { key: "  ", type: "shortText" },
      { key: "name", type: "shortText" },
    ]);
    expect(result.errors).toEqual([]);
    expect(result.fields).toEqual([{ key: "name", type: "shortText" }]);
  });

  it("rejects a control character and returns no fields for it", () => {
    const result = validateFields([{ key: "bad\nname", type: "shortText" }]);
    expect(result.errors.length).toBe(1);
    expect(result.fields).toEqual([]);
  });

  it("trims keys", () => {
    const result = validateFields([{ key: "  name  ", type: "shortText" }]);
    expect(result.fields).toEqual([{ key: "name", type: "shortText" }]);
  });

  it("accepts Chinese, punctuation and digit-first field names", () => {
    const result = validateFields([
      { key: "联系电话（备用）", type: "shortText" },
      { key: "1月金额", type: "decimal" },
      { key: "备注/说明", type: "longText" },
    ]);
    expect(result.errors).toEqual([]);
    expect(result.fields).toHaveLength(3);
  });

  it("rejects NFKC/case-insensitive duplicates", () => {
    const result = validateFields([
      { key: "Ａ", type: "shortText" },
      { key: "a", type: "shortText" },
    ]);
    expect(result.errors).toContain("同一张表内的字段名称不能重复。");
  });
});

describe("TABLE_FIELD_TYPES / TABLE_NAME_PATTERN", () => {
  it("exposes every normalized product data type", () => {
    expect(TABLE_FIELD_TYPES).toEqual([
      "shortText", "longText", "richText", "boolean",
      "integer", "float", "decimal",
      "date", "dateTime", "autoDate", "time",
      "email", "url", "uuid", "select", "multiSelect",
      "json", "geoPoint", "geoJson", "file",
      "relation", "lookup", "formula", "list", "hash", "secret",
    ]);
  });

  it("TABLE_NAME_PATTERN matches a Unicode display name", () => {
    expect(TABLE_NAME_PATTERN.test("中文名称 ✅")).toBe(true);
  });
});
