/**
 * B1 Task 6: map the UI-neutral `Editor` discriminated union to Tabulator
 * cell-editor configuration, and provide local validation + value parsing.
 *
 * The editor factory is pure: it reads an `Editor` and a column's validation
 * rules and returns a plain object the grid layer passes to Tabulator. It
 * never touches the DOM directly.
 */

import type {
  Editor,
  NumberEditor,
  SingleSelectEditor,
  MultiSelectEditor,
  DateEditor,
  ValidationRule,
} from "@/contracts";

/** Outcome of local validation for one cell value. */
export interface LocalValidation {
  readonly ok: boolean;
  readonly error?: string;
}

/**
 * Validate a candidate cell value against the column's rules BEFORE sending it
 * to the host. This gives the user immediate feedback (red cell) without a
 * round-trip; the host's authoritative validation is still the final word.
 *
 * Returns `{ ok: true }` when the value is locally acceptable.
 */
export function validateLocally(
  editor: Editor,
  rules: readonly ValidationRule[],
  value: unknown,
  nullable: boolean,
): LocalValidation {
  if (value === null || value === undefined || value === "") {
    if (!nullable && rules.some((r) => r.kind === "required")) {
      return { ok: false, error: "this column is required" };
    }
    return { ok: true };
  }

  // Numeric range.
  if (editor.kind === "number") {
    const n = typeof value === "number" ? value : Number(value);
    if (!Number.isFinite(n)) {
      return { ok: false, error: `${String(value)} is not a valid number` };
    }
    for (const rule of rules) {
      if (rule.kind === "range") {
        const min = rule.minValue as number | null | undefined;
        const max = rule.maxValue as number | null | undefined;
        if (min !== null && min !== undefined && n < min) {
          return { ok: false, error: `value must be >= ${min}` };
        }
        if (max !== null && max !== undefined && n > max) {
          return { ok: false, error: `value must be <= ${max}` };
        }
      }
    }
  }

  // Single-select choice membership.
  if (editor.kind === "single_select") {
    for (const rule of rules) {
      if (rule.kind === "choice") {
        const allowCustom = Boolean(rule.allowCustom);
        if (!allowCustom) {
          const options = rule.options as readonly string[];
          if (!options.includes(String(value))) {
            return { ok: false, error: `${String(value)} is not a valid choice` };
          }
        }
      }
    }
  }

  return { ok: true };
}

/**
 * Parse a raw user-typed string into the storage form for the editor's kind.
 * Used before sending `newValue` to the host so integers are integers, etc.
 */
export function parseValue(editor: Editor, raw: string): unknown {
  if (raw === "") {
    return null;
  }
  if (editor.kind === "number") {
    const n = Number(raw);
    return Number.isFinite(n) ? n : raw;
  }
  if (editor.kind === "boolean") {
    return /^(1|true|yes|y)$/i.test(raw);
  }
  if (editor.kind === "multi_select") {
    // Multi-select is stored as JSON-encoded array by the backend; the grid
    // edits it via a dialog, not inline text, so this is a fallback.
    return raw;
  }
  return raw;
}

/**
 * Produce a Tabulator editor descriptor for the column's editor kind. The grid
 * layer maps this onto the Tabulator `editor` / `editorParams` column props.
 *
 * Tabulator's built-in editors are: "input", "textarea", "number", "tickbox",
 * "select", "datetime". We pick the closest match; the host's dialog-based
 * multi-select and foreign-key pickers are handled by a custom editor the grid
 * wires up separately (Task 6 integration).
 */
export function tabulatorEditor(editor: Editor): {
  editor: string;
  editorParams?: Record<string, unknown>;
} {
  switch (editor.kind) {
    case "number": {
      const params: Record<string, unknown> = {};
      const num = editor as NumberEditor;
      if (num.minValue !== null && num.minValue !== undefined) {
        params.min = num.minValue;
      }
      if (num.maxValue !== null && num.maxValue !== undefined) {
        params.max = num.maxValue;
      }
      return { editor: "number", editorParams: params };
    }
    case "boolean":
      return { editor: "tickbox" };
    case "date": {
      const d = editor as DateEditor;
      return {
        editor: "datetime",
        editorParams: {
          format: d.format ?? "yyyy-MM-dd",
        },
      };
    }
    case "single_select": {
      const s = editor as SingleSelectEditor;
      return {
        editor: "select",
        editorParams: {
          values: ["", ...s.options],
          autocomplete: !s.allowCustom,
        },
      };
    }
    case "multi_select": {
      // Multi-select uses a custom host-driven dialog; the grid registers a
      // placeholder editor that opens it. The descriptor here is a marker the
      // grid recognizes.
      const m = editor as MultiSelectEditor;
      return {
        editor: "vibetable_multi_select",
        editorParams: { options: m.options, allowCustom: m.allowCustom },
      };
    }
    case "text":
    default:
      return { editor: editor.multiline ? "textarea" : "input" };
  }
}
