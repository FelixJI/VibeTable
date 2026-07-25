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
import { createCalendarDateEditor, type CalendarDateEditor } from "./calendarDateEditor";

/** Outcome of local validation for one cell value. */
export interface LocalValidation {
  readonly ok: boolean;
  readonly error?: string;
}

/**
 * Reject numeric input that exceeds the column's declared precision/scale.
 *
 * Decimal storage enforces `scale` (max digits after the decimal point) and
 * `precision` (max significant digits); integer storage rejects any fractional
 * part. Counting is done on the decimal expansion of the value as a string so
 * no floating-point rounding is introduced — consistent with the grid's "keep
 * decimals exact" rule. A finite number has already been confirmed by the
 * caller; non-finite input is treated as "no precision opinion".
 *
 * Scientific notation (e.g. `1.23e2`) is normalized via `Number` then
 * `String`, which yields the plain-decimal expansion for values within JS
 * safe range.
 */
function checkNumberScale(editor: NumberEditor, value: unknown): LocalValidation {
  const scale = editor.scale ?? null;
  const precision = editor.precision ?? null;
  // No declared constraints -> nothing to check.
  if (scale === null && precision === null && editor.storage !== "integer") {
    return { ok: true };
  }

  // Normalize to a plain-decimal string (no exponent). `Number` → `String`
  // expands scientific notation; a string that is already plain decimal is
  // used verbatim. Non-finite values slip through as the raw string, which the
  // digit regex below simply won't match — treated as unconstrained.
  const numeric = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(numeric)) {
    return { ok: true };
  }
  let text = typeof value === "string" ? value.trim() : String(numeric);
  // If the raw text carries an exponent, expand it so digit counting is exact.
  if (/e/i.test(text)) {
    text = String(numeric);
  }
  // Strip a leading sign and the decimal point for digit counting.
  const negative = text.startsWith("-");
  const unsigned = negative ? text.slice(1) : text.replace(/^\+/, "");
  const dotIndex = unsigned.indexOf(".");
  const intPart = dotIndex >= 0 ? unsigned.slice(0, dotIndex) : unsigned;
  const fracPart = dotIndex >= 0 ? unsigned.slice(dotIndex + 1) : "";
  // Guard against unexpected shapes (e.g. trailing non-digits): if the parts
  // are not pure digits we cannot reason about precision, so do not block.
  if (!/^\d*$/.test(intPart) || !/^\d*$/.test(fracPart)) {
    return { ok: true };
  }
  const fracDigits = fracPart.length;
  const intDigits = intPart.replace(/^0+/, "").length; // leading zeros aren't significant

  if (editor.storage === "integer" && fracDigits > 0) {
    return { ok: false, error: "this column is an integer; fractional digits are not allowed" };
  }
  if (scale !== null && fracDigits > scale) {
    return {
      ok: false,
      error:
        scale === 0
          ? "this column does not allow fractional digits"
          : `this column allows at most ${scale} fractional digit${scale === 1 ? "" : "s"}`,
    };
  }
  if (precision !== null) {
    const significant = intDigits + fracDigits;
    if (significant > precision) {
      return { ok: false, error: `this column allows at most ${precision} significant digits` };
    }
  }
  return { ok: true };
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
    // Scale/precision guard: reject input that exceeds the column's declared
    // precision rather than letting the DB silently truncate it. Count digits
    // on the source string to avoid floating-point rounding artifacts (the grid
    // keeps decimals exact by never rounding in the data layer).
    const scaleCheck = checkNumberScale(editor, value);
    if (!scaleCheck.ok) {
      return scaleCheck;
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

  if (editor.kind === "multi_select") {
    if (!Array.isArray(value)) {
      return { ok: false, error: "this column requires a list of choices" };
    }
    if (!editor.allowCustom) {
      const invalid = value.find((item) => !editor.options.includes(String(item)));
      if (invalid !== undefined) {
        return { ok: false, error: `${String(invalid)} is not a valid choice` };
      }
    }
  }

  if (editor.kind === "json") {
    if (typeof value === "string") {
      try {
        JSON.parse(value);
      } catch {
        return { ok: false, error: "this column requires valid JSON" };
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
    try {
      const parsed = JSON.parse(raw) as unknown;
      return Array.isArray(parsed) ? parsed : raw;
    } catch {
      return raw.split(",").map((item) => item.trim()).filter(Boolean);
    }
  }
  if (editor.kind === "json") {
    try {
      return JSON.parse(raw) as unknown;
    } catch {
      return raw;
    }
  }
  return raw;
}

/**
 * Produce a Tabulator editor descriptor for the column's editor kind. The grid
 * layer maps this onto the Tabulator `editor` / `editorParams` column props.
 *
 * Tabulator's built-in editors are: "input", "textarea", "number", "tickbox",
 * and "list". We pick the closest match; the host's dialog-based
 * multi-select and foreign-key pickers are handled by a custom editor the grid
 * wires up separately (Task 6 integration).
 */
export function tabulatorEditor(editor: Editor): {
  editor: string | CalendarDateEditor;
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
      // Hint the spinner/stepper to the column's scale so the increment matches
      // the smallest representable unit (e.g. step 0.01 for a 2-digit money
      // column). This is a soft hint only; hard enforcement happens in
      // validateLocally via checkNumberScale.
      if (num.scale !== null && num.scale !== undefined) {
        params.step = num.scale <= 0 ? 1 : Number((10 ** -num.scale).toPrecision(15));
      }
      return { editor: "number", editorParams: params };
    }
    case "boolean":
      return { editor: "tickbox" };
    case "date": {
      const d = editor as DateEditor;
      return {
        editor: createCalendarDateEditor(d.dateType),
      };
    }
    case "single_select": {
      const s = editor as SingleSelectEditor;
      return {
        editor: "list",
        editorParams: {
          values: ["", ...s.options],
          autocomplete: true,
          freetext: s.allowCustom === true,
          clearable: true,
        },
      };
    }
    case "multi_select": {
      const m = editor as MultiSelectEditor;
      return {
        editor: "list",
        editorParams: {
          values: m.options,
          multiselect: true,
          autocomplete: false,
          clearable: true,
        },
      };
    }
    case "json":
      // JSON uses the product modal editor opened by GridHost. Keep the
      // Tabulator cell read-only so raw text can never bypass structured
      // parsing and server validation.
      return { editor: "input" };
    case "text":
    default:
      return { editor: editor.multiline ? "textarea" : "input" };
  }
}
