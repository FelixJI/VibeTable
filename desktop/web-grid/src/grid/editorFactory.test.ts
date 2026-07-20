/**
 * B1 Task 6 tests for editorFactory: local validation, parsing, and Tabulator
 * editor selection for each editor kind.
 */

import { describe, expect, it } from "vitest";
import {
  parseValue,
  tabulatorEditor,
  validateLocally,
} from "./editorFactory";
import type { Editor, ValidationRule } from "@/contracts";

const textEditor: Editor = { kind: "text" };
const numberEditor: Editor = { kind: "number", storage: "decimal" };
const singleSelectEditor: Editor = {
  kind: "single_select",
  options: ["A", "B", "C"],
  allowCustom: false,
};

describe("validateLocally", () => {
  it("accepts a valid text value", () => {
    const result = validateLocally(textEditor, [], "hello", true);
    expect(result.ok).toBe(true);
  });

  it("rejects an empty value when required", () => {
    const rules: ValidationRule[] = [{ kind: "required" }];
    const result = validateLocally(textEditor, rules, "", false);
    expect(result.ok).toBe(false);
    expect(result.error).toMatch(/required/);
  });

  it("accepts null when nullable", () => {
    const result = validateLocally(textEditor, [], null, true);
    expect(result.ok).toBe(true);
  });

  it("accepts a number in range", () => {
    const rules: ValidationRule[] = [
      { kind: "range", minValue: 0, maxValue: 100 },
    ];
    const result = validateLocally(numberEditor, rules, 50, true);
    expect(result.ok).toBe(true);
  });

  it("rejects a number out of range", () => {
    const rules: ValidationRule[] = [
      { kind: "range", minValue: 0, maxValue: 100 },
    ];
    const result = validateLocally(numberEditor, rules, 150, true);
    expect(result.ok).toBe(false);
    expect(result.error).toMatch(/<= 100/);
  });

  it("rejects a non-numeric value for a number editor", () => {
    const result = validateLocally(numberEditor, [], "abc", true);
    expect(result.ok).toBe(false);
  });

  it("rejects a choice not in the single-select options", () => {
    const rules: ValidationRule[] = [
      { kind: "choice", options: ["A", "B", "C"], allowCustom: false },
    ];
    const result = validateLocally(singleSelectEditor, rules, "Z", true);
    expect(result.ok).toBe(false);
  });

  it("accepts a custom choice when allowCustom is true", () => {
    const rules: ValidationRule[] = [
      { kind: "choice", options: ["A", "B", "C"], allowCustom: true },
    ];
    const result = validateLocally(singleSelectEditor, rules, "Z", true);
    expect(result.ok).toBe(true);
  });
});

describe("parseValue", () => {
  it("parses a number string to a number", () => {
    expect(parseValue(numberEditor, "42.5")).toBe(42.5);
  });

  it("returns null for empty string", () => {
    expect(parseValue(textEditor, "")).toBeNull();
  });

  it("parses boolean-ish strings", () => {
    const boolEditor: Editor = { kind: "boolean" };
    expect(parseValue(boolEditor, "true")).toBe(true);
    expect(parseValue(boolEditor, "0")).toBe(false);
  });
});

describe("tabulatorEditor", () => {
  it("returns input for single-line text", () => {
    expect(tabulatorEditor(textEditor).editor).toBe("input");
  });

  it("returns textarea for multiline text", () => {
    expect(tabulatorEditor({ kind: "text", multiline: true }).editor).toBe(
      "textarea",
    );
  });

  it("returns number editor with min/max params", () => {
    const result = tabulatorEditor({
      kind: "number",
      storage: "integer",
      minValue: 0,
      maxValue: 10,
    });
    expect(result.editor).toBe("number");
    expect(result.editorParams?.min).toBe(0);
    expect(result.editorParams?.max).toBe(10);
  });

  it("returns tickbox for boolean", () => {
    expect(tabulatorEditor({ kind: "boolean" }).editor).toBe("tickbox");
  });

  it("returns the work-calendar editor for dates", () => {
    expect(typeof tabulatorEditor({ kind: "date", dateType: "date" }).editor).toBe("function");
  });

  it("returns select for single-select with a blank option", () => {
    const result = tabulatorEditor({
      kind: "single_select",
      options: ["A", "B"],
    });
    expect(result.editor).toBe("select");
    expect(result.editorParams?.values).toEqual(["", "A", "B"]);
  });

  it("returns a custom marker for multi-select", () => {
    const result = tabulatorEditor({
      kind: "multi_select",
      options: ["A"],
    });
    expect(result.editor).toBe("vibetable_multi_select");
  });
});
