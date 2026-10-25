import { useWorkCalendarStore } from "@/stores/workCalendarStore";
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
	LookupSourcePageIntent,
  LookupValueProvenance,
  NormalizedRelationDescriptor,
  TablePage,
} from "@/contracts";
import { tabulatorEditor, validateLocally } from "./editorFactory";
import type { CalendarDateEditor } from "./calendarDateEditor";
import { isRowIdColumn } from "./gridState";
import { lookupFormatter, relationFormatter } from "./relationLookupRenderer";
import { formatFormulaDisplayValue, formulaStateLabel } from "./computedValueDisplay";
import { formatFieldDisplay, enumDisplayParts, progressDisplay } from "./commonFieldDisplay";
import { getLocale, t } from "@/i18n";

/**
 * The hidden `rowKey` field name in the host/WebView contract.
 * Never rendered as a column; present on every row for transport.
 */
export const ROW_KEY_FIELD = "rowKey";
/** Synthetic narrow row-number column used for explicit whole-row selection. */
export const ROW_NUMBER_FIELD = "__vt_row_number";

/**
 * Preserve a readable scan width for each product field family. Tabulator's
 * `fitColumns` layout respects these floors and exposes horizontal scrolling
 * once the combined minimum width exceeds the viewport. 新建列的默认宽度
 * 同样取家族紧凑值，避免 fitColumns 把少量字段拉伸铺满视口；用户拖宽后
 * 的宽度由视图持久化覆盖，优先于该默认值。
 */
function columnMinWidth(column: ColumnSchema): number {
  if (column.kind === "attachment") return 170;
  if (column.kind === "relation" || column.kind === "lookup") return 150;
  switch (column.dataType) {
    case "boolean":
      return 84;
    case "integer":
    case "decimal":
    case "time":
      return 104;
    case "date":
      return 116;
    case "datetime":
      return 156;
    case "json":
      return 168;
    case "text":
    default:
      return 136;
  }
}

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
    | GridCellFormatter;
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
  readonly widthGrow?: number;
  readonly minWidth?: number;
  readonly visible?: boolean;
  readonly frozen?: boolean;
  readonly headerSort?: boolean;
  readonly headerFilter?: boolean | string;
  readonly headerFilterPlaceholder?: string;
  readonly headerFilterParams?: Record<string, unknown>;
  readonly headerFilterFunc?: (headerValue: unknown, rowValue: unknown) => boolean;
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
  readonly onLookupSourceRequested?: (source: LookupValueProvenance) => void;
	readonly onLookupSourcePageRequested?: (intent: LookupSourcePageIntent) => void;
  readonly onAttachmentOpenRequested?: (
    rowKey: string | number,
    column: ColumnSchema,
    trigger: HTMLElement | null,
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
  expectedDigest: string | null,
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
  getElement?(): HTMLElement;
}

type GridCellFormatter = (
  cell: TabulatorCellLike,
  formatterParams?: Record<string, unknown>,
  onRendered?: (callback: () => void) => void,
) => HTMLElement;

function interactiveFormatter(
  formatter: (cell: { getValue(): unknown }) => HTMLElement,
  label: string,
): GridCellFormatter {
  return (cell, _formatterParams, onRendered) => {
    const configureCell = () => {
      const element = cell.getElement?.();
      if (!element) return;
      element.tabIndex = 0;
      element.classList.add("vt-structured-cell");
      element.setAttribute("aria-label", label);
      element.setAttribute("aria-haspopup", "dialog");
      element.setAttribute("aria-keyshortcuts", "Enter Space Shift+F10");
    };
    if (onRendered) onRendered(configureCell);
    else configureCell();
    return formatter(cell);
  };
}

