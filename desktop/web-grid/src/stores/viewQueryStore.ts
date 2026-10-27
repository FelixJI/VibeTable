import { defineStore } from "pinia";
import { headerFilterConditions, isFilterCondition } from "@/grid/viewQuery";
import { canEditHeaderValueFilter, headerValueFilterLeaves, isFieldValueGroup } from "@/grid/headerFilterMenu";
import { isRowIdFieldName } from "@/grid/gridState";
import type {
  FilterExpression,
  ColumnSchema,
  GroupCondition,
  PresetView,
  SortCondition,
  SummaryCondition,
  TableQuery,
} from "@/contracts";

export function cloneFilterExpressions(filters: readonly FilterExpression[]): FilterExpression[] {
  return filters.map((filter) => "filters" in filter
    ? {
        groupLogic: filter.groupLogic ?? "AND",
        ...(filter.logic ? { logic: filter.logic } : {}),
        filters: cloneFilterExpressions(filter.filters),
      }
    : { ...filter });
}

function mirroredHeaderFilters(
  filters: readonly FilterExpression[],
  columns?: readonly ColumnSchema[],
): FilterExpression[] {
  return headerFilterConditions(filters, columns).map(filter => ({ ...filter }));
}

function sameFilter(left: FilterExpression, right: FilterExpression): boolean {
  if (!("field" in left) || !("field" in right)) return false;
  return left.field === right.field
    && left.operator === right.operator
    && left.logic === right.logic
    && JSON.stringify(left.value) === JSON.stringify(right.value);
}

function sameExpression(left: FilterExpression, right: FilterExpression): boolean {
  if (isFilterCondition(left) || isFilterCondition(right)) return sameFilter(left, right);
  return left.groupLogic === right.groupLogic && left.logic === right.logic
    && left.filters.length === right.filters.length
    && left.filters.every((child, index) => sameExpression(child, right.filters[index]!));
}

/** 顶层单列条件组与镜像叶子集合的多重集等价（列级 OR 组的移除语义）。 */
function sameLeafMultiset(
  group: readonly FilterExpression[],
  managed: readonly FilterExpression[],
): boolean {
  if (group.length !== managed.length) return false;
  const remaining = [...managed];
  for (const leaf of group) {
    const index = remaining.findIndex(candidate => sameFilter(leaf, candidate));
    if (index < 0) return false;
    remaining.splice(index, 1);
  }
  return true;
}

function withoutManagedHeaderFilters(
  filters: readonly FilterExpression[],
  managed: readonly FilterExpression[],
): FilterExpression[] {
  const managedByField = new Map<string, FilterExpression[]>();
  const collect = (leaf: FilterExpression): void => {
    if (!isFilterCondition(leaf)) return;
    const list = managedByField.get(leaf.field) ?? [];
    list.push(leaf);
    managedByField.set(leaf.field, list);
  };
  for (const entry of managed) {
    // 镜像可能是平铺叶子或单列组（勾选面板/列级 OR token 组）：组展开为
    // 叶子参与字段级移除。
    if (isFilterCondition(entry)) collect(entry);
    else if (isFieldValueGroup(entry)) entry.filters.forEach(collect);
  }
  const result = cloneFilterExpressions(filters).filter(entry =>
    !managed.some(candidate => sameExpression(entry, candidate)));
  // 先移除与镜像叶子精确匹配的平铺叶子，再移除叶子集合等价的单列组
  // （列级 OR 组在 filters 里是 FilterGroup，在镜像是展开后的叶子）。
  for (let index = result.length - 1; index >= 0; index -= 1) {
    const entry = result[index]!;
    if (isFilterCondition(entry)) {
      const managed = managedByField.get(entry.field) ?? [];
      if (managed.some(candidate => sameFilter(entry, candidate))) result.splice(index, 1);
      continue;
    }
    if (!isFieldValueGroup(entry)) continue;
    // 镜像组只承载列级 OR 语义（勾选面板 OR 组 / 分词 token 组）：叶子
    // 相同但布尔结构不同的高级 AND 组，以及顶层 OR 耦合的组，绝不因
    // 镜像而被移除，否则仅排序的 runtime 更新就会改写保存语义。
    if (entry.groupLogic !== "OR" || entry.logic === "OR") continue;
    const groupField = entry.filters[0]!.field;
    const managed = managedByField.get(groupField) ?? [];
    if (managed.length > 0 && sameLeafMultiset(entry.filters, managed)) {
      result.splice(index, 1);
      // 该字段的镜像叶子已由这个组消费，避免误伤后续同字段叶子。
      managedByField.set(groupField, []);
    }
  }
  return result;
}

/**
 * 默认可见列：行 ID（主键，物理名 `id`）默认隐藏，与网格列构建规则
 * （`isRowIdColumn`）保持同步；用户在“显示字段”中显式勾选后仍可显示。
 */
export function defaultVisibleFields(allFields: readonly string[]): string[] {
  return allFields.filter(field => !isRowIdFieldName(field));
}

