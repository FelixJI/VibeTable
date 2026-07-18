/**
 * Read-only Tabulator grid builder.
 *
 * Builds the base grid from a host `TablePage`; mutation and paste controllers
 * attach their behavior separately.
 *
 * Design:
 *   - `buildColumns(page)`  : pure. Maps the backend `ColumnSchema[]` to
 *     Tabulator column definitions, hides `rowKey`
 *     (transport metadata, never a visible column), and leaves decimal raw
 *     values untouched.
 *   - `buildOptions(page)`  : pure. Returns the Tabulator options object:
 *     `selectableRange:true`, data passed through
 *     verbatim (decimals intact).
 *   - `createGrid(element, page)`: side-effectful. Instantiates a Tabulator
 *     on `element` using the above. Returns the instance for teardown/refresh.
 */

import { TabulatorFull } from "tabulator-tables";
import type { TabulatorOptions } from "tabulator-tables";
import type { ColumnSchema, TablePage } from "@/contracts";

/**
 * The hidden `rowKey` field name in the host/WebView contract.
 * Never rendered as a column; present on every row for transport.
 */
export const ROW_KEY_FIELD = "rowKey";

/** A Tabulator column definition (structural — we only set what Phase A needs). */
export interface GridColumnDefinition {
  /** Row-dict key this column reads. */
  readonly field: string;
  /** Heading text. */
  readonly title: string;
  /** Base-grid editability; mutation controllers may supply editors separately. */
  readonly editable: boolean;
  /** Hint for formatters; aligns with backend `ColumnSchema.dataType`. */
  readonly dataType: ColumnSchema["dataType"];
  /**
   * Decimal formatter reads but does NOT mutate the underlying value. We keep
   * a reference here so tests can assert raw data is untouched. The actual
   * Tabulator formatter is a pure display function.
   */
  readonly formatter?:
    | "plaintext"
    | "money"
    | "tickCross"
    | "datetime"
    | "rownum";
  readonly formatterParams?: Record<string, unknown>;
  /** Whether NULL is allowed (display hint). */
  readonly nullable?: boolean;
}

/**
 * Build Tabulator column definitions for a `TablePage`.
 *
 * Pure & total: identical input -> identical output. No DOM access. The grid
 * test exercises this directly.
 */
export function buildColumns(page: TablePage): GridColumnDefinition[] {
  return page.columns.map((col) => toColumnDef(col));
}

function toColumnDef(col: ColumnSchema): GridColumnDefinition {
  // Phase A forces read-only REGARDLESS of what the backend advertises. The
  // backend already sets editable:false, but we defend in depth: a future
  // "editable" column from a misconfigured backend must not become editable
  // in Phase A.
  const def: GridColumnDefinition = {
    field: col.name,
    title: col.title,
    editable: false,
    dataType: col.dataType,
    ...(col.nullable !== undefined ? { nullable: col.nullable } : {}),
  };

  // Light display hints per data type. Formatters are display-only and MUST
  // NOT mutate the underlying cell value (decimals preserved exactly).
  switch (col.dataType) {
    case "decimal":
      // Show numeric value with a thousands separator but DO NOT round or
      // re-store. Tabulator's "money" formatter reads-only.
      return {
        ...def,
        formatter: "money",
        formatterParams: {
          precision: 6, // do not truncate; show full precision
          thousand: ",",
          symbol: "",
        },
      };
    case "boolean":
      return { ...def, formatter: "tickCross" };
    case "date":
      return { ...def, formatter: "datetime" };
    case "integer":
    case "text":
    default:
      return { ...def, formatter: "plaintext" };
  }
}

/**
 * Build Tabulator options for a read-only Phase-A grid.
 *
 * - `selectableRange:true` enables range selection (highlight, copy-out via
 *   the host clipboard later). It does NOT enable paste.
 * - `clipboard:false` DISABLES clipboard entirely (paste is a Phase B2 task).
 *   Defensive: `clipboardPasteAction` is explicitly unset (null) so that even
 *   if a future override turns clipboard on, no paste handler is wired.
 * - `data` carries the rows verbatim; decimals are not reformatted here.
 *   `rowKey` stays in the row dicts but has no column, so it is invisible.
 */
export function buildOptions(page: TablePage): TabulatorOptions {
  // Defensive copy of rows so external mutation cannot leak back into the
  // caller's TablePage. Values (incl. decimals) are NOT transformed.
  const data = page.rows.map((row) => ({ ...row }));

  const options: TabulatorOptions = {
    columns: buildColumns(page) as unknown[],
    data,
    layout: "fitColumns",
    // Read-only Phase A:
    selectableRange: true, // highlight ranges; copy via host later
    clipboard: false, // paste disabled (Phase B2)
    // Explicitly do NOT register a paste action.
    clipboardPasteAction: undefined,
    // Defensive: no edit module interactions.
    editEventQueue: undefined,
    // Header sort off by default for remote-mode pages (Phase A).
    ...(page.mode === "remote" ? { headerSort: false } : {}),
  };

  // Strip the undefined keys so the object is clean for assertion & wire.
  return stripUndefined(options);
}

/**
 * Instantiate a Tabulator grid on `element` using the read-only options
 * derived from `page`. Returns the instance (caller owns lifecycle/teardown).
 */
export function createGrid(
  element: HTMLElement | string,
  page: TablePage,
): TabulatorFull {
  const options = buildOptions(page);
  // Cast: our minimal ambient TabulatorOptions is intentionally permissive;
  // Tabulator's real options object is far larger than Phase A needs.
  return new TabulatorFull(element, options);
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Remove top-level `undefined` entries so callers can assert exact shapes. */
function stripUndefined<T extends Record<string, unknown>>(obj: T): T {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(obj)) {
    if (v !== undefined) out[k] = v;
  }
  return out as T;
}