/**
 * Build Tabulator column definitions for a `TablePage`.
 *
 * Pure & total: identical input -> identical output. No DOM access. The grid
 * test exercises this directly.
 *
 * When `editSchema` is provided, each column whose matching `ColumnEditSchema`
 * entry is `editable:true` gets a Tabulator editor attached. JSON uses the
 * structured modal owned by WorkspaceView and therefore stays read-only in
 * Tabulator itself; columns absent from `editSchema` or flagged
 * `editable:false` stay read-only.
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
    // System fields (including createdAt/updatedAt) are server-owned even if
    // a stale or malicious edit schema advertises a scalar editor.
    if (col.kind === "system") return { ...def, editable: false };
    // Managed attachments are edited exclusively through the native File
    // boundary in WorkspaceView.  The host edit schema describes their
    // underlying storage as text, but that must never replace the attachment
    // formatter/double-click action with Tabulator's scalar text editor.
    if (col.kind === "attachment") return { ...def, editable: false };
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
          formatter: interactiveFormatter(
            def.formatter as (cell: { getValue(): unknown }) => HTMLElement,
            t("grid.cell.editRelation", { column: col.title }),
          ),
          cellDblClick: (_event: MouseEvent, cell: TabulatorCellLike) => {
            const rowKey = cell.getRow().getData()[ROW_KEY_FIELD] as string | number;
            relationLookup.onRelationEditRequested?.(rowKey, col.name, relation, cell.getValue());
          },
        } : { cssClass: "vt-relation-cell" }),
      };
    }
    const edit = editByName.get(col.name);
    if (col.dataType === "json") {
      const editable = !!edit?.editable;
      return {
        ...def,
        editable: false,
        ...(editable
          ? {
              cssClass: "vt-json-cell vt-structured-cell",
              formatter: interactiveFormatter(
                def.formatter as (cell: { getValue(): unknown }) => HTMLElement,
                t("grid.cell.editJson", { column: col.title }),
              ),
            }
          : {}),
      };
    }
    const editable = !!edit?.editable && edit.editor.kind !== "json";
    if (editable && edit) {
      const ed = tabulatorEditor(edit.editor, () => { const calendar = useWorkCalendarStore(); return calendar.available ? calendar.overrides : null; });
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

/**
 * Convert product column definitions to the exact Tabulator surface.
 * Product-only metadata must never cross this boundary: Tabulator logs a
 * warning for every unknown key and obscures real renderer diagnostics.
 */
