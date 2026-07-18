import { describe, expect, it } from "vitest";
import {
  TABLE_FIELD_TYPES,
  TABLE_NAME_PATTERN,
  validateFields,
  validateTableName,
} from "./tableAdminValidation";

describe("validateTableName", () => {
  it("accepts a valid identifier", () => {
    expect(validateTableName("projects")).toBeNull();
    expect(validateTableName("my_table_2")).toBeNull();
  });

  it("rejects empty/whitespace", () => {
    expect(validateTableName("")).not.toBeNull();
    expect(validateTableName("   ")).not.toBeNull();
  });

  it("rejects names starting with a digit or underscore", () => {
    expect(validateTableName("1table")).not.toBeNull();
    expect(validateTableName("_private")).not.toBeNull();
  });

  it("rejects names over 64 chars", () => {
    expect(validateTableName("a".repeat(65))).not.toBeNull();
    expect(validateTableName("a".repeat(64))).toBeNull();
  });
});

describe("validateFields", () => {
  it("skips rows whose key is blank", () => {
    const result = validateFields([
      { key: "  ", type: "string" },
      { key: "name", type: "string" },
    ]);
    expect(result.errors).toEqual([]);
    expect(result.fields).toEqual([{ key: "name", type: "string" }]);
  });

  it("rejects a non-blank invalid key and returns no fields for it", () => {
    const result = validateFields([{ key: "1bad", type: "string" }]);
    expect(result.errors.length).toBe(1);
    expect(result.fields).toEqual([]);
  });

  it("trims keys", () => {
    const result = validateFields([{ key: "  name  ", type: "string" }]);
    expect(result.fields).toEqual([{ key: "name", type: "string" }]);
  });
});

describe("TABLE_FIELD_TYPES / TABLE_NAME_PATTERN", () => {
  it("exposes exactly the six backend field types", () => {
    expect(TABLE_FIELD_TYPES).toEqual([
      "string",
      "integer",
      "decimal",
      "date",
      "boolean",
      "text",
    ]);
  });

  it("TABLE_NAME_PATTERN matches a valid name", () => {
    expect(TABLE_NAME_PATTERN.test("good_name1")).toBe(true);
  });
});
