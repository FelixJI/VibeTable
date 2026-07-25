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

  it("rejects fractional digits beyond a decimal column's scale", () => {
    const editor: Editor = { kind: "number", storage: "decimal", scale: 2 };
    const result = validateLocally(editor, [], "3.14159", true);
    expect(result.ok).toBe(false);
    expect(result.error).toMatch(/2 fractional/);
  });

  it("accepts a decimal value within the column's scale", () => {
    const editor: Editor = { kind: "number", storage: "decimal", scale: 2 };
    expect(validateLocally(editor, [], "3.14", true).ok).toBe(true);
  });

  it("rejects any fractional part for an integer-storage column", () => {
    const editor: Editor = { kind: "number", storage: "integer" };
    const result = validateLocally(editor, [], "3.7", true);
    expect(result.ok).toBe(false);
    expect(result.error).toMatch(/integer/);
  });

  it("accepts an integer value for an integer-storage column", () => {
    const editor: Editor = { kind: "number", storage: "integer" };
    expect(validateLocally(editor, [], "3", true).ok).toBe(true);
  });

  it("treats scale 0 as 'no fractional digits'", () => {
    const editor: Editor = { kind: "number", storage: "decimal", scale: 0 };
    expect(validateLocally(editor, [], "3", true).ok).toBe(true);
    expect(validateLocally(editor, [], "3.5", true).ok).toBe(false);
  });

  it("counts fractional digits on a numeric value, not just strings", () => {
    const editor: Editor = { kind: "number", storage: "decimal", scale: 2 };
    expect(validateLocally(editor, [], 3.141, true).ok).toBe(false);
    expect(validateLocally(editor, [], 3.14, true).ok).toBe(true);
  });

  it("rejects values exceeding the column precision", () => {
    const editor: Editor = {
      kind: "number",
      storage: "decimal",
      scale: 2,
      precision: 4,
    };
    // 4 significant digits allowed: 12.34 ok, 123.45 has 5 -> reject.
    expect(validateLocally(editor, [], "12.34", true).ok).toBe(true);
    expect(validateLocally(editor, [], "123.45", true).ok).toBe(false);
  });

  it("ignores precision/scale when not declared (backward-compatible)", () => {
    // No scale/precision -> the legacy numberEditor must keep old behavior.
    expect(validateLocally(numberEditor, [], "3.14159", true).ok).toBe(true);
  });

  it("expands scientific notation before counting fractional digits", () => {
    const editor: Editor = { kind: "number", storage: "decimal", scale: 2 };
    // 1e-3 == 0.001 -> 3 fractional digits > 2 -> reject.
    expect(validateLocally(editor, [], "1e-3", true).ok).toBe(false);
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

  it("derives a step from scale for the decimal spinner", () => {
    const result = tabulatorEditor({
      kind: "number",
      storage: "decimal",
      scale: 2,
    });
    expect(result.editor).toBe("number");
    expect(result.editorParams?.step).toBeCloseTo(0.01, 10);
  });

  it("uses step 1 for a zero-scale (integer-like) column", () => {
    const result = tabulatorEditor({
      kind: "number",
      storage: "decimal",
      scale: 0,
    });
    expect(result.editorParams?.step).toBe(1);
  });

  it("omits step when scale is not declared", () => {
    const result = tabulatorEditor({
      kind: "number",
      storage: "decimal",
    });
    expect(result.editorParams?.step).toBeUndefined();
  });

  it("returns tickbox for boolean", () => {
    expect(tabulatorEditor({ kind: "boolean" }).editor).toBe("tickbox");
  });

  it("returns the work-calendar editor for dates", () => {
    expect(typeof tabulatorEditor({ kind: "date", dateType: "date" }).editor).toBe("function");
  });

  it("uses Tabulator 6's list editor for single-select values", () => {
    const result = tabulatorEditor({
      kind: "single_select",
      options: ["A", "B"],
    });
    expect(result.editor).toBe("list");
    expect(result.editorParams?.values).toEqual(["", "A", "B"]);
    expect(result.editorParams).toMatchObject({
      autocomplete: true,
      freetext: false,
      clearable: true,
    });
  });

  it("returns Tabulator's real non-autocomplete multi-select list editor", () => {
    const result = tabulatorEditor({
      kind: "multi_select",
      options: ["A"],
    });
    expect(result.editor).toBe("list");
    expect(result.editorParams).toMatchObject({
      values: ["A"],
      multiselect: true,
      autocomplete: false,
    });
  });
});