export function buildTabulatorColumns(
  page: TablePage,
  editSchema?: readonly ColumnEditSchema[] | null,
  relationLookup?: RelationLookupGridContext | null,
): Array<Omit<GridColumnDefinition, "dataType" | "nullable">> {
  return buildGridColumns(page, editSchema, relationLookup)
    .map(({ dataType: _dataType, nullable: _nullable, ...column }) => column);
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
    width: columnMinWidth(col),
    widthGrow: 0,
    minWidth: columnMinWidth(col),
    headerFilter: "input",
    headerFilterPlaceholder: t("grid.filter.placeholder"),
    headerFilterParams: {
      elementAttributes: {
        "aria-label": t("grid.filter.ariaLabel", { column: col.title }),
      },
    },
    ...(col.nullable !== undefined ? { nullable: col.nullable } : {}),
    // 行 ID 默认隐藏：保持与视图默认可见列一致，可由视图重新显示。
    ...(isRowIdColumn(col) ? { visible: false } : {}),
  };

  if (col.kind === "attachment") {
    const actionable = !!(
      col.attachmentPolicy
      && relationLookup?.onAttachmentOpenRequested
    );
    return {
      ...def,
      editable: false,
      formatter: actionable
        ? interactiveFormatter(
            attachmentFormatter,
            t("grid.cell.openAttachment", { column: col.title }),
          )
        : attachmentFormatter,
      cssClass: actionable
        ? "vt-attachment-cell vt-structured-cell"
        : "vt-attachment-cell",
      ...(actionable
        ? {
            cellDblClick: (event: MouseEvent, cell: TabulatorCellLike) => {
              const rowKey = cell.getRow().getData()[ROW_KEY_FIELD] as string | number;
              event.preventDefault();
              const trigger = cell.getElement?.() ?? null;
              trigger?.blur();
              relationLookup.onAttachmentOpenRequested?.(rowKey, col, trigger);
            },
          }
        : {}),
    };
  }
  if (col.kind === "relation") {
    const relation = col.relationId ? relationLookup?.relations.get(col.relationId) : undefined;
    return {
      ...def,
      formatter: relation
        ? relationFormatter(relation, col.name)
        : () => {
            const node = document.createElement("span");
            node.className = "vt-lookup-state vt-lookup-state--invalid";
            node.textContent = t("grid.relation.invalid");
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
        relationLookup?.onLookupSourceRequested,
		relationLookup?.onLookupSourcePageRequested,
        col.display,
      ),
      cssClass: "vt-lookup-cell",
    };
  }
  if (col.kind === "formula") {
    return {
      ...def,
      editable: false,
      // Formula result and element types come from the compiler projection;
      // row values never decide whether a result receives numeric formatting.
      formatter: (cell: { getValue(): unknown }) => formulaValueFormatter(cell, col),
      cssClass: "vt-formula-cell",
    };
  }

  // Light display hints per data type. Formatters are display-only and MUST
  // NOT mutate the underlying cell value (decimals preserved exactly).
  switch (col.dataType) {
    case "decimal":
    case "integer":
      // With the authoritative DisplaySpec the shared numeric formatter owns
      // scale/grouping/currency/percent/unit; non-number values fall back to
      // the raw rendering and are never coerced.
      if (col.display) {
        return {
          ...def,
          formatter: commonFieldFormatter(col),
        };
      }
      if (col.dataType === "integer") {
        return { ...def, formatter: "plaintext" };
      }
      // Show numeric value with a thousands separator but DO NOT round or
      // re-store. Tabulator's "money" formatter reads-only. Precision follows
      // the column's declared scale when the product schema reports one (e.g. 2 for a
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
      return { ...def, formatter: commonFieldFormatter(col) };
    case "date":
    case "datetime":
      return { ...def, formatter: col.display ? commonFieldFormatter(col) : temporalValueFormatter };
    case "time":
      return { ...def, formatter: col.display ? commonFieldFormatter(col) : "plaintext" };
    case "json":
      return {
        ...def,
        formatter: jsonValueFormatter,
        headerFilterFunc: jsonHeaderFilter,
        cssClass: "vt-json-cell",
      };
    case "text":
    default:
      return { ...def, formatter: commonFieldFormatter(col) };
  }
}

/** Render the shared display text and safe enum/progress decorations without writing values. */
function commonFieldFormatter(column: ColumnSchema): GridCellFormatter {
  return (cell) => {
    const raw = cell.getValue();
    const element = document.createElement("span");
    element.textContent = formatFieldDisplay(raw, column, getLocale());
    if (raw === null || raw === undefined || raw === "" || (column.display?.kind === "select" && Array.isArray(raw) && raw.length === 0)) { element.className = "vt-cell-empty"; return element; }
    if (column.display?.kind === "select") {
      element.textContent = "";
      for (const [index, part] of enumDisplayParts(raw, column.enumOptions).entries()) {
        if (index > 0) element.append(document.createTextNode("、"));
        const tag = document.createElement("span");
        tag.textContent = part.text;
        if (part.color) { tag.style.borderBottom = `3px solid ${part.color}`; }
        tag.style.marginRight = "6px";
        element.append(tag);
      }
    }
    const progress = column.display ? progressDisplay(raw, column.display) : null;
    if (progress) {
      element.title = progress.outside ? `超出显示范围 · 原值 ${String(raw)}` : String(raw);
      element.style.background = `linear-gradient(to right, var(--vt-color-primary-100) ${progress.width}%, transparent ${progress.width}%)`;
      element.style.display = "block";
      element.dataset.progressOutside = String(progress.outside);
      if (progress.outside) element.setAttribute("aria-label", `${element.textContent}，超出显示范围`);
    }
    if (column.display?.kind === "url" && typeof raw === "string") {
      try {
        const url = new URL(raw);
        if (url.protocol === "http:" || url.protocol === "https:") {
          const link = document.createElement("a"); link.href = url.href;
          link.target = "_blank"; link.rel = "noreferrer";
          link.textContent = element.textContent; link.title = raw;
          link.addEventListener("click", (event) => event.stopPropagation());
          element.replaceChildren(link);
        }
      } catch { /* malformed values remain safe text */ }
    }
    return element;
  };
}

