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

export const useViewQueryStore = defineStore("view-query", {
  state: () => ({
    collection: "",
    search: "",
    filters: [] as FilterExpression[],
    sorts: [] as SortCondition[],
    groups: [] as GroupCondition[],
    summaries: [] as SummaryCondition[],
    visibleFields: [] as string[],
  }),
  actions: {
    reset(collection = "", allFields: readonly string[] = []) {
      this.collection = collection;
      this.search = "";
      this.filters = [];
      this.sorts = [];
      this.groups = [];
      this.summaries = [];
      this.visibleFields = [...allFields];
    },
    replace(collection: string, view: PresetView, allFields: readonly string[]) {
      this.collection = collection;
      this.search = view.search;
      this.filters = cloneFilterExpressions(view.filters);
      this.sorts = [...view.sorts];
      this.groups = [...(view.groups ?? [])].slice(0, 2);
      this.summaries = [...(view.summaries ?? [])].slice(0, 3);
      const available = new Set(allFields);
      const saved = view.visibleFields.filter((field) => available.has(field));
      this.visibleFields = allFields.length === 0
        ? [...view.visibleFields]
        : saved.length > 0 ? saved : [...allFields];
    },
    updateRuntime(query: {
      readonly filters: readonly FilterExpression[];
      readonly sorts: readonly SortCondition[];
      readonly groups: readonly GroupCondition[];
    }) {
      this.filters = cloneFilterExpressions(query.filters);
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
      this.groups = [...input.groups].slice(0, 2);
      this.summaries = [...input.summaries].slice(0, 3);
      this.visibleFields = [...input.visibleFields];
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