export const useViewQueryStore = defineStore("view-query", {
  state: () => ({
    collection: "",
    search: "",
    filters: [] as FilterExpression[],
    headerFilters: [] as FilterExpression[],
    sorts: [] as SortCondition[],
    groups: [] as GroupCondition[],
    summaries: [] as SummaryCondition[],
    visibleFields: [] as string[],
    collapsedGroupKeys: [] as string[],
  }),
  actions: {
    reset(collection = "", allFields: readonly string[] = []) {
      this.collection = collection;
      this.search = "";
      this.filters = [];
      this.headerFilters = [];
      this.sorts = [];
      this.groups = [];
      this.summaries = [];
      this.visibleFields = defaultVisibleFields(allFields);
      this.collapsedGroupKeys = [];
    },
    replace(collection: string, view: PresetView, allFields: readonly string[], columns?: readonly ColumnSchema[]) {
      this.collection = collection;
      this.search = view.search;
      this.filters = cloneFilterExpressions(view.filters);
      this.headerFilters = mirroredHeaderFilters(view.filters, columns);
      this.sorts = [...view.sorts];
      this.groups = [...(view.groups ?? [])].slice(0, 2);
      this.summaries = [...(view.summaries ?? [])].slice(0, 3);
      this.collapsedGroupKeys = [...(view.collapsedGroupKeys ?? [])].slice(0, 512);
      const available = new Set(allFields);
      const saved = view.visibleFields.filter((field) => available.has(field));
      this.visibleFields = allFields.length === 0
        ? [...view.visibleFields]
        : saved.length > 0 || view.columns?.some(column => column.visible === false)
          ? saved : defaultVisibleFields(allFields);
    },
    updateRuntime(query: {
      readonly headerFilters: readonly FilterExpression[];
      readonly sorts: readonly SortCondition[];
      readonly groups: readonly GroupCondition[];
    }) {
      this.filters = withoutManagedHeaderFilters(this.filters, this.headerFilters);
      this.filters.push(...cloneFilterExpressions(query.headerFilters));
      this.headerFilters = cloneFilterExpressions(query.headerFilters);
      this.sorts = query.sorts.map(sort => {
        const previous = this.sorts.find(item => item.field === sort.field);
        return previous ? { ...sort, ...previous, direction: sort.direction } : { ...sort };
      });
      this.groups = [...query.groups].slice(0, 2);
    },
    updateDefinition(input: {
      readonly filters: readonly FilterExpression[];
      readonly groups: readonly GroupCondition[];
      readonly summaries: readonly SummaryCondition[];
      readonly visibleFields: readonly string[];
      /** 当前 schema：用于只镜像可经表头输入往返的条件。 */
      readonly columns?: readonly ColumnSchema[];
    }) {
      this.filters = cloneFilterExpressions(input.filters);
      this.headerFilters = mirroredHeaderFilters(input.filters, input.columns);
      this.groups = [...input.groups].slice(0, 2);
      this.summaries = [...input.summaries].slice(0, 3);
      this.visibleFields = [...input.visibleFields];
    },
    /**
     * 应用字段头部勾选值面板的字段级筛选：替换该字段全部顶层条件
     * （勾选面板是字段级控件，与 Excel 一致，接管该字段既有平铺叶子与
     * 单列条件组），并移除该字段的表头输入镜像；菜单条件始终留在 filters，
     * 避免后续排序用真实输入快照覆盖菜单条件。其余字段、跨字段/
     * 嵌套条件组、排序与分组保持不变；重查由既有 runtime 管道
     * （GridHost 的 viewQueryChange 事件）触发。
     */
    applyHeaderValueFilter(field: string, values: readonly unknown[], column?: ColumnSchema) {
      if (!canEditHeaderValueFilter(this.filters, field)) return;
      const rest = this.filters.filter(filter =>
        isFilterCondition(filter)
          ? filter.field !== field
          : !isFieldValueGroup(filter) || filter.filters[0]!.field !== field);
      const leaves = headerValueFilterLeaves(field, values, column);
      this.filters = [...rest, ...cloneFilterExpressions(leaves)];
      this.headerFilters = [
        ...this.headerFilters.filter(filter =>
          isFilterCondition(filter)
            ? filter.field !== field
            : !isFieldValueGroup(filter)
              || filter.filters[0]!.field !== field),
      ];
    },
    toggleGroup(key: string) {
      this.collapsedGroupKeys = this.collapsedGroupKeys.includes(key)
        ? this.collapsedGroupKeys.filter(item => item !== key)
        : [...this.collapsedGroupKeys, key].slice(-512);
    },
    toQuery(groupOffset = 0): TableQuery {
      return {
        ...(this.search.trim() ? { keyword: this.search.trim() } : {}),
        filters: cloneFilterExpressions(this.filters),
        sorts: [...this.sorts],
        ...(this.groups.length ? { groups: [...this.groups] } : {}),
        ...(this.summaries.length ? { summaries: [...this.summaries] } : {}),
        offset: 0,
        limit: 500,
        groupOffset,
        groupLimit: 100,
      };
    },
  },
});