function formulaValueFormatter(
  cell: { getValue(): unknown },
  column: ColumnSchema,
): HTMLElement {
  const raw = cell.getValue();
  const root = document.createElement("span");
  const envelope = raw && typeof raw === "object" && !Array.isArray(raw)
    ? raw as Record<string, unknown>
    : null;
  // Non-ready computed states render their real state first; a stale value
  // never leaks as a new result.
  if (envelope && envelope.state !== "ready") {
    const label = formulaStateLabel(envelope.state);
    if (label) {
      root.className = `vt-lookup-state vt-lookup-state--${envelope.state}`;
      root.textContent = t(label);
      const diagnostic = envelope.diagnostic;
      if (diagnostic && typeof diagnostic === "object" && !Array.isArray(diagnostic)) {
        const message = (diagnostic as Record<string, unknown>).message;
        if (typeof message === "string") root.title = message;
      }
      return root;
    }
  }
  // The Go query port delivers ready formula results as bare values; legacy
  // envelopes carrying state=ready unwrap to the same unified value branch.
  const value = envelope && envelope.state === "ready" ? envelope.value : raw;
  root.textContent = formatFormulaDisplayValue(value, column);
  return root;
}

/**
 * Match structured values by their provider-neutral JSON representation.
 *
 * Tabulator's default text filter coerces objects to `[object Object]`, which
 * makes nested values impossible to find. Serialization is read-only and
 * guarded so malformed/cyclic host values cannot break grid filtering.
 */
export function jsonHeaderFilter(headerValue: unknown, rowValue: unknown): boolean {
  const needle = String(headerValue ?? "").trim().toLocaleLowerCase();
  if (!needle) return true;
  try {
    const serialized = JSON.stringify(rowValue);
    return typeof serialized === "string"
      && serialized.toLocaleLowerCase().includes(needle);
  } catch {
    return false;
  }
}

/**
 * Render structured values as a compact, safe summary.
 *
 * `textContent` is used exclusively: user-provided JSON can never become HTML.
 * The raw value stays untouched so double-click editing continues to receive
 * the original object/array instead of a display string.
 */
export function jsonValueFormatter(cell: { getValue(): unknown }): HTMLElement {
  const value = cell.getValue();
  const element = document.createElement("span");
  element.className = "vt-json-value";

  if (value == null) {
    element.classList.add("vt-cell-empty");
    element.textContent = "—";
    element.title = t("grid.json.empty");
    return element;
  }

  if (Array.isArray(value)) {
    element.textContent = `[…] · ${t("grid.json.items", { count: value.length })}`;
  } else if (typeof value === "object") {
    element.textContent = `{…} · ${t("grid.json.keys", { count: Object.keys(value).length })}`;
  } else {
    element.textContent = String(value);
  }

  try {
    const serialized = JSON.stringify(value);
    element.title = serialized.length > 2000
      ? `${serialized.slice(0, 1999)}…`
      : serialized;
  } catch {
    element.title = String(value);
  }
  return element;
}

/**
 * Render date/date-time values without Tabulator's optional Luxon dependency.
 *
 * Date-only values remain calendar dates. Timestamp values are parsed as
 * instants and displayed in the workstation timezone with millisecond
 * precision. The underlying cell value and raw-value tooltip remain unchanged.
 */
