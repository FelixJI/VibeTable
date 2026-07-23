/**
 * Tabulator grid builder — read-only by default, editable per-column when an
 * `editSchema` is supplied (Task M3).
 *
 * Builds the base grid from a host `TablePage`; mutation and paste controllers
 * attach their behavior separately.
 *
 * Design:
 *   - `buildColumns(page, editSchema?)`: pure. Maps the backend `ColumnSchema[]`
 *     to Tabulator column definitions, hides `rowKey` (transport metadata,
 *     never a visible column), and leaves decimal raw values untouched. When
 *     `editSchema` is provided, columns the host marks `editable:true` get a
 *     Tabulator editor attached via `editorFactory.tabulatorEditor`;
 *     `multi_select` columns degrade to read-only (no host dialog).
 *   - `buildOptions(page, opts?)`  : pure. Returns the Tabulator options object:
 *     `selectableRange:true`, data passed through verbatim (decimals intact),
 *     and — when `onCellEdited` is supplied — `cellEditing`/`cellEdited`
 *     callbacks that capture oldValue and forward the edit.
 *   - `createGrid(element, page, opts?)`: side-effectful. Instantiates a
 *     Tabulator on `element` using the above. Returns the instance for
 *     teardown/refresh.
 */

import { TabulatorFull } from "tabulator-tables";
import type { TabulatorOptions } from "tabulator-tables";
import type {
  ColumnEditSchema,
  ColumnSchema,
  LookupDefinition,
  NormalizedRelationDescriptor,
  TablePage,
} from "@/contracts";
import { tabulatorEditor, validateLocally } from "./editorFactory";
import type { CalendarDateEditor } from "./calendarDateEditor";
import { lookupFormatter, relationFormatter } from "./relationLookupRenderer";

/**
 * The hidden `rowKey` field name in the host/WebView contract.
 * Never rendered as a column; present on every row for transport.
 */
export const ROW_KEY_FIELD = "rowKey";
/** Synthetic narrow row-number column used for explicit whole-row selection. */
export const ROW_NUMBER_FIELD = "__vt_row_number";

/** A Tabulator column definition (structural — we only set what we use). */
export interface GridColumnDefinition {
  /** Row-dict key this column reads. */
  readonly field: string;
  /** Heading text. */
  readonly title: string;
  /** Whether Tabulator should allow inline editing of this column. */
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
    | "rownum"
    | ((cell: { getValue(): unknown }) => HTMLElement);
  readonly formatterParams?: Record<string, unknown>;
  /** Whether NULL is allowed (display hint). */
  readonly nullable?: boolean;
  /**
   * Tabulator editor name (e.g. "input", "number", "tickbox", "select",
   * "datetime"). Present only on editable columns; undefined means read-only.
   * Set via `editorFactory.tabulatorEditor` from the host's `Editor` spec.
   */
  readonly editor?: string | CalendarDateEditor;
  /** Tabulator editor params (e.g. `{ min, max }`, `{ values, autocomplete }`). */
  readonly editorParams?: Record<string, unknown>;
  readonly width?: number;
  readonly minWidth?: number;
  readonly frozen?: boolean;
  readonly headerSort?: boolean;
  readonly headerFilter?: boolean | string;
  readonly resizable?: boolean;
  readonly hozAlign?: "left" | "center" | "right";
  readonly cssClass?: string;
  readonly cellDblClick?: (event: MouseEvent, cell: TabulatorCellLike) => void;
}

export interface RelationLookupGridContext {
  readonly relations: ReadonlyMap<string, NormalizedRelationDescriptor>;
  readonly lookups: ReadonlyMap<string, LookupDefinition>;
  readonly relationEditAvailable: boolean;
  readonly lookupQueryAvailable: boolean;
  readonly lookupUnavailableReason?: string | null;
  readonly onRelationEditRequested?: (
    rowKey: string | number,
    column: string,
    descriptor: NormalizedRelationDescriptor,
    value: unknown,
  ) => void;
}

