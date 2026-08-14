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

export interface DataSourceViewGrid {
  readonly initialized?: boolean;
  getColumns(): readonly ViewColumn[];
  getSorters?(): readonly { field: string; dir: "asc" | "desc" }[];
  getHeaderFilters?(): readonly { field: string; value: unknown }[];
  setColumnLayout?(layout: readonly {
    field: string;
    width?: number;
    visible: boolean;
    frozen: boolean;
  }[]): void | Promise<void>;
  setSort?(sorters: readonly { field: string; dir: "asc" | "desc" }[]): void | Promise<void>;
  clearHeaderFilter?(): void;
  setHeaderFilterValue?(field: string, value: unknown): void;
}

export interface DataSourceViewGridSource {
  readonly current: Readonly<Ref<DataSourceViewGrid | null>>;
}

function isObject(value: unknown): value is Record<PropertyKey, unknown> {
  return typeof value === "object" && value !== null;
}

function hasOptionalFunction(
  value: Record<PropertyKey, unknown>,
  property: PropertyKey,
): boolean {
  return value[property] === undefined || typeof value[property] === "function";
}

function isDataSourceViewGrid(value: unknown): value is DataSourceViewGrid {
  if (!isObject(value) || typeof value.getColumns !== "function") return false;
  return [
    "getSorters",
    "getHeaderFilters",
    "setColumnLayout",
    "setSort",
    "clearHeaderFilter",
    "setHeaderFilterValue",
  ].every(property => hasOptionalFunction(value, property));
}

/**
 * Isolates Tabulator's incomplete declarations from the stable view-state seam.
 * Runtime capability checks keep the assertion at one verified composition boundary.
 */
export function createTabulatorDataSourceViewAdapter(
  grid: TabulatorFull | null,
): DataSourceViewGrid | null {
  const candidate: unknown = grid;
  return isDataSourceViewGrid(candidate) ? candidate : null;
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
  const savedColumns = [...view.columns]
    .filter((column) => currentFields.has(column.name))
    .sort((left, right) => (left.order ?? 0) - (right.order ?? 0));
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
  if (columns.length && grid.setColumnLayout) {
    await grid.setColumnLayout(columns.map((column) => ({
      field: column.name,
      ...(column.width != null ? { width: column.width } : {}),
      visible: column.visible ?? true,
      frozen: column.frozen ?? false,
    })));
  }
  const sorters: Array<{ field: string; dir: "asc" | "desc" }> = view.sorts.map((sort) => ({
    field: typeof sort.field === "string" ? sort.field : "",
    dir: sort.direction === "desc" ? "desc" as const : "asc" as const,
  })).filter((sort) => currentFields.has(sort.field));
  await grid.setSort?.(sorters);
  grid.clearHeaderFilter?.();
  for (const filter of headerFilterConditions(view.filters)) {
    const field = filter.field;
    if (currentFields.has(field)) {
      grid.setHeaderFilterValue?.(field, filter.value);
    }
  }
}