export function temporalValueFormatter(
  cell: { getValue(): unknown },
  params: { readonly timeZone?: string } = {},
): HTMLElement {
  const value = cell.getValue();
  const element = document.createElement("span");
  element.className = "vt-temporal-value";
  if (value == null || value === "") {
    element.classList.add("vt-cell-empty");
    element.textContent = "—";
    return element;
  }
  const raw = String(value);
  if (/^\d{4}-\d{2}-\d{2}$/u.test(raw)) {
    element.textContent = raw;
    element.title = raw;
    return element;
  }
  const candidate = raw.replace(
    /^(\d{4}-\d{2}-\d{2}) (?=\d{2}:\d{2})/u,
    "$1T",
  );
  const instant = new Date(candidate);
  if (Number.isNaN(instant.getTime())) {
    element.textContent = raw;
    element.title = raw;
    return element;
  }
  const formatter = new Intl.DateTimeFormat(getLocale(), {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    fractionalSecondDigits: 3,
    hourCycle: "h23",
    ...(params.timeZone ? { timeZone: params.timeZone } : {}),
  });
  const parts = Object.fromEntries(
    formatter.formatToParts(instant)
      .filter(({ type }) => type !== "literal")
      .map(({ type, value: partValue }) => [type, partValue]),
  );
  element.textContent = `${parts.year}-${parts.month}-${parts.day} `
    + `${parts.hour}:${parts.minute}:${parts.second}.${parts.fractionalSecond}`;
  element.title = raw;
  return element;
}

function attachmentFormatter(cell: { getValue(): unknown }): HTMLElement {
  const value = cell.getValue();
  const entries = Array.isArray(value)
    ? value
    : value == null || value === ""
      ? []
      : [value];
  const labels = entries.map((entry) => {
    if (typeof entry === "string") return entry;
    if (entry && typeof entry === "object") {
      const ref = entry as {
        readonly originalName?: unknown;
        readonly storedName?: unknown;
      };
      if (typeof ref.originalName === "string") return ref.originalName;
      if (typeof ref.storedName === "string") return ref.storedName;
    }
    return t("grid.attachment.fallbackName");
  });
  const element = document.createElement("span");
  element.className = "vt-attachment-summary";
  const icon = document.createElement("span");
  icon.className = labels.length
    ? "vt-attachment-summary__icon vt-attachment-summary__icon--existing"
    : "vt-attachment-summary__icon vt-attachment-summary__icon--add";
  icon.setAttribute("aria-hidden", "true");
  const label = document.createElement("span");
  label.className = "vt-attachment-summary__label";
  label.textContent = labels.length
    ? t("grid.attachment.summary", {
        count: labels.length,
        names: labels.join(t("grid.listSeparator")),
      })
    : t("grid.attachment.add");
  element.append(icon, label);
  element.title = labels.length
    ? `${labels.join("\n")}\n${t("grid.attachment.openHint")}`
    : t("grid.attachment.openHint");
  return element;
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
const editingExpectedDigests = new Map<string, string | null>();

export function buildEditEventHandlers(
  editSchema: readonly ColumnEditSchema[] | null | undefined,
  onCellEdited?: CellEditedHandler,
  onValidationError?: CellValidationErrorHandler,
): {
  cellEditing: (cell: TabulatorCellLike) => void;
  cellEdited: (cell: TabulatorCellLike) => void;
} | null {
  if (!onCellEdited) return null;
  const editByName = new Map(
    (editSchema ?? []).map((column) => [column.name, column] as const),
  );
  return {
    cellEditing: (cell) => {
      const row = cell.getRow().getData();
      const rowKey = row[ROW_KEY_FIELD] as number | string;
      const key = `${rowKey}:${cell.getField()}`;
      const digest = row.__vibetableDigest;
      editingOldValues.set(key, cell.getValue());
      editingExpectedDigests.set(
        key,
        typeof digest === "string" && /^sha256:[0-9a-f]{64}$/u.test(digest)
          ? digest
          : null,
      );
    },
    cellEdited: (cell) => {
      const row = cell.getRow().getData();
      const rowKey = row[ROW_KEY_FIELD] as number | string;
      const field = cell.getField();
      const key = `${rowKey}:${field}`;
      const oldValue = editingOldValues.get(key);
      const expectedDigest = editingExpectedDigests.get(key) ?? null;
      editingOldValues.delete(key);
      editingExpectedDigests.delete(key);
      const newValue = cell.getValue();
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
      onCellEdited(rowKey, field, oldValue, newValue, expectedDigest);
    },
  };
}

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
  // `dataType` and `nullable` are VibeTable metadata used while constructing
  // editors/formatters; they are not Tabulator column options. Passing them to
  // Tabulator produces runtime warnings for every refresh, so strip them at
  // the library boundary after the product-specific column has been built.
  const columns = buildTabulatorColumns(
    page,
    opts?.editSchema,
    opts?.relationLookup,
  );

  const options: TabulatorOptions = {
    columns: columns as unknown[],
    data,
    index: ROW_KEY_FIELD,
    layout: "fitColumns",
    placeholder: t("grid.empty"),
    // Keep menus/edit popups inside the grid so scoped light/dark product
    // theming applies instead of falling back to document.body defaults.
    popupContainer: true,
    // Range selection consumes the default focus interaction. Keep editing
    // explicit and deterministic: a deliberate double click opens the editor.
    editTriggerEvent: "dblclick",
    // Read-only Phase A:
    selectableRange: true, // highlight ranges; copy via host later
    // Avoid synthetic focus while Tabulator is constructing replacement cells
    // during setColumns. Deliberate pointer/keyboard range interaction still
    // focuses its target, while table loads and locale rebuilds no longer
    // create a DOM Selection against a not-yet-attached cell.
    selectableRangeAutoFocus: false,
    // Treat the first synthetic row-number column as the official range row
    // header. This enables whole-row selection and makes the single frozen
    // column a supported Tabulator configuration.
    selectableRangeRows: true,
    clipboard: false, // paste disabled (Phase B2)
    // Explicitly do NOT register a paste action.
    clipboardPasteAction: undefined,
    // Defensive: no edit module interactions.
    editEventQueue: undefined,
    // In remote mode Tabulator records the user's sort/filter AST but does not
    // reorder the currently loaded page. useTabulator forwards the events to
    // the host/Lookup authoritative full-dataset query pipeline.
    sortMode: "remote",
    filterMode: "remote",
    movableColumns: true,
  };

  // Strip the undefined keys so the object is clean for assertion & wire.
  return stripUndefined(options);
}