/**
 * Callback shape for an inline cell edit. Forwarded by `cellEdited` after
 * oldValue is captured in `cellEditing`. Routed by `useTabulator` ->
 * `WorkspaceView` to `mutationService.updateCell`.
 */
export type CellEditedHandler = (
  rowKey: number | string,
  column: string,
  oldValue: unknown,
  newValue: unknown,
) => void;

/**
 * Reported when an inline edit fails local validation (e.g. a value with too
 * many fractional digits for the column's scale). The grid has already rolled
 * the cell back to its pre-edit value; the host surfaces this for UX feedback
 * (toast/banner) since the change was never forwarded to the mutation service.
 */
export type CellValidationErrorHandler = (
  rowKey: number | string,
  column: string,
  error: string,
) => void;

/** Minimal Tabulator CellComponent surface our wiring relies on. */
interface TabulatorCellLike {
  getField(): string;
  getValue(): unknown;
  setValue(value: unknown): void;
  getRow(): { getData(): Record<string, unknown> };
}

/**
 * Build Tabulator column definitions for a `TablePage`.
 *
 * Pure & total: identical input -> identical output. No DOM access. The grid
 * test exercises this directly.
 *
 * When `editSchema` is provided, each column whose matching `ColumnEditSchema`
 * entry is `editable:true` AND whose `editor.kind !== "multi_select"` gets a
 * Tabulator editor attached. multi_select columns degrade to read-only
 * (web-grid ships no host dialog for them); columns absent from `editSchema`
 * or flagged `editable:false` stay read-only.
 */
export function buildColumns(
  page: TablePage,
  editSchema?: readonly ColumnEditSchema[] | null,
  relationLookup?: RelationLookupGridContext | null,
): GridColumnDefinition[] {
  const editByName = new Map(
    (editSchema ?? []).map((c) => [c.name, c] as const),
  );
  const dataColumns = page.columns.map((col) => {
    const def = toColumnDef(col, relationLookup);
    if (col.kind === "lookup") return { ...def, editable: false };
    if (col.kind === "relation") {
      const relation = col.relationId ? relationLookup?.relations.get(col.relationId) : undefined;
      const editable = !!(
        relation
        && relation.state === "valid"
        && col.editable
        && relationLookup?.relationEditAvailable
        && relationLookup.onRelationEditRequested
      );
      return {
        ...def,
        editable: false,
        ...(editable ? {
          cssClass: "vt-relation-cell vt-relation-cell--editable",
          cellDblClick: (_event: MouseEvent, cell: TabulatorCellLike) => {
            const rowKey = cell.getRow().getData()[ROW_KEY_FIELD] as string | number;
            relationLookup.onRelationEditRequested?.(rowKey, col.name, relation, cell.getValue());
          },
        } : { cssClass: "vt-relation-cell" }),
      };
    }
    const edit = editByName.get(col.name);
    // multi_select degrades: no host dialog in web-grid (spec §7.3).
    const editable = !!edit?.editable && edit.editor.kind !== "multi_select";
    if (editable && edit) {
      const ed = tabulatorEditor(edit.editor);
      return {
        ...def,
        editable: true,
        editor: ed.editor,
        ...(ed.editorParams ? { editorParams: ed.editorParams } : {}),
      };
    }
    return { ...def, editable: false };
  });
  return dataColumns;
}

/** Add the synthetic row-number gutter used to select exactly one whole row. */
export function buildGridColumns(
  page: TablePage,
  editSchema?: readonly ColumnEditSchema[] | null,
  relationLookup?: RelationLookupGridContext | null,
): GridColumnDefinition[] {
  const rowNumber: GridColumnDefinition = {
    field: ROW_NUMBER_FIELD,
    title: "",
    editable: false,
    dataType: "integer",
    formatter: "rownum",
    width: 42,
    minWidth: 42,
    frozen: true,
    headerSort: false,
    resizable: false,
    hozAlign: "center",
    cssClass: "vt-row-number",
  };
  return [rowNumber, ...buildColumns(page, editSchema, relationLookup)];
}

