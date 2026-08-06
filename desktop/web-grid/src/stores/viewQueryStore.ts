import { defineStore } from "pinia";
import type {
  FilterExpression,
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

function mirroredHeaderFilters(filters: readonly FilterExpression[]): FilterExpression[] {
  if (!filters.every(filter => "field" in filter)) return [];
  return filters
    .filter(filter => "field" in filter && filter.operator === "eq")
    .map(filter => ({ ...filter }));
}

function sameFilter(left: FilterExpression, right: FilterExpression): boolean {
  if (!("field" in left) || !("field" in right)) return false;
  return left.field === right.field
    && left.operator === right.operator
    && left.logic === right.logic
    && JSON.stringify(left.value) === JSON.stringify(right.value);
}

function withoutManagedHeaderFilters(
  filters: readonly FilterExpression[],
  managed: readonly FilterExpression[],
): FilterExpression[] {
  const result = cloneFilterExpressions(filters);
  for (const header of managed) {
    const index = result.findIndex(filter => sameFilter(filter, header));
    if (index >= 0) result.splice(index, 1);
  }
  return result;
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
      this.visibleFields = [...allFields];
      this.collapsedGroupKeys = [];
    },
    replace(collection: string, view: PresetView, allFields: readonly string[]) {
      this.collection = collection;
      this.search = view.search;
      this.filters = cloneFilterExpressions(view.filters);
      this.headerFilters = mirroredHeaderFilters(view.filters);
      this.sorts = [...view.sorts];
      this.groups = [...(view.groups ?? [])].slice(0, 2);
      this.summaries = [...(view.summaries ?? [])].slice(0, 3);
      this.collapsedGroupKeys = [...(view.collapsedGroupKeys ?? [])].slice(0, 512);
      const available = new Set(allFields);
      const saved = view.visibleFields.filter((field) => available.has(field));
      this.visibleFields = allFields.length === 0
        ? [...view.visibleFields]
        : saved.length > 0 ? saved : [...allFields];
    },
    updateRuntime(query: {
      readonly headerFilters: readonly FilterExpression[];
      readonly sorts: readonly SortCondition[];
      readonly groups: readonly GroupCondition[];
    }) {
      this.filters = withoutManagedHeaderFilters(this.filters, this.headerFilters);
      this.filters.push(...cloneFilterExpressions(query.headerFilters));
      this.headerFilters = cloneFilterExpressions(query.headerFilters);
      this.sorts = [...query.sorts];
      this.groups = [...query.groups].slice(0, 2);
    },
    updateDefinition(input: {
      readonly filters: readonly FilterExpression[];
      readonly groups: readonly GroupCondition[];
      readonly summaries: readonly SummaryCondition[];
      readonly visibleFields: readonly string[];
    }) {
      this.filters = cloneFilterExpressions(input.filters);
      this.headerFilters = mirroredHeaderFilters(input.filters);
      this.groups = [...input.groups].slice(0, 2);
      this.summaries = [...input.summaries].slice(0, 3);
      this.visibleFields = [...input.visibleFields];
    },
    toggleGroup(key: string) {
      this.collapsedGroupKeys = this.collapsedGroupKeys.includes(key)
        ? this.collapsedGroupKeys.filter(item => item !== key)
        : [...this.collapsedGroupKeys, key].slice(-512);
    },
    toQuery(groupOffset = 0): TableQuery {
      return {
        ...(this.search ? { keyword: this.search } : {}),
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