/**
 * 可打印字符键入即进入当前选中单元格的编辑（电子表格习惯）：
 * 修饰键（Ctrl/Meta/Alt）组合、空格与多字符功能键不触发；
 * IME 发起的首个键入（key="Process"）也会进入编辑并让组合输入在
 * 编辑器内继续。双击、F2 等既有编辑入口与导航快捷键保持不变。
 */
export function shouldStartCellEditOnKey(event: {
  readonly key: string;
  readonly ctrlKey: boolean;
  readonly metaKey: boolean;
  readonly altKey: boolean;
  readonly isComposing?: boolean;
}): boolean {
  // 组合输入进行中不拦截：目标已是输入框，交由原生编辑路径。
  if (event.isComposing) return false;
  if (event.ctrlKey || event.metaKey || event.altKey) return false;
  // Windows IME 发起组合的首个 keydown；进入编辑后组合在编辑器中继续。
  if (event.key === "Process") return true;
  return event.key.length === 1 && event.key !== " ";
}

/** 只有支持文本选区的控件才能安全地移动插入点；用允许列表避免新类型误抛 InvalidStateError。 */
const SELECTION_INPUT_TYPES = new Set([
  "text", "search", "url", "tel", "password", "",
]);
function supportsSelection(
  element: HTMLInputElement | HTMLTextAreaElement,
): boolean {
  if (element instanceof HTMLTextAreaElement) return true;
  if (!(element instanceof HTMLInputElement)) return false;
  return SELECTION_INPUT_TYPES.has(element.type);
}

