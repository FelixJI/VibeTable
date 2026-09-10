import type {
  ColumnState,
  FilterCondition,
  FilterExpression,
  SortCondition,
} from "@/contracts";
import { computed, type Ref } from "vue";
import type { TabulatorFull } from "tabulator-tables";
import { ROW_NUMBER_FIELD } from "./createGrid";
import { headerFilterConditions } from "./viewQuery";
import { orderSavedColumns } from "./gridState";

interface DataSourceViewState {
  readonly columns?: readonly ColumnState[];
  readonly sorts: readonly SortCondition[];
  readonly filters: readonly FilterExpression[];
  readonly search: string;
  readonly layout: string;
  readonly kind?: "table" | "calendar" | "timeline" | "kanban" | "gallery";
  readonly density?: "compact" | "comfortable" | "cozy";
  readonly isDefault?: boolean;
}

interface ViewColumn {
  getField(): string;
  getWidth?(): number;
  isVisible?(): boolean;
  getDefinition?(): { frozen?: boolean };
}

interface GridPresentation {
  readonly columns: readonly {
    field: string;
    width?: number;
    visible: boolean;
    frozen: boolean;
  }[];
  readonly sorters: readonly { column: string; dir: "asc" | "desc" }[];
  readonly headerFilters: readonly { field: string; value: unknown }[];
}

export interface DataSourceViewGrid {
  readonly initialized?: boolean;
  getColumns(): readonly ViewColumn[];
  getSorters?(): readonly { field: string; dir: "asc" | "desc" }[];
  getHeaderFilters?(): readonly { field: string; value: unknown }[];
  applyPresentation?(presentation: GridPresentation): void | Promise<void>;
}

export interface DataSourceViewGridSource {
  readonly current: Readonly<Ref<DataSourceViewGrid | null>>;
}

interface TabulatorViewGrid {
  readonly initialized?: boolean;
  getColumns(): readonly (ViewColumn & { getDefinition(): Record<string, unknown> })[];
  getData(): Record<string, unknown>[];
  getSorters(): readonly { field: string; dir: "asc" | "desc" }[];
  getHeaderFilters(): readonly { field: string; value: unknown }[];
  setColumns(columns: unknown[]): void;
  setSort(sorters: GridPresentation["sorters"]): void;
  clearHeaderFilter(): void;
  setHeaderFilterValue(field: string, value: unknown): void;
  replaceData(rows: readonly Record<string, unknown>[]): Promise<void>;
}

function isTabulatorViewGrid(value: unknown): value is TabulatorViewGrid {
  if (typeof value !== "object" || value === null
    || typeof Reflect.get(value, "getColumns") !== "function") return false;
  return [
    "getColumns", "getData", "getSorters", "getHeaderFilters", "setColumns",
    "setSort", "clearHeaderFilter", "setHeaderFilterValue", "replaceData",
  ].every(property => typeof Reflect.get(value, property) === "function"
    || (Reflect.get(value, "initialized") === false && Reflect.get(value, property) === undefined));
}

/** Keeps runtime column bindings and host-owned rows outside saved presentation. */
export function createTabulatorDataSourceViewAdapter(
  table: TabulatorFull | null,
): DataSourceViewGrid | null {
  const grid: unknown = table;
  if (!isTabulatorViewGrid(grid)) return null;
  return {
    get initialized() { return grid.initialized; },
    getColumns: () => grid.getColumns(),
    getSorters: () => grid.getSorters(),
    getHeaderFilters: () => grid.getHeaderFilters(),
    applyPresentation({ columns, sorters, headerFilters }) {
      const current = grid.getColumns();
      const definitions = new Map(current.map(column => [column.getField(), column.getDefinition()]));
      const next = [
        ...current.filter(column => !isDataField(column.getField())).map(column => column.getDefinition()),
        ...columns.map(column => ({ ...definitions.get(column.field), ...column })),
      ];
      const rows = grid.getData();
      // setColumnLayout merges INITIAL options.columns, undoing later schema/editor
      // updates. Rebuild from current definitions and overlay only saved layout.
      grid.setColumns(next);
      // This product has no Tabulator Ajax data source. In remote mode these APIs
      // synchronously reload null as []; restore the host-fed rows in the same turn,
      // before a newer authoritative dataset can arrive. Do not change query modes.
      grid.setSort(sorters);
      grid.clearHeaderFilter();
      for (const filter of headerFilters) grid.setHeaderFilterValue(filter.field, filter.value);
      return grid.replaceData(rows);
    },
  };
}

