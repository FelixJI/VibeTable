import type { JsonValue } from "./types.js";

/** Display classification for a raw cell value. `missing`/`null` are distinct
 * from `0`/`false`/`""` so a page can render placeholders without mutating data. */
export type CellValueKind = "missing" | "null" | "boolean" | "number" | "string" | "array" | "object" | "computed";

export interface CellDisplay {
  readonly kind: CellValueKind;
  /** Pure display text derived from the raw value; never written back to data. */
  readonly text: string;
  /** Only set for `computed` cells: false means the projected value is not fresh. */
  readonly fresh: boolean | null;
}

const computedShape = (value: JsonValue): { state: unknown; value: unknown; fresh: unknown } | null => {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const keys = Object.keys(value);
  if (keys.length !== 3 || !keys.includes("state") || !keys.includes("value") || !keys.includes("fresh")) return null;
  return value as { state: unknown; value: unknown; fresh: unknown };
};

/** Describe one raw cell for display. Pure: no locale, no host state, no permissions. */
export function describeCellValue(value: JsonValue | undefined, options?: { readonly computed?: boolean }): CellDisplay {
  if (value === undefined) return { kind: "missing", text: "", fresh: null };
  if (value === null) return { kind: "null", text: "", fresh: null };
  if (typeof value === "boolean") return { kind: "boolean", text: value ? "true" : "false", fresh: null };
  if (typeof value === "number") return { kind: "number", text: String(value), fresh: null };
  if (typeof value === "string") return { kind: "string", text: value, fresh: null };
  if (Array.isArray(value)) return { kind: "array", text: JSON.stringify(value), fresh: null };
  if (options?.computed) {
    // A JSON field that merely contains the three keys stays serialized as-is.
    const cell = computedShape(value);
    if (cell && typeof cell.fresh === "boolean") {
      const inner = describeCellValue(cell.value as JsonValue | undefined);
      return { kind: "computed", text: cell.fresh ? inner.text : "", fresh: cell.fresh };
    }
  }
  return { kind: "object", text: JSON.stringify(value), fresh: null };
}

/** Display text only; see `describeCellValue` for the classified shape. */
export function formatCellValue(value: JsonValue | undefined, options?: { readonly computed?: boolean }): string {
  return describeCellValue(value, options).text;
}

/** True only for `null`/`undefined`; `0`, `false` and `""` are real values. */
export function isMissingCell(value: JsonValue | undefined): boolean {
  return value === null || value === undefined;
}