/**
 * 键入启动编辑后，把焦点同步移到编辑控件并建立“替换”语义：
 * 不 preventDefault、不注入键入字符，原始 keydown 的默认动作（原生插入 /
 * IME 组合）会落到新聚焦的编辑器里，负号、小数开头和中文组合输入都由
 * 浏览器原生处理。
 * - text/textarea：全选旧值，首键替换（type-over）。
 * - number：Chromium 不支持全选，改为先清空旧值（合法空串，不手写
 *   '-'/'.'），首键由原生默认动作插入，避免旧值与新键入拼成非法数字
 *   被浏览器清空。
 */
export function activateEditorForKeyStroke(
  element: HTMLInputElement | HTMLTextAreaElement,
): void {
  element.focus();
  if (supportsSelection(element)) {
    element.select();
  } else if (element instanceof HTMLInputElement && element.type === "number") {
    element.value = "";
  }
}

interface EditableCellComponent {
  edit(force?: boolean): boolean | void;
  getElement?(): HTMLElement;
}

/** 取当前活动选区左上角可编辑单元格；无选区或不可编辑时返回 null。 */
function activeEditableCell(grid: TabulatorFull): EditableCellComponent | null {
  const runtime = grid as unknown as {
    getRanges?: () => Array<{
      getRows?: () => Array<{ getCell?: (field: string) => unknown }>;
      getColumns?: () => Array<{ getField: () => string }>;
    }>;
  };
  const range = runtime.getRanges?.().at(-1);
  const rows = range?.getRows?.() ?? [];
  const columns = range?.getColumns?.() ?? [];
  if (rows.length === 0 || columns.length === 0) return null;
  const cell = rows[0]!.getCell?.(columns[0]!.getField()) as EditableCellComponent | undefined;
  return cell && typeof cell.edit === "function" ? cell : null;
}

function bindTypeToStartEdit(grid: TabulatorFull, element: HTMLElement): void {
  const onKeyDown = (event: KeyboardEvent): void => {
    if (!shouldStartCellEditOnKey(event)) return;
    const target = event.target;
    if (target instanceof HTMLElement
      && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName))) {
      return;
    }
    const cell = activeEditableCell(grid);
    if (!cell) return;
    // 不传 force：Tabulator 的 edit(force) 会绕过 editable 回调
    // （busy/只读列的动态禁用），无 force 时只读单元格返回 false，不进入编辑。
    if (cell.edit() === false) return;
    // 不 preventDefault、不注入字符：聚焦编辑器后，原始 keydown 的默认
    // 动作（原生插入/IME 组合）落到编辑器里，负号与小数开头由浏览器处理。
    const editor = cell.getElement?.().querySelector<HTMLInputElement | HTMLTextAreaElement>(
      "input, textarea",
    );
    if (editor && !editor.readOnly && !editor.disabled) {
      activateEditorForKeyStroke(editor);
    }
  };
  element.addEventListener("keydown", onKeyDown);
  (grid as unknown as {
    on?: (event: string, handler: () => void) => void;
  }).on?.("tableDestroyed", () => element.removeEventListener("keydown", onKeyDown));
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
  const grid = new TabulatorFull(element, options);
  const host = typeof element === "string"
    ? (grid as unknown as { element?: HTMLElement }).element
    : element;
  if (host) bindTypeToStartEdit(grid, host);
  const editHandlers = buildEditEventHandlers(
    opts?.editSchema,
    opts?.onCellEdited,
    opts?.onValidationError,
  );
  if (editHandlers) {
    const eventGrid = grid as unknown as {
      on(event: string, handler: (cell: TabulatorCellLike) => void): void;
    };
    eventGrid.on("cellEditing", editHandlers.cellEditing);
    eventGrid.on("cellEdited", editHandlers.cellEdited);
  }
  return grid;
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