export function createTabulatorDataSourceViewSource(
  grid: Ref<TabulatorFull | null>,
): DataSourceViewGridSource {
  return { current: computed(() => createTabulatorDataSourceViewAdapter(grid.value)) };
}

function isDataField(field: string): boolean {
  return field !== "rowKey" && field !== ROW_NUMBER_FIELD && !field.startsWith("__");
}

export function captureDataSourceView(
  grid: DataSourceViewGrid | null,
  options: { isDefault?: boolean; density?: DataSourceViewState["density"] } = {},
): DataSourceViewState {
  const gridColumns = grid && typeof grid.getColumns === "function"
    ? grid.getColumns()
    : [];
  const columns: ColumnState[] = gridColumns
    .map((column, order) => ({
      name: column.getField(),
      order,
      width: column.getWidth?.() ?? null,
      visible: column.isVisible?.() ?? true,
      frozen: column.getDefinition?.().frozen ?? false,
    }))
    .filter((column) => isDataField(column.name));
  const canReadRuntimeState = grid?.initialized !== false;
  const sorts = (canReadRuntimeState ? grid?.getSorters?.() ?? [] : [])
    .filter((sorter) => isDataField(sorter.field))
    .map((sorter) => ({ field: sorter.field, direction: sorter.dir }));
  const filters: FilterCondition[] = (
    canReadRuntimeState ? grid?.getHeaderFilters?.() ?? [] : []
  )
    .filter((filter) => isDataField(filter.field))
    .map((filter) => ({ field: filter.field, operator: "eq", value: filter.value }));

  return {
    kind: "table",
    layout: "table",
    columns,
    sorts,
    filters,
    search: "",
    density: options.density ?? "comfortable",
    isDefault: options.isDefault ?? false,
  };
}

export async function applyDataSourceView(
  grid: DataSourceViewGrid | null,
  view: DataSourceViewState,
): Promise<void> {
  if (!grid || typeof grid.getColumns !== "function" || !Array.isArray(view.columns)) return;
  const currentColumns = grid.getColumns()
    .map((column, order) => ({
      column,
      field: column.getField(),
      order,
    }))
    .filter(({ field }) => isDataField(field));
  const currentFields = new Set(currentColumns.map(({ field }) => field));
  const savedColumns = orderSavedColumns(view.columns
    .filter((column) => currentFields.has(column.name)));
  const savedFields = new Set(savedColumns.map((column) => column.name));
  const columns = savedColumns.length
    ? [
        ...savedColumns,
        ...currentColumns
          .filter(({ field }) => !savedFields.has(field))
          .map(({ column, field, order }) => ({
            name: field,
            order: savedColumns.length + order,
            width: column.getWidth?.() ?? null,
            visible: column.isVisible?.() ?? true,
            frozen: column.getDefinition?.().frozen ?? false,
          })),
      ]
    : currentColumns.map(({ column, field, order }) => ({
        name: field,
        order,
        width: column.getWidth?.() ?? null,
        visible: column.isVisible?.() ?? true,
        frozen: column.getDefinition?.().frozen ?? false,
      }));
  const sorters: GridPresentation["sorters"] = view.sorts.map((sort) => ({
    column: typeof sort.field === "string" ? sort.field : "",
    dir: sort.direction === "desc" ? "desc" as const : "asc" as const,
  })).filter((sort) => currentFields.has(sort.column));
  await grid.applyPresentation?.({
    columns: columns.map((column) => ({
      field: column.name,
      ...(column.width != null ? { width: column.width } : {}),
      visible: column.visible ?? true,
      frozen: column.frozen ?? false,
    })),
    sorters,
    headerFilters: headerFilterConditions(view.filters)
      .filter(filter => currentFields.has(filter.field))
      .map(({ field, value }) => ({ field, value })),
  });
}