function toColumnDef(
  col: ColumnSchema,
  relationLookup?: RelationLookupGridContext | null,
): GridColumnDefinition {
  // Phase A forces read-only REGARDLESS of what the backend advertises. The
  // backend already sets editable:false, but we defend in depth: a future
  // "editable" column from a misconfigured backend must not become editable
  // in Phase A.
  const def: GridColumnDefinition = {
    field: col.name,
    title: col.title,
    editable: false,
    dataType: col.dataType,
    headerFilter: "input",
    ...(col.nullable !== undefined ? { nullable: col.nullable } : {}),
  };

  if (col.kind === "relation") {
    const relation = col.relationId ? relationLookup?.relations.get(col.relationId) : undefined;
    return {
      ...def,
      formatter: relation
        ? relationFormatter(relation)
        : () => {
            const node = document.createElement("span");
            node.className = "vt-lookup-state vt-lookup-state--invalid";
            node.textContent = "关系无效";
            return node;
          },
    };
  }
  if (col.kind === "lookup") {
    const lookup = col.lookupId ? relationLookup?.lookups.get(col.lookupId) : undefined;
    return {
      ...def,
      editable: false,
      formatter: lookupFormatter(
        lookup,
        !!relationLookup?.lookupQueryAvailable,
        relationLookup?.lookupUnavailableReason,
      ),
      cssClass: "vt-lookup-cell",
    };
  }

  // Light display hints per data type. Formatters are display-only and MUST
  // NOT mutate the underlying cell value (decimals preserved exactly).
  switch (col.dataType) {
    case "decimal":
      // Show numeric value with a thousands separator but DO NOT round or
      // re-store. Tabulator's "money" formatter reads-only. Precision follows
      // the column's declared scale when Directus reports one (e.g. 2 for a
      // money column), falling back to 6 so high-precision values are not
      // truncated on display.
      return {
        ...def,
        formatter: "money",
        formatterParams: {
          precision: col.scale ?? 6,
          thousand: ",",
          symbol: "",
        },
      };
    case "boolean":
      return { ...def, formatter: "tickCross" };
    case "date":
    case "datetime":
      return { ...def, formatter: "datetime" };
    case "time":
      return { ...def, formatter: "plaintext" };
    case "integer":
    case "text":
    default:
      return { ...def, formatter: "plaintext" };
  }
}

/**
 * Module-level cache of pre-edit cell values, keyed by `${rowKey}:${field}`.
 *
 * Tabulator fires `cellEditing` BEFORE the value changes (so `cell.getValue()`
 * is still the old value) and `cellEdited` AFTER (so the old value is gone).
 * We stash the old value here between the two callbacks so `cellEdited` can
 * forward `(rowKey, column, oldValue, newValue)` to the host.
 *
 * Module-scoped (not per-grid): there is only one grid per WebView, so a single
 * map is sufficient and avoids plumbing a closure through buildOptions. Entries
 * are deleted in `cellEdited` as they're consumed; the map never grows beyond
 * the number of in-flight edits (typically one).
 */
const editingOldValues = new Map<string, unknown>();

/**
 * Build Tabulator options for a grid that is read-only unless `onCellEdited`
 * is supplied (Task M3: enabling edits).
 *
 * - `selectableRange:true` enables range selection (highlight, copy-out via
 *   the host clipboard later). It does NOT enable paste.
 * - `clipboard:false` DISABLES clipboard entirely (paste is a Phase B2 task).
 *   Defensive: `clipboardPasteAction` is explicitly unset (null) so that even
 *   if a future override turns clipboard on, no paste handler is wired.
 * - `data` carries the rows verbatim; decimals are not reformatted here.
 *   `rowKey` stays in the row dicts but has no column, so it is invisible.
 * - When `onCellEdited` is supplied, `cellEditing` captures the pre-edit value
 *   and `cellEdited` forwards `(rowKey, column, oldValue, newValue)` to it.
 *   Tabulator still gates editing per-column via each column's `editable` flag
 *   (set in `buildColumns`); these callbacks only fire for editable cells.
 * - When `editSchema` is supplied, columns are built with editors attached.
 */
export function buildOptions(
  page: TablePage,
  opts?: {
    editSchema?: readonly ColumnEditSchema[] | null;
    onCellEdited?: CellEditedHandler;
    onValidationError?: CellValidationErrorHandler;
    relationLookup?: RelationLookupGridContext | null;
  },
): TabulatorOptions {
  // Defensive copy of rows so external mutation cannot leak back into the
  // caller's TablePage. Values (incl. decimals) are NOT transformed.
  const data = page.rows.map((row) => ({ ...row }));

  const onCellEdited = opts?.onCellEdited;
  const onValidationError = opts?.onValidationError;
  // Column editor lookup so cellEdited can validate against the column's
  // scale/precision before forwarding to the mutation service. Built once here
  // and shared with buildColumns above.
  const editByName = new Map(
    (opts?.editSchema ?? []).map((c) => [c.name, c] as const),
  );

  const options: TabulatorOptions = {
    columns: buildGridColumns(page, opts?.editSchema, opts?.relationLookup) as unknown[],
    data,
    layout: "fitColumns",
    // Read-only Phase A:
    selectableRange: true, // highlight ranges; copy via host later
    clipboard: false, // paste disabled (Phase B2)
    // Explicitly do NOT register a paste action.
    clipboardPasteAction: undefined,
    // Defensive: no edit module interactions.
    editEventQueue: undefined,
    // In remote mode Tabulator records the user's sort/filter AST but does not
    // reorder the currently loaded page. useTabulator forwards the events to
    // the host/Lookup authoritative full-dataset query pipeline.
    ...(page.mode === "remote" ? { sortMode: "remote", filterMode: "remote" } : {}),
    // Task M3: editable grid wiring. Only registered when the caller cares
    // about edits (keeps the read-only Phase-A options object clean).
    ...(onCellEdited
      ? {
          // cellEditing fires BEFORE Tabulator commits the new value, so the
          // cell still holds the OLD value. Cache it for cellEdited.
          cellEditing: (cell: TabulatorCellLike) => {
            const row = cell.getRow().getData();
            const rowKey = row[ROW_KEY_FIELD] as number | string;
            editingOldValues.set(`${rowKey}:${cell.getField()}`, cell.getValue());
          },
          // cellEdited fires AFTER the value is committed; oldValue is gone
          // from the cell, so retrieve it from the cache built in cellEditing.
          cellEdited: (cell: TabulatorCellLike) => {
            const row = cell.getRow().getData();
            const rowKey = row[ROW_KEY_FIELD] as number | string;
            const field = cell.getField();
            const key = `${rowKey}:${field}`;
            const oldValue = editingOldValues.get(key);
            editingOldValues.delete(key);
            const newValue = cell.getValue();

            // Validate locally BEFORE forwarding. This blocks edits that the DB
            // would silently truncate (e.g. 3.14159 into a 2-digit decimal
            // column) and rolls the cell back to its pre-edit value. The host's
            // authoritative validation still runs server-side as a backstop.
            const editCol = editByName.get(field);
            if (editCol) {
              const result = validateLocally(
                editCol.editor,
                editCol.validation,
                newValue,
                editCol.nullable,
              );
              if (!result.ok) {
                cell.setValue(oldValue);
                onValidationError?.(rowKey, field, result.error ?? "invalid value");
                return;
              }
            }
            onCellEdited(rowKey, field, oldValue, newValue);
          },
        }
      : {}),
  };

  // Strip the undefined keys so the object is clean for assertion & wire.
  return stripUndefined(options);
}

/**
 * Instantiate a Tabulator grid on `element` using the options derived from
 * `page`. Returns the instance (caller owns lifecycle/teardown).
 *
 * Pass `{ editSchema, onCellEdited }` to enable per-column editing and route
 * committed edits to the mutation service.
 */
export function createGrid(
  element: HTMLElement | string,
  page: TablePage,
  opts?: {
    editSchema?: readonly ColumnEditSchema[] | null;
    onCellEdited?: CellEditedHandler;
    onValidationError?: CellValidationErrorHandler;
    relationLookup?: RelationLookupGridContext | null;
  },
): TabulatorFull {
  const options = buildOptions(page, opts);
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
